#!/bin/sh
set -eu

BACKUP_DIR="${BACKUP_DIR:-./backups}"
mkdir -p "$BACKUP_DIR"
umask 077

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
temporary="$BACKUP_DIR/pansou-$timestamp.dump.tmp"
destination="$BACKUP_DIR/pansou-$timestamp.dump"
container="${POSTGRES_CONTAINER:-pansou-postgres}"
database="${POSTGRES_DB:-pansou}"
user="${POSTGRES_USER:-pansou}"

trap 'rm -f "$temporary"' EXIT HUP INT TERM
docker exec "$container" pg_dump -U "$user" -d "$database" -Fc > "$temporary"
mv "$temporary" "$destination"
trap - EXIT HUP INT TERM

# Keep the current backup plus the previous six daily backups.
find "$BACKUP_DIR" -type f -name 'pansou-*.dump' -mtime +6 -delete
printf '%s\n' "$destination"
