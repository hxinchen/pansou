package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const keywordAPISyncRunColumns = `
	id, source_id, source_id_snapshot, source_name_snapshot, trigger, status, config_revision,
	request_summary, unlimited, total_iterations, completed_iterations,
	success_iterations, failed_iterations, current_iteration,
	raw_extracted_count, unique_count, new_count, existing_count,
	request_count, success_count, failure_count, error_message,
	lease_owner, lease_token, lease_until, queued_at, started_at, completed_at,
	created_at, updated_at`

const keywordAPISyncIterationColumns = `
	id, run_id, sequence, iteration_value, status, http_status, duration_ms,
	response_bytes, raw_item_count, unique_item_count, cross_iteration_new,
	new_keyword_count, existing_keyword_count, error_message, samples,
	started_at, completed_at, created_at, updated_at`

func scanKeywordAPISyncRun(row rowScanner) (KeywordAPISyncRun, error) {
	var run KeywordAPISyncRun
	var liveSourceID pgtype.Int8
	var sourceID int64
	var totalIterations pgtype.Int4
	var requestSummary []byte
	var leaseUntil, startedAt, completedAt pgtype.Timestamptz
	err := row.Scan(
		&run.ID, &liveSourceID, &sourceID, &run.SourceName, &run.Trigger, &run.Status, &run.ConfigRevision,
		&requestSummary, &run.Unlimited, &totalIterations, &run.CompletedIterations,
		&run.SuccessIterations, &run.FailedIterations, &run.CurrentIteration,
		&run.RawItemCount, &run.UniqueItemCount, &run.NewKeywordCount, &run.ExistingKeywordCount,
		&run.RequestCount, &run.SuccessCount, &run.FailureCount, &run.ErrorMessage,
		&run.LeaseOwner, &run.LeaseToken, &leaseUntil, &run.QueuedAt, &startedAt, &completedAt,
		&run.CreatedAt, &run.UpdatedAt,
	)
	if err != nil {
		return KeywordAPISyncRun{}, err
	}
	run.SourceID = &sourceID
	if liveSourceID.Valid {
		value := liveSourceID.Int64
		run.LiveSourceID = &value
		run.SourceExists = true
	}
	if totalIterations.Valid {
		value := int(totalIterations.Int32)
		run.TotalIterations = &value
	}
	run.LeaseUntil = timestampPointer(leaseUntil)
	run.StartedAt = timestampPointer(startedAt)
	run.CompletedAt = timestampPointer(completedAt)
	if len(requestSummary) > 0 {
		_ = json.Unmarshal(requestSummary, &run.RequestSummary)
	}
	return run, nil
}

func scanKeywordAPISyncIteration(row rowScanner) (KeywordAPISyncIteration, error) {
	var iteration KeywordAPISyncIteration
	var samples []byte
	var startedAt, completedAt pgtype.Timestamptz
	err := row.Scan(
		&iteration.ID, &iteration.RunID, &iteration.Sequence, &iteration.IterationValue,
		&iteration.Status, &iteration.HTTPStatus, &iteration.DurationMS, &iteration.ResponseBytes,
		&iteration.RawItemCount, &iteration.UniqueItemCount, &iteration.CrossIterationNew,
		&iteration.NewKeywordCount, &iteration.ExistingKeywordCount, &iteration.ErrorMessage,
		&samples, &startedAt, &completedAt, &iteration.CreatedAt, &iteration.UpdatedAt,
	)
	if err != nil {
		return KeywordAPISyncIteration{}, err
	}
	iteration.StartedAt = timestampPointer(startedAt)
	iteration.CompletedAt = timestampPointer(completedAt)
	_ = json.Unmarshal(samples, &iteration.Samples)
	return iteration, nil
}

func keywordAPISyncRequestSummary(source KeywordAPISource) KeywordAPISyncConfigSnapshot {
	requestURL := strings.TrimSpace(source.RequestURL)
	if parsed, err := url.Parse(requestURL); err == nil {
		parsed.User = nil
		parsed.Path = ""
		parsed.RawPath = ""
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		requestURL = parsed.String()
	}
	proxyScheme := ""
	if parsed, err := url.Parse(strings.TrimSpace(source.ProxyURL)); err == nil {
		proxyScheme = strings.ToLower(parsed.Scheme)
	}
	headerKeys := sortedStringMapKeys(source.RequestHeaders)
	queryKeys := sortedStringMapKeys(source.QueryParams)
	return KeywordAPISyncConfigSnapshot{
		RequestMethod: source.RequestMethod, RequestURL: requestURL, HeaderKeys: headerKeys,
		QueryKeys: queryKeys, BodyType: source.BodyType, HasRequestBody: strings.TrimSpace(source.RequestBody) != "",
		ProxyScheme: proxyScheme, TimeoutSeconds: source.TimeoutSeconds, ResponsePath: source.ResponsePath,
		DefaultKeywordType: source.DefaultKeywordType, DefaultKeywordEnabled: source.DefaultKeywordEnabled,
		DefaultPriority: source.DefaultPriority, DefaultCooldownSeconds: source.DefaultCooldownSeconds,
		IterationEnabled: source.IterationEnabled, IterationLocation: source.IterationLocation,
		IterationPath: source.IterationPath, IterationStart: source.IterationStart, IterationStep: source.IterationStep,
		IterationCount: source.IterationCount, IterationDelaySeconds: source.IterationDelaySeconds,
		IterationUnlimited: source.IterationUnlimited, IterationNoKeywordStopCount: source.IterationNoKeywordStopCount,
		IterationRandomDelayMinSeconds: source.IterationRandomDelayMinSeconds,
		IterationRandomDelayMaxSeconds: source.IterationRandomDelayMaxSeconds,
	}
}

func sortedStringMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func normalizeKeywordAPISyncTrigger(trigger string) (string, error) {
	trigger = strings.ToLower(strings.TrimSpace(trigger))
	if trigger == "" {
		trigger = KeywordAPISyncTriggerManual
	}
	switch trigger {
	case KeywordAPISyncTriggerManual, KeywordAPISyncTriggerSave, KeywordAPISyncTriggerScheduled, KeywordAPISyncTriggerLegacy:
		return trigger, nil
	default:
		return "", fmt.Errorf("%w: keyword API sync trigger", ErrInvalid)
	}
}

// lockKeywordAPISyncRunAndSourceTx always locks the live source before the run.
// Enqueue and delete use the same order, which prevents source/run deadlocks.
func lockKeywordAPISyncRunAndSourceTx(ctx context.Context, tx pgx.Tx, runID int64) (KeywordAPISyncRun, *KeywordAPISource, error) {
	candidate, err := scanKeywordAPISyncRun(tx.QueryRow(ctx, "SELECT "+keywordAPISyncRunColumns+" FROM keyword_api_sync_runs WHERE id=$1", runID))
	if errors.Is(err, pgx.ErrNoRows) {
		return KeywordAPISyncRun{}, nil, ErrNotFound
	}
	if err != nil {
		return KeywordAPISyncRun{}, nil, fmt.Errorf("read keyword API sync run before locking: %w", err)
	}

	var source *KeywordAPISource
	if candidate.LiveSourceID != nil {
		locked, sourceErr := scanKeywordAPISource(tx.QueryRow(ctx, "SELECT "+keywordAPISourceColumns+" FROM keyword_api_sources WHERE id=$1 FOR UPDATE", *candidate.LiveSourceID))
		if sourceErr == nil {
			source = &locked
		} else if !errors.Is(sourceErr, pgx.ErrNoRows) {
			return KeywordAPISyncRun{}, nil, fmt.Errorf("lock keyword API source for sync run: %w", sourceErr)
		}
	}

	run, err := scanKeywordAPISyncRun(tx.QueryRow(ctx, "SELECT "+keywordAPISyncRunColumns+" FROM keyword_api_sync_runs WHERE id=$1 FOR UPDATE", runID))
	if errors.Is(err, pgx.ErrNoRows) {
		return KeywordAPISyncRun{}, nil, ErrNotFound
	}
	if err != nil {
		return KeywordAPISyncRun{}, nil, fmt.Errorf("lock keyword API sync run: %w", err)
	}
	if run.LiveSourceID == nil {
		return run, nil, nil
	}
	if source == nil || source.ID != *run.LiveSourceID {
		return KeywordAPISyncRun{}, nil, fmt.Errorf("%w: keyword API sync source changed while locking", ErrConflict)
	}
	return run, source, nil
}

