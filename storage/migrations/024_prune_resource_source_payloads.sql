DROP INDEX IF EXISTS resources_content_trgm_idx;
DROP INDEX IF EXISTS resources_url_trgm_idx;
DROP INDEX IF EXISTS resource_sources_lookup_idx;
DROP INDEX IF EXISTS resource_keywords_type_idx;

ALTER TABLE resource_sources
    DROP COLUMN IF EXISTS content;
