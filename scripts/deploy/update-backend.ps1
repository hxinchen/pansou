param(
    [string]$HostName = "103.236.97.248",
    [int]$Port = 22348,
    [string]$User = "root",
    [string]$KeyPath = (Join-Path $HOME ".ssh\yanhuo"),
    [string]$RemoteRoot = "/opt/pansou-web",
    [string]$EnabledPlugins = "labi,zhizhen,shandian,duoduo,muou,qqpd,gying,weibo",
    [string]$Channels = "",
    [string]$ProxyUrl = "socks5h://192.168.16.1:7890",
    [string]$MihomoControllerUrl = "http://192.168.16.1:9090",
    [string]$MihomoManagedGroups = "良心云",
    [string]$BrowserGatewayUrl = "http://192.168.16.1:18789/fetch",
    [string]$PublicBaseUrl = "http://103.236.97.248:22350",
    [switch]$EnableTieredSearch,
    [switch]$EnableProxyPool,
    [int]$LinkCheckWorkers = 8,
    [int]$LinkCheckPerPlatform = 2,
    [int]$GracefulStopSeconds = 30,
    [switch]$SkipPublicCheck
)

$ErrorActionPreference = "Stop"

function Invoke-Checked {
    param(
        [string]$FilePath,
        [string[]]$Arguments
    )

    & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$FilePath failed with exit code $LASTEXITCODE"
    }
}

function ConvertTo-BashSingleQuoted {
    param([string]$Value)
    return "'" + $Value.Replace("'", "'\''") + "'"
}

