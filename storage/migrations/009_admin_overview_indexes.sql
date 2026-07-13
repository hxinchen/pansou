CREATE INDEX IF NOT EXISTS resources_valid_first_seen_idx
    ON resources (first_seen_at)
    WHERE check_status = 'valid';

CREATE INDEX IF NOT EXISTS collection_run_items_completed_found_idx
    ON collection_run_items (completed_at)
    INCLUDE (found_count)
    WHERE completed_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS resource_sources_contribution_idx
    ON resource_sources (source_type, source_key, resource_id)
    INCLUDE (discovery_count);
