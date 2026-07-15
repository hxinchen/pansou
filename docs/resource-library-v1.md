# PanSou resource library V1

The resource library is optional at runtime. When `DATABASE_URL` is absent,
PanSou keeps its original live-search and disk-cache behavior. When it is set,
startup applies embedded SQL migrations before the HTTP server begins serving.
A migration failure is fatal so an incompatible schema is never used.

## Compose deployment

Create the local secret file before the first start:

```bash
mkdir -p secrets backups
password="$(openssl rand -hex 32)"
admin_password="$(openssl rand -hex 24)"
jwt_secret="$(openssl rand -hex 48)"
printf 'POSTGRES_PASSWORD=%s\nDATABASE_URL=postgres://pansou:%s@postgres:5432/pansou?sslmode=disable\nAUTH_USERS=admin:%s\nAUTH_JWT_SECRET=%s\n' \
  "$password" "$password" "$admin_password" "$jwt_secret" > secrets/database.env
chmod 600 secrets/database.env
docker compose up -d
```

`secrets/database.env` and `backups/` are ignored by Git. PostgreSQL data lives
in the named `pansou-postgres` volume. The existing search cache remains in the
separate `pansou-cache` volume and is not imported into PostgreSQL.

The management UI is served at `/admin/`. It requires the existing JWT system:

```env
AUTH_ENABLED=true
AUTH_USERS=admin:a-long-unique-password
AUTH_JWT_SECRET=a-separate-random-signing-secret
```

Add these values to the same protected environment file, or inject them through
the deployment platform. Management APIs return `503` when authentication or
PostgreSQL is not configured.

Optional collection tuning variables keep conservative defaults:

| Variable | Default | Purpose |
| --- | --- | --- |
| `COLLECTION_INTERVAL_SECONDS` | `60` | Scheduler eligibility check interval |
| `COLLECTION_DEFAULT_COOLDOWN_HOURS` | `168` | Global keyword cooldown |
| `LINK_CHECK_WORKERS` | `4` | Concurrent asynchronous link checks |
| `HYBRID_REFRESH_AFTER_MINUTES` | `60` | Age before a database hit triggers a background live refresh |

## Periodic link rechecks

Administrators can open **Resource library → Link-check policy** in `/admin/`
to select terminal states for periodic rechecking and set one shared interval
from one hour through 365 days. The policy is stored in PostgreSQL and is
reloaded by the in-process scheduler within one minute; no external cron or
service restart is required.

The migrated default is disabled with `valid` and `unknown` selected and a
seven-day interval. Newly discovered `pending` resources are always checked,
even when periodic rechecks are disabled. A visible resource must receive two
definitive negative results at least one hour apart before its persisted state
changes to `invalid`, `expired`, `cancelled`, or `violation`.

## Daily backup

`scripts/deploy/update-backend.ps1` installs the following job idempotently on
hosts with `crontab` or `/etc/cron.d`. For a Compose-only installation, add the
same host cron entry after placing the repository at `/opt/pansou`:

```cron
17 3 * * * cd /opt/pansou && /bin/sh ./scripts/backup-postgres.sh >> /var/log/pansou-backup.log 2>&1
```

The script targets the `pansou-postgres` container by default, writes
custom-format `pg_dump` files with mode `600`, and keeps the latest seven daily
backups. Test restoration before relying on the schedule:

```bash
docker exec pansou-postgres pg_restore --list /backups/pansou-YYYYMMDDTHHMMSSZ.dump >/dev/null
```

## Rollout checks

1. Confirm `GET /api/health` reports the database as healthy.
2. Confirm an unauthenticated request to `/api/admin/overview` returns `401`.
3. Add a keyword in `/admin/`, run it, and watch the run finish.
4. Search the same keyword twice and confirm the resource count does not grow
   for duplicate normalized URLs.
5. Open the resource-library link-check policy and confirm its disabled,
   seven-day default before explicitly enabling it.
6. Execute `/bin/sh scripts/backup-postgres.sh` once and inspect the generated dump.
