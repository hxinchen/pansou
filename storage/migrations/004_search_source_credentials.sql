CREATE TABLE IF NOT EXISTS search_source_configs (
    id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    version BIGINT NOT NULL CHECK (version > 0),
    schema_version INTEGER NOT NULL CHECK (schema_version > 0),
    config JSONB NOT NULL CHECK (jsonb_typeof(config) = 'object'),
    updated_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS search_source_config_events (
    id BIGSERIAL PRIMARY KEY,
    actor_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    base_version BIGINT NOT NULL CHECK (base_version >= 0),
    result_version BIGINT CHECK (result_version IS NULL OR result_version > 0),
    result TEXT NOT NULL CHECK (result IN ('success', 'failed')),
    error_code TEXT NOT NULL DEFAULT '',
    change_summary JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(change_summary) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (result = 'success' AND result_version = base_version + 1 AND error_code = '')
        OR
        (result = 'failed' AND result_version IS NULL AND error_code <> '')
    )
);

CREATE TABLE IF NOT EXISTS plugin_credentials (
    id BIGSERIAL PRIMARY KEY,
    public_id TEXT NOT NULL UNIQUE,
    plugin_key TEXT NOT NULL,
    scope TEXT NOT NULL CHECK (scope IN ('user_private', 'admin_private', 'public_shared')),
    owner_user_id BIGINT REFERENCES users(id) ON DELETE CASCADE,
    created_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    display_name TEXT NOT NULL DEFAULT '',
    public_metadata JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(public_metadata) = 'object'),
    secret_schema_version INTEGER NOT NULL CHECK (secret_schema_version > 0),
    binding_fingerprint BYTEA NOT NULL CHECK (octet_length(binding_fingerprint) = 32),
    ciphertext BYTEA NOT NULL CHECK (octet_length(ciphertext) >= 16),
    nonce BYTEA NOT NULL CHECK (octet_length(nonce) = 12),
    key_version INTEGER NOT NULL CHECK (key_version > 0),
    credential_fingerprint BYTEA NOT NULL CHECK (octet_length(credential_fingerprint) = 32),
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    owner_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    admin_suspended_at TIMESTAMPTZ,
    admin_suspended_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'invalid', 'expired')),
    expires_at TIMESTAMPTZ,
    cooldown_until TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    last_failure_at TIMESTAMPTZ,
    last_error_code TEXT NOT NULL DEFAULT '',
    consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (scope = 'user_private' AND owner_user_id IS NOT NULL)
        OR
        (scope IN ('admin_private', 'public_shared') AND owner_user_id IS NULL)
    ),
    CHECK (admin_suspended_by IS NULL OR admin_suspended_at IS NOT NULL)
);

CREATE TABLE IF NOT EXISTS data_migrations (
    migration_key TEXT PRIMARY KEY,
    completed_at TIMESTAMPTZ NOT NULL,
    summary JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(summary) = 'object')
);

CREATE INDEX IF NOT EXISTS search_source_config_events_created_idx
    ON search_source_config_events (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS search_source_config_events_actor_idx
    ON search_source_config_events (actor_user_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS plugin_credentials_candidates_idx
    ON plugin_credentials (plugin_key, scope, status, owner_enabled, admin_suspended_at,
        expires_at, cooldown_until);
CREATE INDEX IF NOT EXISTS plugin_credentials_user_candidates_idx
    ON plugin_credentials (plugin_key, owner_user_id, status, owner_enabled,
        admin_suspended_at, expires_at, cooldown_until, last_success_at DESC,
        consecutive_failures, last_used_at, id)
    WHERE scope = 'user_private';
CREATE INDEX IF NOT EXISTS plugin_credentials_admin_candidates_idx
    ON plugin_credentials (plugin_key, scope, status, owner_enabled,
        admin_suspended_at, expires_at, cooldown_until, last_success_at DESC,
        consecutive_failures, last_used_at, id)
    WHERE scope IN ('admin_private', 'public_shared');
CREATE INDEX IF NOT EXISTS plugin_credentials_owner_idx
    ON plugin_credentials (owner_user_id, plugin_key, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS plugin_credentials_user_fingerprint_idx
    ON plugin_credentials (plugin_key, owner_user_id, credential_fingerprint)
    WHERE scope = 'user_private';
CREATE UNIQUE INDEX IF NOT EXISTS plugin_credentials_admin_fingerprint_idx
    ON plugin_credentials (plugin_key, credential_fingerprint)
    WHERE scope IN ('admin_private', 'public_shared');
