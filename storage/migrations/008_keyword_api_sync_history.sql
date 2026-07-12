ALTER TABLE keyword_api_sources
    ADD COLUMN IF NOT EXISTS sync_config_revision BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS last_applied_config_revision BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS result_stale BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE keyword_api_sources
    DROP CONSTRAINT IF EXISTS keyword_api_sources_sync_config_revision_check,
    DROP CONSTRAINT IF EXISTS keyword_api_sources_last_applied_config_revision_check;

ALTER TABLE keyword_api_sources
    ADD CONSTRAINT keyword_api_sources_sync_config_revision_check
        CHECK (sync_config_revision > 0),
    ADD CONSTRAINT keyword_api_sources_last_applied_config_revision_check
        CHECK (last_applied_config_revision >= 0);

CREATE TABLE IF NOT EXISTS keyword_api_sync_runs (
    id BIGSERIAL PRIMARY KEY,
    source_id BIGINT REFERENCES keyword_api_sources(id) ON DELETE SET NULL,
    source_id_snapshot BIGINT NOT NULL CHECK (source_id_snapshot > 0),
    source_name_snapshot TEXT NOT NULL DEFAULT '',
    trigger TEXT NOT NULL DEFAULT 'manual'
        CHECK (trigger IN ('manual', 'save', 'scheduled', 'legacy')),
    status TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'success', 'partial', 'failed', 'interrupted', 'cancelled')),
    config_revision BIGINT NOT NULL DEFAULT 1 CHECK (config_revision > 0),
    request_summary JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(request_summary) = 'object'),
    unlimited BOOLEAN NOT NULL DEFAULT FALSE,
    total_iterations INTEGER CHECK (total_iterations IS NULL OR total_iterations >= 0),
    completed_iterations INTEGER NOT NULL DEFAULT 0 CHECK (completed_iterations >= 0),
    success_iterations INTEGER NOT NULL DEFAULT 0 CHECK (success_iterations >= 0),
    failed_iterations INTEGER NOT NULL DEFAULT 0 CHECK (failed_iterations >= 0),
    current_iteration INTEGER NOT NULL DEFAULT 0 CHECK (current_iteration >= 0),
    raw_extracted_count INTEGER NOT NULL DEFAULT 0 CHECK (raw_extracted_count >= 0),
    unique_count INTEGER NOT NULL DEFAULT 0 CHECK (unique_count >= 0),
    new_count INTEGER NOT NULL DEFAULT 0 CHECK (new_count >= 0),
    existing_count INTEGER NOT NULL DEFAULT 0 CHECK (existing_count >= 0),
    request_count INTEGER NOT NULL DEFAULT 0 CHECK (request_count >= 0),
    success_count INTEGER NOT NULL DEFAULT 0 CHECK (success_count >= 0),
    failure_count INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    error_message TEXT NOT NULL DEFAULT '',
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_token TEXT NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    queued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (success_count + failure_count <= request_count),
    CHECK (completed_iterations <= COALESCE(total_iterations, completed_iterations))
);

CREATE TABLE IF NOT EXISTS keyword_api_sync_iterations (
    id BIGSERIAL PRIMARY KEY,
    run_id BIGINT NOT NULL REFERENCES keyword_api_sync_runs(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    iteration_value BIGINT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'success', 'failed', 'skipped', 'interrupted')),
    http_status INTEGER NOT NULL DEFAULT 0 CHECK (http_status >= 0),
    duration_ms BIGINT NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    response_bytes BIGINT NOT NULL DEFAULT 0 CHECK (response_bytes >= 0),
    raw_item_count INTEGER NOT NULL DEFAULT 0 CHECK (raw_item_count >= 0),
    unique_item_count INTEGER NOT NULL DEFAULT 0 CHECK (unique_item_count >= 0),
    cross_iteration_new INTEGER NOT NULL DEFAULT 0 CHECK (cross_iteration_new >= 0),
    new_keyword_count INTEGER NOT NULL DEFAULT 0 CHECK (new_keyword_count >= 0),
    existing_keyword_count INTEGER NOT NULL DEFAULT 0 CHECK (existing_keyword_count >= 0),
    error_message TEXT NOT NULL DEFAULT '',
    samples JSONB NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(samples) = 'array'),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (run_id, sequence)
);

CREATE UNIQUE INDEX IF NOT EXISTS keyword_api_sync_runs_one_queued_idx
    ON keyword_api_sync_runs (source_id) WHERE source_id IS NOT NULL AND status = 'queued';
CREATE UNIQUE INDEX IF NOT EXISTS keyword_api_sync_runs_one_running_idx
    ON keyword_api_sync_runs (source_id) WHERE source_id IS NOT NULL AND status = 'running';
CREATE INDEX IF NOT EXISTS keyword_api_sync_runs_source_created_idx
    ON keyword_api_sync_runs (source_id_snapshot, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS keyword_api_sync_runs_status_queued_idx
    ON keyword_api_sync_runs (status, queued_at, id);
CREATE INDEX IF NOT EXISTS keyword_api_sync_runs_history_idx
    ON keyword_api_sync_runs (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS keyword_api_sync_iterations_run_idx
    ON keyword_api_sync_iterations (run_id, sequence);

-- Preserve the pre-history summary without inventing per-iteration records.
-- These rows are intentionally marked with config revision 1 while the source
-- remains unapplied (revision 0), so the UI can explain that a fresh sync is
-- required to validate the old result.
INSERT INTO keyword_api_sync_runs (
    source_id, source_id_snapshot, source_name_snapshot, trigger, status, config_revision,
    request_summary, unlimited, total_iterations, completed_iterations,
    success_iterations, failed_iterations, current_iteration,
    raw_extracted_count, unique_count, new_count, existing_count,
    request_count, success_count, failure_count, error_message,
    queued_at, started_at, completed_at, created_at, updated_at
)
SELECT id, id, name, 'legacy',
    CASE last_status
        WHEN 'success' THEN 'success'
        WHEN 'partial' THEN 'partial'
        WHEN 'failed' THEN 'failed'
        ELSE 'interrupted'
    END,
    1,
    jsonb_build_object('legacy', true),
    FALSE, 0, 0, 0, 0, 0,
    GREATEST(last_item_count, 0), GREATEST(last_item_count, 0), 0, 0,
    GREATEST(last_request_count, 0), GREATEST(last_success_count, 0),
    GREATEST(last_failure_count, 0), left(last_error, 2000),
    COALESCE(last_synced_at, created_at), COALESCE(last_synced_at, created_at),
    COALESCE(last_synced_at, created_at), COALESCE(last_synced_at, created_at),
    COALESCE(last_synced_at, created_at)
FROM keyword_api_sources
WHERE last_synced_at IS NOT NULL OR last_status <> 'pending';

UPDATE keyword_api_sources
SET result_stale = TRUE
WHERE last_synced_at IS NOT NULL OR last_status <> 'pending';

UPDATE keyword_api_sources
SET last_status = 'failed',
    last_error = 'upgrade interrupted previous synchronization',
    updated_at = now()
WHERE last_status = 'running';
