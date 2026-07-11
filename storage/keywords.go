package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

const keywordColumns = `
	id, keyword, normalized_keyword, keyword_type, source_type, source_key,
	external_id, source_metadata, enabled, priority, cooldown_seconds,
	last_run_at, last_success_at, next_eligible_at, created_at, updated_at`

func scanKeyword(row rowScanner) (Keyword, error) {
	var keyword Keyword
	var metadata []byte
	var cooldown pgtype.Int8
	var lastRun, lastSuccess, nextEligible pgtype.Timestamptz
	err := row.Scan(
		&keyword.ID, &keyword.Keyword, &keyword.NormalizedKeyword, &keyword.KeywordType,
		&keyword.SourceType, &keyword.SourceKey, &keyword.ExternalID, &metadata,
		&keyword.Enabled, &keyword.Priority, &cooldown, &lastRun, &lastSuccess,
		&nextEligible, &keyword.CreatedAt, &keyword.UpdatedAt,
	)
	if err != nil {
		return Keyword{}, err
	}
	keyword.SourceMetadata = decodeMetadata(metadata)
	if cooldown.Valid {
		value := cooldown.Int64
		keyword.CooldownSeconds = &value
	}
	keyword.LastRunAt = timestampPointer(lastRun)
	keyword.LastSuccessAt = timestampPointer(lastSuccess)
	keyword.NextEligibleAt = timestampPointer(nextEligible)
	return keyword, nil
}

