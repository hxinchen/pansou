CREATE TABLE IF NOT EXISTS search_source_metrics_daily (
    metric_date DATE NOT NULL,
    source TEXT NOT NULL,
    runs BIGINT NOT NULL DEFAULT 0,
    failures BIGINT NOT NULL DEFAULT 0,
    timeouts BIGINT NOT NULL DEFAULT 0,
    rate_limited BIGINT NOT NULL DEFAULT 0,
    skipped BIGINT NOT NULL DEFAULT 0,
    result_count BIGINT NOT NULL DEFAULT 0,
    unique_count BIGINT NOT NULL DEFAULT 0,
    total_duration_ms BIGINT NOT NULL DEFAULT 0,
    p50_ms BIGINT NOT NULL DEFAULT 0,
    p95_ms BIGINT NOT NULL DEFAULT 0,
    max_ms BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (metric_date, source)
);

CREATE INDEX IF NOT EXISTS search_source_metrics_source_date_idx
    ON search_source_metrics_daily (source, metric_date DESC);
