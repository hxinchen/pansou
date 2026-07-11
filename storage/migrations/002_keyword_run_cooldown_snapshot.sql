ALTER TABLE collection_run_items
    ADD COLUMN IF NOT EXISTS cooldown_seconds BIGINT
        CHECK (cooldown_seconds IS NULL OR cooldown_seconds >= 0);