func timestampPointer(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

func (s *Store) CreateKeyword(ctx context.Context, input CreateKeywordInput) (Keyword, error) {
	if s == nil || s.pool == nil {
		return Keyword{}, fmt.Errorf("storage is disabled")
	}
	normalized := NormalizeKeyword(input.Keyword)
	if normalized == "" {
		return Keyword{}, fmt.Errorf("%w: empty keyword", ErrInvalid)
	}
	if input.CooldownSeconds != nil && *input.CooldownSeconds < 0 {
		return Keyword{}, fmt.Errorf("%w: negative cooldown", ErrInvalid)
	}
	if input.KeywordType = strings.TrimSpace(input.KeywordType); input.KeywordType == "" {
		input.KeywordType = DefaultKeywordType
	}
	if input.SourceType = strings.TrimSpace(input.SourceType); input.SourceType == "" {
		input.SourceType = DefaultSourceType
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Keyword{}, fmt.Errorf("begin create keyword: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	keyword, err := scanKeyword(tx.QueryRow(ctx, `
		INSERT INTO keywords (
			keyword, normalized_keyword, keyword_type, source_type, source_key,
			external_id, source_metadata, enabled, priority, cooldown_seconds
		) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10)
		RETURNING `+keywordColumns,
		strings.TrimSpace(input.Keyword), normalized, input.KeywordType, input.SourceType,
		strings.TrimSpace(input.SourceKey), strings.TrimSpace(input.ExternalID), metadataJSON(input.SourceMetadata),
		enabled, input.Priority, input.CooldownSeconds,
	))
	if err != nil {
		return Keyword{}, mapWriteError("create keyword", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE resource_keywords SET keyword_id=$1
		WHERE normalized_keyword=$2 AND keyword_id IS NULL`, keyword.ID, normalized); err != nil {
		return Keyword{}, fmt.Errorf("attach keyword resources: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Keyword{}, fmt.Errorf("commit create keyword: %w", err)
	}
	return keyword, nil
}

func (s *Store) GetKeyword(ctx context.Context, id int64) (Keyword, error) {
	if s == nil || s.pool == nil {
		return Keyword{}, fmt.Errorf("storage is disabled")
	}
	keyword, err := scanKeyword(s.pool.QueryRow(ctx, "SELECT "+keywordColumns+" FROM keywords WHERE id=$1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Keyword{}, ErrNotFound
	}
	if err != nil {
		return Keyword{}, fmt.Errorf("get keyword: %w", err)
	}
	return keyword, nil
}

func (s *Store) GetKeywordByNormalized(ctx context.Context, keyword string) (Keyword, error) {
	if s == nil || s.pool == nil {
		return Keyword{}, fmt.Errorf("storage is disabled")
	}
	normalized := NormalizeKeyword(keyword)
	if normalized == "" {
		return Keyword{}, ErrNotFound
	}
	result, err := scanKeyword(s.pool.QueryRow(ctx, "SELECT "+keywordColumns+" FROM keywords WHERE normalized_keyword=$1", normalized))
	if errors.Is(err, pgx.ErrNoRows) {
		return Keyword{}, ErrNotFound
	}
	if err != nil {
		return Keyword{}, fmt.Errorf("get keyword by normalized value: %w", err)
	}
	return result, nil
}

func (s *Store) ListKeywords(ctx context.Context, filter KeywordFilter) (KeywordPage, error) {
	if s == nil || s.pool == nil {
		return KeywordPage{}, fmt.Errorf("storage is disabled")
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize, 50, 200)
	conditions := []string{"TRUE"}
	args := make([]any, 0, 6)
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if filter.Query != "" {
		placeholder := addArg("%" + strings.TrimSpace(filter.Query) + "%")
		conditions = append(conditions, "(keyword ILIKE "+placeholder+" OR source_key ILIKE "+placeholder+")")
	}
	if filter.KeywordType != "" {
		conditions = append(conditions, "keyword_type="+addArg(filter.KeywordType))
	}
	if filter.SourceType != "" {
		conditions = append(conditions, "source_type="+addArg(filter.SourceType))
	}
	if filter.Enabled != nil {
		conditions = append(conditions, "enabled="+addArg(*filter.Enabled))
	}
	if filter.EligibleAt != nil {
		conditions = append(conditions, "enabled AND (next_eligible_at IS NULL OR next_eligible_at<="+addArg(*filter.EligibleAt)+")")
	}
	where := strings.Join(conditions, " AND ")
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM keywords WHERE "+where, args...).Scan(&total); err != nil {
		return KeywordPage{}, fmt.Errorf("count keywords: %w", err)
	}
	queryArgs := append(append([]any(nil), args...), pageSize, (page-1)*pageSize)
	rows, err := s.pool.Query(ctx, "SELECT "+keywordColumns+" FROM keywords WHERE "+where+
		" ORDER BY priority DESC, created_at ASC, id ASC"+fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), queryArgs...)
	if err != nil {
		return KeywordPage{}, fmt.Errorf("list keywords: %w", err)
	}
	defer rows.Close()
	items := make([]Keyword, 0, pageSize)
	for rows.Next() {
		keyword, scanErr := scanKeyword(rows)
		if scanErr != nil {
			return KeywordPage{}, fmt.Errorf("scan keyword: %w", scanErr)
		}
		items = append(items, keyword)
	}
	if err := rows.Err(); err != nil {
		return KeywordPage{}, fmt.Errorf("iterate keywords: %w", err)
	}
	return KeywordPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (s *Store) ListEligibleKeywords(ctx context.Context, at time.Time, limit int) ([]Keyword, error) {
	if at.IsZero() {
		at = s.now()
	}
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	rows, err := s.pool.Query(ctx, "SELECT "+keywordColumns+` FROM keywords
		WHERE enabled AND (next_eligible_at IS NULL OR next_eligible_at <= $1)
		ORDER BY priority DESC, COALESCE(last_run_at, '-infinity'::timestamptz), id
		LIMIT $2`, at, limit)
	if err != nil {
		return nil, fmt.Errorf("list eligible keywords: %w", err)
	}
	defer rows.Close()
	result := make([]Keyword, 0)
	for rows.Next() {
		keyword, scanErr := scanKeyword(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan eligible keyword: %w", scanErr)
		}
		result = append(result, keyword)
	}
	return result, rows.Err()
}

func (s *Store) UpdateKeyword(ctx context.Context, id int64, input UpdateKeywordInput) (Keyword, error) {
	if s == nil || s.pool == nil {
		return Keyword{}, fmt.Errorf("storage is disabled")
	}
	var keywordValue, normalizedValue any
	if input.Keyword != nil {
		normalized := NormalizeKeyword(*input.Keyword)
		if normalized == "" {
			return Keyword{}, fmt.Errorf("%w: empty keyword", ErrInvalid)
		}
		keywordValue, normalizedValue = strings.TrimSpace(*input.Keyword), normalized
	}
	if input.CooldownSeconds != nil && *input.CooldownSeconds != nil && **input.CooldownSeconds < 0 {
		return Keyword{}, fmt.Errorf("%w: negative cooldown", ErrInvalid)
	}
	var cooldownValue any
	if input.CooldownSeconds != nil {
		cooldownValue = *input.CooldownSeconds
	}
	var metadataValue any
	if input.SourceMetadata != nil {
		metadataValue = metadataJSON(*input.SourceMetadata)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Keyword{}, fmt.Errorf("begin update keyword: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	updated, err := scanKeyword(tx.QueryRow(ctx, `
		UPDATE keywords SET
			keyword=CASE WHEN $2::boolean THEN $3 ELSE keyword END,
			normalized_keyword=CASE WHEN $2::boolean THEN $4 ELSE normalized_keyword END,
			keyword_type=CASE WHEN $5::boolean THEN $6 ELSE keyword_type END,
			source_type=CASE WHEN $7::boolean THEN $8 ELSE source_type END,
			source_key=CASE WHEN $9::boolean THEN $10 ELSE source_key END,
			external_id=CASE WHEN $11::boolean THEN $12 ELSE external_id END,
			source_metadata=CASE WHEN $13::boolean THEN $14::jsonb ELSE source_metadata END,
			enabled=CASE WHEN $15::boolean THEN $16 ELSE enabled END,
			priority=CASE WHEN $17::boolean THEN $18 ELSE priority END,
			cooldown_seconds=CASE WHEN $19::boolean THEN $20::bigint ELSE cooldown_seconds END,
			updated_at=now()
		WHERE id=$1 RETURNING `+keywordColumns,
		id, input.Keyword != nil, keywordValue, normalizedValue,
		input.KeywordType != nil, optionalString(input.KeywordType), input.SourceType != nil, optionalString(input.SourceType),
		input.SourceKey != nil, optionalString(input.SourceKey), input.ExternalID != nil, optionalString(input.ExternalID),
		input.SourceMetadata != nil, metadataValue, input.Enabled != nil, optionalBool(input.Enabled),
		input.Priority != nil, optionalInt(input.Priority), input.CooldownSeconds != nil, cooldownValue,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Keyword{}, ErrNotFound
	}
	if err != nil {
		return Keyword{}, mapWriteError("update keyword", err)
	}
	if input.Keyword != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO resource_keywords (
				resource_id, keyword_id, keyword, normalized_keyword, keyword_type,
				first_seen_at, last_seen_at, discovery_count
			)
			SELECT resource_id, $1, $2, $3, $4, first_seen_at, last_seen_at, discovery_count
			FROM resource_keywords WHERE keyword_id=$1 AND normalized_keyword<>$3
			ON CONFLICT (resource_id, normalized_keyword) DO UPDATE SET
				keyword_id=$1, keyword=EXCLUDED.keyword, keyword_type=EXCLUDED.keyword_type,
				first_seen_at=LEAST(resource_keywords.first_seen_at, EXCLUDED.first_seen_at),
				last_seen_at=GREATEST(resource_keywords.last_seen_at, EXCLUDED.last_seen_at),
				discovery_count=resource_keywords.discovery_count + EXCLUDED.discovery_count`,
			updated.ID, updated.Keyword, updated.NormalizedKeyword, updated.KeywordType); err != nil {
			return Keyword{}, fmt.Errorf("merge renamed keyword associations: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM resource_keywords
			WHERE keyword_id=$1 AND normalized_keyword<>$2`, updated.ID, updated.NormalizedKeyword); err != nil {
			return Keyword{}, fmt.Errorf("remove renamed keyword associations: %w", err)
		}
	}
	if input.KeywordType != nil {
		if _, err := tx.Exec(ctx, `UPDATE resource_keywords SET keyword_type=$2
			WHERE keyword_id=$1`, updated.ID, updated.KeywordType); err != nil {
			return Keyword{}, fmt.Errorf("update resource keyword type: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Keyword{}, fmt.Errorf("commit update keyword: %w", err)
	}
	return updated, nil
}

func (s *Store) DeleteKeyword(ctx context.Context, id int64) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	command, err := s.pool.Exec(ctx, "DELETE FROM keywords WHERE id=$1", id)
	if err != nil {
		return fmt.Errorf("delete keyword: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) TouchKeywordSuccess(ctx context.Context, id int64, succeededAt time.Time) error {
	if succeededAt.IsZero() {
		succeededAt = s.now()
	}
	defaultSeconds := int64(s.defaultCooldown / time.Second)
	command, err := s.pool.Exec(ctx, `UPDATE keywords SET
		last_run_at=$2, last_success_at=$2,
		next_eligible_at=$2 + COALESCE(cooldown_seconds,$3) * interval '1 second',
		updated_at=now() WHERE id=$1`, id, succeededAt, defaultSeconds)
	if err != nil {
		return fmt.Errorf("touch keyword success: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) TouchKeywordSuccessByNormalized(ctx context.Context, keyword string, succeededAt time.Time) error {
	if succeededAt.IsZero() {
		succeededAt = s.now()
	}
	normalized := NormalizeKeyword(keyword)
	defaultSeconds := int64(s.defaultCooldown / time.Second)
	command, err := s.pool.Exec(ctx, `UPDATE keywords SET
		last_run_at=$2, last_success_at=$2,
		next_eligible_at=$2 + COALESCE(cooldown_seconds,$3) * interval '1 second',
		updated_at=now() WHERE normalized_keyword=$1`, normalized, succeededAt, defaultSeconds)
	if err != nil {
		return fmt.Errorf("touch keyword success: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) TouchKeywordFailure(ctx context.Context, id int64, failedAt time.Time) error {
	if failedAt.IsZero() {
		failedAt = s.now()
	}
	command, err := s.pool.Exec(ctx, `UPDATE keywords SET last_run_at=$2, updated_at=now() WHERE id=$1`, id, failedAt)
	if err != nil {
		return fmt.Errorf("touch keyword failure: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func mapWriteError(operation string, err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrConflict, operation)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func optionalString(value *string) any {
	if value == nil {
		return nil
	}
	return strings.TrimSpace(*value)
}

func optionalBool(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
