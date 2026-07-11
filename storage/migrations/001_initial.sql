CREATE TABLE IF NOT EXISTS resources (
    id BIGSERIAL PRIMARY KEY,
    normalized_url TEXT NOT NULL UNIQUE,
    url TEXT NOT NULL,
    password TEXT NOT NULL DEFAULT '',
    platform TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL DEFAULT '',
    link_datetime TIMESTAMPTZ,
    check_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (check_status IN ('pending', 'valid', 'invalid', 'unknown', 'unsupported')),
    last_checked_at TIMESTAMPTZ,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    discovery_count BIGINT NOT NULL DEFAULT 1 CHECK (discovery_count > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS keywords (
    id BIGSERIAL PRIMARY KEY,
    keyword TEXT NOT NULL,
    normalized_keyword TEXT NOT NULL UNIQUE,
    keyword_type TEXT NOT NULL DEFAULT 'general',
    source_type TEXT NOT NULL DEFAULT 'manual',
    source_key TEXT NOT NULL DEFAULT '',
    external_id TEXT NOT NULL DEFAULT '',
    source_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    priority INTEGER NOT NULL DEFAULT 0,
    cooldown_seconds BIGINT CHECK (cooldown_seconds IS NULL OR cooldown_seconds >= 0),
    last_run_at TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    next_eligible_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS keywords_external_identity_idx
    ON keywords (source_type, source_key, external_id)
    WHERE external_id <> '';

CREATE TABLE IF NOT EXISTS resource_sources (
    id BIGSERIAL PRIMARY KEY,
    resource_id BIGINT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    source_type TEXT NOT NULL,
    source_key TEXT NOT NULL DEFAULT '',
    source_identity TEXT NOT NULL DEFAULT '',
    message_id TEXT NOT NULL DEFAULT '',
    unique_id TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL DEFAULT '',
    discovered_at TIMESTAMPTZ NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    discovery_count BIGINT NOT NULL DEFAULT 1 CHECK (discovery_count > 0),
    source_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (resource_id, source_type, source_key, source_identity)
);

CREATE TABLE IF NOT EXISTS resource_keywords (
    resource_id BIGINT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    keyword_id BIGINT REFERENCES keywords(id) ON DELETE SET NULL,
    keyword TEXT NOT NULL,
    normalized_keyword TEXT NOT NULL,
    keyword_type TEXT NOT NULL DEFAULT 'general',
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    discovery_count BIGINT NOT NULL DEFAULT 1 CHECK (discovery_count > 0),
    PRIMARY KEY (resource_id, normalized_keyword)
);

CREATE TABLE IF NOT EXISTS collection_runs (
    id BIGSERIAL PRIMARY KEY,
    trigger TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'success', 'success_empty', 'failed')),
    forced BOOLEAN NOT NULL DEFAULT FALSE,
    source_summary JSONB NOT NULL DEFAULT '{}'::jsonb,
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS collection_run_items (
    id BIGSERIAL PRIMARY KEY,
    run_id BIGINT NOT NULL REFERENCES collection_runs(id) ON DELETE CASCADE,
    keyword_id BIGINT REFERENCES keywords(id) ON DELETE SET NULL,
    keyword TEXT NOT NULL,
    normalized_keyword TEXT NOT NULL,
    keyword_type TEXT NOT NULL DEFAULT 'general',
    priority INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'success', 'success_empty', 'failed')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    found_count INTEGER NOT NULL DEFAULT 0 CHECK (found_count >= 0),
    new_count INTEGER NOT NULL DEFAULT 0 CHECK (new_count >= 0),
    duplicate_count INTEGER NOT NULL DEFAULT 0 CHECK (duplicate_count >= 0),
    source_summary JSONB NOT NULL DEFAULT '{}'::jsonb,
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    UNIQUE (run_id, normalized_keyword)
);

CREATE INDEX IF NOT EXISTS resources_last_seen_idx ON resources (last_seen_at DESC);
CREATE INDEX IF NOT EXISTS resources_first_seen_idx ON resources (first_seen_at DESC);
CREATE INDEX IF NOT EXISTS resources_platform_idx ON resources (platform);
CREATE INDEX IF NOT EXISTS resources_check_due_idx ON resources (check_status, last_checked_at)
    WHERE check_status IN ('pending', 'invalid', 'unknown');
CREATE INDEX IF NOT EXISTS resource_sources_resource_idx ON resource_sources (resource_id);
CREATE INDEX IF NOT EXISTS resource_sources_lookup_idx ON resource_sources (source_type, source_key);
CREATE INDEX IF NOT EXISTS resource_keywords_keyword_idx ON resource_keywords (normalized_keyword, resource_id);
CREATE INDEX IF NOT EXISTS resource_keywords_type_idx ON resource_keywords (keyword_type);
CREATE INDEX IF NOT EXISTS keywords_eligibility_idx
    ON keywords (enabled, next_eligible_at, priority DESC);
CREATE INDEX IF NOT EXISTS collection_runs_created_idx ON collection_runs (created_at DESC);
CREATE INDEX IF NOT EXISTS collection_run_items_claim_idx
    ON collection_run_items (status, priority DESC, created_at, id);
