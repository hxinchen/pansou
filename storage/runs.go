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

const (
	runErrorSummaryLimit      = 4096
	runErrorItemLimit         = 500
	runErrorDisplayLimit      = 5
	runMissingErrorDetailText = "failed without error detail"
)

const runSelect = `
	SELECT cr.id, cr.trigger, cr.status, cr.forced,
		count(i.id)::int AS total_items,
		count(i.id) FILTER (WHERE i.status='pending')::int AS pending_items,
		count(i.id) FILTER (WHERE i.status='running')::int AS running_items,
		count(i.id) FILTER (WHERE i.status IN ('success','success_empty','failed'))::int AS completed_items,
		count(i.id) FILTER (WHERE i.status='success')::int AS success_items,
		count(i.id) FILTER (WHERE i.status='success_empty')::int AS empty_items,
		count(i.id) FILTER (WHERE i.status='failed')::int AS failed_items,
		COALESCE(sum(i.found_count),0)::int AS found_count,
		COALESCE(sum(i.new_count),0)::int AS new_count,
		COALESCE(sum(i.duplicate_count),0)::int AS duplicate_count,
		left(cr.error_message, 4096), cr.created_at, cr.started_at, cr.completed_at
	FROM collection_runs cr
	LEFT JOIN collection_run_items i ON i.run_id=cr.id`

const runGroup = ` GROUP BY cr.id, cr.trigger, cr.status, cr.forced,
	cr.error_message, cr.created_at, cr.started_at, cr.completed_at`

func scanRun(row rowScanner) (CollectionRun, error) {
	var run CollectionRun
	err := row.Scan(
		&run.ID, &run.Trigger, &run.Status, &run.Forced, &run.TotalItems,
		&run.PendingItems, &run.RunningItems, &run.CompletedItems, &run.SuccessItems, &run.EmptyItems,
		&run.FailedItems, &run.FoundCount, &run.NewCount, &run.DuplicateCount,
		&run.ErrorMessage, &run.CreatedAt, &run.StartedAt, &run.CompletedAt,
	)
	if err != nil {
		return CollectionRun{}, err
	}
	return run, nil
}

const runItemColumns = `
	id, run_id, keyword_id, keyword, normalized_keyword, keyword_type, priority, cooldown_seconds,
	status, attempts, found_count, new_count, duplicate_count, source_summary,
	error_message, created_at, started_at, completed_at,
	CASE WHEN started_at IS NULL THEN 0 ELSE
		(EXTRACT(EPOCH FROM (COALESCE(completed_at, now())-started_at))*1000)::bigint END`

func scanRunItem(row rowScanner) (CollectionRunItem, error) {
	var item CollectionRunItem
	var sourceSummary []byte
	err := row.Scan(
		&item.ID, &item.RunID, &item.KeywordID, &item.Keyword, &item.NormalizedKeyword,
		&item.KeywordType, &item.Priority, &item.CooldownSeconds, &item.Status, &item.Attempts,
		&item.FoundCount, &item.NewCount, &item.DuplicateCount, &sourceSummary,
		&item.ErrorMessage, &item.CreatedAt, &item.StartedAt, &item.CompletedAt, &item.DurationMS,
	)
	if err != nil {
		return CollectionRunItem{}, err
	}
	item.SourceSummary = decodeMetadata(sourceSummary)
	return item, nil
}

const runItemSummaryColumns = `
	i.id, i.run_id, i.keyword_id, i.keyword, i.normalized_keyword, i.keyword_type,
	i.priority, i.cooldown_seconds, i.status, i.attempts, i.found_count, i.new_count,
	i.duplicate_count,
	COALESCE(jsonb_object_length(i.source_summary), 0),
	COALESCE((SELECT count(*) FROM jsonb_each(i.source_summary) s WHERE s.value->>'status'='success'), 0),
	COALESCE((SELECT count(*) FROM jsonb_each(i.source_summary) s WHERE s.value->>'status'='success_empty'), 0),
	COALESCE((SELECT count(*) FROM jsonb_each(i.source_summary) s WHERE s.value->>'status'='failed'), 0),
	left(i.error_message, 500), i.created_at, i.started_at, i.completed_at,
	CASE WHEN i.started_at IS NULL THEN 0 ELSE
		(EXTRACT(EPOCH FROM (COALESCE(i.completed_at, now())-i.started_at))*1000)::bigint END`

