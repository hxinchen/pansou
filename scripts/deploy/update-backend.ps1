param(
    [string]$HostName = "103.236.97.248",
    [int]$Port = 22348,
    [string]$User = "root",
    [string]$KeyPath = (Join-Path $HOME ".ssh\yanhuo"),
    [string]$RemoteRoot = "/opt/pansou-web",
    [string]$EnabledPlugins = "labi,zhizhen,shandian,duoduo,muou,qqpd,gying,weibo",
    [string]$Channels = "",
    [string]$ProxyUrl = "socks5h://192.168.0.1:7890",
    [string]$BrowserGatewayUrl = "http://192.168.16.1:18789/fetch",
    [string]$PublicBaseUrl = "http://103.236.97.248:22350",
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
$browserGatewayUrlQ = ConvertTo-BashSingleQuoted $BrowserGatewayUrl

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
BROWSER_GATEWAY_URL_VALUE=$browserGatewayUrlQ

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

docker build -t local/pansou-api:latest build

if docker ps -a --format '{{.Names}}' | grep -qx 'pansou-api'; then
  docker rm -f pansou-api
fi

docker run -d \
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
  -e CACHE_PATH=/app/cache \
  -e "TRUSTED_PROXIES=`$TRUSTED_PROXIES_VALUE" \
  -e "PROXY=`$PROXY_URL_VALUE" \
  -e "KEYWORD_BROWSER_GATEWAY_URL=`$BROWSER_GATEWAY_URL_VALUE" \
  -v "`$REMOTE_ROOT/cache:/app/cache" \
  local/pansou-api:latest

for i in `$(seq 1 30); do
  if curl -fsS http://127.0.0.1:8889/api/health; then
    echo
    docker ps --filter name=pansou-api --format 'container={{.Names}} status={{.Status}} ports={{.Ports}}'
    exit 0
  fi
  sleep 2
done

docker logs --tail 160 pansou-api
exit 1
"@

    Write-Host "Rebuilding and restarting backend container..."
    Invoke-RemoteScript $remoteScript

    if (!$SkipPublicCheck) {
        Write-Host "Checking public API..."
        $health = Invoke-WebRequest -Uri "$PublicBaseUrl/api/health" -UseBasicParsing -TimeoutSec 20
        Write-Host "Public API OK: HTTP $($health.StatusCode)"
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