func (s *Store) EnqueueKeywordAPISourceSync(ctx context.Context, sourceID int64, trigger string, at time.Time) (KeywordAPISyncRun, bool, error) {
	if s == nil || s.pool == nil {
		return KeywordAPISyncRun{}, false, fmt.Errorf("storage is disabled")
	}
	if sourceID <= 0 {
		return KeywordAPISyncRun{}, false, fmt.Errorf("%w: source ID", ErrInvalid)
	}
	trigger, err := normalizeKeywordAPISyncTrigger(trigger)
	if err != nil {
		return KeywordAPISyncRun{}, false, err
	}
	if at.IsZero() {
		at = s.now()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return KeywordAPISyncRun{}, false, fmt.Errorf("begin enqueue keyword API sync: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	source, err := scanKeywordAPISource(tx.QueryRow(ctx, "SELECT "+keywordAPISourceColumns+" FROM keyword_api_sources WHERE id=$1 FOR UPDATE", sourceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return KeywordAPISyncRun{}, false, ErrNotFound
	}
	if err != nil {
		return KeywordAPISyncRun{}, false, fmt.Errorf("lock keyword API source for enqueue: %w", err)
	}
	if strings.TrimSpace(source.RequestURL) == "" || strings.TrimSpace(source.ResponsePath) == "" {
		return KeywordAPISyncRun{}, false, fmt.Errorf("%w: source is incomplete", ErrInvalid)
	}
	run, alreadyActive, err := s.enqueueKeywordAPISourceSyncTx(ctx, tx, source, trigger, at)
	if err != nil {
		return KeywordAPISyncRun{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return KeywordAPISyncRun{}, false, fmt.Errorf("commit enqueue keyword API sync: %w", err)
	}
	return run, alreadyActive, nil
}

func (s *Store) enqueueKeywordAPISourceSyncTx(ctx context.Context, tx pgx.Tx, source KeywordAPISource, trigger string, at time.Time) (KeywordAPISyncRun, bool, error) {
	running, err := scanKeywordAPISyncRun(tx.QueryRow(ctx, "SELECT "+keywordAPISyncRunColumns+` FROM keyword_api_sync_runs
		WHERE source_id=$1 AND status='running' ORDER BY id DESC LIMIT 1 FOR UPDATE`, source.ID))
	if err == nil && running.ConfigRevision == source.SyncConfigRevision {
		return running, true, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return KeywordAPISyncRun{}, false, fmt.Errorf("read running keyword API sync: %w", err)
	}

	queued, err := scanKeywordAPISyncRun(tx.QueryRow(ctx, "SELECT "+keywordAPISyncRunColumns+` FROM keyword_api_sync_runs
		WHERE source_id=$1 AND status='queued' ORDER BY id DESC LIMIT 1 FOR UPDATE`, source.ID))
	if err == nil {
		if queued.ConfigRevision == source.SyncConfigRevision {
			return queued, true, nil
		}
		if err := cancelQueuedKeywordAPISyncRunTx(ctx, tx, queued.ID, "replaced by a newer configuration", at); err != nil {
			return KeywordAPISyncRun{}, false, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return KeywordAPISyncRun{}, false, fmt.Errorf("read queued keyword API sync: %w", err)
	}

	summary := keywordAPISyncRequestSummary(source)
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return KeywordAPISyncRun{}, false, fmt.Errorf("encode keyword API sync request summary: %w", err)
	}
	var totalIterations *int
	if !source.IterationEnabled {
		value := 1
		totalIterations = &value
	} else if !source.IterationUnlimited {
		value := source.IterationCount
		totalIterations = &value
	}
	run, err := scanKeywordAPISyncRun(tx.QueryRow(ctx, `INSERT INTO keyword_api_sync_runs (
		source_id, source_id_snapshot, source_name_snapshot, trigger, status, config_revision,
		request_summary, unlimited, total_iterations, queued_at
	) VALUES ($1,$1,$2,$3,'queued',$4,$5::jsonb,$6,$7,$8)
	RETURNING `+keywordAPISyncRunColumns, source.ID, source.Name, trigger, source.SyncConfigRevision,
		summaryJSON, source.IterationEnabled && source.IterationUnlimited, totalIterations, at))
	if err != nil {
		return KeywordAPISyncRun{}, false, mapWriteError("enqueue keyword API sync", err)
	}
	if totalIterations != nil {
		for sequence := 1; sequence <= *totalIterations; sequence++ {
			iterationValue := int64(0)
			if source.IterationEnabled {
				iterationValue = source.IterationStart + int64(sequence-1)*source.IterationStep
			}
			if _, err := tx.Exec(ctx, `INSERT INTO keyword_api_sync_iterations
				(run_id, sequence, iteration_value, status) VALUES($1,$2,$3,'queued')`,
				run.ID, sequence, iterationValue); err != nil {
				return KeywordAPISyncRun{}, false, mapWriteError("precreate keyword API sync iteration", err)
			}
		}
	}
	return run, false, nil
}

func cancelQueuedKeywordAPISyncRunTx(ctx context.Context, tx pgx.Tx, runID int64, message string, at time.Time) error {
	message = truncateKeywordAPIError(message)
	if _, err := tx.Exec(ctx, `UPDATE keyword_api_sync_iterations SET status='skipped', completed_at=$2, updated_at=now()
		WHERE run_id=$1 AND status='queued'`, runID, at); err != nil {
		return fmt.Errorf("skip cancelled keyword API sync iterations: %w", err)
	}
	command, err := tx.Exec(ctx, `UPDATE keyword_api_sync_runs SET status='cancelled', error_message=$2,
		completed_at=$3, lease_owner='', lease_token='', lease_until=NULL, updated_at=now()
		WHERE id=$1 AND status='queued'`, runID, message, at)
	if err != nil {
		return fmt.Errorf("cancel queued keyword API sync: %w", err)
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("%w: keyword API sync is no longer queued", ErrConflict)
	}
	return nil
}

func (s *Store) EnqueueDueKeywordAPISourceSync(ctx context.Context, at time.Time) (*KeywordAPISyncRun, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("storage is disabled")
	}
	if at.IsZero() {
		at = s.now()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin enqueue due keyword API sync: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	source, err := scanKeywordAPISource(tx.QueryRow(ctx, `SELECT `+keywordAPISourceColumns+` FROM keyword_api_sources
		WHERE enabled AND next_sync_at IS NOT NULL AND next_sync_at <= $1
		ORDER BY next_sync_at, id FOR UPDATE SKIP LOCKED LIMIT 1`, at))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim due keyword API source for enqueue: %w", err)
	}
	run, _, err := s.enqueueKeywordAPISourceSyncTx(ctx, tx, source, KeywordAPISyncTriggerScheduled, at)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE keyword_api_sources SET
		next_sync_at=$2::timestamptz + sync_interval_seconds * interval '1 second', updated_at=now()
		WHERE id=$1`, source.ID, at); err != nil {
		return nil, fmt.Errorf("advance keyword API source schedule: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit enqueue due keyword API sync: %w", err)
	}
	return &run, nil
}

func (s *Store) ClaimNextKeywordAPISyncRun(ctx context.Context, leaseOwner, leaseToken string, at, leaseUntil time.Time) (*KeywordAPISyncClaim, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("storage is disabled")
	}
	leaseOwner, leaseToken = strings.TrimSpace(leaseOwner), strings.TrimSpace(leaseToken)
	if leaseOwner == "" || leaseToken == "" {
		return nil, fmt.Errorf("%w: keyword API sync lease identity", ErrInvalid)
	}
	if at.IsZero() {
		at = s.now()
	}
	if leaseUntil.IsZero() {
		leaseUntil = at.Add(45 * time.Second)
	}
	if !leaseUntil.After(at) {
		return nil, fmt.Errorf("%w: keyword API sync lease deadline", ErrInvalid)
	}
	for {
		claim, retry, err := s.claimNextKeywordAPISyncRun(ctx, leaseOwner, leaseToken, at, leaseUntil)
		if err != nil {
			return nil, err
		}
		if retry {
			continue
		}
		return claim, nil
	}
}

func (s *Store) claimNextKeywordAPISyncRun(ctx context.Context, leaseOwner, leaseToken string, at, leaseUntil time.Time) (*KeywordAPISyncClaim, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin claim keyword API sync: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	candidate, err := scanKeywordAPISyncRun(tx.QueryRow(ctx, `SELECT `+keywordAPISyncRunColumns+` FROM keyword_api_sync_runs queued
		WHERE queued.status='queued' AND (queued.source_id IS NULL OR NOT EXISTS (
			SELECT 1 FROM keyword_api_sync_runs active
			WHERE active.source_id=queued.source_id AND active.status='running'
		)) ORDER BY queued.queued_at, queued.id LIMIT 1`))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("select queued keyword API sync: %w", err)
	}

	if candidate.LiveSourceID == nil {
		run, lockErr := scanKeywordAPISyncRun(tx.QueryRow(ctx, "SELECT "+keywordAPISyncRunColumns+" FROM keyword_api_sync_runs WHERE id=$1 FOR UPDATE", candidate.ID))
		if lockErr != nil && !errors.Is(lockErr, pgx.ErrNoRows) {
			return nil, false, fmt.Errorf("lock orphaned keyword API sync run: %w", lockErr)
		}
		if lockErr == nil && run.Status == KeywordAPISyncRunStatusQueued {
			if err := cancelQueuedKeywordAPISyncRunTx(ctx, tx, run.ID, "source was deleted before synchronization", at); err != nil {
				return nil, false, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, false, fmt.Errorf("commit orphaned keyword API sync cancellation: %w", err)
		}
		return nil, true, nil
	}

	source, err := scanKeywordAPISource(tx.QueryRow(ctx, "SELECT "+keywordAPISourceColumns+" FROM keyword_api_sources WHERE id=$1 FOR UPDATE", *candidate.LiveSourceID))
	if errors.Is(err, pgx.ErrNoRows) {
		run, lockErr := scanKeywordAPISyncRun(tx.QueryRow(ctx, "SELECT "+keywordAPISyncRunColumns+" FROM keyword_api_sync_runs WHERE id=$1 FOR UPDATE", candidate.ID))
		if lockErr == nil && run.Status == KeywordAPISyncRunStatusQueued {
			if err := cancelQueuedKeywordAPISyncRunTx(ctx, tx, run.ID, "source was deleted before synchronization", at); err != nil {
				return nil, false, err
			}
		} else if lockErr != nil && !errors.Is(lockErr, pgx.ErrNoRows) {
			return nil, false, fmt.Errorf("lock deleted-source keyword API sync run: %w", lockErr)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, false, fmt.Errorf("commit deleted-source keyword API sync cancellation: %w", err)
		}
		return nil, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("lock keyword API source for sync claim: %w", err)
	}

	run, err := scanKeywordAPISyncRun(tx.QueryRow(ctx, "SELECT "+keywordAPISyncRunColumns+" FROM keyword_api_sync_runs WHERE id=$1 FOR UPDATE", candidate.ID))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (run.Status != KeywordAPISyncRunStatusQueued || run.LiveSourceID == nil || *run.LiveSourceID != source.ID)) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("lock queued keyword API sync: %w", err)
	}
	if run.ConfigRevision != source.SyncConfigRevision {
		trigger := run.Trigger
		if err := cancelQueuedKeywordAPISyncRunTx(ctx, tx, run.ID, "replaced by a newer configuration", at); err != nil {
			return nil, false, err
		}
		run, _, err = s.enqueueKeywordAPISourceSyncTx(ctx, tx, source, trigger, at)
		if err != nil {
			return nil, false, err
		}
	}
	run, err = scanKeywordAPISyncRun(tx.QueryRow(ctx, `UPDATE keyword_api_sync_runs SET
		status='running', lease_owner=$2, lease_token=$3, lease_until=$4,
		started_at=COALESCE(started_at,$5), updated_at=now()
		WHERE id=$1 AND status='queued' RETURNING `+keywordAPISyncRunColumns,
		run.ID, leaseOwner, leaseToken, leaseUntil, at))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("claim keyword API sync: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE keyword_api_sources SET
		last_status='running', last_error='',
		next_sync_at=$2::timestamptz + sync_interval_seconds * interval '1 second', updated_at=now()
		WHERE id=$1`, source.ID, at); err != nil {
		return nil, false, fmt.Errorf("mark keyword API source running: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit keyword API sync claim: %w", err)
	}
	return &KeywordAPISyncClaim{Run: run, Source: source}, false, nil
}

