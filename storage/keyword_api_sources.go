package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const keywordAPISourceColumns = `
	id, name, enabled, request_method, request_url, request_headers, query_params,
	body_type, request_body, proxy_url, timeout_seconds, response_path,
	sync_interval_seconds, default_keyword_type, default_keyword_enabled,
	default_priority, default_cooldown_seconds, iteration_enabled, iteration_location,
	iteration_path, iteration_start, iteration_step, iteration_count, iteration_delay_seconds,
	next_sync_at, last_synced_at, last_status, last_error, last_item_count,
	last_request_count, last_success_count, last_failure_count, created_at, updated_at`

func scanKeywordAPISource(row rowScanner) (KeywordAPISource, error) {
	var source KeywordAPISource
	var headers, query []byte
	var cooldown pgtype.Int8
	var nextSync, lastSynced pgtype.Timestamptz
	err := row.Scan(
		&source.ID, &source.Name, &source.Enabled, &source.RequestMethod, &source.RequestURL,
		&headers, &query, &source.BodyType, &source.RequestBody, &source.ProxyURL,
		&source.TimeoutSeconds, &source.ResponsePath, &source.SyncIntervalSeconds,
		&source.DefaultKeywordType, &source.DefaultKeywordEnabled, &source.DefaultPriority,
		&cooldown, &source.IterationEnabled, &source.IterationLocation, &source.IterationPath,
		&source.IterationStart, &source.IterationStep, &source.IterationCount,
		&source.IterationDelaySeconds, &nextSync, &lastSynced, &source.LastStatus,
		&source.LastError, &source.LastItemCount, &source.LastRequestCount,
		&source.LastSuccessCount, &source.LastFailureCount, &source.CreatedAt, &source.UpdatedAt,
	)
	if err != nil {
		return KeywordAPISource{}, err
	}
	source.RequestHeaders = decodeStringMap(headers)
	source.QueryParams = decodeStringMap(query)
	if cooldown.Valid {
		value := cooldown.Int64
		source.DefaultCooldownSeconds = &value
	}
	source.NextSyncAt = timestampPointer(nextSync)
	source.LastSyncedAt = timestampPointer(lastSynced)
	return source, nil
}

func (s *Store) CreateKeywordAPISource(ctx context.Context, input CreateKeywordAPISourceInput) (KeywordAPISource, error) {
	if s == nil || s.pool == nil {
		return KeywordAPISource{}, fmt.Errorf("storage is disabled")
	}
	source, err := normalizeKeywordAPISourceCreate(input, s.now())
	if err != nil {
		return KeywordAPISource{}, err
	}
	created, err := scanKeywordAPISource(s.pool.QueryRow(ctx, `INSERT INTO keyword_api_sources (
		name, enabled, request_method, request_url, request_headers, query_params,
		body_type, request_body, proxy_url, timeout_seconds, response_path,
		sync_interval_seconds, default_keyword_type, default_keyword_enabled,
		default_priority, default_cooldown_seconds, iteration_enabled, iteration_location,
		iteration_path, iteration_start, iteration_step, iteration_count, iteration_delay_seconds,
		next_sync_at
	) VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)
	RETURNING `+keywordAPISourceColumns,
		source.Name, source.Enabled, source.RequestMethod, source.RequestURL,
		encodeStringMap(source.RequestHeaders), encodeStringMap(source.QueryParams),
		source.BodyType, source.RequestBody, source.ProxyURL, source.TimeoutSeconds,
		source.ResponsePath, source.SyncIntervalSeconds, source.DefaultKeywordType,
		source.DefaultKeywordEnabled, source.DefaultPriority, source.DefaultCooldownSeconds,
		source.IterationEnabled, source.IterationLocation, source.IterationPath,
		source.IterationStart, source.IterationStep, source.IterationCount,
		source.IterationDelaySeconds, source.NextSyncAt,
	))
	if err != nil {
		return KeywordAPISource{}, mapWriteError("create keyword API source", err)
	}
	return created, nil
}

