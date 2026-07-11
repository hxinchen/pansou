CREATE TABLE IF NOT EXISTS users (
    id BIGSERIAL PRIMARY KEY,
    username TEXT NOT NULL,
    normalized_username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin', 'user')),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    expires_at TIMESTAMPTZ,
    must_change_password BOOLEAN NOT NULL DEFAULT TRUE,
    auth_version BIGINT NOT NULL DEFAULT 1 CHECK (auth_version > 0),
    rps_limit INTEGER NOT NULL DEFAULT 3 CHECK (rps_limit > 0),
    rpm_limit INTEGER NOT NULL DEFAULT 60 CHECK (rpm_limit > 0),
    rate_limit_disabled BOOLEAN NOT NULL DEFAULT FALSE,
    last_login_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS api_keys (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    key_prefix TEXT NOT NULL,
    key_hash TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS api_request_logs (
    id BIGSERIAL PRIMARY KEY,
    request_id TEXT NOT NULL DEFAULT '',
    user_id BIGINT NOT NULL REFERENCES users(id),
    auth_type TEXT NOT NULL CHECK (auth_type IN ('web', 'api_key')),
    method TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    keyword TEXT NOT NULL DEFAULT '',
    status_code INTEGER NOT NULL,
    duration_ms BIGINT NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    result_count INTEGER NOT NULL DEFAULT 0 CHECK (result_count >= 0),
    cache_status TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '',
    source_ip TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS users_active_admin_idx
    ON users (role, enabled, expires_at) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS users_created_idx ON users (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS api_keys_hash_active_idx
    ON api_keys (key_hash) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS api_request_logs_user_created_idx
    ON api_request_logs (user_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS api_request_logs_status_created_idx
    ON api_request_logs (status_code, created_at DESC);
CREATE INDEX IF NOT EXISTS api_request_logs_created_idx
    ON api_request_logs (created_at, id);
