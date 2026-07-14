ALTER TABLE keyword_api_sources
    ADD COLUMN IF NOT EXISTS request_executor TEXT NOT NULL DEFAULT 'http';

ALTER TABLE keyword_api_sources
    DROP CONSTRAINT IF EXISTS keyword_api_sources_request_executor_check;

ALTER TABLE keyword_api_sources
    ADD CONSTRAINT keyword_api_sources_request_executor_check
    CHECK (request_executor IN ('http', 'browser'));
