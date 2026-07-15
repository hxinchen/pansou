CREATE TABLE IF NOT EXISTS link_check_policy (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    statuses TEXT[] NOT NULL DEFAULT ARRAY['valid', 'unknown']::TEXT[],
    interval_seconds BIGINT NOT NULL DEFAULT 604800,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT link_check_policy_statuses_check CHECK (
        array_position(statuses, NULL) IS NULL
        AND statuses <@ ARRAY[
            'valid',
            'unknown',
            'invalid',
            'expired',
            'cancelled',
            'violation',
            'locked'
        ]::TEXT[]
    ),
    CONSTRAINT link_check_policy_enabled_statuses_check CHECK (
        NOT enabled OR cardinality(statuses) > 0
    ),
    CONSTRAINT link_check_policy_interval_check CHECK (
        interval_seconds BETWEEN 3600 AND 31536000
        AND interval_seconds % 3600 = 0
    )
);

INSERT INTO link_check_policy (singleton, enabled, statuses, interval_seconds)
VALUES (TRUE, FALSE, ARRAY['valid', 'unknown']::TEXT[], 604800)
ON CONFLICT (singleton) DO NOTHING;

ALTER TABLE resources
    ADD COLUMN IF NOT EXISTS candidate_check_status TEXT,
    ADD COLUMN IF NOT EXISTS candidate_checked_at TIMESTAMPTZ;
