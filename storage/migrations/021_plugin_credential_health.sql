ALTER TABLE plugin_credentials
    ADD COLUMN IF NOT EXISTS last_health_check_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_health_status TEXT NOT NULL DEFAULT 'unknown',
    ADD COLUMN IF NOT EXISTS last_health_error_code TEXT NOT NULL DEFAULT '';

ALTER TABLE plugin_credentials
    DROP CONSTRAINT IF EXISTS plugin_credentials_last_health_status_check;

ALTER TABLE plugin_credentials
    ADD CONSTRAINT plugin_credentials_last_health_status_check
    CHECK (last_health_status IN ('unknown', 'healthy', 'error', 'invalid'));

CREATE INDEX IF NOT EXISTS plugin_credentials_health_due_idx
    ON plugin_credentials (plugin_key, last_health_check_at, id)
    WHERE owner_enabled = TRUE
      AND admin_suspended_at IS NULL
      AND status IN ('active', 'expired');
