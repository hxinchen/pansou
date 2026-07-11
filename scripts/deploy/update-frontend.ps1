param(
    [string]$HostName = "103.236.97.248",
    [int]$Port = 22348,
    [string]$User = "root",
    [string]$KeyPath = (Join-Path $HOME ".ssh\yanhuo"),
    [string]$RemoteRoot = "/opt/pansou-web",
    [string]$FrontendRoot = "",
    [string]$BasePath = "/pansou/",
    [string]$PublicBaseUrl = "http://103.236.97.248:22348",
    [switch]$SkipNpmInstall,
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
if ([string]::IsNullOrWhiteSpace($FrontendRoot)) {
    $FrontendRoot = (Resolve-Path (Join-Path $repoRoot "..\pansou-web")).Path
} else {
    $FrontendRoot = (Resolve-Path $FrontendRoot).Path
}

$artifact = Join-Path $repoRoot "pansou-web-dist.tar.gz"
$remoteRootQ = ConvertTo-BashSingleQuoted $RemoteRoot
$npmCommandInfo = Get-Command "npm.cmd" -ErrorAction SilentlyContinue
if ($npmCommandInfo) {
    $npmCommand = $npmCommandInfo.Source
} else {
    $npmCommand = "npm"
}

if (!(Test-Path -LiteralPath $KeyPath)) {
    throw "SSH key not found: $KeyPath"
}

if (!(Test-Path -LiteralPath (Join-Path $FrontendRoot "package.json"))) {
    throw "Frontend package.json not found. Expected pansou-web repo at: $FrontendRoot"
}

Push-Location $FrontendRoot
try {
    if (!$SkipNpmInstall -and !(Test-Path -LiteralPath (Join-Path $FrontendRoot "node_modules"))) {
        Write-Host "Installing frontend dependencies..."
        Invoke-Checked -FilePath $npmCommand -Arguments @("ci")
    }

    Write-Host "Building frontend with base path $BasePath..."
    Invoke-Checked -FilePath $npmCommand -Arguments @("run", "build", "--", "--base=$BasePath")

    if (Test-Path -LiteralPath $artifact) {
        Remove-Item -LiteralPath $artifact -Force
    }

    Write-Host "Packing frontend dist..."
    Invoke-Checked -FilePath "tar" -Arguments @("-C", (Join-Path $FrontendRoot "dist"), "-czf", $artifact, ".")

    Write-Host "Uploading frontend bundle..."
    Invoke-Checked -FilePath "scp" -Arguments @(
        "-i", $KeyPath,
        "-P", $Port.ToString(),
        "-o", "StrictHostKeyChecking=accept-new",
        $artifact,
        "${User}@${HostName}:${RemoteRoot}/build/"
    )

    $remoteScript = @"
set -euo pipefail
REMOTE_ROOT=$remoteRootQ

mkdir -p "`$REMOTE_ROOT/build" "`$REMOTE_ROOT/frontend"
rm -rf "`$REMOTE_ROOT/frontend"/*
tar -xzf "`$REMOTE_ROOT/build/pansou-web-dist.tar.gz" -C "`$REMOTE_ROOT/frontend"
caddy validate --config /etc/caddy/Caddyfile
systemctl reload caddy || systemctl restart caddy
curl -fsS -o /tmp/pansou-index.html -w 'frontend_http=%{http_code} bytes=%{size_download}\n' http://127.0.0.1/pansou/
asset=`$(grep -o '/pansou/assets/[^" ]*\.js' /tmp/pansou-index.html | head -1)
if [ -z "`$asset" ]; then
  echo "frontend asset not found in /tmp/pansou-index.html"
  exit 1
fi
curl -fsS -o /dev/null -w "asset_http=%{http_code}\n" "http://127.0.0.1`$asset"
curl -fsS -o /dev/null -w "report_http=%{http_code}\n" http://127.0.0.1/pansou/report.html
"@

    Write-Host "Updating remote frontend files..."
    Invoke-RemoteScript $remoteScript

    if (!$SkipPublicCheck) {
        Write-Host "Checking public frontend..."
        $page = Invoke-WebRequest -Uri "$PublicBaseUrl/pansou/" -UseBasicParsing -TimeoutSec 20
        Write-Host "Public frontend OK: HTTP $($page.StatusCode)"
        $report = Invoke-WebRequest -Uri "$PublicBaseUrl/pansou/report.html" -UseBasicParsing -TimeoutSec 20
        Write-Host "Public report page OK: HTTP $($report.StatusCode)"
    }

    Write-Host "Frontend deployment complete."
}
finally {
    Pop-Location
    if (Test-Path -LiteralPath $artifact) {
        Remove-Item -LiteralPath $artifact -Force
    }
}
