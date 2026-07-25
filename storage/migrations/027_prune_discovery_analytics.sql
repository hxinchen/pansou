DROP INDEX IF EXISTS resource_sources_contribution_idx;

ALTER TABLE resources
    DROP COLUMN IF EXISTS discovery_count;

ALTER TABLE resource_sources
    DROP COLUMN IF EXISTS discovery_count;

ALTER TABLE resource_keyword_links
    DROP COLUMN IF EXISTS discovery_count,
    DROP COLUMN IF EXISTS first_seen_at,
    DROP COLUMN IF EXISTS last_seen_at;

CREATE INDEX resource_sources_contribution_idx
    ON resource_sources (source_type, source_key, resource_id);
