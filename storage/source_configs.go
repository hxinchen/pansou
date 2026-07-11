package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const searchSourceConfigColumns = `
	id, version, schema_version, config, updated_by, created_at, updated_at`

const searchSourceConfigEventColumns = `
	id, actor_user_id, base_version, result_version, result, error_code,
	change_summary, created_at`

func scanSearchSourceConfig(row rowScanner) (SearchSourceConfig, error) {
	var config SearchSourceConfig
	var document []byte
	err := row.Scan(&config.ID, &config.Version, &config.SchemaVersion, &document,
		&config.UpdatedBy, &config.CreatedAt, &config.UpdatedAt)
	config.Config = append(json.RawMessage(nil), document...)
	return config, err
}

func scanSearchSourceConfigEvent(row rowScanner) (SearchSourceConfigEvent, error) {
	var event SearchSourceConfigEvent
	var summary []byte
	err := row.Scan(&event.ID, &event.ActorUserID, &event.BaseVersion,
		&event.ResultVersion, &event.Result, &event.ErrorCode, &summary, &event.CreatedAt)
	if err == nil {
		event.ChangeSummary = decodeStrictJSONObject(summary)
	}
	return event, err
}

func (s *Store) GetSearchSourceConfig(ctx context.Context) (SearchSourceConfig, error) {
	if s == nil || s.pool == nil {
		return SearchSourceConfig{}, fmt.Errorf("storage is disabled")
	}
	config, err := scanSearchSourceConfig(s.pool.QueryRow(ctx,
		`SELECT `+searchSourceConfigColumns+` FROM search_source_configs WHERE id=1`))
	if errors.Is(err, pgx.ErrNoRows) {
		return SearchSourceConfig{}, ErrNotFound
	}
	if err != nil {
		return SearchSourceConfig{}, fmt.Errorf("get search source config: %w", err)
	}
	return config, nil
}

func (s *Store) InitializeSearchSourceConfig(ctx context.Context, input InitializeSearchSourceConfigInput) (SearchSourceConfig, bool, error) {
	if s == nil || s.pool == nil {
		return SearchSourceConfig{}, false, fmt.Errorf("storage is disabled")
	}
	document, err := validateRawJSONObject(input.Config)
	if err != nil {
		return SearchSourceConfig{}, false, fmt.Errorf("%w: source config: %v", ErrInvalid, err)
	}
	if input.SchemaVersion <= 0 {
		return SearchSourceConfig{}, false, fmt.Errorf("%w: source config schema version", ErrInvalid)
	}
	config, err := scanSearchSourceConfig(s.pool.QueryRow(ctx, `
		INSERT INTO search_source_configs (id,version,schema_version,config,updated_by)
		VALUES(1,1,$1,$2::jsonb,$3)
		ON CONFLICT (id) DO NOTHING
		RETURNING `+searchSourceConfigColumns, input.SchemaVersion, document, input.UpdatedBy))
	if err == nil {
		return config, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return SearchSourceConfig{}, false, mapWriteError("initialize search source config", err)
	}
	config, err = s.GetSearchSourceConfig(ctx)
	return config, false, err
}

