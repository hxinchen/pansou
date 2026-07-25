\set ON_ERROR_STOP on
\timing on

SET statement_timeout = 0;
SET lock_timeout = '5s';

INSERT INTO resource_keyword_terms (normalized_keyword, keyword, keyword_type)
SELECT DISTINCT ON (normalized_keyword)
    normalized_keyword, keyword, keyword_type
FROM resource_keywords
ORDER BY normalized_keyword, char_length(keyword) DESC, last_seen_at DESC
ON CONFLICT (normalized_keyword) DO UPDATE SET
    keyword = CASE
        WHEN char_length(EXCLUDED.keyword) > char_length(resource_keyword_terms.keyword)
            THEN EXCLUDED.keyword
        ELSE resource_keyword_terms.keyword
    END,
    keyword_type = CASE
        WHEN resource_keyword_terms.keyword_type = 'general' THEN EXCLUDED.keyword_type
        ELSE resource_keyword_terms.keyword_type
    END,
    updated_at = now();

INSERT INTO resource_contents (content_hash, content)
SELECT digest(content, 'sha256'), content
FROM resources
WHERE content <> ''
GROUP BY digest(content, 'sha256'), content
ON CONFLICT (content_hash) DO NOTHING;

SELECT format($sql$
INSERT INTO resource_keyword_links (
    resource_id, term_id, keyword_id, first_seen_at, last_seen_at, discovery_count
)
SELECT rk.resource_id, term.id, rk.keyword_id, rk.first_seen_at, rk.last_seen_at, rk.discovery_count
FROM resource_keywords rk
JOIN resource_keyword_terms term ON term.normalized_keyword = rk.normalized_keyword
WHERE rk.resource_id >= %s AND rk.resource_id < %s
ON CONFLICT (resource_id, term_id) DO UPDATE SET
    keyword_id = COALESCE(resource_keyword_links.keyword_id, EXCLUDED.keyword_id),
    first_seen_at = LEAST(resource_keyword_links.first_seen_at, EXCLUDED.first_seen_at),
    last_seen_at = GREATEST(resource_keyword_links.last_seen_at, EXCLUDED.last_seen_at),
    discovery_count = GREATEST(resource_keyword_links.discovery_count, EXCLUDED.discovery_count);
$sql$, lower_bound, lower_bound + 10000)
FROM generate_series(
    0::bigint,
    COALESCE((SELECT max(id) FROM resources), 0),
    10000::bigint
) AS bounds(lower_bound)
\gexec

SELECT format($sql$
UPDATE resources r
SET content_id = content.id
FROM resource_contents content
WHERE r.id >= %s AND r.id < %s
  AND r.content <> ''
  AND content.content_hash = digest(r.content, 'sha256')
  AND content.content = r.content
  AND r.content_id IS DISTINCT FROM content.id;
$sql$, lower_bound, lower_bound + 10000)
FROM generate_series(
    0::bigint,
    COALESCE((SELECT max(id) FROM resources), 0),
    10000::bigint
) AS bounds(lower_bound)
\gexec

ANALYZE resource_contents;
ANALYZE resource_keyword_terms;
ANALYZE resource_keyword_links;
ANALYZE resources;

SELECT
    (SELECT count(*) FROM resource_keywords) AS legacy_keyword_links,
    (SELECT count(*) FROM resource_keyword_links) AS normalized_keyword_links,
    (SELECT count(*) FROM resources WHERE content <> '') AS resources_with_content,
    (SELECT count(*) FROM resources WHERE content_id IS NOT NULL) AS resources_with_content_id,
    (SELECT count(*) FROM resource_contents) AS unique_contents;