func (s *Store) RenewKeywordAPISyncRunLease(ctx context.Context, runID int64, leaseOwner, leaseToken string, leaseUntil, at time.Time) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	leaseOwner, leaseToken = strings.TrimSpace(leaseOwner), strings.TrimSpace(leaseToken)
	if runID <= 0 || leaseOwner == "" || leaseToken == "" {
		return fmt.Errorf("%w: keyword API sync lease", ErrInvalid)
	}
	if at.IsZero() {
		at = s.now()
	}
	if !leaseUntil.After(at) {
		return fmt.Errorf("%w: keyword API sync lease deadline", ErrInvalid)
	}
	command, err := s.pool.Exec(ctx, `UPDATE keyword_api_sync_runs SET lease_until=$4, updated_at=now()
		WHERE id=$1 AND status='running' AND lease_owner=$2 AND lease_token=$3
		AND lease_until IS NOT NULL AND lease_until >= $5`, runID, leaseOwner, leaseToken, leaseUntil, at)
	if err != nil {
		return fmt.Errorf("renew keyword API sync lease: %w", err)
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("%w: keyword API sync lease", ErrConflict)
	}
	return nil
}

func validateKeywordAPISyncLease(run KeywordAPISyncRun, leaseOwner, leaseToken string) error {
	if run.Status != KeywordAPISyncRunStatusRunning || run.LeaseOwner != strings.TrimSpace(leaseOwner) || run.LeaseToken != strings.TrimSpace(leaseToken) {
		return fmt.Errorf("%w: keyword API sync lease", ErrConflict)
	}
	return nil
}

