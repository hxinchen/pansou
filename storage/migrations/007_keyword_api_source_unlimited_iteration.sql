ALTER TABLE keyword_api_sources
    ADD COLUMN IF NOT EXISTS iteration_unlimited BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS iteration_no_keyword_stop_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS iteration_random_delay_min_seconds INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS iteration_random_delay_max_seconds INTEGER NOT NULL DEFAULT 0;

ALTER TABLE keyword_api_sources
    ADD CONSTRAINT keyword_api_sources_iteration_no_keyword_stop_count_check
        CHECK (iteration_no_keyword_stop_count BETWEEN 0 AND 100),
    ADD CONSTRAINT keyword_api_sources_iteration_random_delay_min_check
        CHECK (iteration_random_delay_min_seconds BETWEEN -3600 AND 3600),
    ADD CONSTRAINT keyword_api_sources_iteration_random_delay_max_check
        CHECK (iteration_random_delay_max_seconds BETWEEN -3600 AND 3600),
    ADD CONSTRAINT keyword_api_sources_iteration_random_delay_range_check
        CHECK (iteration_random_delay_min_seconds <= iteration_random_delay_max_seconds),
    ADD CONSTRAINT keyword_api_sources_iteration_unlimited_stop_check
        CHECK (
            NOT iteration_enabled
            OR NOT iteration_unlimited
            OR iteration_no_keyword_stop_count BETWEEN 1 AND 100
        );
