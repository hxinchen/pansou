package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

const userColumns = `
	id, username, normalized_username, password_hash, role, enabled, expires_at,
	must_change_password, auth_version, rps_limit, rpm_limit, rate_limit_disabled,
	last_login_at, created_at, updated_at, deleted_at`

const apiKeyColumns = `
	id, user_id, key_prefix, key_hash, created_at, last_used_at, revoked_at`

func NormalizeUsername(username string) string {
	fields := strings.FieldsFunc(strings.TrimSpace(username), unicode.IsSpace)
	return strings.ToLower(strings.Join(fields, " "))
}

func scanUser(row rowScanner) (User, error) {
	var user User
	err := row.Scan(
		&user.ID, &user.Username, &user.NormalizedUsername, &user.PasswordHash,
		&user.Role, &user.Enabled, &user.ExpiresAt, &user.MustChangePassword,
		&user.AuthVersion, &user.RPSLimit, &user.RPMLimit, &user.RateLimitDisabled,
		&user.LastLoginAt, &user.CreatedAt, &user.UpdatedAt, &user.DeletedAt,
	)
	return user, err
}

func scanAPIKey(row rowScanner) (APIKey, error) {
	var key APIKey
	err := row.Scan(&key.ID, &key.UserID, &key.KeyPrefix, &key.KeyHash,
		&key.CreatedAt, &key.LastUsedAt, &key.RevokedAt)
	return key, err
}

func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("storage is disabled")
	}
	var count int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return count, nil
}

func (s *Store) CountActiveAdmins(ctx context.Context, at time.Time) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("storage is disabled")
	}
	if at.IsZero() {
		at = s.now()
	}
	var count int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users
		WHERE role='admin' AND enabled AND deleted_at IS NULL
			AND (expires_at IS NULL OR expires_at > $1)`, at).Scan(&count); err != nil {
		return 0, fmt.Errorf("count active admins: %w", err)
	}
	return count, nil
}

func (s *Store) CreateUser(ctx context.Context, input CreateUserInput) (User, error) {
	if s == nil || s.pool == nil {
		return User{}, fmt.Errorf("storage is disabled")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("begin create user: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	user, err := s.insertUserTx(ctx, tx, input)
	if err != nil {
		return User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit create user: %w", err)
	}
	return user, nil
}

func (s *Store) CreateUserWithAPIKey(ctx context.Context, input CreateUserInput, keyInput APIKeyInput) (User, APIKey, error) {
	if s == nil || s.pool == nil {
		return User{}, APIKey{}, fmt.Errorf("storage is disabled")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, APIKey{}, fmt.Errorf("begin create user credentials: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	user, err := s.insertUserTx(ctx, tx, input)
	if err != nil {
		return User{}, APIKey{}, err
	}
	keyInput.UserID = user.ID
	if keyInput.CreatedAt.IsZero() {
		keyInput.CreatedAt = s.now()
	}
	key, err := insertAPIKeyTx(ctx, tx, keyInput)
	if err != nil {
		return User{}, APIKey{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, APIKey{}, fmt.Errorf("commit create user credentials: %w", err)
	}
	return user, key, nil
}

func (s *Store) insertUserTx(ctx context.Context, tx pgx.Tx, input CreateUserInput) (User, error) {
	normalized := NormalizeUsername(input.Username)
	if normalized == "" {
		return User{}, fmt.Errorf("%w: empty username", ErrInvalid)
	}
	if strings.TrimSpace(input.PasswordHash) == "" {
		return User{}, fmt.Errorf("%w: empty password hash", ErrInvalid)
	}
	role := strings.TrimSpace(input.Role)
	if role == "" {
		role = UserRoleUser
	}
	if !validUserRole(role) {
		return User{}, fmt.Errorf("%w: user role %q", ErrInvalid, role)
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	mustChange := true
	if input.MustChangePassword != nil {
		mustChange = *input.MustChangePassword
	}
	rps := input.RPSLimit
	if rps == 0 {
		rps = DefaultUserRPSLimit
	}
	rpm := input.RPMLimit
	if rpm == 0 {
		rpm = DefaultUserRPMLimit
	}
	if rps < 1 || rpm < 1 {
		return User{}, fmt.Errorf("%w: rate limits must be positive", ErrInvalid)
	}
	user, err := scanUser(tx.QueryRow(ctx, `INSERT INTO users (
		username, normalized_username, password_hash, role, enabled, expires_at,
		must_change_password, rps_limit, rpm_limit, rate_limit_disabled
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING `+userColumns,
		strings.TrimSpace(input.Username), normalized, strings.TrimSpace(input.PasswordHash),
		role, enabled, input.ExpiresAt, mustChange, rps, rpm, input.RateLimitDisabled,
	))
	if err != nil {
		return User{}, mapWriteError("create user", err)
	}
	return user, nil
}

func (s *Store) GetUserByID(ctx context.Context, id int64) (User, error) {
	if s == nil || s.pool == nil {
		return User{}, fmt.Errorf("storage is disabled")
	}
	user, err := scanUser(s.pool.QueryRow(ctx, `SELECT `+userColumns+`
		FROM users WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("get user by id: %w", err)
	}
	return user, nil
}

