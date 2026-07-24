package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const pluginCredentialColumns = `
	id, public_id, plugin_key, scope, owner_user_id, created_by_user_id,
	display_name, public_metadata, secret_schema_version, binding_fingerprint,
	ciphertext, nonce, key_version, credential_fingerprint, revision, owner_enabled,
	admin_suspended_at, admin_suspended_by, status, expires_at, cooldown_until,
	last_used_at, last_success_at, last_failure_at, last_error_code,
	consecutive_failures, last_health_check_at, last_health_status,
	last_health_error_code, created_at, updated_at`

func scanPluginCredential(row rowScanner) (PluginCredential, error) {
	var credential PluginCredential
	var metadata []byte
	err := row.Scan(
		&credential.ID, &credential.PublicID, &credential.PluginKey, &credential.Scope,
		&credential.OwnerUserID, &credential.CreatedByUserID, &credential.DisplayName,
		&metadata, &credential.SecretSchemaVersion, &credential.BindingFingerprint,
		&credential.Ciphertext, &credential.Nonce, &credential.KeyVersion,
		&credential.CredentialFingerprint, &credential.Revision, &credential.OwnerEnabled,
		&credential.AdminSuspendedAt, &credential.AdminSuspendedBy, &credential.Status,
		&credential.ExpiresAt, &credential.CooldownUntil, &credential.LastUsedAt,
		&credential.LastSuccessAt, &credential.LastFailureAt, &credential.LastErrorCode,
		&credential.ConsecutiveFailures, &credential.LastHealthCheckAt,
		&credential.LastHealthStatus, &credential.LastHealthErrorCode,
		&credential.CreatedAt, &credential.UpdatedAt,
	)
	if err == nil {
		credential.PublicMetadata = decodeStrictJSONObject(metadata)
	}
	return credential, err
}

