CREATE TABLE IF NOT EXISTS keyword_api_sources (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL CHECK (btrim(name) <> ''),
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    request_method TEXT NOT NULL DEFAULT 'GET'
        CHECK (request_method IN ('GET', 'POST', 'PUT', 'PATCH')),
    request_url TEXT NOT NULL DEFAULT '',
    request_headers JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(request_headers) = 'object'),
    query_params JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(query_params) = 'object'),
    body_type TEXT NOT NULL DEFAULT 'none'
        CHECK (body_type IN ('none', 'json', 'form', 'raw')),
    request_body TEXT NOT NULL DEFAULT '',
    proxy_url TEXT NOT NULL DEFAULT '',
    timeout_seconds INTEGER NOT NULL DEFAULT 15
        CHECK (timeout_seconds BETWEEN 1 AND 60),
    response_path TEXT NOT NULL DEFAULT '',
    sync_interval_seconds BIGINT NOT NULL DEFAULT 3600
        CHECK (sync_interval_seconds >= 60),
    default_keyword_type TEXT NOT NULL DEFAULT 'general'
        CHECK (btrim(default_keyword_type) <> ''),
    default_keyword_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    default_priority INTEGER NOT NULL DEFAULT 0,
    default_cooldown_seconds BIGINT
        CHECK (default_cooldown_seconds IS NULL OR default_cooldown_seconds >= 0),
    next_sync_at TIMESTAMPTZ,
    last_synced_at TIMESTAMPTZ,
    last_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (last_status IN ('pending', 'running', 'success', 'failed')),
    last_error TEXT NOT NULL DEFAULT '',
    last_item_count INTEGER NOT NULL DEFAULT 0 CHECK (last_item_count >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (NOT enabled OR (btrim(request_url) <> '' AND btrim(response_path) <> ''))
);

CREATE TABLE IF NOT EXISTS keyword_api_source_items (
    source_id BIGINT NOT NULL REFERENCES keyword_api_sources(id) ON DELETE CASCADE,
    keyword_id BIGINT NOT NULL REFERENCES keywords(id) ON DELETE CASCADE,
    external_value TEXT NOT NULL,
    normalized_value TEXT NOT NULL CHECK (btrim(normalized_value) <> ''),
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (source_id, normalized_value)
);

CREATE INDEX IF NOT EXISTS keyword_api_sources_due_idx
    ON keyword_api_sources (next_sync_at, id) WHERE enabled;
CREATE INDEX IF NOT EXISTS keyword_api_source_items_keyword_idx
    ON keyword_api_source_items (keyword_id, source_id);
