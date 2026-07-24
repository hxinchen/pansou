ALTER TABLE proxy_target_stats
    ADD COLUMN consecutive_failures INTEGER NOT NULL DEFAULT 0
        CHECK (consecutive_failures >= 0),
    ADD COLUMN cooldown_until TIMESTAMPTZ;

CREATE INDEX proxy_target_stats_cooldown_idx
    ON proxy_target_stats (target_key, cooldown_until, proxy_id)
    WHERE cooldown_until IS NOT NULL;