func (s *Store) CreatePluginCredential(ctx context.Context, input CreatePluginCredentialInput) (PluginCredential, error) {
	if s == nil || s.pool == nil {
		return PluginCredential{}, fmt.Errorf("storage is disabled")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PluginCredential{}, fmt.Errorf("begin create plugin credential: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	credential, err := s.insertPluginCredentialTx(ctx, tx, input)
	if err != nil {
		return PluginCredential{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PluginCredential{}, fmt.Errorf("commit create plugin credential: %w", err)
	}
	return credential, nil
}

func (s *Store) insertPluginCredentialTx(ctx context.Context, tx pgx.Tx, input CreatePluginCredentialInput) (PluginCredential, error) {
	if err := validatePluginCredentialInput(input); err != nil {
		return PluginCredential{}, err
	}
	if input.Scope == CredentialScopeUserPrivate {
		var active bool
		if err := tx.QueryRow(ctx, `SELECT enabled AND deleted_at IS NULL
			AND (expires_at IS NULL OR expires_at>$2)
			FROM users WHERE id=$1 FOR SHARE`, *input.OwnerUserID, s.now()).Scan(&active); errors.Is(err, pgx.ErrNoRows) {
			return PluginCredential{}, ErrNotFound
		} else if err != nil {
			return PluginCredential{}, fmt.Errorf("lock credential owner: %w", err)
		}
		if !active {
			return PluginCredential{}, fmt.Errorf("%w: credential owner is inactive", ErrConflict)
		}
	}
	metadata, err := marshalStrictJSON(input.PublicMetadata)
	if err != nil {
		return PluginCredential{}, fmt.Errorf("%w: credential public metadata: %v", ErrInvalid, err)
	}
	createdAt := input.CreatedAt
	if createdAt.IsZero() {
		createdAt = s.now()
	}
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = CredentialStatusActive
	}
	credential, err := scanPluginCredential(tx.QueryRow(ctx, `
		INSERT INTO plugin_credentials (
			public_id,plugin_key,scope,owner_user_id,created_by_user_id,display_name,
			public_metadata,secret_schema_version,binding_fingerprint,ciphertext,nonce,
			key_version,credential_fingerprint,owner_enabled,status,expires_at,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$17)
		RETURNING `+pluginCredentialColumns,
		strings.TrimSpace(input.PublicID), strings.TrimSpace(input.PluginKey), input.Scope,
		input.OwnerUserID, input.CreatedByUserID, strings.TrimSpace(input.DisplayName), metadata,
		input.SecretSchemaVersion, input.BindingFingerprint, input.Ciphertext, input.Nonce,
		input.KeyVersion, input.CredentialFingerprint, input.OwnerEnabled, status,
		input.ExpiresAt, createdAt))
	if err != nil {
		return PluginCredential{}, mapWriteError("create plugin credential", err)
	}
	return credential, nil
}

func validatePluginCredentialInput(input CreatePluginCredentialInput) error {
	if strings.TrimSpace(input.PublicID) == "" || strings.TrimSpace(input.PluginKey) == "" {
		return fmt.Errorf("%w: credential identity", ErrInvalid)
	}
	if !validCredentialScopeOwner(input.Scope, input.OwnerUserID) {
		return fmt.Errorf("%w: credential scope/owner", ErrInvalid)
	}
	if input.SecretSchemaVersion <= 0 || input.KeyVersion <= 0 ||
		len(input.BindingFingerprint) != 32 || len(input.CredentialFingerprint) != 32 ||
		len(input.Nonce) != 12 || len(input.Ciphertext) < 16 {
		return fmt.Errorf("%w: credential envelope", ErrInvalid)
	}
	status := strings.TrimSpace(input.Status)
	if status != "" && !validCredentialStatus(status) {
		return fmt.Errorf("%w: credential status %q", ErrInvalid, status)
	}
	return nil
}

func (s *Store) GetPluginCredentialByPublicID(ctx context.Context, publicID string) (PluginCredential, error) {
	if s == nil || s.pool == nil {
		return PluginCredential{}, fmt.Errorf("storage is disabled")
	}
	credential, err := scanPluginCredential(s.pool.QueryRow(ctx, `SELECT `+pluginCredentialColumns+`
		FROM plugin_credentials WHERE public_id=$1`, strings.TrimSpace(publicID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return PluginCredential{}, ErrNotFound
	}
	if err != nil {
		return PluginCredential{}, fmt.Errorf("get plugin credential: %w", err)
	}
	return credential, nil
}

func (s *Store) ListUserPluginCredentials(ctx context.Context, ownerUserID int64, filter PluginCredentialFilter) (PluginCredentialPage, error) {
	if ownerUserID <= 0 {
		return PluginCredentialPage{}, fmt.Errorf("%w: credential owner", ErrInvalid)
	}
	filter.OwnerUserID = &ownerUserID
	filter.Scopes = []string{CredentialScopeUserPrivate}
	filter.IncludeSecrets = false
	return s.ListPluginCredentials(ctx, filter)
}

func (s *Store) ListAdminPluginCredentials(ctx context.Context, filter PluginCredentialFilter) (PluginCredentialPage, error) {
	filter.IncludeSecrets = false
	return s.ListPluginCredentials(ctx, filter)
}

func (s *Store) CountPluginCredentials(ctx context.Context) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("storage is disabled")
	}
	var count int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM plugin_credentials`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count plugin credentials: %w", err)
	}
	return count, nil
}

func (s *Store) ListPluginCredentials(ctx context.Context, filter PluginCredentialFilter) (PluginCredentialPage, error) {
	if s == nil || s.pool == nil {
		return PluginCredentialPage{}, fmt.Errorf("storage is disabled")
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize, 50, 200)
	where, args, err := buildPluginCredentialWhere(filter)
	if err != nil {
		return PluginCredentialPage{}, err
	}
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM plugin_credentials WHERE `+where, args...).Scan(&total); err != nil {
		return PluginCredentialPage{}, fmt.Errorf("count plugin credentials: %w", err)
	}
	queryArgs := append(append([]any(nil), args...), pageSize, (page-1)*pageSize)
	rows, err := s.pool.Query(ctx, `SELECT `+pluginCredentialColumns+` FROM plugin_credentials
		WHERE `+where+` ORDER BY created_at DESC, id DESC`+
		fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), queryArgs...)
	if err != nil {
		return PluginCredentialPage{}, fmt.Errorf("list plugin credentials: %w", err)
	}
	defer rows.Close()
	items := make([]PluginCredential, 0, pageSize)
	for rows.Next() {
		credential, scanErr := scanPluginCredential(rows)
		if scanErr != nil {
			return PluginCredentialPage{}, fmt.Errorf("scan plugin credential: %w", scanErr)
		}
		if !filter.IncludeSecrets {
			clearCredentialSecrets(&credential)
		}
		items = append(items, credential)
	}
	if err := rows.Err(); err != nil {
		return PluginCredentialPage{}, fmt.Errorf("iterate plugin credentials: %w", err)
	}
	return PluginCredentialPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (s *Store) ListPluginCredentialHealthCandidates(ctx context.Context, pluginKeys []string, dueBefore time.Time, limit int) ([]PluginCredential, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("storage is disabled")
	}
	pluginKeys = normalizeStringList(pluginKeys)
	if len(pluginKeys) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT `+pluginCredentialColumns+` FROM plugin_credentials
		WHERE plugin_key=ANY($1::text[])
			AND owner_enabled=TRUE AND admin_suspended_at IS NULL
			AND status IN ('active','expired')
			AND (last_health_check_at IS NULL OR last_health_check_at<=$2)
		ORDER BY last_health_check_at ASC NULLS FIRST, id ASC
		LIMIT $3`, pluginKeys, dueBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("list credential health candidates: %w", err)
	}
	defer rows.Close()
	credentials := make([]PluginCredential, 0, limit)
	for rows.Next() {
		credential, scanErr := scanPluginCredential(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan credential health candidate: %w", scanErr)
		}
		credentials = append(credentials, credential)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate credential health candidates: %w", err)
	}
	return credentials, nil
}

func buildPluginCredentialWhere(filter PluginCredentialFilter) (string, []any, error) {
	conditions := []string{"TRUE"}
	args := make([]any, 0, 8)
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if filter.OwnerUserID != nil {
		conditions = append(conditions, "owner_user_id="+addArg(*filter.OwnerUserID))
	}
	if pluginKeys := normalizeStringList(filter.PluginKeys); len(pluginKeys) > 0 {
		conditions = append(conditions, "plugin_key=ANY("+addArg(pluginKeys)+"::text[])")
	}
	if scopes := normalizeStringList(filter.Scopes); len(scopes) > 0 {
		for _, scope := range scopes {
			if !validCredentialScope(scope) {
				return "", nil, fmt.Errorf("%w: credential scope %q", ErrInvalid, scope)
			}
		}
		conditions = append(conditions, "scope=ANY("+addArg(scopes)+"::text[])")
	}
	if statuses := normalizeStringList(filter.Statuses); len(statuses) > 0 {
		for _, status := range statuses {
			if !validCredentialStatus(status) {
				return "", nil, fmt.Errorf("%w: credential status %q", ErrInvalid, status)
			}
		}
		conditions = append(conditions, "status=ANY("+addArg(statuses)+"::text[])")
	}
	if filter.OwnerEnabled != nil {
		conditions = append(conditions, "owner_enabled="+addArg(*filter.OwnerEnabled))
	}
	if filter.Suspended != nil {
		if *filter.Suspended {
			conditions = append(conditions, "admin_suspended_at IS NOT NULL")
		} else {
			conditions = append(conditions, "admin_suspended_at IS NULL")
		}
	}
	return strings.Join(conditions, " AND "), args, nil
}

func (s *Store) ListUsableUserCredentialCandidates(ctx context.Context, ownerUserID int64, pluginKey string, at time.Time, limit int) ([]PluginCredential, error) {
	private, err := s.listUsableCredentialLayer(ctx, ownerUserID, pluginKey, CredentialScopeUserPrivate, at, limit)
	if err != nil {
		return nil, err
	}
	remaining := remainingCredentialLimit(limit, len(private))
	if remaining == 0 {
		return private, nil
	}
	shared, err := s.listUsableCredentialLayer(ctx, 0, pluginKey, CredentialScopePublicShared, at, remaining)
	if err != nil {
		return nil, err
	}
	return append(private, shared...), nil
}

func (s *Store) ListUsableUserPrivateCredentialCandidates(ctx context.Context, ownerUserID int64, pluginKey string, at time.Time, limit int) ([]PluginCredential, error) {
	return s.listUsableCredentialLayer(ctx, ownerUserID, pluginKey, CredentialScopeUserPrivate, at, limit)
}

func (s *Store) ListUsableAdminCredentialCandidates(ctx context.Context, pluginKey string, at time.Time, limit int) ([]PluginCredential, error) {
	private, err := s.listUsableCredentialLayer(ctx, 0, pluginKey, CredentialScopeAdminPrivate, at, limit)
	if err != nil {
		return nil, err
	}
	remaining := remainingCredentialLimit(limit, len(private))
	if remaining == 0 {
		return private, nil
	}
	shared, err := s.listUsableCredentialLayer(ctx, 0, pluginKey, CredentialScopePublicShared, at, remaining)
	if err != nil {
		return nil, err
	}
	return append(private, shared...), nil
}

func (s *Store) ListUsableAdminPrivateCredentialCandidates(ctx context.Context, pluginKey string, at time.Time, limit int) ([]PluginCredential, error) {
	return s.listUsableCredentialLayer(ctx, 0, pluginKey, CredentialScopeAdminPrivate, at, limit)
}

func (s *Store) ListUsableSharedCredentialCandidates(ctx context.Context, pluginKey string, at time.Time, limit int) ([]PluginCredential, error) {
	return s.listUsableCredentialLayer(ctx, 0, pluginKey, CredentialScopePublicShared, at, limit)
}

func (s *Store) listUsableCredentialLayer(ctx context.Context, ownerUserID int64, pluginKey, scope string, at time.Time, limit int) ([]PluginCredential, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("storage is disabled")
	}
	if strings.TrimSpace(pluginKey) == "" || !validCredentialScope(scope) {
		return nil, fmt.Errorf("%w: credential candidate query", ErrInvalid)
	}
	if at.IsZero() {
		at = s.now()
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	ownerClause := "c.owner_user_id IS NULL"
	args := []any{strings.TrimSpace(pluginKey), scope, at, limit}
	joinClause := ""
	if scope == CredentialScopeUserPrivate {
		if ownerUserID <= 0 {
			return nil, fmt.Errorf("%w: credential owner", ErrInvalid)
		}
		ownerClause = "c.owner_user_id=$5"
		args = append(args, ownerUserID)
		joinClause = ` JOIN users u ON u.id=c.owner_user_id
			AND u.enabled AND u.deleted_at IS NULL AND (u.expires_at IS NULL OR u.expires_at>$3)`
	}
	rows, err := s.pool.Query(ctx, `SELECT `+prefixCredentialColumns("c")+`
		FROM plugin_credentials c`+joinClause+`
		WHERE c.plugin_key=$1 AND c.scope=$2 AND `+ownerClause+`
			AND c.owner_enabled AND c.admin_suspended_at IS NULL AND c.status='active'
			AND (c.expires_at IS NULL OR c.expires_at>$3)
			AND (c.cooldown_until IS NULL OR c.cooldown_until<=$3)
		ORDER BY c.last_success_at DESC NULLS LAST, c.consecutive_failures,
			c.last_used_at ASC NULLS FIRST, c.id
		LIMIT $4`, args...)
	if err != nil {
		return nil, fmt.Errorf("list usable credential candidates: %w", err)
	}
	defer rows.Close()
	result := make([]PluginCredential, 0)
	for rows.Next() {
		credential, scanErr := scanPluginCredential(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan usable credential candidate: %w", scanErr)
		}
		result = append(result, credential)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usable credential candidates: %w", err)
	}
	return result, nil
}

func prefixCredentialColumns(prefix string) string {
	columns := strings.Split(pluginCredentialColumns, ",")
	for index := range columns {
		columns[index] = prefix + "." + strings.TrimSpace(columns[index])
	}
	return strings.Join(columns, ", ")
}

func remainingCredentialLimit(limit, used int) int {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if used >= limit {
		return 0
	}
	return limit - used
}

func (s *Store) ReplacePluginCredentialEnvelopeCAS(ctx context.Context, input ReplaceCredentialEnvelopeInput) (PluginCredential, error) {
	if s == nil || s.pool == nil {
		return PluginCredential{}, fmt.Errorf("storage is disabled")
	}
	if input.ExpectedRevision <= 0 || !validCredentialScope(input.Scope) || !validCredentialStatus(input.Status) ||
		input.SecretSchemaVersion <= 0 || input.KeyVersion <= 0 || len(input.BindingFingerprint) != 32 ||
		len(input.CredentialFingerprint) != 32 || len(input.Nonce) != 12 || len(input.Ciphertext) < 16 {
		return PluginCredential{}, fmt.Errorf("%w: replacement credential envelope", ErrInvalid)
	}
	metadata, err := marshalStrictJSON(input.PublicMetadata)
	if err != nil {
		return PluginCredential{}, fmt.Errorf("%w: credential public metadata: %v", ErrInvalid, err)
	}
	updatedAt := input.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = s.now()
	}
	credential, err := scanPluginCredential(s.pool.QueryRow(ctx, `
		UPDATE plugin_credentials SET scope=$3,display_name=$4,public_metadata=$5::jsonb,
			secret_schema_version=$6,binding_fingerprint=$7,ciphertext=$8,nonce=$9,
			key_version=$10,credential_fingerprint=$11,status=$12,expires_at=$13,
			revision=revision+1,updated_at=$14
		WHERE public_id=$1 AND revision=$2 AND scope IN ('admin_private','public_shared')
			AND $3 IN ('admin_private','public_shared')
		RETURNING `+pluginCredentialColumns,
		strings.TrimSpace(input.PublicID), input.ExpectedRevision, input.Scope,
		strings.TrimSpace(input.DisplayName), metadata, input.SecretSchemaVersion,
		input.BindingFingerprint, input.Ciphertext, input.Nonce, input.KeyVersion,
		input.CredentialFingerprint, input.Status, input.ExpiresAt, updatedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if scanErr := s.pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM plugin_credentials WHERE public_id=$1)`, input.PublicID).Scan(&exists); scanErr != nil {
			return PluginCredential{}, fmt.Errorf("check credential replacement: %w", scanErr)
		}
		if !exists {
			return PluginCredential{}, ErrNotFound
		}
		return PluginCredential{}, fmt.Errorf("%w: plugin credential revision", ErrConflict)
	}
	if err != nil {
		return PluginCredential{}, mapWriteError("replace plugin credential", err)
	}
	return credential, nil
}

