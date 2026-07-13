UPDATE collection_runs
SET error_message = left(error_message, 4096)
WHERE char_length(error_message) > 4096;

ALTER TABLE collection_runs
    DROP COLUMN IF EXISTS source_summary;

CREATE INDEX IF NOT EXISTS collection_run_items_run_order_idx
    ON collection_run_items (run_id, priority DESC, id);

CREATE INDEX IF NOT EXISTS collection_run_items_run_status_order_idx
    ON collection_run_items (run_id, status, priority DESC, id);

CREATE INDEX IF NOT EXISTS resource_sources_resource_seen_idx
    ON resource_sources (resource_id, last_seen_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS resource_keywords_resource_seen_idx
    ON resource_keywords (resource_id, last_seen_at DESC, normalized_keyword);