func scanRunItemSummary(row rowScanner) (CollectionRunItem, error) {
	var item CollectionRunItem
	err := row.Scan(
		&item.ID, &item.RunID, &item.KeywordID, &item.Keyword, &item.NormalizedKeyword,
		&item.KeywordType, &item.Priority, &item.CooldownSeconds, &item.Status, &item.Attempts,
		&item.FoundCount, &item.NewCount, &item.DuplicateCount, &item.SourceTotal,
		&item.SourceSuccess, &item.SourceEmpty, &item.SourceFailed, &item.ErrorMessage,
		&item.CreatedAt, &item.StartedAt, &item.CompletedAt, &item.DurationMS,
	)
	return item, err
}

func (s *Store) CreateRun(ctx context.Context, input CreateRunInput) (CollectionRun, error) {
	if s == nil || s.pool == nil {
		return CollectionRun{}, fmt.Errorf("storage is disabled")
	}
	trigger := strings.TrimSpace(input.Trigger)
	if trigger == "" {
		trigger = "manual"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CollectionRun{}, fmt.Errorf("begin create run: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	items, err := s.snapshotRunItems(ctx, tx, input)
	if err != nil {
		return CollectionRun{}, err
	}
	status := RunPending
	var completedAt *time.Time
	if len(items) == 0 {
		status = RunSuccessEmpty
		now := s.now()
		completedAt = &now
	}
	var runID int64
	if err := tx.QueryRow(ctx, `INSERT INTO collection_runs(trigger,status,forced,completed_at)
		VALUES($1,$2,$3,$4) RETURNING id`, trigger, status, input.Force, completedAt).Scan(&runID); err != nil {
		return CollectionRun{}, fmt.Errorf("insert collection run: %w", err)
	}
	for _, item := range items {
		if _, err := tx.Exec(ctx, `INSERT INTO collection_run_items (
			run_id, keyword_id, keyword, normalized_keyword, keyword_type, priority, cooldown_seconds
		) VALUES ($1,$2,$3,$4,$5,$6,$7)`, runID, item.KeywordID, item.Keyword,
			item.NormalizedKeyword, item.KeywordType, item.Priority, item.CooldownSeconds); err != nil {
			return CollectionRun{}, fmt.Errorf("insert collection run item: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return CollectionRun{}, fmt.Errorf("commit create run: %w", err)
	}
	return s.GetRun(ctx, runID)
}

func (s *Store) snapshotRunItems(ctx context.Context, tx pgx.Tx, input CreateRunInput) ([]CollectionRunItem, error) {
	items := make([]CollectionRunItem, 0)
	seen := make(map[string]struct{})
	appendKeyword := func(keyword Keyword) {
		if _, exists := seen[keyword.NormalizedKeyword]; exists {
			return
		}
		seen[keyword.NormalizedKeyword] = struct{}{}
		id := keyword.ID
		items = append(items, CollectionRunItem{KeywordID: &id, Keyword: keyword.Keyword,
			NormalizedKeyword: keyword.NormalizedKeyword, KeywordType: keyword.KeywordType,
			Priority: keyword.Priority, CooldownSeconds: keyword.CooldownSeconds})
	}

	if len(input.KeywordIDs) > 0 {
		rows, err := tx.Query(ctx, "SELECT "+keywordColumns+` FROM keywords
			WHERE id=ANY($1::bigint[]) AND enabled
				AND ($2 OR next_eligible_at IS NULL OR next_eligible_at <= $3)
			ORDER BY priority DESC, id FOR SHARE`, input.KeywordIDs, input.Force, s.now())
		if err != nil {
			return nil, fmt.Errorf("snapshot selected keywords: %w", err)
		}
		for rows.Next() {
			keyword, scanErr := scanKeyword(rows)
			if scanErr != nil {
				rows.Close()
				return nil, fmt.Errorf("scan selected keyword: %w", scanErr)
			}
			appendKeyword(keyword)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate selected keywords: %w", err)
		}
		rows.Close()
	}

	for _, requested := range input.Keywords {
		normalized := NormalizeKeyword(requested.Keyword)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		var managed Keyword
		managed, err := scanKeyword(tx.QueryRow(ctx, "SELECT "+keywordColumns+" FROM keywords WHERE normalized_keyword=$1 FOR SHARE", normalized))
		if err == nil {
			appendKeyword(managed)
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("resolve run keyword: %w", err)
		}
		keywordType := strings.TrimSpace(requested.KeywordType)
		if keywordType == "" {
			keywordType = DefaultKeywordType
		}
		seen[normalized] = struct{}{}
		items = append(items, CollectionRunItem{KeywordID: requested.KeywordID, Keyword: strings.TrimSpace(requested.Keyword),
			NormalizedKeyword: normalized, KeywordType: keywordType, Priority: requested.Priority,
			CooldownSeconds: requested.CooldownSeconds})
	}

	if len(input.KeywordIDs) == 0 && len(input.Keywords) == 0 {
		rows, err := tx.Query(ctx, "SELECT "+keywordColumns+` FROM keywords
			WHERE enabled AND ($1 OR next_eligible_at IS NULL OR next_eligible_at <= $2)
			ORDER BY priority DESC, COALESCE(last_run_at, '-infinity'::timestamptz), id FOR SHARE`, input.Force, s.now())
		if err != nil {
			return nil, fmt.Errorf("snapshot eligible keywords: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			keyword, scanErr := scanKeyword(rows)
			if scanErr != nil {
				return nil, fmt.Errorf("scan eligible run keyword: %w", scanErr)
			}
			appendKeyword(keyword)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate eligible run keywords: %w", err)
		}
	}
	return items, nil
}

// MarkRunItemRunning starts one snapshotted keyword item and its containing run.
func (s *Store) MarkRunItemRunning(ctx context.Context, runID, itemID int64, startedAt time.Time) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	if startedAt.IsZero() {
		startedAt = s.now()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin run item start: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	command, err := tx.Exec(ctx, `UPDATE collection_run_items SET
		status='running', attempts=attempts+1, started_at=$3, completed_at=NULL,
		error_message=''
		WHERE run_id=$1 AND id=$2 AND status='pending'`, runID, itemID, startedAt)
	if err != nil {
		return fmt.Errorf("mark run item running: %w", err)
	}
	if command.RowsAffected() == 0 {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM collection_run_items
			WHERE run_id=$1 AND id=$2`, runID, itemID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read run item status: %w", err)
		}
		return fmt.Errorf("%w: run item is %s", ErrConflict, status)
	}
	if _, err := tx.Exec(ctx, `UPDATE collection_runs SET status='running',
		started_at=COALESCE(started_at,$2), completed_at=NULL WHERE id=$1`, runID, startedAt); err != nil {
		return fmt.Errorf("start collection run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit run item start: %w", err)
	}
	return nil
}

// ClaimNextRunItem atomically claims at most one item across all batches. The
// advisory transaction lock enforces the V1 single-keyword execution rule.
func (s *Store) ClaimNextRunItem(ctx context.Context) (*CollectionRunItem, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("storage is disabled")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin claim run item: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var locked bool
	if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", int64(0x50616e436f6c)).Scan(&locked); err != nil {
		return nil, fmt.Errorf("lock run claim: %w", err)
	}
	if !locked {
		return nil, nil
	}
	item, err := scanRunItem(tx.QueryRow(ctx, `
		WITH candidate AS (
			SELECT i.id FROM collection_run_items i
			JOIN collection_runs r ON r.id=i.run_id
			WHERE i.status='pending'
				AND NOT EXISTS (SELECT 1 FROM collection_run_items running WHERE running.status='running')
			ORDER BY r.created_at, r.id, i.priority DESC, i.id
			FOR UPDATE OF i SKIP LOCKED LIMIT 1
		), claimed AS (
			UPDATE collection_run_items i SET status='running', attempts=i.attempts+1,
				started_at=COALESCE(i.started_at, now()), completed_at=NULL, error_message=''
			FROM candidate WHERE i.id=candidate.id RETURNING i.*
		)
		SELECT `+runItemColumns+` FROM claimed`))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim run item: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE collection_runs SET status='running',
		started_at=COALESCE(started_at, now()) WHERE id=$1`, item.RunID); err != nil {
		return nil, fmt.Errorf("start collection run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit run item claim: %w", err)
	}
	return &item, nil
}

func (s *Store) CompleteRunItem(ctx context.Context, id int64, input CompleteRunItemInput) (CollectionRun, error) {
	if s == nil || s.pool == nil {
		return CollectionRun{}, fmt.Errorf("storage is disabled")
	}
	if !terminalRunStatus(input.Status) {
		return CollectionRun{}, fmt.Errorf("%w: terminal run status %q", ErrInvalid, input.Status)
	}
	if input.CompletedAt.IsZero() {
		input.CompletedAt = s.now()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CollectionRun{}, fmt.Errorf("begin complete run item: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var runID int64
	var keywordID *int64
	var previousStatus string
	if err := tx.QueryRow(ctx, `SELECT run_id, keyword_id, status FROM collection_run_items
		WHERE id=$1 FOR UPDATE`, id).Scan(&runID, &keywordID, &previousStatus); errors.Is(err, pgx.ErrNoRows) {
		return CollectionRun{}, ErrNotFound
	} else if err != nil {
		return CollectionRun{}, fmt.Errorf("lock run item: %w", err)
	}
	if previousStatus != RunRunning {
		return CollectionRun{}, fmt.Errorf("%w: run item is %s", ErrConflict, previousStatus)
	}
	if _, err := tx.Exec(ctx, `UPDATE collection_run_items SET
		status=$2, found_count=$3, new_count=$4, duplicate_count=$5,
		source_summary=$6::jsonb, error_message=$7, completed_at=$8
		WHERE id=$1`, id, input.Status, nonNegative(input.FoundCount), nonNegative(input.NewCount),
		nonNegative(input.DuplicateCount), metadataJSON(input.SourceSummary), input.ErrorMessage, input.CompletedAt); err != nil {
		return CollectionRun{}, fmt.Errorf("complete run item: %w", err)
	}
	if keywordID != nil {
		if input.Status == RunSuccess || input.Status == RunSuccessEmpty {
			nextEligible := input.NextEligibleAt
			if nextEligible == nil {
				calculated := input.CompletedAt.Add(s.defaultCooldown)
				var cooldownSeconds *int64
				if err := tx.QueryRow(ctx, `SELECT cooldown_seconds FROM keywords WHERE id=$1`, *keywordID).Scan(&cooldownSeconds); err != nil {
					return CollectionRun{}, fmt.Errorf("read keyword cooldown: %w", err)
				}
				calculated = nextEligibleAt(input.CompletedAt, cooldownSeconds, s.defaultCooldown)
				nextEligible = &calculated
			}
			if _, err := tx.Exec(ctx, `UPDATE keywords SET last_run_at=$2, last_success_at=$2,
				next_eligible_at=$3, updated_at=now()
				WHERE id=$1`, *keywordID, input.CompletedAt, *nextEligible); err != nil {
				return CollectionRun{}, fmt.Errorf("cool down completed keyword: %w", err)
			}
		} else if _, err := tx.Exec(ctx, `UPDATE keywords SET last_run_at=$2, updated_at=now()
			WHERE id=$1`, *keywordID, input.CompletedAt); err != nil {
			return CollectionRun{}, fmt.Errorf("record failed keyword run: %w", err)
		}
	}
	if err := finalizeRunTx(ctx, tx, runID, input.CompletedAt); err != nil {
		return CollectionRun{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CollectionRun{}, fmt.Errorf("commit completed run item: %w", err)
	}
	return s.GetRunExecutionContext(ctx, runID)
}

// CompleteRun records the runner's terminal batch status after every item has finished.
func (s *Store) CompleteRun(ctx context.Context, runID int64, status string, completedAt time.Time) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	if !terminalRunStatus(status) {
		return fmt.Errorf("%w: terminal run status %q", ErrInvalid, status)
	}
	if completedAt.IsZero() {
		completedAt = s.now()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin complete run: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var currentStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM collection_runs WHERE id=$1 FOR UPDATE`, runID).Scan(&currentStatus); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("lock collection run: %w", err)
	}
	var unfinished bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM collection_run_items
		WHERE run_id=$1 AND status IN ('pending','running'))`, runID).Scan(&unfinished); err != nil {
		return fmt.Errorf("count unfinished run items: %w", err)
	}
	if unfinished {
		return fmt.Errorf("%w: collection run has unfinished items", ErrConflict)
	}
	errorSummary, err := buildRunErrorSummaryTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE collection_runs SET status=$2,
		error_message=$3, completed_at=$4 WHERE id=$1`, runID, status, errorSummary, completedAt); err != nil {
		return fmt.Errorf("complete collection run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit completed run: %w", err)
	}
	return nil
}

func finalizeRunTx(ctx context.Context, tx pgx.Tx, runID int64, at time.Time) error {
	var hasPending, hasRunning bool
	if err := tx.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM collection_run_items WHERE run_id=$1 AND status='pending'),
		EXISTS(SELECT 1 FROM collection_run_items WHERE run_id=$1 AND status='running')`, runID).Scan(&hasPending, &hasRunning); err != nil {
		return fmt.Errorf("read collection run state: %w", err)
	}
	if hasPending || hasRunning {
		status := RunPending
		if hasRunning {
			status = RunRunning
		}
		if _, err := tx.Exec(ctx, `UPDATE collection_runs SET status=$2, completed_at=NULL WHERE id=$1`, runID, status); err != nil {
			return fmt.Errorf("update active collection run: %w", err)
		}
		return nil
	}

	var hasSuccess, hasEmpty bool
	if err := tx.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM collection_run_items WHERE run_id=$1 AND (status='success' OR found_count>0)),
		EXISTS(SELECT 1 FROM collection_run_items WHERE run_id=$1 AND status='success_empty')`, runID).Scan(&hasSuccess, &hasEmpty); err != nil {
		return fmt.Errorf("read terminal collection run state: %w", err)
	}
	status := RunFailed
	if hasSuccess {
		status = RunSuccess
	} else if hasEmpty {
		status = RunSuccessEmpty
	}
	errorSummary, err := buildRunErrorSummaryTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE collection_runs SET status=$2,
		error_message=$3, completed_at=$4 WHERE id=$1`, runID, status, errorSummary, at); err != nil {
		return fmt.Errorf("finalize collection run: %w", err)
	}
	return nil
}

func buildRunErrorSummaryTx(ctx context.Context, tx pgx.Tx, runID int64) (string, error) {
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM collection_run_items
		WHERE run_id=$1 AND status='failed'`, runID).Scan(&total); err != nil {
		return "", fmt.Errorf("count collection run errors: %w", err)
	}
	if total == 0 {
		return "", nil
	}
	rows, err := tx.Query(ctx, `SELECT left(error_message, $2) FROM collection_run_items
		WHERE run_id=$1 AND status='failed' ORDER BY id LIMIT $3`, runID, runErrorItemLimit, runErrorDisplayLimit)
	if err != nil {
		return "", fmt.Errorf("list collection run errors: %w", err)
	}
	defer rows.Close()
	messages := make([]string, 0, min(total, runErrorDisplayLimit))
	for rows.Next() {
		var message string
		if err := rows.Scan(&message); err != nil {
			return "", fmt.Errorf("scan collection run error: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate collection run errors: %w", err)
	}
	return formatRunErrorSummary(messages, total), nil
}

func formatRunErrorSummary(messages []string, total int) string {
	displayed := min(len(messages), runErrorDisplayLimit)
	if total < displayed {
		displayed = total
	}
	parts := make([]string, 0, displayed+1)
	for _, message := range messages[:displayed] {
		message = strings.TrimSpace(message)
		if message == "" {
			message = runMissingErrorDetailText
		}
		parts = append(parts, truncateRunSourceText(message, runErrorItemLimit))
	}
	if total > displayed {
		parts = append(parts, fmt.Sprintf("... and %d more", total-displayed))
	}
	return truncateRunSourceText(strings.Join(parts, "; "), runErrorSummaryLimit)
}

func (s *Store) GetRun(ctx context.Context, id int64) (CollectionRun, error) {
	if s == nil || s.pool == nil {
		return CollectionRun{}, fmt.Errorf("storage is disabled")
	}
	run, err := scanRun(s.pool.QueryRow(ctx, runSelect+" WHERE cr.id=$1"+runGroup, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return CollectionRun{}, ErrNotFound
	}
	if err != nil {
		return CollectionRun{}, fmt.Errorf("get collection run: %w", err)
	}
	items, err := s.listRunItems(ctx, id)
	if err != nil {
		return CollectionRun{}, err
	}
	run.Items = items
	return run, nil
}

// GetRunSummary returns batch counters and at most the currently running item.
func (s *Store) GetRunSummary(ctx context.Context, id int64) (CollectionRun, error) {
	if s == nil || s.pool == nil {
		return CollectionRun{}, fmt.Errorf("storage is disabled")
	}
	run, err := scanRun(s.pool.QueryRow(ctx, runSelect+" WHERE cr.id=$1"+runGroup, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return CollectionRun{}, ErrNotFound
	}
	if err != nil {
		return CollectionRun{}, fmt.Errorf("get collection run summary: %w", err)
	}
	item, err := scanRunItemSummary(s.pool.QueryRow(ctx, "SELECT "+runItemSummaryColumns+` FROM collection_run_items i
		WHERE i.run_id=$1 AND i.status='running' ORDER BY i.id LIMIT 1`, id))
	if err == nil {
		run.CurrentItem = &item
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return CollectionRun{}, fmt.Errorf("get current collection run item: %w", err)
	}
	return run, nil
}

// GetRunExecutionContext loads only immutable/small batch fields required by
// a claimed keyword. It deliberately avoids aggregating every item in the run.
func (s *Store) GetRunExecutionContext(ctx context.Context, id int64) (CollectionRun, error) {
	if s == nil || s.pool == nil {
		return CollectionRun{}, fmt.Errorf("storage is disabled")
	}
	var run CollectionRun
	err := s.pool.QueryRow(ctx, `SELECT id, trigger, status, forced, left(error_message, 4096),
		created_at, started_at, completed_at FROM collection_runs WHERE id=$1`, id).Scan(
		&run.ID, &run.Trigger, &run.Status, &run.Forced, &run.ErrorMessage,
		&run.CreatedAt, &run.StartedAt, &run.CompletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CollectionRun{}, ErrNotFound
	}
	if err != nil {
		return CollectionRun{}, fmt.Errorf("get collection run execution context: %w", err)
	}
	return run, nil
}

func (s *Store) ListRunItems(ctx context.Context, runID int64, filter RunItemFilter) (RunItemPage, error) {
	if s == nil || s.pool == nil {
		return RunItemPage{}, fmt.Errorf("storage is disabled")
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize, 30, 100)
	conditions := []string{"i.run_id=$1"}
	args := []any{runID}
	add := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		conditions = append(conditions, "(i.keyword ILIKE "+add("%"+query+"%")+" OR i.normalized_keyword ILIKE "+fmt.Sprintf("$%d", len(args))+")")
	}
	if statuses := normalizeStringList(filter.Statuses); len(statuses) > 0 {
		for _, status := range statuses {
			if status != RunPending && status != RunRunning && !terminalRunStatus(status) {
				return RunItemPage{}, fmt.Errorf("%w: collection run item status", ErrInvalid)
			}
		}
		conditions = append(conditions, "i.status=ANY("+add(statuses)+"::text[])")
	}
	where := strings.Join(conditions, " AND ")
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM collection_run_items i WHERE "+where, args...).Scan(&total); err != nil {
		return RunItemPage{}, fmt.Errorf("count collection run items: %w", err)
	}
	if total == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM collection_runs WHERE id=$1)", runID).Scan(&exists); err != nil {
			return RunItemPage{}, fmt.Errorf("check collection run: %w", err)
		}
		if !exists {
			return RunItemPage{}, ErrNotFound
		}
	}
	queryArgs := append(append([]any(nil), args...), pageSize, (page-1)*pageSize)
	rows, err := s.pool.Query(ctx, "SELECT "+runItemSummaryColumns+" FROM collection_run_items i WHERE "+where+
		" ORDER BY i.priority DESC, i.id"+fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), queryArgs...)
	if err != nil {
		return RunItemPage{}, fmt.Errorf("list collection run items: %w", err)
	}
	defer rows.Close()
	items := make([]CollectionRunItem, 0, pageSize)
	for rows.Next() {
		item, scanErr := scanRunItemSummary(rows)
		if scanErr != nil {
			return RunItemPage{}, fmt.Errorf("scan collection run item summary: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return RunItemPage{}, fmt.Errorf("iterate collection run items: %w", err)
	}
	return RunItemPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (s *Store) ListRunItemSources(ctx context.Context, runID, itemID int64, filter RunSourceFilter) (RunSourcePage, error) {
	if s == nil || s.pool == nil {
		return RunSourcePage{}, fmt.Errorf("storage is disabled")
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize, 50, 100)
	conditions := []string{"i.run_id=$1", "i.id=$2"}
	args := []any{runID, itemID}
	add := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if types := normalizeStringList(filter.Types); len(types) > 0 {
		conditions = append(conditions, "entry.value->>'type'=ANY("+add(types)+"::text[])")
	}
	if statuses := normalizeStringList(filter.Statuses); len(statuses) > 0 {
		conditions = append(conditions, "entry.value->>'status'=ANY("+add(statuses)+"::text[])")
	}
	where := strings.Join(conditions, " AND ")
	var exists bool
	if err := s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM collection_run_items WHERE run_id=$1 AND id=$2)", runID, itemID).Scan(&exists); err != nil {
		return RunSourcePage{}, fmt.Errorf("check collection run item: %w", err)
	}
	if !exists {
		return RunSourcePage{}, ErrNotFound
	}
	from := "collection_run_items i CROSS JOIN LATERAL jsonb_each(i.source_summary) entry"
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM "+from+" WHERE "+where, args...).Scan(&total); err != nil {
		return RunSourcePage{}, fmt.Errorf("count collection run item sources: %w", err)
	}
	queryArgs := append(append([]any(nil), args...), pageSize, (page-1)*pageSize)
	rows, err := s.pool.Query(ctx, "SELECT entry.key, entry.value FROM "+from+" WHERE "+where+
		" ORDER BY COALESCE(entry.value->>'type',''), COALESCE(entry.value->>'key',entry.key), entry.key"+
		fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), queryArgs...)
	if err != nil {
		return RunSourcePage{}, fmt.Errorf("list collection run item sources: %w", err)
	}
	defer rows.Close()
	items := make([]RunSource, 0, pageSize)
	for rows.Next() {
		var mapKey string
		var raw []byte
		if err := rows.Scan(&mapKey, &raw); err != nil {
			return RunSourcePage{}, fmt.Errorf("scan collection run source: %w", err)
		}
		var item RunSource
		if err := json.Unmarshal(raw, &item); err != nil {
			return RunSourcePage{}, fmt.Errorf("decode collection run source: %w", err)
		}
		if item.Key == "" {
			item.Key = mapKey
		}
		item.Key = truncateRunSourceText(item.Key, 300)
		item.Type = truncateRunSourceText(item.Type, 100)
		item.Status = truncateRunSourceText(item.Status, 100)
		item.Error = truncateRunSourceText(item.Error, 1000)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return RunSourcePage{}, fmt.Errorf("iterate collection run item sources: %w", err)
	}
	return RunSourcePage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func truncateRunSourceText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func (s *Store) listRunItems(ctx context.Context, runID int64) ([]CollectionRunItem, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+runItemColumns+` FROM collection_run_items
		WHERE run_id=$1 ORDER BY priority DESC, id`, runID)
	if err != nil {
		return nil, fmt.Errorf("list run items: %w", err)
	}
	defer rows.Close()
	items := make([]CollectionRunItem, 0)
	for rows.Next() {
		item, scanErr := scanRunItem(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan run item: %w", scanErr)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListRuns(ctx context.Context, filter RunFilter) (RunPage, error) {
	if s == nil || s.pool == nil {
		return RunPage{}, fmt.Errorf("storage is disabled")
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize, 25, 100)
	conditions := []string{"TRUE"}
	args := make([]any, 0, 5)
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if filter.Trigger != "" {
		conditions = append(conditions, "cr.trigger="+addArg(filter.Trigger))
	}
	if values := normalizeStringList(filter.Statuses); len(values) > 0 {
		conditions = append(conditions, "cr.status=ANY("+addArg(values)+"::text[])")
	}
	if filter.From != nil {
		conditions = append(conditions, "cr.created_at>="+addArg(*filter.From))
	}
	if filter.To != nil {
		conditions = append(conditions, "cr.created_at<"+addArg(*filter.To))
	}
	where := strings.Join(conditions, " AND ")
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM collection_runs cr WHERE "+where, args...).Scan(&total); err != nil {
		return RunPage{}, fmt.Errorf("count collection runs: %w", err)
	}
	queryArgs := append(append([]any(nil), args...), pageSize, (page-1)*pageSize)
	rows, err := s.pool.Query(ctx, runSelect+" WHERE "+where+runGroup+
		" ORDER BY cr.created_at DESC, cr.id DESC"+fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), queryArgs...)
	if err != nil {
		return RunPage{}, fmt.Errorf("list collection runs: %w", err)
	}
	defer rows.Close()
	runs := make([]CollectionRun, 0, pageSize)
	for rows.Next() {
		run, scanErr := scanRun(rows)
		if scanErr != nil {
			return RunPage{}, fmt.Errorf("scan collection run: %w", scanErr)
		}
		runs = append(runs, compactRunListItem(run))
	}
	if err := rows.Err(); err != nil {
		return RunPage{}, fmt.Errorf("iterate collection runs: %w", err)
	}
	return RunPage{Items: runs, Total: total, Page: page, PageSize: pageSize}, nil
}

// compactRunListItem keeps the collection-run list a statistics-only payload.
// Error summaries and nested detail belong to GetRunSummary/ListRunItems and
// can otherwise make a page exceed its response-size budget with multibyte
// error text.
func compactRunListItem(run CollectionRun) CollectionRun {
	run.ErrorMessage = ""
	run.CurrentItem = nil
	run.Items = nil
	return run
}

// RecoverRunningItems makes interrupted work claimable again after a restart.
func (s *Store) RecoverRunningItems(ctx context.Context) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("storage is disabled")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin run recovery: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	command, err := tx.Exec(ctx, `UPDATE collection_run_items SET status='pending',
		started_at=NULL, completed_at=NULL,
		error_message=CASE WHEN error_message='' THEN 'recovered after restart'
			ELSE error_message || '; recovered after restart' END
		WHERE status='running'`)
	if err != nil {
		return 0, fmt.Errorf("recover running items: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE collection_runs r SET status='pending',
		started_at=NULL, completed_at=NULL
		WHERE status='running' AND EXISTS (
			SELECT 1 FROM collection_run_items i WHERE i.run_id=r.id AND i.status='pending'
		)`); err != nil {
		return 0, fmt.Errorf("recover running batches: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit run recovery: %w", err)
	}
	return command.RowsAffected(), nil
}

func terminalRunStatus(status string) bool {
	return status == RunSuccess || status == RunSuccessEmpty || status == RunFailed
}

// IsTerminalRunStatus reports whether status can complete an item or run.
func IsTerminalRunStatus(status string) bool { return terminalRunStatus(status) }

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