func (s *Store) ReplaceUserPluginCredentialEnvelopeCAS(ctx context.Context, ownerUserID int64, input ReplaceCredentialEnvelopeInput) (PluginCredential, error) {
	if s == nil || s.pool == nil {
		return PluginCredential{}, fmt.Errorf("storage is disabled")
	}
	if ownerUserID <= 0 || input.ExpectedRevision <= 0 || input.Scope != CredentialScopeUserPrivate ||
		!validCredentialStatus(input.Status) || input.SecretSchemaVersion <= 0 || input.KeyVersion <= 0 ||
		len(input.BindingFingerprint) != 32 || len(input.CredentialFingerprint) != 32 ||
		len(input.Nonce) != 12 || len(input.Ciphertext) < 16 {
		return PluginCredential{}, fmt.Errorf("%w: user replacement credential envelope", ErrInvalid)
	}
	metadata, err := marshalStrictJSON(input.PublicMetadata)
	if err != nil {
		return PluginCredential{}, fmt.Errorf("%w: credential public metadata: %v", ErrInvalid, err)
	}
	updatedAt := input.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = s.now()
	}
	credential, err := scanPluginCredential(s.pool.QueryRow(ctx, `
		UPDATE plugin_credentials SET display_name=$4,public_metadata=$5::jsonb,
			secret_schema_version=$6,binding_fingerprint=$7,ciphertext=$8,nonce=$9,
			key_version=$10,credential_fingerprint=$11,status=$12,expires_at=$13,
			revision=revision+1,updated_at=$14
		WHERE public_id=$1 AND revision=$2 AND owner_user_id=$3 AND scope='user_private'
		RETURNING `+pluginCredentialColumns,
		strings.TrimSpace(input.PublicID), input.ExpectedRevision, ownerUserID,
		strings.TrimSpace(input.DisplayName), metadata, input.SecretSchemaVersion,
		input.BindingFingerprint, input.Ciphertext, input.Nonce, input.KeyVersion,
		input.CredentialFingerprint, input.Status, input.ExpiresAt, updatedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if scanErr := s.pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM plugin_credentials WHERE public_id=$1 AND owner_user_id=$2
				AND scope='user_private')`, input.PublicID, ownerUserID).Scan(&exists); scanErr != nil {
			return PluginCredential{}, fmt.Errorf("check user credential replacement: %w", scanErr)
		}
		if !exists {
			return PluginCredential{}, ErrNotFound
		}
		return PluginCredential{}, fmt.Errorf("%w: plugin credential revision", ErrConflict)
	}
	if err != nil {
		return PluginCredential{}, mapWriteError("replace user plugin credential", err)
	}
	return credential, nil
}

func (s *Store) SetAdminCredentialEnabled(ctx context.Context, publicID string, enabled bool) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	command, err := s.pool.Exec(ctx, `UPDATE plugin_credentials SET owner_enabled=$2,
		updated_at=now() WHERE public_id=$1 AND scope IN ('admin_private','public_shared')`,
		strings.TrimSpace(publicID), enabled)
	if err != nil {
		return fmt.Errorf("set administrator credential enabled: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetCredentialOwnerEnabled(ctx context.Context, publicID string, ownerUserID int64, enabled bool) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	command, err := s.pool.Exec(ctx, `UPDATE plugin_credentials SET owner_enabled=$3,
		updated_at=now() WHERE public_id=$1 AND owner_user_id=$2 AND scope='user_private'`,
		strings.TrimSpace(publicID), ownerUserID, enabled)
	if err != nil {
		return fmt.Errorf("set credential owner enabled: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetCredentialAdminSuspension(ctx context.Context, input CredentialSuspensionInput) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	if input.AdminUserID <= 0 || strings.TrimSpace(input.PublicID) == "" {
		return fmt.Errorf("%w: credential suspension", ErrInvalid)
	}
	at := input.At
	if at.IsZero() {
		at = s.now()
	}
	var err error
	if input.Suspended {
		result, execErr := s.pool.Exec(ctx, `UPDATE plugin_credentials SET
			admin_suspended_at=$2,admin_suspended_by=$3,updated_at=now() WHERE public_id=$1`,
			strings.TrimSpace(input.PublicID), at, input.AdminUserID)
		err = execErr
		if err == nil && result.RowsAffected() == 0 {
			return ErrNotFound
		}
	} else {
		result, execErr := s.pool.Exec(ctx, `UPDATE plugin_credentials SET
			admin_suspended_at=NULL,admin_suspended_by=NULL,updated_at=now() WHERE public_id=$1`,
			strings.TrimSpace(input.PublicID))
		err = execErr
		if err == nil && result.RowsAffected() == 0 {
			return ErrNotFound
		}
	}
	if err != nil {
		return fmt.Errorf("set credential administrator suspension: %w", err)
	}
	return nil
}

func (s *Store) DeleteUserPluginCredential(ctx context.Context, publicID string, ownerUserID int64) error {
	return s.deletePluginCredential(ctx, `public_id=$1 AND owner_user_id=$2 AND scope='user_private'`, publicID, ownerUserID)
}

func (s *Store) DeleteAdminPluginCredential(ctx context.Context, publicID string) error {
	return s.deletePluginCredential(ctx, `public_id=$1 AND scope IN ('admin_private','public_shared')`, publicID)
}

func (s *Store) DeletePluginCredentialByAdmin(ctx context.Context, publicID string) error {
	return s.deletePluginCredential(ctx, `public_id=$1`, publicID)
}

func (s *Store) deletePluginCredential(ctx context.Context, predicate string, args ...any) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM plugin_credentials WHERE `+predicate, args...)
	if err != nil {
		return fmt.Errorf("delete plugin credential: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RecordPluginCredentialSuccess(ctx context.Context, input CredentialSuccessInput) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	at := input.SucceededAt
	if at.IsZero() {
		at = s.now()
	}
	command, err := s.pool.Exec(ctx, `UPDATE plugin_credentials SET
		last_used_at=GREATEST(COALESCE(last_used_at,$2),$2),
		last_success_at=GREATEST(COALESCE(last_success_at,$2),$2),
		last_error_code='',consecutive_failures=0,cooldown_until=NULL,updated_at=now()
		WHERE public_id=$1 AND (last_success_at IS NULL OR last_success_at<$2)
			AND (last_failure_at IS NULL OR last_failure_at<$2)`, input.PublicID, at)
	if err != nil {
		return fmt.Errorf("record credential success: %w", err)
	}
	if command.RowsAffected() == 0 {
		var exists bool
		if scanErr := s.pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM plugin_credentials WHERE public_id=$1)`, input.PublicID).Scan(&exists); scanErr != nil {
			return fmt.Errorf("check credential success: %w", scanErr)
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}

func (s *Store) RecordPluginCredentialFailure(ctx context.Context, input CredentialFailureInput) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	if input.Status != "" && !validCredentialStatus(input.Status) {
		return fmt.Errorf("%w: credential failure status %q", ErrInvalid, input.Status)
	}
	at := input.FailedAt
	if at.IsZero() {
		at = s.now()
	}
	status := input.Status
	if status == "" {
		status = CredentialStatusActive
	}
	command, err := s.pool.Exec(ctx, `UPDATE plugin_credentials SET
		last_used_at=GREATEST(COALESCE(last_used_at,$2),$2),last_failure_at=$2,
		last_error_code=$3,consecutive_failures=consecutive_failures+1,
		status=$4,cooldown_until=$5,updated_at=now()
		WHERE public_id=$1
			AND (last_success_at IS NULL OR last_success_at<$2)
			AND (last_failure_at IS NULL OR last_failure_at<$2)`,
		input.PublicID, at, strings.TrimSpace(input.ErrorCode), status, input.CooldownUntil)
	if err != nil {
		return fmt.Errorf("record credential failure: %w", err)
	}
	if command.RowsAffected() == 0 {
		var exists bool
		if scanErr := s.pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM plugin_credentials WHERE public_id=$1)`, input.PublicID).Scan(&exists); scanErr != nil {
			return fmt.Errorf("check credential failure: %w", scanErr)
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}

func (s *Store) RecordPluginCredentialHealth(ctx context.Context, input CredentialHealthInput) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	if !validCredentialHealthStatus(input.HealthStatus) {
		return fmt.Errorf("%w: credential health status %q", ErrInvalid, input.HealthStatus)
	}
	credentialStatus := strings.TrimSpace(input.CredentialStatus)
	if credentialStatus != "" && !validCredentialStatus(credentialStatus) {
		return fmt.Errorf("%w: credential status %q", ErrInvalid, credentialStatus)
	}
	checkedAt := input.CheckedAt
	if checkedAt.IsZero() {
		checkedAt = s.now()
	}
	command, err := s.pool.Exec(ctx, `UPDATE plugin_credentials SET
		last_health_check_at=$2,last_health_status=$3,last_health_error_code=$4,
		status=CASE WHEN $5='' THEN status ELSE $5 END,
		last_error_code=CASE
			WHEN $5='invalid' THEN $4
			WHEN $5='active' THEN ''
			ELSE last_error_code
		END,
		updated_at=now()
		WHERE public_id=$1
			AND (last_health_check_at IS NULL OR last_health_check_at<$2)`,
		strings.TrimSpace(input.PublicID), checkedAt, input.HealthStatus,
		strings.TrimSpace(input.ErrorCode), credentialStatus)
	if err != nil {
		return fmt.Errorf("record credential health: %w", err)
	}
	if command.RowsAffected() == 0 {
		var exists bool
		if scanErr := s.pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM plugin_credentials WHERE public_id=$1)`, input.PublicID).Scan(&exists); scanErr != nil {
			return fmt.Errorf("check credential health: %w", scanErr)
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}

func validCredentialScopeOwner(scope string, ownerUserID *int64) bool {
	if scope == CredentialScopeUserPrivate {
		return ownerUserID != nil && *ownerUserID > 0
	}
	return (scope == CredentialScopeAdminPrivate || scope == CredentialScopePublicShared) && ownerUserID == nil
}

func validCredentialScope(scope string) bool {
	return scope == CredentialScopeUserPrivate || scope == CredentialScopeAdminPrivate || scope == CredentialScopePublicShared
}

func validCredentialStatus(status string) bool {
	return status == CredentialStatusActive || status == CredentialStatusInvalid || status == CredentialStatusExpired
}

func validCredentialHealthStatus(status string) bool {
	return status == CredentialHealthUnknown || status == CredentialHealthHealthy ||
		status == CredentialHealthError || status == CredentialHealthInvalid
}

func clearCredentialSecrets(credential *PluginCredential) {
	credential.BindingFingerprint = nil
	credential.Ciphertext = nil
	credential.Nonce = nil
	credential.CredentialFingerprint = nil
}
