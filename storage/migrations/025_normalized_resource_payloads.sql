CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS resource_contents (
    id BIGSERIAL PRIMARY KEY,
    content_hash BYTEA NOT NULL UNIQUE,
    content TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE resources
    ADD COLUMN IF NOT EXISTS content_id BIGINT REFERENCES resource_contents(id) ON DELETE SET NULL;

CREATE TABLE IF NOT EXISTS resource_keyword_terms (
    id BIGSERIAL PRIMARY KEY,
    normalized_keyword TEXT NOT NULL UNIQUE,
    keyword TEXT NOT NULL,
    keyword_type TEXT NOT NULL DEFAULT 'general',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS resource_keyword_links (
    resource_id BIGINT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    term_id BIGINT NOT NULL REFERENCES resource_keyword_terms(id),
    keyword_id BIGINT REFERENCES keywords(id) ON DELETE SET NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    discovery_count BIGINT NOT NULL DEFAULT 1 CHECK (discovery_count > 0),
    PRIMARY KEY (resource_id, term_id)
);

CREATE INDEX IF NOT EXISTS resource_keyword_links_term_resource_idx
    ON resource_keyword_links (term_id, resource_id);

CREATE INDEX IF NOT EXISTS resource_keyword_links_resource_seen_idx
    ON resource_keyword_links (resource_id, last_seen_at DESC, term_id);

CREATE INDEX IF NOT EXISTS resource_keyword_links_keyword_id_idx
    ON resource_keyword_links (keyword_id, term_id, resource_id)
    WHERE keyword_id IS NOT NULL;