func (s *Store) GetUserByNormalizedUsername(ctx context.Context, username string) (User, error) {
	if s == nil || s.pool == nil {
		return User{}, fmt.Errorf("storage is disabled")
	}
	normalized := NormalizeUsername(username)
	if normalized == "" {
		return User{}, ErrNotFound
	}
	user, err := scanUser(s.pool.QueryRow(ctx, `SELECT `+userColumns+`
		FROM users WHERE normalized_username=$1`, normalized))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("get user by normalized username: %w", err)
	}
	return user, nil
}

func (s *Store) ListUsers(ctx context.Context, filter UserFilter) (UserPage, error) {
	if s == nil || s.pool == nil {
		return UserPage{}, fmt.Errorf("storage is disabled")
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize, 50, 200)
	conditions := []string{"TRUE"}
	args := make([]any, 0, 8)
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if !filter.IncludeDeleted {
		conditions = append(conditions, "deleted_at IS NULL")
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		placeholder := addArg("%" + query + "%")
		conditions = append(conditions, "(username ILIKE "+placeholder+" OR normalized_username ILIKE "+placeholder+")")
	}
	if roles := normalizeStringList(filter.Roles); len(roles) > 0 {
		for _, role := range roles {
			if !validUserRole(role) {
				return UserPage{}, fmt.Errorf("%w: user role %q", ErrInvalid, role)
			}
		}
		conditions = append(conditions, "role=ANY("+addArg(roles)+"::text[])")
	}
	if filter.Enabled != nil {
		conditions = append(conditions, "enabled="+addArg(*filter.Enabled))
	}
	if filter.ExpiresBefore != nil {
		conditions = append(conditions, "expires_at IS NOT NULL AND expires_at<"+addArg(*filter.ExpiresBefore))
	}
	if filter.ExpiresAfter != nil {
		conditions = append(conditions, "(expires_at IS NULL OR expires_at>"+addArg(*filter.ExpiresAfter)+")")
	}
	where := strings.Join(conditions, " AND ")
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE `+where, args...).Scan(&total); err != nil {
		return UserPage{}, fmt.Errorf("count users: %w", err)
	}
	queryArgs := append(append([]any(nil), args...), pageSize, (page-1)*pageSize)
	rows, err := s.pool.Query(ctx, `SELECT `+userColumns+` FROM users WHERE `+where+
		` ORDER BY created_at DESC, id DESC`+fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), queryArgs...)
	if err != nil {
		return UserPage{}, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	items := make([]User, 0, pageSize)
	for rows.Next() {
		user, scanErr := scanUser(rows)
		if scanErr != nil {
			return UserPage{}, fmt.Errorf("scan user: %w", scanErr)
		}
		items = append(items, user)
	}
	if err := rows.Err(); err != nil {
		return UserPage{}, fmt.Errorf("iterate users: %w", err)
	}
	return UserPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (s *Store) UpdateUser(ctx context.Context, id int64, input UpdateUserInput) (User, error) {
	if s == nil || s.pool == nil {
		return User{}, fmt.Errorf("storage is disabled")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("begin update user: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockUserAdministration(ctx, tx); err != nil {
		return User{}, err
	}
	current, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users
		WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("lock user: %w", err)
	}
	candidate := current
	sets := make([]string, 0, 10)
	args := []any{id}
	addSet := func(column string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf("%s=$%d", column, len(args)))
	}
	securityChanged := false
	if input.Username != nil {
		normalized := NormalizeUsername(*input.Username)
		if normalized == "" {
			return User{}, fmt.Errorf("%w: empty username", ErrInvalid)
		}
		candidate.Username = strings.TrimSpace(*input.Username)
		candidate.NormalizedUsername = normalized
		addSet("username", candidate.Username)
		addSet("normalized_username", normalized)
		securityChanged = securityChanged || normalized != current.NormalizedUsername
	}
	if input.Role != nil {
		candidate.Role = strings.TrimSpace(*input.Role)
		if !validUserRole(candidate.Role) {
			return User{}, fmt.Errorf("%w: user role %q", ErrInvalid, candidate.Role)
		}
		addSet("role", candidate.Role)
		securityChanged = securityChanged || candidate.Role != current.Role
	}
	if input.Enabled != nil {
		candidate.Enabled = *input.Enabled
		addSet("enabled", candidate.Enabled)
		securityChanged = securityChanged || candidate.Enabled != current.Enabled
	}
	if input.ExpiresAt != nil {
		candidate.ExpiresAt = *input.ExpiresAt
		addSet("expires_at", candidate.ExpiresAt)
		securityChanged = true
	}
	if input.MustChangePassword != nil {
		candidate.MustChangePassword = *input.MustChangePassword
		addSet("must_change_password", candidate.MustChangePassword)
		securityChanged = securityChanged || candidate.MustChangePassword != current.MustChangePassword
	}
	if input.RPSLimit != nil {
		if *input.RPSLimit < 1 {
			return User{}, fmt.Errorf("%w: RPS limit must be positive", ErrInvalid)
		}
		candidate.RPSLimit = *input.RPSLimit
		addSet("rps_limit", candidate.RPSLimit)
	}
	if input.RPMLimit != nil {
		if *input.RPMLimit < 1 {
			return User{}, fmt.Errorf("%w: RPM limit must be positive", ErrInvalid)
		}
		candidate.RPMLimit = *input.RPMLimit
		addSet("rpm_limit", candidate.RPMLimit)
	}
	if input.RateLimitDisabled != nil {
		candidate.RateLimitDisabled = *input.RateLimitDisabled
		addSet("rate_limit_disabled", candidate.RateLimitDisabled)
	}
	if len(sets) == 0 {
		return current, nil
	}
	at := s.now()
	if current.IsEffectiveAdminAt(at) && !candidate.IsEffectiveAdminAt(at) {
		if err := ensureAnotherActiveAdmin(ctx, tx, id, at); err != nil {
			return User{}, err
		}
	}
	if securityChanged {
		sets = append(sets, "auth_version=auth_version+1")
	}
	sets = append(sets, "updated_at=now()")
	updated, err := scanUser(tx.QueryRow(ctx, `UPDATE users SET `+strings.Join(sets, ", ")+
		` WHERE id=$1 RETURNING `+userColumns, args...))
	if err != nil {
		return User{}, mapWriteError("update user", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit update user: %w", err)
	}
	return updated, nil
}

func (s *Store) SoftDeleteUser(ctx context.Context, id int64, deletedAt time.Time) (User, error) {
	if s == nil || s.pool == nil {
		return User{}, fmt.Errorf("storage is disabled")
	}
	if deletedAt.IsZero() {
		deletedAt = s.now()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("begin delete user: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockUserAdministration(ctx, tx); err != nil {
		return User{}, err
	}
	current, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users
		WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("lock deleted user: %w", err)
	}
	if current.IsEffectiveAdminAt(deletedAt) {
		if err := ensureAnotherActiveAdmin(ctx, tx, id, deletedAt); err != nil {
			return User{}, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM plugin_credentials
		WHERE owner_user_id=$1 AND scope='user_private'`, id); err != nil {
		return User{}, fmt.Errorf("delete user private plugin credentials: %w", err)
	}
	deleted, err := scanUser(tx.QueryRow(ctx, `UPDATE users SET enabled=FALSE,
		deleted_at=$2, auth_version=auth_version+1, updated_at=now()
		WHERE id=$1 RETURNING `+userColumns, id, deletedAt))
	if err != nil {
		return User{}, fmt.Errorf("soft delete user: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit delete user: %w", err)
	}
	return deleted, nil
}