func (s *Store) CompareAndSwapSearchSourceConfig(ctx context.Context, input UpdateSearchSourceConfigInput) (SearchSourceConfig, error) {
	if s == nil || s.pool == nil {
		return SearchSourceConfig{}, fmt.Errorf("storage is disabled")
	}
	document, err := validateRawJSONObject(input.Config)
	if err != nil {
		return SearchSourceConfig{}, fmt.Errorf("%w: source config: %v", ErrInvalid, err)
	}
	if input.ExpectedVersion <= 0 || input.SchemaVersion <= 0 {
		return SearchSourceConfig{}, fmt.Errorf("%w: source config version", ErrInvalid)
	}
	summary, err := marshalStrictJSON(input.ChangeSummary)
	if err != nil {
		return SearchSourceConfig{}, fmt.Errorf("%w: source config summary: %v", ErrInvalid, err)
	}
	updatedAt := input.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = s.now()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SearchSourceConfig{}, fmt.Errorf("begin source config update: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	config, err := scanSearchSourceConfig(tx.QueryRow(ctx, `
		UPDATE search_source_configs SET version=version+1, schema_version=$2,
			config=$3::jsonb, updated_by=$4, updated_at=$5
		WHERE id=1 AND version=$1 RETURNING `+searchSourceConfigColumns,
		input.ExpectedVersion, input.SchemaVersion, document, input.UpdatedBy, updatedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if scanErr := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM search_source_configs WHERE id=1)`).Scan(&exists); scanErr != nil {
			return SearchSourceConfig{}, fmt.Errorf("check source config conflict: %w", scanErr)
		}
		if !exists {
			return SearchSourceConfig{}, ErrNotFound
		}
		if _, eventErr := insertSearchSourceConfigEventTx(ctx, tx, CreateSearchSourceConfigEventInput{
			ActorUserID: input.UpdatedBy, BaseVersion: input.ExpectedVersion,
			Result: SourceConfigEventFailed, ErrorCode: "source_config_conflict",
			ChangeSummary: input.ChangeSummary, CreatedAt: updatedAt,
		}); eventErr != nil {
			return SearchSourceConfig{}, eventErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return SearchSourceConfig{}, fmt.Errorf("commit source config conflict event: %w", commitErr)
		}
		return SearchSourceConfig{}, fmt.Errorf("%w: search source config version", ErrConflict)
	}
	if err != nil {
		return SearchSourceConfig{}, mapWriteError("update search source config", err)
	}
	resultVersion := config.Version
	if _, err := scanSearchSourceConfigEvent(tx.QueryRow(ctx, `
		INSERT INTO search_source_config_events (
			actor_user_id,base_version,result_version,result,error_code,change_summary,created_at
		) VALUES ($1,$2,$3,'success','',$4::jsonb,$5)
		RETURNING `+searchSourceConfigEventColumns,
		input.UpdatedBy, input.ExpectedVersion, resultVersion, summary, updatedAt)); err != nil {
		return SearchSourceConfig{}, fmt.Errorf("record source config success: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return SearchSourceConfig{}, fmt.Errorf("commit source config update: %w", err)
	}
	return config, nil
}

