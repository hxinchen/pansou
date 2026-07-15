ALTER TABLE keyword_api_sources
    ADD COLUMN IF NOT EXISTS iteration_stop_mode TEXT NOT NULL DEFAULT 'normal';

ALTER TABLE keyword_api_sources
    DROP CONSTRAINT IF EXISTS keyword_api_sources_iteration_stop_mode_check;

ALTER TABLE keyword_api_sources
    ADD CONSTRAINT keyword_api_sources_iteration_stop_mode_check
        CHECK (iteration_stop_mode IN ('normal', 'strict'));

ALTER TABLE keyword_api_sources
    ALTER COLUMN iteration_stop_mode SET DEFAULT 'strict';