func ensureAnotherActiveAdmin(ctx context.Context, tx pgx.Tx, excludedID int64, at time.Time) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM users WHERE id<>$1 AND role='admin' AND enabled
			AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at>$2)
	)`, excludedID, at).Scan(&exists); err != nil {
		return fmt.Errorf("check replacement administrator: %w", err)
	}
	if !exists {
		return fmt.Errorf("%w: cannot remove the last active administrator", ErrConflict)
	}
	return nil
}

func lockUserAdministration(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(0x50616e55736572)); err != nil {
		return fmt.Errorf("lock user administration: %w", err)
	}
	return nil
}

func (s *Store) SetUserPassword(ctx context.Context, id int64, passwordHash string, mustChange bool, changedAt time.Time) (User, error) {
	if s == nil || s.pool == nil {
		return User{}, fmt.Errorf("storage is disabled")
	}
	if strings.TrimSpace(passwordHash) == "" {
		return User{}, fmt.Errorf("%w: empty password hash", ErrInvalid)
	}
	if changedAt.IsZero() {
		changedAt = s.now()
	}
	user, err := scanUser(s.pool.QueryRow(ctx, `UPDATE users SET password_hash=$2,
		must_change_password=$3, auth_version=auth_version+1, updated_at=$4
		WHERE id=$1 AND deleted_at IS NULL RETURNING `+userColumns,
		id, strings.TrimSpace(passwordHash), mustChange, changedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("set user password: %w", err)
	}
	return user, nil
}

func (s *Store) UpdateUserLastLogin(ctx context.Context, id int64, loggedInAt time.Time) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	if loggedInAt.IsZero() {
		loggedInAt = s.now()
	}
	command, err := s.pool.Exec(ctx, `UPDATE users SET last_login_at=$2, updated_at=now()
		WHERE id=$1 AND deleted_at IS NULL`, id, loggedInAt)
	if err != nil {
		return fmt.Errorf("update user last login: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateAPIKey(ctx context.Context, userID int64, input APIKeyInput) (APIKey, error) {
	if s == nil || s.pool == nil {
		return APIKey{}, fmt.Errorf("storage is disabled")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return APIKey{}, fmt.Errorf("begin create API key: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := requireExistingUser(ctx, tx, userID); err != nil {
		return APIKey{}, err
	}
	input.UserID = userID
	if input.CreatedAt.IsZero() {
		input.CreatedAt = s.now()
	}
	key, err := insertAPIKeyTx(ctx, tx, input)
	if err != nil {
		return APIKey{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return APIKey{}, fmt.Errorf("commit create API key: %w", err)
	}
	return key, nil
}

func insertAPIKeyTx(ctx context.Context, tx pgx.Tx, input APIKeyInput) (APIKey, error) {
	if input.UserID <= 0 || strings.TrimSpace(input.KeyPrefix) == "" || strings.TrimSpace(input.KeyHash) == "" {
		return APIKey{}, fmt.Errorf("%w: incomplete API key", ErrInvalid)
	}
	key, err := scanAPIKey(tx.QueryRow(ctx, `INSERT INTO api_keys (
		user_id, key_prefix, key_hash, created_at
	) VALUES ($1,$2,$3,$4) RETURNING `+apiKeyColumns,
		input.UserID, strings.TrimSpace(input.KeyPrefix), strings.TrimSpace(input.KeyHash), input.CreatedAt,
	))
	if err != nil {
		return APIKey{}, mapWriteError("create API key", err)
	}
	return key, nil
}

func requireExistingUser(ctx context.Context, tx pgx.Tx, userID int64) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM users WHERE id=$1 AND deleted_at IS NULL
	)`, userID).Scan(&exists); err != nil {
		return fmt.Errorf("check API key user: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetAPIKeyForUser(ctx context.Context, userID int64) (APIKey, error) {
	if s == nil || s.pool == nil {
		return APIKey{}, fmt.Errorf("storage is disabled")
	}
	key, err := scanAPIKey(s.pool.QueryRow(ctx, `SELECT `+apiKeyColumns+` FROM api_keys WHERE user_id=$1`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, fmt.Errorf("get user API key: %w", err)
	}
	return key, nil
}

func (s *Store) GetUserByAPIKeyHash(ctx context.Context, keyHash string) (User, APIKey, error) {
	if s == nil || s.pool == nil {
		return User{}, APIKey{}, fmt.Errorf("storage is disabled")
	}
	var user User
	var key APIKey
	err := s.pool.QueryRow(ctx, `SELECT
		u.id, u.username, u.normalized_username, u.password_hash, u.role, u.enabled,
		u.expires_at, u.must_change_password, u.auth_version, u.rps_limit, u.rpm_limit,
		u.rate_limit_disabled, u.last_login_at, u.created_at, u.updated_at, u.deleted_at,
		k.id, k.user_id, k.key_prefix, k.key_hash, k.created_at, k.last_used_at, k.revoked_at
		FROM api_keys k JOIN users u ON u.id=k.user_id
		WHERE k.key_hash=$1 AND k.revoked_at IS NULL`, strings.TrimSpace(keyHash)).Scan(
		&user.ID, &user.Username, &user.NormalizedUsername, &user.PasswordHash,
		&user.Role, &user.Enabled, &user.ExpiresAt, &user.MustChangePassword,
		&user.AuthVersion, &user.RPSLimit, &user.RPMLimit, &user.RateLimitDisabled,
		&user.LastLoginAt, &user.CreatedAt, &user.UpdatedAt, &user.DeletedAt,
		&key.ID, &key.UserID, &key.KeyPrefix, &key.KeyHash, &key.CreatedAt, &key.LastUsedAt, &key.RevokedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, APIKey{}, ErrNotFound
	}
	if err != nil {
		return User{}, APIKey{}, fmt.Errorf("get user by API key hash: %w", err)
	}
	return user, key, nil
}

func (s *Store) ResetAPIKey(ctx context.Context, userID int64, input APIKeyInput, resetAt time.Time) (APIKey, error) {
	if s == nil || s.pool == nil {
		return APIKey{}, fmt.Errorf("storage is disabled")
	}
	if strings.TrimSpace(input.KeyPrefix) == "" || strings.TrimSpace(input.KeyHash) == "" {
		return APIKey{}, fmt.Errorf("%w: incomplete API key", ErrInvalid)
	}
	if resetAt.IsZero() {
		resetAt = s.now()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return APIKey{}, fmt.Errorf("begin reset API key: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := requireExistingUser(ctx, tx, userID); err != nil {
		return APIKey{}, err
	}
	key, err := scanAPIKey(tx.QueryRow(ctx, `INSERT INTO api_keys (
		user_id, key_prefix, key_hash, created_at
	) VALUES ($1,$2,$3,$4)
	ON CONFLICT (user_id) DO UPDATE SET key_prefix=EXCLUDED.key_prefix,
		key_hash=EXCLUDED.key_hash, created_at=EXCLUDED.created_at,
		last_used_at=NULL, revoked_at=NULL
	RETURNING `+apiKeyColumns, userID, strings.TrimSpace(input.KeyPrefix), strings.TrimSpace(input.KeyHash), resetAt))
	if err != nil {
		return APIKey{}, mapWriteError("reset API key", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return APIKey{}, fmt.Errorf("commit reset API key: %w", err)
	}
	return key, nil
}

func (s *Store) RevokeAPIKey(ctx context.Context, userID int64, revokedAt time.Time) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	if revokedAt.IsZero() {
		revokedAt = s.now()
	}
	command, err := s.pool.Exec(ctx, `UPDATE api_keys SET revoked_at=$2
		WHERE user_id=$1 AND revoked_at IS NULL`, userID, revokedAt)
	if err != nil {
		return fmt.Errorf("revoke API key: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateAPIKeyLastUsed(ctx context.Context, userID int64, usedAt time.Time) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	if usedAt.IsZero() {
		usedAt = s.now()
	}
	command, err := s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at=$2
		WHERE user_id=$1 AND revoked_at IS NULL`, userID, usedAt)
	if err != nil {
		return fmt.Errorf("update API key last used: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func validUserRole(role string) bool {
	return role == UserRoleAdmin || role == UserRoleUser
}