func (s *Store) RecordSearchSourceConfigEvent(ctx context.Context, input CreateSearchSourceConfigEventInput) (SearchSourceConfigEvent, error) {
	if s == nil || s.pool == nil {
		return SearchSourceConfigEvent{}, fmt.Errorf("storage is disabled")
	}
	if input.CreatedAt.IsZero() {
		input.CreatedAt = s.now()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SearchSourceConfigEvent{}, fmt.Errorf("begin source config event: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	event, err := insertSearchSourceConfigEventTx(ctx, tx, input)
	if err != nil {
		return SearchSourceConfigEvent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SearchSourceConfigEvent{}, fmt.Errorf("commit source config event: %w", err)
	}
	return event, nil
}

func insertSearchSourceConfigEventTx(ctx context.Context, tx pgx.Tx, input CreateSearchSourceConfigEventInput) (SearchSourceConfigEvent, error) {
	if _, err := validateSearchSourceConfigEventInput(input); err != nil {
		return SearchSourceConfigEvent{}, err
	}
	summary, err := marshalStrictJSON(input.ChangeSummary)
	if err != nil {
		return SearchSourceConfigEvent{}, fmt.Errorf("%w: source config event summary: %v", ErrInvalid, err)
	}
	createdAt := input.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	event, err := scanSearchSourceConfigEvent(tx.QueryRow(ctx, `
		INSERT INTO search_source_config_events (
			actor_user_id,base_version,result_version,result,error_code,change_summary,created_at
		) VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7)
		RETURNING `+searchSourceConfigEventColumns,
		input.ActorUserID, input.BaseVersion, input.ResultVersion, input.Result,
		strings.TrimSpace(input.ErrorCode), summary, createdAt))
	if err != nil {
		return SearchSourceConfigEvent{}, mapWriteError("record source config event", err)
	}
	return event, nil
}

func validateSearchSourceConfigEventInput(input CreateSearchSourceConfigEventInput) (CreateSearchSourceConfigEventInput, error) {
	if input.Result != SourceConfigEventSuccess && input.Result != SourceConfigEventFailed {
		return input, fmt.Errorf("%w: source config event result %q", ErrInvalid, input.Result)
	}
	if input.BaseVersion < 0 ||
		input.Result == SourceConfigEventSuccess && (input.ResultVersion == nil || *input.ResultVersion != input.BaseVersion+1 || input.ErrorCode != "") ||
		input.Result == SourceConfigEventFailed && (input.ResultVersion != nil || strings.TrimSpace(input.ErrorCode) == "") {
		return input, fmt.Errorf("%w: source config event version", ErrInvalid)
	}
	return input, nil
}

func (s *Store) ListSearchSourceConfigEvents(ctx context.Context, filter SearchSourceConfigEventFilter) (SearchSourceConfigEventPage, error) {
	if s == nil || s.pool == nil {
		return SearchSourceConfigEventPage{}, fmt.Errorf("storage is disabled")
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize, 50, 200)
	conditions := []string{"TRUE"}
	args := make([]any, 0, 5)
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if filter.ActorUserID != nil {
		conditions = append(conditions, "actor_user_id="+addArg(*filter.ActorUserID))
	}
	if results := normalizeStringList(filter.Results); len(results) > 0 {
		for _, result := range results {
			if result != SourceConfigEventSuccess && result != SourceConfigEventFailed {
				return SearchSourceConfigEventPage{}, fmt.Errorf("%w: source config event result %q", ErrInvalid, result)
			}
		}
		conditions = append(conditions, "result=ANY("+addArg(results)+"::text[])")
	}
	if filter.From != nil {
		conditions = append(conditions, "created_at>="+addArg(*filter.From))
	}
	if filter.To != nil {
		conditions = append(conditions, "created_at<"+addArg(*filter.To))
	}
	where := strings.Join(conditions, " AND ")
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM search_source_config_events WHERE `+where, args...).Scan(&total); err != nil {
		return SearchSourceConfigEventPage{}, fmt.Errorf("count source config events: %w", err)
	}
	queryArgs := append(append([]any(nil), args...), pageSize, (page-1)*pageSize)
	rows, err := s.pool.Query(ctx, `SELECT `+searchSourceConfigEventColumns+`
		FROM search_source_config_events WHERE `+where+
		` ORDER BY created_at DESC, id DESC`+fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), queryArgs...)
	if err != nil {
		return SearchSourceConfigEventPage{}, fmt.Errorf("list source config events: %w", err)
	}
	defer rows.Close()
	items := make([]SearchSourceConfigEvent, 0, pageSize)
	for rows.Next() {
		event, scanErr := scanSearchSourceConfigEvent(rows)
		if scanErr != nil {
			return SearchSourceConfigEventPage{}, fmt.Errorf("scan source config event: %w", scanErr)
		}
		items = append(items, event)
	}
	if err := rows.Err(); err != nil {
		return SearchSourceConfigEventPage{}, fmt.Errorf("iterate source config events: %w", err)
	}
	return SearchSourceConfigEventPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func validateRawJSONObject(value json.RawMessage) ([]byte, error) {
	if len(value) == 0 || !json.Valid(value) {
		return nil, errors.New("invalid JSON")
	}
	var object map[string]any
	if err := json.Unmarshal(value, &object); err != nil || object == nil {
		return nil, errors.New("JSON value must be an object")
	}
	return append([]byte(nil), value...), nil
}

func marshalStrictJSON(value map[string]any) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage(`{}`), nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func decodeStrictJSONObject(data []byte) map[string]any {
	result := make(map[string]any)
	if len(data) > 0 {
		_ = json.Unmarshal(data, &result)
	}
	return result
}