func (s *Store) GetKeywordAPISource(ctx context.Context, id int64) (KeywordAPISource, error) {
	if s == nil || s.pool == nil {
		return KeywordAPISource{}, fmt.Errorf("storage is disabled")
	}
	source, err := scanKeywordAPISource(s.pool.QueryRow(ctx, "SELECT "+keywordAPISourceColumns+" FROM keyword_api_sources WHERE id=$1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return KeywordAPISource{}, ErrNotFound
	}
	if err != nil {
		return KeywordAPISource{}, fmt.Errorf("get keyword API source: %w", err)
	}
	return source, nil
}

func (s *Store) ListKeywordAPISources(ctx context.Context, filter KeywordAPISourceFilter) (KeywordAPISourcePage, error) {
	if s == nil || s.pool == nil {
		return KeywordAPISourcePage{}, fmt.Errorf("storage is disabled")
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize, 50, 200)
	conditions := []string{"TRUE"}
	args := make([]any, 0, 5)
	add := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		placeholder := add("%" + query + "%")
		conditions = append(conditions, "(name ILIKE "+placeholder+" OR request_url ILIKE "+placeholder+")")
	}
	if filter.Enabled != nil {
		conditions = append(conditions, "enabled="+add(*filter.Enabled))
	}
	if len(filter.Statuses) > 0 {
		conditions = append(conditions, "last_status=ANY("+add(filter.Statuses)+")")
	}
	where := strings.Join(conditions, " AND ")
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM keyword_api_sources WHERE "+where, args...).Scan(&total); err != nil {
		return KeywordAPISourcePage{}, fmt.Errorf("count keyword API sources: %w", err)
	}
	queryArgs := append(append([]any(nil), args...), pageSize, (page-1)*pageSize)
	rows, err := s.pool.Query(ctx, "SELECT "+keywordAPISourceColumns+" FROM keyword_api_sources WHERE "+where+
		" ORDER BY created_at DESC, id DESC"+fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), queryArgs...)
	if err != nil {
		return KeywordAPISourcePage{}, fmt.Errorf("list keyword API sources: %w", err)
	}
	defer rows.Close()
	items := make([]KeywordAPISource, 0, pageSize)
	for rows.Next() {
		source, scanErr := scanKeywordAPISource(rows)
		if scanErr != nil {
			return KeywordAPISourcePage{}, fmt.Errorf("scan keyword API source: %w", scanErr)
		}
		items = append(items, source)
	}
	if err := rows.Err(); err != nil {
		return KeywordAPISourcePage{}, fmt.Errorf("iterate keyword API sources: %w", err)
	}
	return KeywordAPISourcePage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (s *Store) UpdateKeywordAPISource(ctx context.Context, id int64, input UpdateKeywordAPISourceInput) (KeywordAPISource, error) {
	if s == nil || s.pool == nil {
		return KeywordAPISource{}, fmt.Errorf("storage is disabled")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return KeywordAPISource{}, fmt.Errorf("begin update keyword API source: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	current, err := scanKeywordAPISource(tx.QueryRow(ctx, "SELECT "+keywordAPISourceColumns+" FROM keyword_api_sources WHERE id=$1 FOR UPDATE", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return KeywordAPISource{}, ErrNotFound
	}
	if err != nil {
		return KeywordAPISource{}, fmt.Errorf("lock keyword API source: %w", err)
	}
	updated := applyKeywordAPISourceUpdate(current, input, s.now())
	if err := validateKeywordAPISource(updated); err != nil {
		return KeywordAPISource{}, err
	}
	updated, err = scanKeywordAPISource(tx.QueryRow(ctx, `UPDATE keyword_api_sources SET
		name=$2, enabled=$3, request_method=$4, request_url=$5, request_headers=$6::jsonb,
		query_params=$7::jsonb, body_type=$8, request_body=$9, proxy_url=$10,
		timeout_seconds=$11, response_path=$12, sync_interval_seconds=$13,
		default_keyword_type=$14, default_keyword_enabled=$15, default_priority=$16,
		default_cooldown_seconds=$17, iteration_enabled=$18, iteration_location=$19,
		iteration_path=$20, iteration_start=$21, iteration_step=$22, iteration_count=$23,
		iteration_delay_seconds=$24, next_sync_at=$25, updated_at=now()
	WHERE id=$1 RETURNING `+keywordAPISourceColumns,
		id, updated.Name, updated.Enabled, updated.RequestMethod, updated.RequestURL,
		encodeStringMap(updated.RequestHeaders), encodeStringMap(updated.QueryParams),
		updated.BodyType, updated.RequestBody, updated.ProxyURL, updated.TimeoutSeconds,
		updated.ResponsePath, updated.SyncIntervalSeconds, updated.DefaultKeywordType,
		updated.DefaultKeywordEnabled, updated.DefaultPriority, updated.DefaultCooldownSeconds,
		updated.IterationEnabled, updated.IterationLocation, updated.IterationPath,
		updated.IterationStart, updated.IterationStep, updated.IterationCount,
		updated.IterationDelaySeconds, updated.NextSyncAt,
	))
	if err != nil {
		return KeywordAPISource{}, mapWriteError("update keyword API source", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return KeywordAPISource{}, fmt.Errorf("commit keyword API source update: %w", err)
	}
	return updated, nil
}

func (s *Store) CopyKeywordAPISource(ctx context.Context, id int64) (KeywordAPISource, error) {
	if s == nil || s.pool == nil {
		return KeywordAPISource{}, fmt.Errorf("storage is disabled")
	}
	source, err := scanKeywordAPISource(s.pool.QueryRow(ctx, `INSERT INTO keyword_api_sources (
		name, enabled, request_method, request_url, request_headers, query_params,
		body_type, request_body, proxy_url, timeout_seconds, response_path,
		sync_interval_seconds, default_keyword_type, default_keyword_enabled,
		default_priority, default_cooldown_seconds, iteration_enabled, iteration_location,
		iteration_path, iteration_start, iteration_step, iteration_count, iteration_delay_seconds,
		next_sync_at, last_synced_at, last_status, last_error, last_item_count,
		last_request_count, last_success_count, last_failure_count
	) SELECT name || ' 副本', FALSE, request_method, request_url, request_headers, query_params,
		body_type, request_body, proxy_url, timeout_seconds, response_path,
		sync_interval_seconds, default_keyword_type, default_keyword_enabled,
		default_priority, default_cooldown_seconds, iteration_enabled, iteration_location,
		iteration_path, iteration_start, iteration_step, iteration_count, iteration_delay_seconds,
		NULL, NULL, 'pending', '', 0, 0, 0, 0
	FROM keyword_api_sources WHERE id=$1
	RETURNING `+keywordAPISourceColumns, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return KeywordAPISource{}, ErrNotFound
	}
	if err != nil {
		return KeywordAPISource{}, mapWriteError("copy keyword API source", err)
	}
	return source, nil
}

func (s *Store) DeleteKeywordAPISource(ctx context.Context, id int64) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	command, err := s.pool.Exec(ctx, "DELETE FROM keyword_api_sources WHERE id=$1", id)
	if err != nil {
		return fmt.Errorf("delete keyword API source: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ClaimDueKeywordAPISource(ctx context.Context, at time.Time) (*KeywordAPISource, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("storage is disabled")
	}
	if at.IsZero() {
		at = s.now()
	}
	source, err := scanKeywordAPISource(s.pool.QueryRow(ctx, `WITH candidate AS (
		SELECT id AS source_id FROM keyword_api_sources
		WHERE enabled AND next_sync_at IS NOT NULL AND next_sync_at <= $1
		ORDER BY next_sync_at, id FOR UPDATE SKIP LOCKED LIMIT 1
	) UPDATE keyword_api_sources source SET
		last_status='running', last_error='',
		next_sync_at=$1 + source.sync_interval_seconds * interval '1 second', updated_at=now()
	FROM candidate WHERE source.id=candidate.source_id RETURNING `+keywordAPISourceColumns, at))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim due keyword API source: %w", err)
	}
	return &source, nil
}

func (s *Store) ClaimKeywordAPISourceForSync(ctx context.Context, id int64, at time.Time) (KeywordAPISource, error) {
	if s == nil || s.pool == nil {
		return KeywordAPISource{}, fmt.Errorf("storage is disabled")
	}
	if at.IsZero() {
		at = s.now()
	}
	source, err := scanKeywordAPISource(s.pool.QueryRow(ctx, `UPDATE keyword_api_sources source SET
		last_status='running', last_error='',
		next_sync_at=$2 + source.sync_interval_seconds * interval '1 second', updated_at=now()
	WHERE id=$1 AND btrim(request_url)<>'' AND btrim(response_path)<>''
		AND (last_status<>'running' OR next_sync_at IS NULL OR next_sync_at <= $2)
	RETURNING `+keywordAPISourceColumns, id, at))
	if errors.Is(err, pgx.ErrNoRows) {
		current, getErr := s.GetKeywordAPISource(ctx, id)
		if getErr != nil {
			return KeywordAPISource{}, getErr
		}
		if strings.TrimSpace(current.RequestURL) == "" || strings.TrimSpace(current.ResponsePath) == "" {
			return KeywordAPISource{}, fmt.Errorf("%w: source is incomplete", ErrInvalid)
		}
		return KeywordAPISource{}, ErrConflict
	}
	if err != nil {
		return KeywordAPISource{}, fmt.Errorf("claim keyword API source: %w", err)
	}
	return source, nil
}

func (s *Store) CompleteKeywordAPISourceSync(ctx context.Context, input KeywordAPISourceSyncInput) (KeywordAPISourceSyncResult, error) {
	if s == nil || s.pool == nil {
		return KeywordAPISourceSyncResult{}, fmt.Errorf("storage is disabled")
	}
	if input.SourceID <= 0 {
		return KeywordAPISourceSyncResult{}, fmt.Errorf("%w: source ID", ErrInvalid)
	}
	if input.SyncedAt.IsZero() {
		input.SyncedAt = s.now()
	}
	status, errorMessage, requestCount, successCount, failureCount, err := normalizeKeywordAPISourceSyncCompletion(input)
	if err != nil {
		return KeywordAPISourceSyncResult{}, err
	}
	values := normalizeKeywordAPISourceValues(input.Values)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return KeywordAPISourceSyncResult{}, fmt.Errorf("begin keyword API source sync: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	source, err := scanKeywordAPISource(tx.QueryRow(ctx, "SELECT "+keywordAPISourceColumns+" FROM keyword_api_sources WHERE id=$1 FOR UPDATE", input.SourceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return KeywordAPISourceSyncResult{}, ErrNotFound
	}
	if err != nil {
		return KeywordAPISourceSyncResult{}, fmt.Errorf("lock keyword API source for sync: %w", err)
	}
	result := KeywordAPISourceSyncResult{Seen: len(input.Values), Unique: len(values), LinkedItems: len(values)}
	for _, value := range values {
		command, insertErr := tx.Exec(ctx, `INSERT INTO keywords (
			keyword, normalized_keyword, keyword_type, source_type, source_key, external_id,
			source_metadata, enabled, priority, cooldown_seconds
		) VALUES ($1,$2,$3,'api',$4,$2,$5::jsonb,$6,$7,$8)
		ON CONFLICT (normalized_keyword) DO NOTHING`,
			value.External, value.Normalized, source.DefaultKeywordType, strconv.FormatInt(source.ID, 10),
			metadataJSON(map[string]any{"keyword_api_source_id": source.ID, "keyword_api_source_name": source.Name}),
			source.DefaultKeywordEnabled, source.DefaultPriority, source.DefaultCooldownSeconds,
		)
		if insertErr != nil {
			return KeywordAPISourceSyncResult{}, mapWriteError("upsert API keyword", insertErr)
		}
		inserted := command.RowsAffected() == 1
		var keywordID int64
		if err := tx.QueryRow(ctx, "SELECT id FROM keywords WHERE normalized_keyword=$1", value.Normalized).Scan(&keywordID); err != nil {
			return KeywordAPISourceSyncResult{}, fmt.Errorf("resolve API keyword: %w", err)
		}
		if inserted {
			result.InsertedKeywords++
			if _, err := tx.Exec(ctx, `UPDATE resource_keywords SET keyword_id=$1
				WHERE normalized_keyword=$2 AND keyword_id IS NULL`, keywordID, value.Normalized); err != nil {
				return KeywordAPISourceSyncResult{}, fmt.Errorf("attach API keyword resources: %w", err)
			}
		} else {
			result.ExistingKeywords++
		}
		if _, err := tx.Exec(ctx, `INSERT INTO keyword_api_source_items (
			source_id, keyword_id, external_value, normalized_value, first_seen_at, last_seen_at
		) VALUES ($1,$2,$3,$4,$5,$5)
		ON CONFLICT (source_id, normalized_value) DO UPDATE SET
			keyword_id=EXCLUDED.keyword_id, external_value=EXCLUDED.external_value,
			last_seen_at=EXCLUDED.last_seen_at`, source.ID, keywordID, value.External, value.Normalized, input.SyncedAt); err != nil {
			return KeywordAPISourceSyncResult{}, fmt.Errorf("upsert keyword API source item: %w", err)
		}
	}
	source, err = scanKeywordAPISource(tx.QueryRow(ctx, `UPDATE keyword_api_sources SET
		last_status=$2, last_error=$3, last_item_count=$4, last_synced_at=$5,
		last_request_count=$6, last_success_count=$7, last_failure_count=$8,
		next_sync_at=$5::timestamptz + sync_interval_seconds * interval '1 second', updated_at=now()
	WHERE id=$1 RETURNING `+keywordAPISourceColumns, source.ID, status, errorMessage, len(values), input.SyncedAt,
		requestCount, successCount, failureCount))
	if err != nil {
		return KeywordAPISourceSyncResult{}, fmt.Errorf("complete keyword API source sync: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return KeywordAPISourceSyncResult{}, fmt.Errorf("commit keyword API source sync: %w", err)
	}
	result.Source = source
	return result, nil
}

func (s *Store) FailKeywordAPISourceSync(ctx context.Context, id int64, errorMessage string, failedAt time.Time) (KeywordAPISource, error) {
	return s.FailKeywordAPISourceSyncWithStats(ctx, id, errorMessage, failedAt, 1, 1)
}

func (s *Store) FailKeywordAPISourceSyncWithStats(ctx context.Context, id int64, errorMessage string, failedAt time.Time, requestCount, failureCount int) (KeywordAPISource, error) {
	if s == nil || s.pool == nil {
		return KeywordAPISource{}, fmt.Errorf("storage is disabled")
	}
	if requestCount < 1 || failureCount != requestCount {
		return KeywordAPISource{}, fmt.Errorf("%w: sync failure counts", ErrInvalid)
	}
	if failedAt.IsZero() {
		failedAt = s.now()
	}
	errorMessage = truncateKeywordAPIError(errorMessage)
	source, err := scanKeywordAPISource(s.pool.QueryRow(ctx, `UPDATE keyword_api_sources SET
		last_status='failed', last_error=$2, last_item_count=0, last_synced_at=$3,
		last_request_count=$4, last_success_count=0, last_failure_count=$5,
		next_sync_at=$3::timestamptz + sync_interval_seconds * interval '1 second', updated_at=now()
	WHERE id=$1 RETURNING `+keywordAPISourceColumns, id, errorMessage, failedAt, requestCount, failureCount))
	if errors.Is(err, pgx.ErrNoRows) {
		return KeywordAPISource{}, ErrNotFound
	}
	if err != nil {
		return KeywordAPISource{}, fmt.Errorf("fail keyword API source sync: %w", err)
	}
	return source, nil
}

func (s *Store) ListKeywordAPISourceItems(ctx context.Context, sourceID int64) ([]KeywordAPISourceItem, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("storage is disabled")
	}
	rows, err := s.pool.Query(ctx, `SELECT source_id, keyword_id, external_value,
		normalized_value, first_seen_at, last_seen_at
	FROM keyword_api_source_items WHERE source_id=$1 ORDER BY first_seen_at, normalized_value`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("list keyword API source items: %w", err)
	}
	defer rows.Close()
	items := make([]KeywordAPISourceItem, 0)
	for rows.Next() {
		var item KeywordAPISourceItem
		if err := rows.Scan(&item.SourceID, &item.KeywordID, &item.ExternalValue, &item.NormalizedValue, &item.FirstSeenAt, &item.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scan keyword API source item: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type normalizedKeywordAPIValue struct {
	External   string
	Normalized string
}

func normalizeKeywordAPISourceSyncCompletion(input KeywordAPISourceSyncInput) (string, string, int, int, int, error) {
	status := strings.ToLower(strings.TrimSpace(input.Status))
	if status == "" {
		status = KeywordAPISourceStatusSuccess
	}
	requestCount, successCount, failureCount := input.RequestCount, input.SuccessCount, input.FailureCount
	if requestCount == 0 && successCount == 0 && failureCount == 0 && status == KeywordAPISourceStatusSuccess {
		requestCount, successCount = 1, 1
	}
	if requestCount < 1 || successCount < 0 || failureCount < 0 || successCount+failureCount != requestCount {
		return "", "", 0, 0, 0, fmt.Errorf("%w: sync completion counts", ErrInvalid)
	}
	switch status {
	case KeywordAPISourceStatusSuccess:
		if successCount < 1 || failureCount != 0 {
			return "", "", 0, 0, 0, fmt.Errorf("%w: success sync counts", ErrInvalid)
		}
		return status, "", requestCount, successCount, failureCount, nil
	case KeywordAPISourceStatusPartial:
		if successCount < 1 || failureCount < 1 {
			return "", "", 0, 0, 0, fmt.Errorf("%w: partial sync counts", ErrInvalid)
		}
		return status, truncateKeywordAPIError(input.ErrorMessage), requestCount, successCount, failureCount, nil
	default:
		return "", "", 0, 0, 0, fmt.Errorf("%w: sync completion status", ErrInvalid)
	}
}

func normalizeKeywordAPISourceValues(values []string) []normalizedKeywordAPIValue {
	seen := make(map[string]struct{}, len(values))
	result := make([]normalizedKeywordAPIValue, 0, len(values))
	for _, value := range values {
		external := strings.TrimSpace(value)
		normalized := NormalizeKeyword(external)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalizedKeywordAPIValue{External: external, Normalized: normalized})
	}
	return result
}

func normalizeKeywordAPISourceCreate(input CreateKeywordAPISourceInput, now time.Time) (KeywordAPISource, error) {
	method := strings.ToUpper(strings.TrimSpace(input.RequestMethod))
	if method == "" {
		method = "GET"
	}
	bodyType := strings.ToLower(strings.TrimSpace(input.BodyType))
	if bodyType == "" {
		bodyType = "none"
	}
	timeout := input.TimeoutSeconds
	if timeout == 0 {
		timeout = KeywordAPISourceDefaultTimeoutSeconds
	}
	interval := input.SyncIntervalSeconds
	if interval == 0 {
		interval = KeywordAPISourceDefaultIntervalSeconds
	}
	keywordType := strings.TrimSpace(input.DefaultKeywordType)
	if keywordType == "" {
		keywordType = DefaultKeywordType
	}
	defaultEnabled := true
	if input.DefaultKeywordEnabled != nil {
		defaultEnabled = *input.DefaultKeywordEnabled
	}
	nextSync := input.NextSyncAt
	if input.Enabled && nextSync == nil {
		value := now
		nextSync = &value
	}
	iterationLocation := strings.ToLower(strings.TrimSpace(input.IterationLocation))
	if iterationLocation == "" {
		iterationLocation = "query"
	}
	iterationCount := input.IterationCount
	if iterationCount == 0 {
		iterationCount = 1
	}
	iterationStep := input.IterationStep
	if !input.IterationEnabled && input.IterationLocation == "" && input.IterationPath == "" && input.IterationCount == 0 && input.IterationDelaySeconds == 0 && input.IterationStart == 0 && input.IterationStep == 0 {
		iterationStep = 20
	}
	source := KeywordAPISource{
		Name: strings.TrimSpace(input.Name), Enabled: input.Enabled, RequestMethod: method,
		RequestURL: strings.TrimSpace(input.RequestURL), RequestHeaders: cloneStringMap(input.RequestHeaders),
		QueryParams: cloneStringMap(input.QueryParams), BodyType: bodyType, RequestBody: input.RequestBody,
		ProxyURL: strings.TrimSpace(input.ProxyURL), TimeoutSeconds: timeout,
		ResponsePath: strings.TrimSpace(input.ResponsePath), SyncIntervalSeconds: interval,
		DefaultKeywordType: keywordType, DefaultKeywordEnabled: defaultEnabled,
		DefaultPriority: input.DefaultPriority, DefaultCooldownSeconds: input.DefaultCooldownSeconds,
		IterationEnabled: input.IterationEnabled, IterationLocation: iterationLocation,
		IterationPath: strings.TrimSpace(input.IterationPath), IterationStart: input.IterationStart,
		IterationStep: iterationStep, IterationCount: iterationCount,
		IterationDelaySeconds: input.IterationDelaySeconds,
		NextSyncAt:            nextSync, LastStatus: KeywordAPISourceStatusPending,
	}
	return source, validateKeywordAPISource(source)
}

func applyKeywordAPISourceUpdate(source KeywordAPISource, input UpdateKeywordAPISourceInput, now time.Time) KeywordAPISource {
	wasEnabled := source.Enabled
	if input.Name != nil {
		source.Name = strings.TrimSpace(*input.Name)
	}
	if input.Enabled != nil {
		source.Enabled = *input.Enabled
	}
	if input.RequestMethod != nil {
		source.RequestMethod = strings.ToUpper(strings.TrimSpace(*input.RequestMethod))
	}
	if input.RequestURL != nil {
		source.RequestURL = strings.TrimSpace(*input.RequestURL)
	}
	if input.RequestHeaders != nil {
		source.RequestHeaders = cloneStringMap(*input.RequestHeaders)
	}
	if input.QueryParams != nil {
		source.QueryParams = cloneStringMap(*input.QueryParams)
	}
	if input.BodyType != nil {
		source.BodyType = strings.ToLower(strings.TrimSpace(*input.BodyType))
	}
	if input.RequestBody != nil {
		source.RequestBody = *input.RequestBody
	}
	if input.ProxyURL != nil {
		source.ProxyURL = strings.TrimSpace(*input.ProxyURL)
	}
	if input.TimeoutSeconds != nil {
		source.TimeoutSeconds = *input.TimeoutSeconds
	}
	if input.ResponsePath != nil {
		source.ResponsePath = strings.TrimSpace(*input.ResponsePath)
	}
	if input.SyncIntervalSeconds != nil {
		source.SyncIntervalSeconds = *input.SyncIntervalSeconds
	}
	if input.DefaultKeywordType != nil {
		source.DefaultKeywordType = strings.TrimSpace(*input.DefaultKeywordType)
	}
	if input.DefaultKeywordEnabled != nil {
		source.DefaultKeywordEnabled = *input.DefaultKeywordEnabled
	}
	if input.DefaultPriority != nil {
		source.DefaultPriority = *input.DefaultPriority
	}
	if input.DefaultCooldownSeconds != nil {
		source.DefaultCooldownSeconds = *input.DefaultCooldownSeconds
	}
	if input.IterationEnabled != nil {
		source.IterationEnabled = *input.IterationEnabled
	}
	if input.IterationLocation != nil {
		source.IterationLocation = strings.ToLower(strings.TrimSpace(*input.IterationLocation))
	}
	if input.IterationPath != nil {
		source.IterationPath = strings.TrimSpace(*input.IterationPath)
	}
	if input.IterationStart != nil {
		source.IterationStart = *input.IterationStart
	}
	if input.IterationStep != nil {
		source.IterationStep = *input.IterationStep
	}
	if input.IterationCount != nil {
		source.IterationCount = *input.IterationCount
	}
	if input.IterationDelaySeconds != nil {
		source.IterationDelaySeconds = *input.IterationDelaySeconds
	}
	if input.NextSyncAt != nil {
		source.NextSyncAt = *input.NextSyncAt
	}
	if source.Enabled && !wasEnabled && input.NextSyncAt == nil {
		value := now
		source.NextSyncAt = &value
	}
	return source
}

func validateKeywordAPISource(source KeywordAPISource) error {
	if source.Name == "" {
		return fmt.Errorf("%w: empty source name", ErrInvalid)
	}
	switch source.RequestMethod {
	case "GET", "POST", "PUT", "PATCH":
	default:
		return fmt.Errorf("%w: request method", ErrInvalid)
	}
	switch source.BodyType {
	case "none", "json", "form", "raw":
	default:
		return fmt.Errorf("%w: body type", ErrInvalid)
	}
	switch source.IterationLocation {
	case "query", "header", "body":
	default:
		return fmt.Errorf("%w: iteration location", ErrInvalid)
	}
	if source.IterationCount < 1 || source.IterationCount > 100 {
		return fmt.Errorf("%w: iteration count", ErrInvalid)
	}
	if source.IterationDelaySeconds < 0 || source.IterationDelaySeconds > 3600 {
		return fmt.Errorf("%w: iteration delay", ErrInvalid)
	}
	if source.IterationEnabled {
		if source.IterationPath == "" {
			return fmt.Errorf("%w: iteration path", ErrInvalid)
		}
		if source.IterationLocation == "body" && source.BodyType != "json" && source.BodyType != "form" {
			return fmt.Errorf("%w: body iteration requires JSON or form body", ErrInvalid)
		}
	}
	if source.TimeoutSeconds < 1 || source.TimeoutSeconds > 60 {
		return fmt.Errorf("%w: timeout", ErrInvalid)
	}
	if source.SyncIntervalSeconds < 60 {
		return fmt.Errorf("%w: sync interval", ErrInvalid)
	}
	if source.DefaultKeywordType == "" {
		return fmt.Errorf("%w: default keyword type", ErrInvalid)
	}
	if source.DefaultCooldownSeconds != nil && *source.DefaultCooldownSeconds < 0 {
		return fmt.Errorf("%w: default cooldown", ErrInvalid)
	}
	if source.RequestURL != "" {
		if err := validateKeywordAPIURL(source.RequestURL, map[string]bool{"http": true, "https": true}); err != nil {
			return err
		}
	}
	if source.ProxyURL != "" {
		if err := validateKeywordAPIURL(source.ProxyURL, map[string]bool{"http": true, "https": true, "socks5": true, "socks5h": true}); err != nil {
			return fmt.Errorf("%w: proxy URL", ErrInvalid)
		}
	}
	if source.BodyType == "json" && strings.TrimSpace(source.RequestBody) != "" && !json.Valid([]byte(source.RequestBody)) {
		return fmt.Errorf("%w: JSON request body", ErrInvalid)
	}
	if source.Enabled && (source.RequestURL == "" || source.ResponsePath == "") {
		return fmt.Errorf("%w: enabled source is incomplete", ErrInvalid)
	}
	return nil
}

func validateKeywordAPIURL(raw string, schemes map[string]bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || !schemes[strings.ToLower(parsed.Scheme)] || parsed.Host == "" {
		return fmt.Errorf("%w: request URL", ErrInvalid)
	}
	return nil
}

func encodeStringMap(value map[string]string) []byte {
	if value == nil {
		return []byte("{}")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return data
}

func decodeStringMap(data []byte) map[string]string {
	result := make(map[string]string)
	_ = json.Unmarshal(data, &result)
	return result
}

func cloneStringMap(value map[string]string) map[string]string {
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func truncateKeywordAPIError(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 2000 {
		runes = runes[:2000]
	}
	return string(runes)
}
