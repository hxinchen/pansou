\set ON_ERROR_STOP on
\timing on

SET statement_timeout = 0;
SET lock_timeout = '30s';

CREATE TEMP TABLE latest_resource_source_ids AS
SELECT DISTINCT ON (resource_id) id
FROM resource_sources
ORDER BY resource_id, last_seen_at DESC, id DESC;

CREATE UNIQUE INDEX latest_resource_source_ids_pkey
    ON latest_resource_source_ids (id);

UPDATE resource_sources source
SET source_metadata = source.source_metadata - 'images' - 'tags'
WHERE (source.source_metadata ? 'images' OR source.source_metadata ? 'tags')
  AND NOT EXISTS (
      SELECT 1 FROM latest_resource_source_ids latest WHERE latest.id=source.id
  );

DROP TABLE latest_resource_source_ids;

VACUUM (FULL, ANALYZE) resources;
VACUUM (FULL, ANALYZE) resource_sources;
VACUUM (ANALYZE) resource_contents;
VACUUM (ANALYZE) resource_keyword_links;

SELECT relname,
       pg_size_pretty(pg_total_relation_size(oid)) AS total_size,
       pg_total_relation_size(oid) AS total_bytes
FROM pg_class
WHERE relname IN (
    'resources', 'resource_contents', 'resource_keyword_terms',
    'resource_keyword_links', 'resource_sources'
)
ORDER BY total_bytes DESC;

SELECT pg_size_pretty(pg_database_size(current_database())) AS database_size,
       pg_database_size(current_database()) AS database_bytes;
