CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX IF NOT EXISTS resources_title_trgm_idx
    ON resources USING gin (title gin_trgm_ops);

CREATE INDEX IF NOT EXISTS resources_content_trgm_idx
    ON resources USING gin (content gin_trgm_ops);

CREATE INDEX IF NOT EXISTS resources_url_trgm_idx
    ON resources USING gin (url gin_trgm_ops);
