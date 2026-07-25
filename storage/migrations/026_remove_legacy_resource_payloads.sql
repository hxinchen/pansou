DROP TABLE IF EXISTS resource_keywords;

ALTER TABLE resources
    DROP COLUMN IF EXISTS content;

DROP INDEX IF EXISTS resource_keyword_links_resource_seen_idx;
DROP INDEX IF EXISTS resource_keyword_links_keyword_id_idx;

CREATE INDEX IF NOT EXISTS resource_keyword_links_keyword_id_idx
    ON resource_keyword_links (keyword_id)
    WHERE keyword_id IS NOT NULL;
