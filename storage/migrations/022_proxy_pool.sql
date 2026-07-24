CREATE TABLE proxy_import_batches (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    source_filename TEXT NOT NULL DEFAULT '',
    total_lines INTEGER NOT NULL DEFAULT 0 CHECK (total_lines >= 0),
    accepted_count INTEGER NOT NULL DEFAULT 0 CHECK (accepted_count >= 0),
    duplicate_count INTEGER NOT NULL DEFAULT 0 CHECK (duplicate_count >= 0),
    invalid_count INTEGER NOT NULL DEFAULT 0 CHECK (invalid_count >= 0),
    expires_at TIMESTAMPTZ NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE proxy_nodes (
    id BIGSERIAL PRIMARY KEY,
    batch_id BIGINT REFERENCES proxy_import_batches(id) ON DELETE SET NULL,
    scheme TEXT NOT NULL CHECK (scheme IN ('http', 'https', 'socks5', 'socks5h')),
    host TEXT NOT NULL,
    port INTEGER NOT NULL CHECK (port > 0 AND port <= 65535),
    display_url TEXT NOT NULL,
    has_auth BOOLEAN NOT NULL DEFAULT FALSE,
    ciphertext BYTEA NOT NULL,
    nonce BYTEA NOT NULL,
    key_version INTEGER NOT NULL DEFAULT 1 CHECK (key_version > 0),
    fingerprint BYTEA NOT NULL UNIQUE,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'healthy', 'cooling', 'disabled', 'expired', 'invalid')),
    latency_ms BIGINT NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
    success_count BIGINT NOT NULL DEFAULT 0 CHECK (success_count >= 0),
    failure_count BIGINT NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    last_checked_at TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    last_failure_at TIMESTAMPTZ,
    cooldown_until TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX proxy_nodes_runtime_idx
    ON proxy_nodes (status, cooldown_until, latency_ms, id)
    WHERE enabled = TRUE;

CREATE INDEX proxy_nodes_probe_due_idx
    ON proxy_nodes (last_checked_at, id)
    WHERE enabled = TRUE AND status IN ('pending', 'healthy', 'cooling');

CREATE INDEX proxy_nodes_batch_idx ON proxy_nodes (batch_id, id);
CREATE INDEX proxy_nodes_expires_idx ON proxy_nodes (expires_at, id) WHERE enabled = TRUE;

CREATE TABLE proxy_target_stats (
    proxy_id BIGINT NOT NULL REFERENCES proxy_nodes(id) ON DELETE CASCADE,
    target_key TEXT NOT NULL,
    success_count BIGINT NOT NULL DEFAULT 0 CHECK (success_count >= 0),
    failure_count BIGINT NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    latency_ms BIGINT NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
    last_success_at TIMESTAMPTZ,
    last_failure_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (proxy_id, target_key)
);

CREATE INDEX proxy_target_stats_target_idx
    ON proxy_target_stats (target_key, success_count DESC, latency_ms, proxy_id);

CREATE TABLE proxy_route_policies (
    id BIGSERIAL PRIMARY KEY,
    target_type TEXT NOT NULL CHECK (target_type IN ('global', 'platform', 'source')),
    target_key TEXT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('baseline_only', 'baseline_first', 'proxy_first', 'proxy_only', 'sticky_proxy')),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (target_type, target_key)
);

INSERT INTO proxy_route_policies(target_type, target_key, mode)
VALUES ('global', '*', 'baseline_first')
ON CONFLICT (target_type, target_key) DO NOTHING;