function Invoke-RemoteScript {
    param([string]$Script)

    $localScript = [System.IO.Path]::GetTempFileName()
    $remoteScriptPath = "/tmp/pansou-deploy-$([guid]::NewGuid().ToString('N')).sh"
    $sshArgs = @(
        "-i", $KeyPath,
        "-p", $Port.ToString(),
        "-o", "StrictHostKeyChecking=accept-new",
        "${User}@${HostName}"
    )

    try {
        $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
        [System.IO.File]::WriteAllText($localScript, ($Script -replace "`r`n", "`n"), $utf8NoBom)

        Invoke-Checked -FilePath "scp" -Arguments @(
            "-i", $KeyPath,
            "-P", $Port.ToString(),
            "-o", "StrictHostKeyChecking=accept-new",
            $localScript,
            "${User}@${HostName}:$remoteScriptPath"
        )

        & ssh @sshArgs "bash -n $remoteScriptPath"
        if ($LASTEXITCODE -ne 0) {
            $syntaxExitCode = $LASTEXITCODE
            & ssh @sshArgs "rm -f $remoteScriptPath"
            throw "remote bash syntax check failed with exit code $syntaxExitCode"
        }

        & ssh @sshArgs "bash $remoteScriptPath; code=`$?; rm -f $remoteScriptPath; exit `$code"
        if ($LASTEXITCODE -ne 0) {
            throw "remote ssh script failed with exit code $LASTEXITCODE"
        }
    }
    finally {
        if (Test-Path -LiteralPath $localScript) {
            Remove-Item -LiteralPath $localScript -Force
        }
    }
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$artifact = Join-Path $repoRoot "pansou-linux-amd64"
$backupScript = Join-Path $repoRoot "scripts\backup-postgres.sh"
$remoteRootQ = ConvertTo-BashSingleQuoted $RemoteRoot
$enabledPluginsQ = ConvertTo-BashSingleQuoted $EnabledPlugins
if ([string]::IsNullOrWhiteSpace($Channels)) {
    $composePath = Join-Path $repoRoot "docker-compose.yml"
    $channelsLine = Select-String -LiteralPath $composePath -Pattern '^\s*-\s*CHANNELS=(.+)$' | Select-Object -First 1
    if (!$channelsLine) {
        throw "CHANNELS was not provided and no CHANNELS entry was found in $composePath"
    }
    $Channels = $channelsLine.Matches[0].Groups[1].Value.Trim()
}
$channelsQ = ConvertTo-BashSingleQuoted $Channels
$proxyUrlQ = ConvertTo-BashSingleQuoted $ProxyUrl
$mihomoControllerUrlQ = ConvertTo-BashSingleQuoted $MihomoControllerUrl
$mihomoManagedGroupsQ = ConvertTo-BashSingleQuoted $MihomoManagedGroups
$browserGatewayUrlQ = ConvertTo-BashSingleQuoted $BrowserGatewayUrl
$deployTag = (Get-Date).ToUniversalTime().ToString("yyyyMMddHHmmss")
$candidateImage = "local/pansou-api:$deployTag"
$candidateImageQ = ConvertTo-BashSingleQuoted $candidateImage
$tieredSearchQ = ConvertTo-BashSingleQuoted $(if ($EnableTieredSearch) { "true" } else { "false" })
$proxyPoolEnabledQ = ConvertTo-BashSingleQuoted $(if ($EnableProxyPool) { "true" } else { "false" })

if (!(Test-Path -LiteralPath $KeyPath)) {
    throw "SSH key not found: $KeyPath"
}

Push-Location $repoRoot
try {
    Write-Host "Building backend for linux/amd64..."
    $oldGoToolchain = $env:GOTOOLCHAIN
    $oldCgoEnabled = $env:CGO_ENABLED
    $oldGoos = $env:GOOS
    $oldGoarch = $env:GOARCH

    $env:GOTOOLCHAIN = "go1.24.9"
    $env:CGO_ENABLED = "0"
    $env:GOOS = "linux"
    $env:GOARCH = "amd64"

    Invoke-Checked -FilePath "go" -Arguments @("build", "-trimpath", "-ldflags=-s -w", "-o", $artifact, ".")

    Write-Host "Uploading backend binary..."
    Invoke-Checked -FilePath "scp" -Arguments @(
        "-i", $KeyPath,
        "-P", $Port.ToString(),
        "-o", "StrictHostKeyChecking=accept-new",
        $artifact,
        "${User}@${HostName}:${RemoteRoot}/build/"
    )

    Write-Host "Uploading PostgreSQL backup script..."
    Invoke-Checked -FilePath "scp" -Arguments @(
        "-i", $KeyPath,
        "-P", $Port.ToString(),
        "-o", "StrictHostKeyChecking=accept-new",
        $backupScript,
        "${User}@${HostName}:${RemoteRoot}/build/backup-postgres.sh"
    )

    $remoteScript = @"
set -euo pipefail
REMOTE_ROOT=$remoteRootQ
ENABLED_PLUGINS_VALUE=$enabledPluginsQ
CHANNELS_VALUE=$channelsQ
PROXY_URL_VALUE=$proxyUrlQ
MIHOMO_CONTROLLER_URL_VALUE=$mihomoControllerUrlQ
MIHOMO_MANAGED_GROUPS_VALUE=$mihomoManagedGroupsQ
BROWSER_GATEWAY_URL_VALUE=$browserGatewayUrlQ
CANDIDATE_IMAGE=$candidateImageQ
TIERED_SEARCH_VALUE=$tieredSearchQ
PROXY_POOL_ENABLED_VALUE=$proxyPoolEnabledQ
GRACEFUL_STOP_SECONDS=$GracefulStopSeconds
LINK_CHECK_WORKERS_VALUE=$LinkCheckWorkers
LINK_CHECK_PLATFORM_VALUE=$LinkCheckPerPlatform
ROLLBACK_CONTAINER=pansou-api-rollback

mkdir -p "`$REMOTE_ROOT/build" "`$REMOTE_ROOT/cache" "`$REMOTE_ROOT/backups" "`$REMOTE_ROOT/scripts"
cd "`$REMOTE_ROOT"
cp /etc/ssl/certs/ca-certificates.crt build/ca-certificates.crt
chmod +x build/pansou-linux-amd64
install -m 700 build/backup-postgres.sh scripts/backup-postgres.sh

PLUGIN_SECRETS_FILE="`$REMOTE_ROOT/plugin-secrets.env"
if [ ! -f "`$PLUGIN_SECRETS_FILE" ]; then
  umask 077
  {
    echo "QQPD_HASH_SALT=`$(openssl rand -hex 32)"
    echo "QQPD_ENCRYPTION_KEY=`$(openssl rand -hex 16)"
    echo "GYING_HASH_SALT=`$(openssl rand -hex 32)"
    echo "GYING_ENCRYPTION_KEY=`$(openssl rand -hex 16)"
    echo "WEIBO_HASH_SALT=`$(openssl rand -hex 32)"
    echo "WEIBO_ENCRYPTION_KEY=`$(openssl rand -hex 16)"
  } > "`$PLUGIN_SECRETS_FILE"
fi
chmod 600 "`$PLUGIN_SECRETS_FILE"

DATABASE_SECRETS_FILE="`$REMOTE_ROOT/database-secrets.env"
if [ ! -f "`$DATABASE_SECRETS_FILE" ]; then
  umask 077
  DB_PASSWORD=`$(openssl rand -hex 32)
  ADMIN_PASSWORD=`$(openssl rand -hex 24)
  {
    echo "POSTGRES_PASSWORD=`$DB_PASSWORD"
    echo "DATABASE_URL=postgres://pansou:`$DB_PASSWORD@pansou-postgres:5432/pansou?sslmode=disable"
    echo "AUTH_USERS=admin:`$ADMIN_PASSWORD"
    echo "AUTH_JWT_SECRET=`$(openssl rand -hex 48)"
  } > "`$DATABASE_SECRETS_FILE"
  echo "Generated PanSou admin password: `$ADMIN_PASSWORD"
fi
chmod 600 "`$DATABASE_SECRETS_FILE"

if ! grep -q '^MIHOMO_CONTROLLER_SECRET=' "`$DATABASE_SECRETS_FILE"; then
  umask 077
  echo "MIHOMO_CONTROLLER_SECRET=`$(openssl rand -hex 32)" >> "`$DATABASE_SECRETS_FILE"
fi
MIHOMO_CONTROLLER_SECRET_VALUE=`$(sed -n 's/^MIHOMO_CONTROLLER_SECRET=//p' "`$DATABASE_SECRETS_FILE" | tail -1)
if [ -z "`$MIHOMO_CONTROLLER_SECRET_VALUE" ]; then
  echo 'Mihomo controller secret is empty.' >&2
  exit 1
fi

MIHOMO_CONFIG=/etc/mihomo/config.yaml
if [ ! -f "`$MIHOMO_CONFIG" ]; then
  echo "Mihomo config not found: `$MIHOMO_CONFIG" >&2
  exit 1
fi
if [ ! -f "`$MIHOMO_CONFIG.pansou-controller.bak" ]; then
  cp -a "`$MIHOMO_CONFIG" "`$MIHOMO_CONFIG.pansou-controller.bak"
fi
MIHOMO_WORK=`$(mktemp)
cp -a "`$MIHOMO_CONFIG" "`$MIHOMO_WORK"
MIHOMO_CHANGED=false
if ! grep -Fxq "external-controller: '192.168.16.1:9090'" "`$MIHOMO_WORK"; then
  if grep -q '^external-controller:' "`$MIHOMO_WORK"; then
    sed -i -E "s|^external-controller:.*`$|external-controller: '192.168.16.1:9090'|" "`$MIHOMO_WORK"
  else
    printf "\nexternal-controller: '192.168.16.1:9090'\n" >> "`$MIHOMO_WORK"
  fi
  MIHOMO_CHANGED=true
fi
if ! grep -Fxq "secret: '`$MIHOMO_CONTROLLER_SECRET_VALUE'" "`$MIHOMO_WORK"; then
  if grep -q '^secret:' "`$MIHOMO_WORK"; then
    sed -i -E "s|^secret:.*`$|secret: '`$MIHOMO_CONTROLLER_SECRET_VALUE'|" "`$MIHOMO_WORK"
  else
    printf "secret: '%s'\n" "`$MIHOMO_CONTROLLER_SECRET_VALUE" >> "`$MIHOMO_WORK"
  fi
  MIHOMO_CHANGED=true
fi
if [ "`$MIHOMO_CHANGED" = true ]; then
  /usr/local/bin/mihomo -t -d /etc/mihomo -f "`$MIHOMO_WORK" >/dev/null
  cp -a "`$MIHOMO_WORK" "`$MIHOMO_CONFIG"
  systemctl restart mihomo
fi
rm -f "`$MIHOMO_WORK"
for i in `$(seq 1 20); do
  if curl -fsS --max-time 2 -H "Authorization: Bearer `$MIHOMO_CONTROLLER_SECRET_VALUE" http://192.168.16.1:9090/version >/dev/null; then
    break
  fi
  sleep 1
done
curl -fsS --max-time 3 -H "Authorization: Bearer `$MIHOMO_CONTROLLER_SECRET_VALUE" http://192.168.16.1:9090/version >/dev/null

if ! docker network inspect pansou-network >/dev/null 2>&1; then
  docker network create pansou-network >/dev/null
fi
TRUSTED_PROXY_SUBNET=`$(docker network inspect --format '{{(index .IPAM.Config 0).Subnet}}' pansou-network)
if [ -z "`$TRUSTED_PROXY_SUBNET" ]; then
  echo 'Unable to determine pansou-network CIDR for TRUSTED_PROXIES.' >&2
  exit 1
fi
TRUSTED_PROXIES_VALUE="`$TRUSTED_PROXY_SUBNET,127.0.0.1,::1"
echo "Trusted reverse-proxy network: `$TRUSTED_PROXIES_VALUE"
if ! docker volume inspect pansou-postgres >/dev/null 2>&1; then
  docker volume create pansou-postgres >/dev/null
fi
HOST_POSTGRES=false
if command -v pg_isready >/dev/null 2>&1 && systemctl is-active --quiet postgresql 2>/dev/null && \
   pg_isready -h 127.0.0.1 -U pansou -d pansou >/dev/null 2>&1; then
  HOST_POSTGRES=true
  echo 'Using system PostgreSQL.'
else
  if ! docker ps --format '{{.Names}}' | grep -qx 'pansou-postgres'; then
    docker rm -f pansou-postgres >/dev/null 2>&1 || true
    docker run -d \
      --name pansou-postgres \
      --restart unless-stopped \
      --network pansou-network \
      --env-file "`$DATABASE_SECRETS_FILE" \
      -e POSTGRES_USER=pansou \
      -e POSTGRES_DB=pansou \
      -v pansou-postgres:/var/lib/postgresql/data \
      -v "`$REMOTE_ROOT/backups:/backups" \
      postgres:16-alpine
  fi

  for i in `$(seq 1 30); do
    if docker exec pansou-postgres pg_isready -U pansou -d pansou >/dev/null 2>&1; then break; fi
    sleep 1
  done
  docker exec pansou-postgres pg_isready -U pansou -d pansou >/dev/null
fi

BACKUP_MARKER='# pansou-postgres-backup'
LEGACY_BACKUP_MARKER='# pansou-postgres-host-backup'
if [ "`$HOST_POSTGRES" = true ] && [ -x "`$REMOTE_ROOT/scripts/backup-postgres-host.sh" ]; then
  BACKUP_JOB="17 3 * * * \"`$REMOTE_ROOT/scripts/backup-postgres-host.sh\" >> \"`$REMOTE_ROOT/backups/backup.log\" 2>&1 `$BACKUP_MARKER"
else
  BACKUP_JOB="17 3 * * * BACKUP_DIR=\"`$REMOTE_ROOT/backups\" /bin/sh \"`$REMOTE_ROOT/scripts/backup-postgres.sh\" >> \"`$REMOTE_ROOT/backups/backup.log\" 2>&1 `$BACKUP_MARKER"
fi
if command -v crontab >/dev/null 2>&1; then
  {
    crontab -l 2>/dev/null |
      grep -vF "`$BACKUP_MARKER" |
      grep -vF "`$LEGACY_BACKUP_MARKER" || true
    printf '%s\n' "`$BACKUP_JOB"
  } | crontab -
elif [ "`$(id -u)" -eq 0 ] && [ -d /etc/cron.d ]; then
  printf '17 3 * * * root BACKUP_DIR="%s/backups" /bin/sh "%s/scripts/backup-postgres.sh" >> "%s/backups/backup.log" 2>&1\n' \
    "`$REMOTE_ROOT" "`$REMOTE_ROOT" "`$REMOTE_ROOT" > /etc/cron.d/pansou-postgres-backup
  chmod 644 /etc/cron.d/pansou-postgres-backup
else
  echo 'WARNING: cron is unavailable; install a daily schedule for scripts/backup-postgres.sh' >&2
fi

cat > build/Dockerfile <<'EOF'
FROM scratch
WORKDIR /app
COPY ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY pansou-linux-amd64 /app/pansou
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
ENV CACHE_PATH=/app/cache
ENV PORT=8888
EXPOSE 8888
CMD ["/app/pansou"]
EOF

docker build -t "`$CANDIDATE_IMAGE" build

pansou_psql() {
  if [ "`$HOST_POSTGRES" = true ]; then
    sudo -u postgres psql -d pansou "`$@"
  else
    docker exec pansou-postgres psql -U pansou -d pansou "`$@"
  fi
}

if [ "`$(pansou_psql -Atqc "SELECT to_regclass('public.resources')")" = "resources" ]; then
  echo 'Preparing pg_trgm indexes while the current API remains online...'
  pansou_psql -v ON_ERROR_STOP=1 -c 'CREATE EXTENSION IF NOT EXISTS pg_trgm'
  pansou_psql -v ON_ERROR_STOP=1 -c 'CREATE INDEX CONCURRENTLY IF NOT EXISTS resources_title_trgm_idx ON resources USING gin (title gin_trgm_ops)'
  pansou_psql -v ON_ERROR_STOP=1 -c 'CREATE INDEX CONCURRENTLY IF NOT EXISTS resources_content_trgm_idx ON resources USING gin (content gin_trgm_ops)'
  pansou_psql -v ON_ERROR_STOP=1 -c 'CREATE INDEX CONCURRENTLY IF NOT EXISTS resources_url_trgm_idx ON resources USING gin (url gin_trgm_ops)'
  VALID_TRGM_INDEXES=`$(pansou_psql -Atqc "SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid WHERE c.relname IN ('resources_title_trgm_idx','resources_content_trgm_idx','resources_url_trgm_idx') AND i.indisvalid AND i.indisready")
  if [ "`$VALID_TRGM_INDEXES" -ne 3 ]; then
    echo "Expected 3 valid trigram indexes, found `$VALID_TRGM_INDEXES. Existing API was not stopped." >&2
    exit 1
  fi
fi

if docker ps -a --format '{{.Names}}' | grep -qx "`$ROLLBACK_CONTAINER"; then
  echo "Stale rollback container `$ROLLBACK_CONTAINER exists; resolve it before deploying." >&2
  exit 1
fi

OLD_CONTAINER_PRESENT=false
if docker ps -a --format '{{.Names}}' | grep -qx 'pansou-api'; then
  echo "Gracefully stopping current API (timeout: `${GRACEFUL_STOP_SECONDS}s)..."
  docker stop -t "`$GRACEFUL_STOP_SECONDS" pansou-api
  docker rename pansou-api "`$ROLLBACK_CONTAINER"
  OLD_CONTAINER_PRESENT=true
fi

rollback_backend() {
  echo 'New backend failed health checks; restoring previous container.' >&2
  docker rm -f pansou-api >/dev/null 2>&1 || true
  if [ "`$OLD_CONTAINER_PRESENT" = true ] && docker ps -a --format '{{.Names}}' | grep -qx "`$ROLLBACK_CONTAINER"; then
    docker rename "`$ROLLBACK_CONTAINER" pansou-api
    docker start pansou-api >/dev/null
    for i in `$(seq 1 30); do
      if curl -fsS http://127.0.0.1:8889/api/health >/dev/null; then
        echo 'Previous backend restored.' >&2
        return 0
      fi
      sleep 2
    done
    echo 'Previous backend container restarted but did not become healthy.' >&2
  fi
  return 1
}

if ! docker run -d \
  --name pansou-api \
  --restart unless-stopped \
  -p 127.0.0.1:8889:8888 \
  --network pansou-network \
  --env-file "`$PLUGIN_SECRETS_FILE" \
  --env-file "`$DATABASE_SECRETS_FILE" \
  -e AUTH_ENABLED=true \
  -e "CHANNELS=`$CHANNELS_VALUE" \
  -e "ENABLED_PLUGINS=`$ENABLED_PLUGINS_VALUE" \
  -e ASYNC_LOG_ENABLED=false \
	-e GC_PERCENT=75 \
	-e HTTP_MAX_CONNS=800 \
	-e TG_SEARCH_WORKERS=20 \
	-e SEARCH_SCHEDULER_ENABLED=true \
	-e SEARCH_ACTIVE_LIMIT=8 \
	-e SEARCH_QUEUE_SIZE=100 \
	-e SEARCH_TG_WORKERS=32 \
	-e SEARCH_PLUGIN_WORKERS=32 \
	-e SEARCH_CREDENTIAL_WORKERS=16 \
	-e SEARCH_PER_REQUEST_TG=20 \
	-e SEARCH_PER_REQUEST_PLUGIN=16 \
	-e SEARCH_PER_SOURCE_LIMIT=2 \
	-e SEARCH_CIRCUIT_FAILURES=5 \
	-e SEARCH_CIRCUIT_COOLDOWN_SECONDS=300 \
	-e SEARCH_METRICS_FLUSH_SECONDS=60 \
	-e "SEARCH_TIERED_ROLLOUT_ENABLED=`$TIERED_SEARCH_VALUE" \
	-e "PROXY_POOL_ENABLED=`$PROXY_POOL_ENABLED_VALUE" \
	-e PROXY_POOL_HEALTH_ENABLED=true \
	-e PROXY_POOL_HEALTH_WORKERS=16 \
	-e PROXY_POOL_PROBE_TIMEOUT_SECONDS=10 \
	-e PROXY_POOL_PROBE_INTERVAL_SECONDS=30 \
	-e PROXY_POOL_REFRESH_SECONDS=30 \
	-e PROXY_POOL_FAILURE_THRESHOLD=3 \
	-e PROXY_POOL_COOLDOWN_SECONDS=300 \
	-e PROXY_POOL_MAX_HOT_NODES=1000 \
	-e PROXY_POOL_MAX_PER_NODE=2 \
	-e "MIHOMO_CONTROLLER_URL=`$MIHOMO_CONTROLLER_URL_VALUE" \
	-e "MIHOMO_MANAGED_GROUPS=`$MIHOMO_MANAGED_GROUPS_VALUE" \
	-e MIHOMO_CONFIG_PATH=/host-mihomo/config.yaml \
	-e MIHOMO_RELOAD_PATH=/etc/mihomo/config.yaml \
	-e MIHOMO_EXIT_INFO_URL=https://ipinfo.io/json \
	-e MIHOMO_CONTROLLER_TIMEOUT_SECONDS=5 \
	-e MIHOMO_DELAY_TEST_URL=http://www.gstatic.com/generate_204 \
	-e MIHOMO_DELAY_TEST_TIMEOUT_SECONDS=6 \
	-e GYING_HEALTH_CHECK_ENABLED=true \
	-e GYING_HEALTH_CHECK_INTERVAL_SECONDS=21600 \
	-e GYING_HEALTH_CHECK_SCAN_SECONDS=1800 \
	-e GYING_HEALTH_CHECK_INITIAL_DELAY_SECONDS=120 \
	-e GYING_HEALTH_CHECK_TIMEOUT_SECONDS=30 \
	-e GYING_HEALTH_CHECK_JITTER_SECONDS=15 \
	-e GYING_HEALTH_CHECK_BATCH_SIZE=50 \
	-v /etc/mihomo:/host-mihomo \
	-e LINK_CHECK_WORKERS=`$LINK_CHECK_WORKERS_VALUE \
	-e LINK_CHECK_TIMEOUT_SECONDS=15 \
	-e LINK_CHECK_PER_PLATFORM=`$LINK_CHECK_PLATFORM_VALUE \
	-e LINK_CHECK_CIRCUIT_FAILURES=5 \
	-e LINK_CHECK_CIRCUIT_COOLDOWN_SECONDS=300 \
	-e LINK_CHECK_BACKLOG_INTERVAL_SECONDS=300 \
	-e LINK_CHECK_WRITE_BATCH_SIZE=16 \
	-e LINK_CHECK_WRITE_FLUSH_SECONDS=1 \
  -e CACHE_PATH=/app/cache \
  -e "TRUSTED_PROXIES=`$TRUSTED_PROXIES_VALUE" \
  -e "PROXY=`$PROXY_URL_VALUE" \
  -e "KEYWORD_BROWSER_GATEWAY_URL=`$BROWSER_GATEWAY_URL_VALUE" \
  -v "`$REMOTE_ROOT/cache:/app/cache" \
  "`$CANDIDATE_IMAGE"; then
  rollback_backend || true
  exit 1
fi

MIHOMO_NOFILE=`$(systemctl show mihomo -p LimitNOFILE --value 2>/dev/null || true)
echo "mihomo LimitNOFILE=`${MIHOMO_NOFILE:-unknown}"
if [ -n "`$MIHOMO_NOFILE" ] && [ "`$MIHOMO_NOFILE" -lt 4096 ]; then
  echo 'WARNING: mihomo LimitNOFILE should be at least 4096.' >&2
fi
if [ "`$HOST_POSTGRES" = true ]; then
  SHARED_BUFFERS=`$(sudo -u postgres psql -d postgres -Atc 'show shared_buffers' 2>/dev/null || true)
  echo "PostgreSQL shared_buffers=`${SHARED_BUFFERS:-unknown}; consider 256MB-512MB after observing total host RSS."
fi

NEW_HEALTHY=false
for i in `$(seq 1 30); do
  if curl -fsS http://127.0.0.1:8889/api/health; then
    echo
    docker ps --filter name=pansou-api --format 'container={{.Names}} status={{.Status}} ports={{.Ports}}'
    NEW_HEALTHY=true
    break
  fi
  sleep 2
done

if [ "`$NEW_HEALTHY" != true ]; then
  docker logs --tail 160 pansou-api || true
  rollback_backend || true
  exit 1
fi

echo "Candidate backend ready: `$CANDIDATE_IMAGE"
"@

    Write-Host "Rebuilding and restarting backend container..."
    Invoke-RemoteScript $remoteScript

    try {
        if (!$SkipPublicCheck) {
            Write-Host "Checking public API..."
            $health = Invoke-WebRequest -Uri "$PublicBaseUrl/api/health" -UseBasicParsing -TimeoutSec 20
            Write-Host "Public API OK: HTTP $($health.StatusCode)"
        }

        $finalizeScript = @"
set -euo pipefail
CANDIDATE_IMAGE=$candidateImageQ
if docker ps -a --format '{{.Names}}' | grep -qx 'pansou-api-rollback'; then
  docker rm pansou-api-rollback >/dev/null
fi
docker tag "`$CANDIDATE_IMAGE" local/pansou-api:latest
echo "Deployment finalized: `$CANDIDATE_IMAGE"
"@
        Invoke-RemoteScript $finalizeScript
    }
    catch {
        $deploymentError = $_
        Write-Warning "Deployment verification failed; restoring previous backend."
        $rollbackScript = @"
set -euo pipefail
if ! docker ps -a --format '{{.Names}}' | grep -qx 'pansou-api-rollback'; then
  echo 'Rollback container is unavailable.' >&2
  exit 1
fi
docker stop -t 15 pansou-api >/dev/null 2>&1 || true
docker rm -f pansou-api >/dev/null 2>&1 || true
docker rename pansou-api-rollback pansou-api
docker start pansou-api >/dev/null
for i in `$(seq 1 30); do
  if curl -fsS http://127.0.0.1:8889/api/health >/dev/null; then
    echo 'Previous backend restored after external verification failure.'
    exit 0
  fi
  sleep 2
done
docker logs --tail 160 pansou-api || true
exit 1
"@
        try {
            Invoke-RemoteScript $rollbackScript
        }
        catch {
            Write-Error "Automatic rollback also failed: $($_.Exception.Message)"
        }
        throw $deploymentError
    }

    Write-Host "Backend deployment complete."
}
finally {
    $env:GOTOOLCHAIN = $oldGoToolchain
    $env:CGO_ENABLED = $oldCgoEnabled
    $env:GOOS = $oldGoos
    $env:GOARCH = $oldGoarch

    Pop-Location
    if (Test-Path -LiteralPath $artifact) {
        Remove-Item -LiteralPath $artifact -Force
    }
}
