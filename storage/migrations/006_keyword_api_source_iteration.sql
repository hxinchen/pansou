ALTER TABLE keyword_api_sources
    ADD COLUMN IF NOT EXISTS iteration_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS iteration_location TEXT NOT NULL DEFAULT 'query',
    ADD COLUMN IF NOT EXISTS iteration_path TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS iteration_start BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS iteration_step BIGINT NOT NULL DEFAULT 20,
    ADD COLUMN IF NOT EXISTS iteration_count INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS iteration_delay_seconds INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_request_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_success_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_failure_count INTEGER NOT NULL DEFAULT 0;

ALTER TABLE keyword_api_sources
    DROP CONSTRAINT IF EXISTS keyword_api_sources_last_status_check;

ALTER TABLE keyword_api_sources
    ADD CONSTRAINT keyword_api_sources_last_status_check
        CHECK (last_status IN ('pending', 'running', 'success', 'partial', 'failed')),
    ADD CONSTRAINT keyword_api_sources_iteration_location_check
        CHECK (iteration_location IN ('query', 'header', 'body')),
    ADD CONSTRAINT keyword_api_sources_iteration_path_check
        CHECK (NOT iteration_enabled OR btrim(iteration_path) <> ''),
    ADD CONSTRAINT keyword_api_sources_iteration_count_check
        CHECK (iteration_count BETWEEN 1 AND 100),
    ADD CONSTRAINT keyword_api_sources_iteration_delay_check
        CHECK (iteration_delay_seconds BETWEEN 0 AND 3600),
    ADD CONSTRAINT keyword_api_sources_iteration_body_check
        CHECK (NOT iteration_enabled OR iteration_location <> 'body' OR body_type IN ('json', 'form')),
    ADD CONSTRAINT keyword_api_sources_last_request_count_check
        CHECK (last_request_count >= 0),
    ADD CONSTRAINT keyword_api_sources_last_success_count_check
        CHECK (last_success_count >= 0),
    ADD CONSTRAINT keyword_api_sources_last_failure_count_check
        CHECK (last_failure_count >= 0),
    ADD CONSTRAINT keyword_api_sources_last_counts_total_check
        CHECK (last_success_count + last_failure_count <= last_request_count);