func (s *Store) BeginKeywordAPISyncIteration(ctx context.Context, input KeywordAPISyncIterationInput) (KeywordAPISyncIteration, error) {
	if s == nil || s.pool == nil {
		return KeywordAPISyncIteration{}, fmt.Errorf("storage is disabled")
	}
	if input.RunID <= 0 || input.Sequence < 1 {
		return KeywordAPISyncIteration{}, fmt.Errorf("%w: keyword API sync iteration", ErrInvalid)
	}
	if input.StartedAt.IsZero() {
		input.StartedAt = s.now()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return KeywordAPISyncIteration{}, fmt.Errorf("begin keyword API sync iteration: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	run, err := scanKeywordAPISyncRun(tx.QueryRow(ctx, "SELECT "+keywordAPISyncRunColumns+" FROM keyword_api_sync_runs WHERE id=$1 FOR UPDATE", input.RunID))
	if errors.Is(err, pgx.ErrNoRows) {
		return KeywordAPISyncIteration{}, ErrNotFound
	}
	if err != nil {
		return KeywordAPISyncIteration{}, fmt.Errorf("lock keyword API sync for iteration: %w", err)
	}
	if err := validateKeywordAPISyncLease(run, input.LeaseOwner, input.LeaseToken); err != nil {
		return KeywordAPISyncIteration{}, err
	}
	if run.TotalIterations != nil && input.Sequence > *run.TotalIterations {
		return KeywordAPISyncIteration{}, fmt.Errorf("%w: keyword API sync iteration index", ErrInvalid)
	}
	iteration, err := scanKeywordAPISyncIteration(tx.QueryRow(ctx, `INSERT INTO keyword_api_sync_iterations (
		run_id, sequence, iteration_value, status, started_at
	) VALUES($1,$2,$3,'running',$4)
	ON CONFLICT (run_id,sequence) DO UPDATE SET
		iteration_value=EXCLUDED.iteration_value, status='running',
		started_at=COALESCE(keyword_api_sync_iterations.started_at,EXCLUDED.started_at), updated_at=now()
	WHERE keyword_api_sync_iterations.status IN ('queued','running')
	RETURNING `+keywordAPISyncIterationColumns, input.RunID, input.Sequence, input.IterationValue, input.StartedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return KeywordAPISyncIteration{}, fmt.Errorf("%w: keyword API sync iteration is already complete", ErrConflict)
	}
	if err != nil {
		return KeywordAPISyncIteration{}, mapWriteError("start keyword API sync iteration", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE keyword_api_sync_runs SET
		current_iteration=GREATEST(current_iteration,$2), updated_at=now() WHERE id=$1`, run.ID, input.Sequence); err != nil {
		return KeywordAPISyncIteration{}, fmt.Errorf("advance keyword API sync progress: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return KeywordAPISyncIteration{}, fmt.Errorf("commit keyword API sync iteration start: %w", err)
	}
	return iteration, nil
}

func (s *Store) CompleteKeywordAPISyncIteration(ctx context.Context, input KeywordAPISyncIterationInput) (KeywordAPISyncIteration, error) {
	if s == nil || s.pool == nil {
		return KeywordAPISyncIteration{}, fmt.Errorf("storage is disabled")
	}
	if input.RunID <= 0 || input.Sequence < 1 || input.DurationMS < 0 || input.ResponseBytes < 0 ||
		input.RawItemCount < 0 || input.UniqueItemCount < 0 || input.CrossIterationNew < 0 ||
		input.NewKeywordCount < 0 || input.ExistingKeywordCount < 0 {
		return KeywordAPISyncIteration{}, fmt.Errorf("%w: keyword API sync iteration result", ErrInvalid)
	}
	status := strings.ToLower(strings.TrimSpace(input.Status))
	if status == "" {
		status = KeywordAPISyncIterationStatusSuccess
	}
	if status != KeywordAPISyncIterationStatusSuccess && status != KeywordAPISyncIterationStatusFailed {
		return KeywordAPISyncIteration{}, fmt.Errorf("%w: keyword API sync iteration status", ErrInvalid)
	}
	if input.CompletedAt.IsZero() {
		input.CompletedAt = s.now()
	}
	samples := normalizeKeywordAPISyncSamples(input.Samples)
	samplesJSON, _ := json.Marshal(samples)
	errorMessage := truncateKeywordAPIError(input.ErrorMessage)
	if status == KeywordAPISyncIterationStatusSuccess {
		errorMessage = ""
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return KeywordAPISyncIteration{}, fmt.Errorf("begin complete keyword API sync iteration: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	run, err := scanKeywordAPISyncRun(tx.QueryRow(ctx, "SELECT "+keywordAPISyncRunColumns+" FROM keyword_api_sync_runs WHERE id=$1 FOR UPDATE", input.RunID))
	if errors.Is(err, pgx.ErrNoRows) {
		return KeywordAPISyncIteration{}, ErrNotFound
	}
	if err != nil {
		return KeywordAPISyncIteration{}, fmt.Errorf("lock keyword API sync for iteration completion: %w", err)
	}
	if err := validateKeywordAPISyncLease(run, input.LeaseOwner, input.LeaseToken); err != nil {
		return KeywordAPISyncIteration{}, err
	}
	if run.TotalIterations != nil && input.Sequence > *run.TotalIterations {
		return KeywordAPISyncIteration{}, fmt.Errorf("%w: keyword API sync iteration index", ErrInvalid)
	}
	startedAt := any(nil)
	if !input.StartedAt.IsZero() {
		startedAt = input.StartedAt
	}
	iteration, err := scanKeywordAPISyncIteration(tx.QueryRow(ctx, `INSERT INTO keyword_api_sync_iterations (
		run_id, sequence, iteration_value, status, http_status, duration_ms, response_bytes,
		raw_item_count, unique_item_count, cross_iteration_new, new_keyword_count,
		existing_keyword_count, error_message, samples, started_at, completed_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::jsonb,$15,$16)
	ON CONFLICT (run_id,sequence) DO UPDATE SET
		iteration_value=EXCLUDED.iteration_value, status=EXCLUDED.status,
		http_status=EXCLUDED.http_status, duration_ms=EXCLUDED.duration_ms,
		response_bytes=EXCLUDED.response_bytes, raw_item_count=EXCLUDED.raw_item_count,
		unique_item_count=EXCLUDED.unique_item_count, cross_iteration_new=EXCLUDED.cross_iteration_new,
		new_keyword_count=EXCLUDED.new_keyword_count, existing_keyword_count=EXCLUDED.existing_keyword_count,
		error_message=EXCLUDED.error_message, samples=EXCLUDED.samples,
		started_at=COALESCE(keyword_api_sync_iterations.started_at,EXCLUDED.started_at),
		completed_at=EXCLUDED.completed_at, updated_at=now()
	WHERE keyword_api_sync_iterations.status IN ('queued','running')
	RETURNING `+keywordAPISyncIterationColumns,
		input.RunID, input.Sequence, input.IterationValue, status, input.HTTPStatus,
		input.DurationMS, input.ResponseBytes, input.RawItemCount, input.UniqueItemCount,
		input.CrossIterationNew, input.NewKeywordCount, input.ExistingKeywordCount,
		errorMessage, samplesJSON, startedAt, input.CompletedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return KeywordAPISyncIteration{}, fmt.Errorf("%w: keyword API sync iteration is already complete", ErrConflict)
	}
	if err != nil {
		return KeywordAPISyncIteration{}, mapWriteError("complete keyword API sync iteration", err)
	}
	if err := refreshKeywordAPISyncRunProgressTx(ctx, tx, run.ID); err != nil {
		return KeywordAPISyncIteration{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return KeywordAPISyncIteration{}, fmt.Errorf("commit keyword API sync iteration completion: %w", err)
	}
	return iteration, nil
}

func normalizeKeywordAPISyncSamples(values []string) []string {
	result := make([]string, 0, 5)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		runes := []rune(value)
		if len(runes) > 120 {
			value = string(runes[:120])
		}
		result = append(result, value)
		if len(result) == 5 {
			break
		}
	}
	return result
}

func refreshKeywordAPISyncRunProgressTx(ctx context.Context, tx pgx.Tx, runID int64) error {
	_, err := tx.Exec(ctx, `UPDATE keyword_api_sync_runs run SET
		completed_iterations=stats.completed,
		success_iterations=stats.succeeded,
		failed_iterations=stats.failed,
		raw_extracted_count=stats.raw_count,
		unique_count=stats.unique_count,
		new_count=stats.new_count,
		existing_count=stats.existing_count,
		request_count=stats.completed,
		success_count=stats.succeeded,
		failure_count=stats.failed,
		updated_at=now()
	FROM (
		SELECT count(*) FILTER (WHERE status IN ('success','failed'))::integer AS completed,
			count(*) FILTER (WHERE status='success')::integer AS succeeded,
			count(*) FILTER (WHERE status='failed')::integer AS failed,
			COALESCE(sum(raw_item_count) FILTER (WHERE status IN ('success','failed')),0)::integer AS raw_count,
			COALESCE(sum(cross_iteration_new) FILTER (WHERE status IN ('success','failed')),0)::integer AS unique_count,
			COALESCE(sum(new_keyword_count) FILTER (WHERE status IN ('success','failed')),0)::integer AS new_count,
			COALESCE(sum(existing_keyword_count) FILTER (WHERE status IN ('success','failed')),0)::integer AS existing_count
		FROM keyword_api_sync_iterations WHERE run_id=$1
	) stats WHERE run.id=$1`, runID)
	if err != nil {
		return fmt.Errorf("refresh keyword API sync progress: %w", err)
	}
	return nil
}

func keywordAPISyncValueSequenceMap(values []string, sequences []int) (map[string]int, error) {
	result := make(map[string]int, len(values))
	if len(sequences) == 0 {
		return result, nil
	}
	if len(sequences) != len(values) {
		return nil, fmt.Errorf("%w: keyword API sync value sequences", ErrInvalid)
	}
	for index, value := range values {
		if sequences[index] < 1 {
			return nil, fmt.Errorf("%w: keyword API sync value sequence", ErrInvalid)
		}
		normalized := NormalizeKeyword(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if _, exists := result[normalized]; !exists {
			result[normalized] = sequences[index]
		}
	}
	return result, nil
}

func (s *Store) FinalizeKeywordAPISyncRun(ctx context.Context, input KeywordAPISyncFinalizeInput) (KeywordAPISyncRun, KeywordAPISourceSyncResult, error) {
	if s == nil || s.pool == nil {
		return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, fmt.Errorf("storage is disabled")
	}
	if input.RunID <= 0 {
		return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, fmt.Errorf("%w: keyword API sync run ID", ErrInvalid)
	}
	if input.RawItemCount < 0 {
		return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, fmt.Errorf("%w: raw keyword API sync item count", ErrInvalid)
	}
	if input.SyncedAt.IsZero() {
		input.SyncedAt = s.now()
	}
	status, errorMessage, requestCount, successCount, failureCount, err := normalizeKeywordAPISyncFinalize(input)
	if err != nil {
		return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, err
	}
	values := normalizeKeywordAPISourceValues(input.Values)
	valueSequences, err := keywordAPISyncValueSequenceMap(input.Values, input.ValueSequences)
	if err != nil {
		return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, fmt.Errorf("begin finalize keyword API sync: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	run, sourcePointer, err := lockKeywordAPISyncRunAndSourceTx(ctx, tx, input.RunID)
	if err != nil {
		return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, err
	}
	if err := validateKeywordAPISyncLease(run, input.LeaseOwner, input.LeaseToken); err != nil {
		return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, err
	}
	if sourcePointer == nil {
		return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, fmt.Errorf("%w: keyword API sync source was deleted", ErrConflict)
	}
	source := *sourcePointer
	result := KeywordAPISourceSyncResult{Source: source, Seen: input.RawItemCount, Unique: len(values), LinkedItems: len(values)}
	type iterationKeywordCounts struct {
		new      int
		existing int
	}
	countsBySequence := make(map[int]iterationKeywordCounts)
	for _, value := range values {
		command, insertErr := tx.Exec(ctx, `INSERT INTO keywords (
			keyword, normalized_keyword, keyword_type, source_type, source_key, external_id,
			source_metadata, enabled, priority, cooldown_seconds
		) VALUES ($1,$2,$3,'api',$4,$2,$5::jsonb,$6,$7,$8)
		ON CONFLICT (normalized_keyword) DO NOTHING`,
			value.External, value.Normalized, run.RequestSummary.DefaultKeywordType, fmt.Sprintf("%d", source.ID),
			metadataJSON(map[string]any{"keyword_api_source_id": source.ID, "keyword_api_source_name": run.SourceName}),
			run.RequestSummary.DefaultKeywordEnabled, run.RequestSummary.DefaultPriority, run.RequestSummary.DefaultCooldownSeconds,
		)
		if insertErr != nil {
			return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, mapWriteError("upsert API keyword for sync run", insertErr)
		}
		inserted := command.RowsAffected() == 1
		var keywordID int64
		if err := tx.QueryRow(ctx, "SELECT id FROM keywords WHERE normalized_keyword=$1", value.Normalized).Scan(&keywordID); err != nil {
			return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, fmt.Errorf("resolve API keyword for sync run: %w", err)
		}
		if inserted {
			result.InsertedKeywords++
			if _, err := tx.Exec(ctx, `UPDATE resource_keywords SET keyword_id=$1
				WHERE normalized_keyword=$2 AND keyword_id IS NULL`, keywordID, value.Normalized); err != nil {
				return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, fmt.Errorf("attach API keyword resources for sync run: %w", err)
			}
		} else {
			result.ExistingKeywords++
		}
		if sequence := valueSequences[value.Normalized]; sequence > 0 {
			counts := countsBySequence[sequence]
			if inserted {
				counts.new++
			} else {
				counts.existing++
			}
			countsBySequence[sequence] = counts
		}
		if _, err := tx.Exec(ctx, `INSERT INTO keyword_api_source_items (
			source_id, keyword_id, external_value, normalized_value, first_seen_at, last_seen_at
		) VALUES ($1,$2,$3,$4,$5,$5)
		ON CONFLICT (source_id, normalized_value) DO UPDATE SET
			keyword_id=EXCLUDED.keyword_id, external_value=EXCLUDED.external_value,
			last_seen_at=EXCLUDED.last_seen_at`, source.ID, keywordID, value.External, value.Normalized, input.SyncedAt); err != nil {
			return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, fmt.Errorf("upsert keyword API source item for sync run: %w", err)
		}
	}
	sequences := make([]int, 0, len(countsBySequence))
	for sequence := range countsBySequence {
		sequences = append(sequences, sequence)
	}
	sort.Ints(sequences)
	for _, sequence := range sequences {
		counts := countsBySequence[sequence]
		command, err := tx.Exec(ctx, `UPDATE keyword_api_sync_iterations SET
			new_keyword_count=$3, existing_keyword_count=$4, updated_at=now()
			WHERE run_id=$1 AND sequence=$2 AND status='success'`, run.ID, sequence, counts.new, counts.existing)
		if err != nil {
			return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, fmt.Errorf("update keyword API sync iteration keyword counts: %w", err)
		}
		if command.RowsAffected() != 1 {
			return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, fmt.Errorf("%w: keyword API sync value sequence %d", ErrConflict, sequence)
		}
	}
	if err := cancelUnfinishedKeywordAPISyncIterationsTx(ctx, tx, run.ID, input.SyncedAt, KeywordAPISyncIterationStatusSkipped); err != nil {
		return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, err
	}
	run, err = scanKeywordAPISyncRun(tx.QueryRow(ctx, `UPDATE keyword_api_sync_runs SET
		status=$2, raw_extracted_count=$3, unique_count=$4, new_count=$5, existing_count=$6,
		request_count=$7, success_count=$8, failure_count=$9, error_message=$10,
		completed_at=$11, lease_owner='', lease_token='', lease_until=NULL, updated_at=now()
		WHERE id=$1 RETURNING `+keywordAPISyncRunColumns,
		run.ID, status, input.RawItemCount, len(values), result.InsertedKeywords, result.ExistingKeywords,
		requestCount, successCount, failureCount, errorMessage, input.SyncedAt))
	if err != nil {
		return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, fmt.Errorf("finalize keyword API sync run: %w", err)
	}
	lastStatus := KeywordAPISourceStatusSuccess
	if status == KeywordAPISyncRunStatusPartial {
		lastStatus = KeywordAPISourceStatusPartial
	}
	resultStale := source.SyncConfigRevision != run.ConfigRevision
	source, err = scanKeywordAPISource(tx.QueryRow(ctx, `UPDATE keyword_api_sources SET
		last_status=$2, last_error=$3, last_item_count=$4, last_synced_at=$5,
		last_request_count=$6, last_success_count=$7, last_failure_count=$8,
		last_applied_config_revision=$9, result_stale=$10,
		next_sync_at=$5::timestamptz + sync_interval_seconds * interval '1 second', updated_at=now()
		WHERE id=$1 RETURNING `+keywordAPISourceColumns,
		source.ID, lastStatus, errorMessage, len(values), input.SyncedAt,
		requestCount, successCount, failureCount, run.ConfigRevision, resultStale))
	if err != nil {
		return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, fmt.Errorf("complete keyword API source for sync run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return KeywordAPISyncRun{}, KeywordAPISourceSyncResult{}, fmt.Errorf("commit keyword API sync run: %w", err)
	}
	result.Source = source
	return run, result, nil
}

func normalizeKeywordAPISyncFinalize(input KeywordAPISyncFinalizeInput) (string, string, int, int, int, error) {
	status := strings.ToLower(strings.TrimSpace(input.Status))
	requestCount, successCount, failureCount := input.RequestCount, input.SuccessCount, input.FailureCount
	if requestCount == 0 && successCount == 0 && failureCount == 0 {
		requestCount, successCount = 1, 1
	}
	if requestCount < 1 || successCount < 1 || failureCount < 0 || successCount+failureCount != requestCount {
		return "", "", 0, 0, 0, fmt.Errorf("%w: sync run completion counts", ErrInvalid)
	}
	if status == "" {
		status = KeywordAPISyncRunStatusSuccess
	}
	if status != KeywordAPISyncRunStatusSuccess && status != KeywordAPISyncRunStatusPartial {
		return "", "", 0, 0, 0, fmt.Errorf("%w: sync run completion status", ErrInvalid)
	}
	if status == KeywordAPISyncRunStatusSuccess && failureCount != 0 {
		return "", "", 0, 0, 0, fmt.Errorf("%w: successful sync run has failures", ErrInvalid)
	}
	if status == KeywordAPISyncRunStatusPartial && failureCount == 0 {
		return "", "", 0, 0, 0, fmt.Errorf("%w: partial sync run has no failures", ErrInvalid)
	}
	errorMessage := truncateKeywordAPIError(input.ErrorMessage)
	if status == KeywordAPISyncRunStatusSuccess {
		errorMessage = ""
	}
	return status, errorMessage, requestCount, successCount, failureCount, nil
}

func cancelUnfinishedKeywordAPISyncIterationsTx(ctx context.Context, tx pgx.Tx, runID int64, at time.Time, status string) error {
	if status != KeywordAPISyncIterationStatusSkipped && status != KeywordAPISyncIterationStatusInterrupted {
		status = KeywordAPISyncIterationStatusSkipped
	}
	_, err := tx.Exec(ctx, `UPDATE keyword_api_sync_iterations SET
		status=CASE WHEN status='queued' THEN 'skipped' ELSE $2 END,
		completed_at=$3, updated_at=now()
		WHERE run_id=$1 AND status IN ('queued','running')`, runID, status, at)
	if err != nil {
		return fmt.Errorf("finish unfinished keyword API sync iterations: %w", err)
	}
	return nil
}

func (s *Store) FailKeywordAPISyncRun(ctx context.Context, runID int64, leaseOwner, leaseToken, message string, failedAt time.Time, requestCount, failureCount int) (KeywordAPISyncRun, error) {
	if s == nil || s.pool == nil {
		return KeywordAPISyncRun{}, fmt.Errorf("storage is disabled")
	}
	if runID <= 0 || requestCount < 0 || failureCount < 0 || failureCount > requestCount {
		return KeywordAPISyncRun{}, fmt.Errorf("%w: failed keyword API sync run", ErrInvalid)
	}
	successCount := requestCount - failureCount
	if failedAt.IsZero() {
		failedAt = s.now()
	}
	message = truncateKeywordAPIError(message)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return KeywordAPISyncRun{}, fmt.Errorf("begin fail keyword API sync run: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	run, _, err := lockKeywordAPISyncRunAndSourceTx(ctx, tx, runID)
	if err != nil {
		return KeywordAPISyncRun{}, err
	}
	if err := validateKeywordAPISyncLease(run, leaseOwner, leaseToken); err != nil {
		return KeywordAPISyncRun{}, err
	}
	if err := cancelUnfinishedKeywordAPISyncIterationsTx(ctx, tx, run.ID, failedAt, KeywordAPISyncIterationStatusInterrupted); err != nil {
		return KeywordAPISyncRun{}, err
	}
	run, err = scanKeywordAPISyncRun(tx.QueryRow(ctx, `UPDATE keyword_api_sync_runs SET
		status='failed', request_count=$2, success_count=$3, failure_count=$4,
		error_message=$5, completed_at=$6, lease_owner='', lease_token='', lease_until=NULL, updated_at=now()
		WHERE id=$1 RETURNING `+keywordAPISyncRunColumns, run.ID, requestCount, successCount, failureCount, message, failedAt))
	if err != nil {
		return KeywordAPISyncRun{}, fmt.Errorf("fail keyword API sync run: %w", err)
	}
	if run.LiveSourceID != nil {
		if _, err := tx.Exec(ctx, `UPDATE keyword_api_sources SET
			last_status='failed', last_error=$2, last_item_count=0, last_synced_at=$3,
			last_request_count=$4, last_success_count=$5, last_failure_count=$6,
			result_stale=result_stale OR sync_config_revision <> $7,
			next_sync_at=$3::timestamptz + sync_interval_seconds * interval '1 second', updated_at=now()
			WHERE id=$1`, *run.LiveSourceID, message, failedAt, requestCount, successCount, failureCount, run.ConfigRevision); err != nil {
			return KeywordAPISyncRun{}, fmt.Errorf("fail keyword API source for sync run: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return KeywordAPISyncRun{}, fmt.Errorf("commit failed keyword API sync run: %w", err)
	}
	return run, nil
}

func (s *Store) InterruptKeywordAPISyncRun(ctx context.Context, runID int64, leaseOwner, leaseToken, message string, interruptedAt time.Time) (KeywordAPISyncRun, error) {
	if s == nil || s.pool == nil {
		return KeywordAPISyncRun{}, fmt.Errorf("storage is disabled")
	}
	if runID <= 0 {
		return KeywordAPISyncRun{}, fmt.Errorf("%w: interrupted keyword API sync run", ErrInvalid)
	}
	if interruptedAt.IsZero() {
		interruptedAt = s.now()
	}
	message = truncateKeywordAPIError(message)
	if message == "" {
		message = "synchronization interrupted"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return KeywordAPISyncRun{}, fmt.Errorf("begin interrupt keyword API sync run: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	run, _, err := lockKeywordAPISyncRunAndSourceTx(ctx, tx, runID)
	if err != nil {
		return KeywordAPISyncRun{}, err
	}
	if err := validateKeywordAPISyncLease(run, leaseOwner, leaseToken); err != nil {
		return KeywordAPISyncRun{}, err
	}
	if err := cancelUnfinishedKeywordAPISyncIterationsTx(ctx, tx, run.ID, interruptedAt, KeywordAPISyncIterationStatusInterrupted); err != nil {
		return KeywordAPISyncRun{}, err
	}
	if err := refreshKeywordAPISyncRunProgressTx(ctx, tx, run.ID); err != nil {
		return KeywordAPISyncRun{}, err
	}
	run, err = scanKeywordAPISyncRun(tx.QueryRow(ctx, `UPDATE keyword_api_sync_runs SET
		status='interrupted', error_message=$2, completed_at=$3,
		lease_owner='', lease_token='', lease_until=NULL, updated_at=now()
		WHERE id=$1 RETURNING `+keywordAPISyncRunColumns, run.ID, message, interruptedAt))
	if err != nil {
		return KeywordAPISyncRun{}, fmt.Errorf("interrupt keyword API sync run: %w", err)
	}
	if run.LiveSourceID != nil {
		if _, err := tx.Exec(ctx, `UPDATE keyword_api_sources SET
			last_status='failed', last_error=$2, last_item_count=0, last_synced_at=$3,
			last_request_count=$4, last_success_count=$5, last_failure_count=$6,
			result_stale=result_stale OR sync_config_revision <> $7,
			next_sync_at=$3::timestamptz + sync_interval_seconds * interval '1 second', updated_at=now()
			WHERE id=$1`, *run.LiveSourceID, message, interruptedAt, run.RequestCount, run.SuccessCount, run.FailureCount, run.ConfigRevision); err != nil {
			return KeywordAPISyncRun{}, fmt.Errorf("interrupt keyword API source sync: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return KeywordAPISyncRun{}, fmt.Errorf("commit interrupted keyword API sync run: %w", err)
	}
	return run, nil
}

func (s *Store) RecoverExpiredKeywordAPISyncRuns(ctx context.Context, at time.Time) (int, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("storage is disabled")
	}
	if at.IsZero() {
		at = s.now()
	}
	rows, err := s.pool.Query(ctx, `SELECT id FROM keyword_api_sync_runs
		WHERE status='running' AND lease_until IS NOT NULL AND lease_until < $1 ORDER BY id`, at)
	if err != nil {
		return 0, fmt.Errorf("list expired keyword API sync runs: %w", err)
	}
	runIDs := make([]int64, 0)
	for rows.Next() {
		var runID int64
		if err := rows.Scan(&runID); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan expired keyword API sync run ID: %w", err)
		}
		runIDs = append(runIDs, runID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate expired keyword API sync run IDs: %w", err)
	}
	rows.Close()
	recovered := 0
	for _, runID := range runIDs {
		updated, err := s.recoverExpiredKeywordAPISyncRun(ctx, runID, at)
		if err != nil {
			return recovered, err
		}
		if updated {
			recovered++
		}
	}
	return recovered, nil
}

func (s *Store) recoverExpiredKeywordAPISyncRun(ctx context.Context, runID int64, at time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin recover expired keyword API sync run: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	run, _, err := lockKeywordAPISyncRunAndSourceTx(ctx, tx, runID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if run.Status != KeywordAPISyncRunStatusRunning || run.LeaseUntil == nil || !run.LeaseUntil.Before(at) {
		return false, nil
	}
	if err := cancelUnfinishedKeywordAPISyncIterationsTx(ctx, tx, run.ID, at, KeywordAPISyncIterationStatusInterrupted); err != nil {
		return false, err
	}
	if err := refreshKeywordAPISyncRunProgressTx(ctx, tx, run.ID); err != nil {
		return false, err
	}
	run, err = scanKeywordAPISyncRun(tx.QueryRow(ctx, `UPDATE keyword_api_sync_runs SET
		status='interrupted', error_message='worker lease expired', completed_at=$2,
		lease_owner='', lease_token='', lease_until=NULL, updated_at=now()
		WHERE id=$1 RETURNING `+keywordAPISyncRunColumns, run.ID, at))
	if err != nil {
		return false, fmt.Errorf("recover expired keyword API sync run: %w", err)
	}
	if run.LiveSourceID != nil {
		if _, err := tx.Exec(ctx, `UPDATE keyword_api_sources SET
			last_status='failed', last_error='worker lease expired', last_item_count=0, last_synced_at=$2,
			last_request_count=$3, last_success_count=$4, last_failure_count=$5,
			result_stale=result_stale OR sync_config_revision <> $6,
			next_sync_at=$2::timestamptz + sync_interval_seconds * interval '1 second', updated_at=now()
			WHERE id=$1 AND last_status='running'`, *run.LiveSourceID, at, run.RequestCount, run.SuccessCount, run.FailureCount, run.ConfigRevision); err != nil {
			return false, fmt.Errorf("recover expired keyword API source sync: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit recovered keyword API sync run: %w", err)
	}
	return true, nil
}

func (s *Store) ListKeywordAPISyncRuns(ctx context.Context, filter KeywordAPISyncRunFilter) (KeywordAPISyncRunPage, error) {
	if s == nil || s.pool == nil {
		return KeywordAPISyncRunPage{}, fmt.Errorf("storage is disabled")
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize, 20, 200)
	conditions := []string{"TRUE"}
	args := make([]any, 0, 7)
	add := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if filter.SourceID != nil {
		conditions = append(conditions, "source_id_snapshot="+add(*filter.SourceID))
	}
	if len(filter.Statuses) > 0 {
		statuses, err := normalizeKeywordAPISyncRunStatuses(filter.Statuses)
		if err != nil {
			return KeywordAPISyncRunPage{}, err
		}
		conditions = append(conditions, "status=ANY("+add(statuses)+")")
	}
	if len(filter.Triggers) > 0 {
		triggers := make([]string, 0, len(filter.Triggers))
		for _, trigger := range filter.Triggers {
			normalized, err := normalizeKeywordAPISyncTrigger(trigger)
			if err != nil {
				return KeywordAPISyncRunPage{}, err
			}
			triggers = append(triggers, normalized)
		}
		conditions = append(conditions, "trigger=ANY("+add(triggers)+")")
	}
	if filter.From != nil {
		conditions = append(conditions, "created_at >= "+add(*filter.From))
	}
	if filter.To != nil {
		conditions = append(conditions, "created_at < "+add(*filter.To))
	}
	where := strings.Join(conditions, " AND ")
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM keyword_api_sync_runs WHERE "+where, args...).Scan(&total); err != nil {
		return KeywordAPISyncRunPage{}, fmt.Errorf("count keyword API sync runs: %w", err)
	}
	queryArgs := append(append([]any(nil), args...), pageSize, (page-1)*pageSize)
	rows, err := s.pool.Query(ctx, "SELECT "+keywordAPISyncRunColumns+" FROM keyword_api_sync_runs WHERE "+where+
		" ORDER BY created_at DESC, id DESC"+fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), queryArgs...)
	if err != nil {
		return KeywordAPISyncRunPage{}, fmt.Errorf("list keyword API sync runs: %w", err)
	}
	defer rows.Close()
	items := make([]KeywordAPISyncRun, 0, pageSize)
	for rows.Next() {
		run, err := scanKeywordAPISyncRun(rows)
		if err != nil {
			return KeywordAPISyncRunPage{}, fmt.Errorf("scan keyword API sync run: %w", err)
		}
		items = append(items, run)
	}
	if err := rows.Err(); err != nil {
		return KeywordAPISyncRunPage{}, fmt.Errorf("iterate keyword API sync runs: %w", err)
	}
	return KeywordAPISyncRunPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func normalizeKeywordAPISyncRunStatuses(statuses []string) ([]string, error) {
	result := make([]string, 0, len(statuses))
	seen := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		status = strings.ToLower(strings.TrimSpace(status))
		switch status {
		case KeywordAPISyncRunStatusQueued, KeywordAPISyncRunStatusRunning, KeywordAPISyncRunStatusSuccess,
			KeywordAPISyncRunStatusPartial, KeywordAPISyncRunStatusFailed, KeywordAPISyncRunStatusInterrupted,
			KeywordAPISyncRunStatusCancelled:
		default:
			return nil, fmt.Errorf("%w: keyword API sync run status", ErrInvalid)
		}
		if _, exists := seen[status]; !exists {
			seen[status] = struct{}{}
			result = append(result, status)
		}
	}
	return result, nil
}

func (s *Store) GetKeywordAPISyncRun(ctx context.Context, id int64) (KeywordAPISyncRun, error) {
	if s == nil || s.pool == nil {
		return KeywordAPISyncRun{}, fmt.Errorf("storage is disabled")
	}
	run, err := scanKeywordAPISyncRun(s.pool.QueryRow(ctx, "SELECT "+keywordAPISyncRunColumns+" FROM keyword_api_sync_runs WHERE id=$1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return KeywordAPISyncRun{}, ErrNotFound
	}
	if err != nil {
		return KeywordAPISyncRun{}, fmt.Errorf("get keyword API sync run: %w", err)
	}
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM keyword_api_sync_iterations WHERE run_id=$1", id).Scan(&run.IterationRecordsTotal); err != nil {
		return KeywordAPISyncRun{}, fmt.Errorf("count keyword API sync iterations: %w", err)
	}
	rows, err := s.pool.Query(ctx, `SELECT `+keywordAPISyncIterationColumns+` FROM keyword_api_sync_iterations
		WHERE run_id=$1 ORDER BY sequence LIMIT 100`, id)
	if err != nil {
		return KeywordAPISyncRun{}, fmt.Errorf("list keyword API sync iterations: %w", err)
	}
	defer rows.Close()
	run.Iterations = make([]KeywordAPISyncIteration, 0, min(run.IterationRecordsTotal, 100))
	for rows.Next() {
		iteration, err := scanKeywordAPISyncIteration(rows)
		if err != nil {
			return KeywordAPISyncRun{}, fmt.Errorf("scan keyword API sync iteration: %w", err)
		}
		run.Iterations = append(run.Iterations, iteration)
	}
	if err := rows.Err(); err != nil {
		return KeywordAPISyncRun{}, fmt.Errorf("iterate keyword API sync iterations: %w", err)
	}
	run.IterationsTruncated = run.IterationRecordsTotal > len(run.Iterations)
	return run, nil
}

func (s *Store) loadKeywordAPISourceRunSummaries(ctx context.Context, sources []*KeywordAPISource) error {
	sourceByID := make(map[int64]*KeywordAPISource, len(sources))
	sourceIDs := make([]int64, 0, len(sources))
	for _, source := range sources {
		if source == nil || source.ID <= 0 {
			continue
		}
		source.ActiveRun = nil
		source.LatestRun = nil
		sourceByID[source.ID] = source
		sourceIDs = append(sourceIDs, source.ID)
	}
	if len(sourceIDs) == 0 {
		return nil
	}

	rows, err := s.pool.Query(ctx, "SELECT "+keywordAPISyncRunColumns+` FROM keyword_api_sync_runs
		WHERE source_id=ANY($1) AND status IN ('queued','running')
		ORDER BY source_id, CASE status WHEN 'running' THEN 0 ELSE 1 END, created_at DESC, id DESC`, sourceIDs)
	if err != nil {
		return fmt.Errorf("list active keyword API sync runs: %w", err)
	}
	for rows.Next() {
		run, scanErr := scanKeywordAPISyncRun(rows)
		if scanErr != nil {
			rows.Close()
			return fmt.Errorf("scan active keyword API sync run: %w", scanErr)
		}
		if run.LiveSourceID != nil {
			if source := sourceByID[*run.LiveSourceID]; source != nil && source.ActiveRun == nil {
				copy := run
				source.ActiveRun = &copy
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate active keyword API sync runs: %w", err)
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, "SELECT DISTINCT ON (source_id_snapshot) "+keywordAPISyncRunColumns+` FROM keyword_api_sync_runs
		WHERE source_id_snapshot=ANY($1) ORDER BY source_id_snapshot, created_at DESC, id DESC`, sourceIDs)
	if err != nil {
		return fmt.Errorf("list latest keyword API sync runs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		run, scanErr := scanKeywordAPISyncRun(rows)
		if scanErr != nil {
			return fmt.Errorf("scan latest keyword API sync run: %w", scanErr)
		}
		if run.SourceID != nil {
			if source := sourceByID[*run.SourceID]; source != nil {
				copy := run
				source.LatestRun = &copy
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate latest keyword API sync runs: %w", err)
	}
	return nil
}
