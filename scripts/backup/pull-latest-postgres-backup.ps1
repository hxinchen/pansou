#Requires -Version 7.0

[CmdletBinding()]
param(
    [string]$BackupDir = 'D:\Backups\PanSou\PostgreSQL',
    [string]$RemoteHost = 'root@103.236.97.248',
    [int]$SshPort = 22348,
    [string]$IdentityFile = "$env:USERPROFILE\.ssh\yanhuo",
    [string]$RemoteBackupDir = '/opt/pansou-web/backups',
    [ValidateRange(1, 5)]
    [int]$MaxRetries = 3
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$mutex = $null
$hasLock = $false
$logFile = $null
$partFile = $null

function Format-ByteSize {
    param([long]$Bytes)

    if ($Bytes -ge 1GB) { return ('{0:N2} GiB' -f ($Bytes / 1GB)) }
    if ($Bytes -ge 1MB) { return ('{0:N1} MiB' -f ($Bytes / 1MB)) }
    if ($Bytes -ge 1KB) { return ('{0:N1} KiB' -f ($Bytes / 1KB)) }
    return "$Bytes B"
}

function Write-LogLine {
    param(
        [Parameter(Mandatory = $true)][string]$Message,
        [ValidateSet('INFO', 'WARN', 'ERROR')][string]$Level = 'INFO',
        [ConsoleColor]$Color = [ConsoleColor]::Gray
    )

    $line = '{0} [{1}] {2}' -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $Level, $Message
    Write-Host $line -ForegroundColor $Color
    if ($script:logFile) {
        Add-Content -LiteralPath $script:logFile -Value $line -Encoding UTF8
    }
}

function Write-Step {
    param([int]$Number, [string]$Text)
    Write-Host "`n[$Number/6] $Text" -ForegroundColor Cyan
}

function Invoke-RemoteCommand {
    param([Parameter(Mandatory = $true)][string]$Command)

    $output = & $script:sshPath @script:sshOptions $RemoteHost $Command 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "SSH 命令失败（退出码 $LASTEXITCODE）：$($output -join [Environment]::NewLine)"
    }
    return @($output)
}

function Get-RemoteHash {
    param([Parameter(Mandatory = $true)][string]$FileName)

    $output = Invoke-RemoteCommand -Command "sha256sum -- '$RemoteBackupDir/$FileName'"
    $match = [regex]::Match(($output -join "`n"), '(?im)^([0-9a-f]{64})\s+')
    if (-not $match.Success) {
        throw "服务器没有返回 $FileName 的有效 SHA-256。"
    }
    return $match.Groups[1].Value.ToLowerInvariant()
}

function Invoke-ScpWithProgress {
    param(
        [Parameter(Mandatory = $true)][string]$RemoteSource,
        [Parameter(Mandatory = $true)][string]$Destination,
        [Parameter(Mandatory = $true)][long]$ExpectedSize,
        [Parameter(Mandatory = $true)][int]$Attempt
    )

    $errorFile = Join-Path $env:TEMP ("pansou-scp-{0}-{1}.log" -f $PID, $Attempt)
    Remove-Item -LiteralPath $errorFile -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $Destination -Force -ErrorAction SilentlyContinue

    $arguments = @(
        '-i', $IdentityFile,
        '-P', [string]$SshPort,
        '-p',
        '-o', 'BatchMode=yes',
        '-o', 'ConnectTimeout=15',
        '-o', 'ServerAliveInterval=15',
        '-o', 'ServerAliveCountMax=3',
        '-o', 'StrictHostKeyChecking=accept-new',
        $RemoteSource,
        $Destination
    )

    $startedAt = Get-Date
    try {
        $process = Start-Process -FilePath $script:scpPath -ArgumentList $arguments -NoNewWindow -PassThru -RedirectStandardError $errorFile
        while (-not $process.HasExited) {
            Start-Sleep -Milliseconds 400
            $currentSize = 0L
            if (Test-Path -LiteralPath $Destination -PathType Leaf) {
                $currentSize = (Get-Item -LiteralPath $Destination).Length
            }
            $elapsed = [math]::Max(0.1, ((Get-Date) - $startedAt).TotalSeconds)
            $speed = [long]($currentSize / $elapsed)
            $percent = if ($ExpectedSize -gt 0) { [math]::Min(100, [math]::Floor($currentSize * 100 / $ExpectedSize)) } else { 0 }
            $eta = if ($speed -gt 0 -and $ExpectedSize -gt $currentSize) {
                [TimeSpan]::FromSeconds(($ExpectedSize - $currentSize) / $speed).ToString('mm\:ss')
            }
            else { '--:--' }
            $status = '{0} / {1}  |  {2}/s  |  剩余约 {3}' -f (Format-ByteSize $currentSize), (Format-ByteSize $ExpectedSize), (Format-ByteSize $speed), $eta
            Write-Progress -Id 1 -Activity "正在下载最新数据库备份（第 $Attempt/$MaxRetries 次）" -Status $status -PercentComplete $percent
            Write-Host ("`r下载进度 {0,3}%  {1} / {2}  {3}/s  剩余 {4}   " -f $percent, (Format-ByteSize $currentSize), (Format-ByteSize $ExpectedSize), (Format-ByteSize $speed), $eta) -NoNewline -ForegroundColor Yellow
        }
        $process.WaitForExit()
        Write-Progress -Id 1 -Activity '正在下载最新数据库备份' -Completed
        Write-Host

        if ($process.ExitCode -ne 0) {
            $detail = if (Test-Path -LiteralPath $errorFile) { (Get-Content -LiteralPath $errorFile -Raw).Trim() } else { '没有错误详情。' }
            throw "SCP 下载失败（退出码 $($process.ExitCode)）：$detail"
        }
    }
    finally {
        Remove-Item -LiteralPath $errorFile -Force -ErrorAction SilentlyContinue
    }
}

try {
    try { Clear-Host } catch { }
    Write-Host '============================================================' -ForegroundColor DarkCyan
    Write-Host '              PanSou PostgreSQL 数据库备份拉取' -ForegroundColor White
    Write-Host '============================================================' -ForegroundColor DarkCyan
    Write-Host '策略：新备份完整下载并校验成功后，才删除本地旧备份。' -ForegroundColor DarkGray

    $mutex = New-Object System.Threading.Mutex($false, 'Local\PanSouLatestPostgresBackup')
    try { $hasLock = $mutex.WaitOne(0, $false) }
    catch [System.Threading.AbandonedMutexException] { $hasLock = $true }
    if (-not $hasLock) {
        throw '另一个数据库备份拉取窗口正在运行，请等待它完成。'
    }

    Write-Step 1 '检查本机环境'
    if (-not (Test-Path -LiteralPath $IdentityFile -PathType Leaf)) {
        throw "找不到 SSH 私钥：$IdentityFile"
    }
    if ($RemoteBackupDir -notmatch '^/[A-Za-z0-9._/-]+$') {
        throw '服务器备份目录包含不支持的字符。'
    }
    $script:sshPath = (Get-Command ssh.exe -ErrorAction Stop).Source
    $script:scpPath = (Get-Command scp.exe -ErrorAction Stop).Source
    New-Item -ItemType Directory -Path $BackupDir -Force | Out-Null
    $logDir = Join-Path $BackupDir '_logs'
    New-Item -ItemType Directory -Path $logDir -Force | Out-Null
    $script:logFile = Join-Path $logDir ('manual-pull-{0}.log' -f (Get-Date -Format 'yyyy-MM'))
    Write-LogLine "本地目录：$BackupDir" 'INFO' Green

    $script:sshOptions = @(
        '-i', $IdentityFile,
        '-p', [string]$SshPort,
        '-o', 'BatchMode=yes',
        '-o', 'ConnectTimeout=15',
        '-o', 'ServerAliveInterval=15',
        '-o', 'ServerAliveCountMax=3',
        '-o', 'StrictHostKeyChecking=accept-new'
    )

    Write-Step 2 '连接服务器并查找最新备份'
    $listCommand = "find '$RemoteBackupDir' -maxdepth 1 -type f -name 'pansou-*.dump' -size +0c -printf '%T@\t%f\t%s\n' | sort -nr | head -n 1"
    $latestOutput = Invoke-RemoteCommand -Command $listCommand
    $match = [regex]::Match(($latestOutput -join "`n"), '(?m)^[0-9.]+\t(pansou-[A-Za-z0-9._-]+\.dump)\t([0-9]+)$')
    if (-not $match.Success) {
        throw "服务器目录 $RemoteBackupDir 中没有找到可下载的备份。"
    }
    $fileName = $match.Groups[1].Value
    $remoteSize = [long]$match.Groups[2].Value
    Write-LogLine "服务器最新备份：$fileName（$(Format-ByteSize $remoteSize)）" 'INFO' Green

    Write-Step 3 '检查备份完整性与本地空间'
    $restoreCheck = Invoke-RemoteCommand -Command "pg_restore --list '$RemoteBackupDir/$fileName' >/dev/null && echo BACKUP_OK"
    if (($restoreCheck -join "`n") -notmatch '(?m)^BACKUP_OK$') {
        throw "服务器最新备份 $fileName 未通过 pg_restore 完整性检查。"
    }
    $remoteHash = Get-RemoteHash -FileName $fileName
    $driveName = ([System.IO.Path]::GetPathRoot((Get-Item -LiteralPath $BackupDir).FullName)).TrimEnd('\').TrimEnd(':')
    $drive = Get-PSDrive -Name $driveName -ErrorAction Stop
    $requiredFree = $remoteSize + 512MB
    if ($drive.Free -lt $requiredFree) {
        throw "磁盘空间不足：至少需要 $(Format-ByteSize $requiredFree)，当前只有 $(Format-ByteSize $drive.Free)。旧备份尚未删除。"
    }
    Write-LogLine "服务器备份完整；SHA-256：$remoteHash" 'INFO' Green
    Write-LogLine "磁盘剩余：$(Format-ByteSize $drive.Free)" 'INFO' Green

    Write-Step 4 '下载到临时文件'
    $destination = Join-Path $BackupDir $fileName
    $partFile = "$destination.part"
    $remoteSource = '{0}:{1}/{2}' -f $RemoteHost, $RemoteBackupDir, $fileName
    $downloaded = $false
    for ($attempt = 1; $attempt -le $MaxRetries; $attempt++) {
        try {
            Invoke-ScpWithProgress -RemoteSource $remoteSource -Destination $partFile -ExpectedSize $remoteSize -Attempt $attempt
            $downloaded = $true
            break
        }
        catch {
            Remove-Item -LiteralPath $partFile -Force -ErrorAction SilentlyContinue
            Write-LogLine $_.Exception.Message 'WARN' Yellow
            if ($attempt -lt $MaxRetries) {
                Write-LogLine '3 秒后自动重试……' 'INFO' Yellow
                Start-Sleep -Seconds 3
            }
        }
    }
    if (-not $downloaded) {
        throw "连续 $MaxRetries 次下载失败。本地旧备份保持不变。"
    }

    Write-Step 5 '校验下载文件并安全替换'
    $localSize = (Get-Item -LiteralPath $partFile).Length
    if ($localSize -ne $remoteSize) {
        throw "下载大小不一致：服务器 $(Format-ByteSize $remoteSize)，本地 $(Format-ByteSize $localSize)。旧备份尚未删除。"
    }
    $localHash = (Get-FileHash -LiteralPath $partFile -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($localHash -ne $remoteHash) {
        throw "SHA-256 校验失败。服务器：$remoteHash；本地：$localHash。旧备份尚未删除。"
    }
    Write-LogLine '文件大小和 SHA-256 校验通过。' 'INFO' Green

    $rollbackFile = $null
    if (Test-Path -LiteralPath $destination -PathType Leaf) {
        $rollbackFile = "$destination.previous-$(Get-Date -Format 'yyyyMMddHHmmss')"
        Move-Item -LiteralPath $destination -Destination $rollbackFile
    }
    try {
        Move-Item -LiteralPath $partFile -Destination $destination
        Set-Content -LiteralPath "$destination.sha256" -Value $remoteHash -Encoding ASCII
    }
    catch {
        if (-not (Test-Path -LiteralPath $destination) -and $rollbackFile -and (Test-Path -LiteralPath $rollbackFile)) {
            Move-Item -LiteralPath $rollbackFile -Destination $destination
        }
        throw "替换本地备份失败，已尝试恢复原备份：$($_.Exception.Message)"
    }

    Write-Step 6 '删除旧备份并完成'
    $removed = 0
    Get-ChildItem -LiteralPath $BackupDir -Filter 'pansou-*.dump' -File | Where-Object {
        $_.FullName -ne $destination
    } | ForEach-Object {
        $oldPath = $_.FullName
        Remove-Item -LiteralPath $oldPath -Force
        Remove-Item -LiteralPath "$oldPath.sha256" -Force -ErrorAction SilentlyContinue
        $removed++
        Write-LogLine "已删除旧备份：$($_.Name)" 'INFO' DarkGray
    }
    if ($rollbackFile) {
        Remove-Item -LiteralPath $rollbackFile -Force -ErrorAction SilentlyContinue
    }
    Get-ChildItem -LiteralPath $BackupDir -Filter 'pansou-*.dump.sha256' -File | Where-Object {
        -not (Test-Path -LiteralPath ($_.FullName -replace '\.sha256$', ''))
    } | Remove-Item -Force

    $finalFile = Get-Item -LiteralPath $destination
    Write-LogLine "完成：$($finalFile.Name)，$(Format-ByteSize $finalFile.Length)；已删除 $removed 份旧备份。" 'INFO' Green
    Write-Host "`n============================================================" -ForegroundColor Green
    Write-Host '  备份拉取成功' -ForegroundColor Green
    Write-Host "  文件：$($finalFile.FullName)" -ForegroundColor White
    Write-Host "  大小：$(Format-ByteSize $finalFile.Length)" -ForegroundColor White
    Write-Host "  校验：SHA-256 通过" -ForegroundColor White
    Write-Host '============================================================' -ForegroundColor Green
    exit 0
}
catch {
    Write-Progress -Id 1 -Activity '正在下载最新数据库备份' -Completed -ErrorAction SilentlyContinue
    if ($partFile) {
        Remove-Item -LiteralPath $partFile -Force -ErrorAction SilentlyContinue
    }
    Write-LogLine $_.Exception.Message 'ERROR' Red
    Write-Host "`n============================================================" -ForegroundColor Red
    Write-Host '  备份拉取失败；原有备份未被提前删除' -ForegroundColor Red
    Write-Host "  原因：$($_.Exception.Message)" -ForegroundColor Yellow
    if ($logFile) { Write-Host "  日志：$logFile" -ForegroundColor DarkGray }
    Write-Host '============================================================' -ForegroundColor Red
    exit 1
}
finally {
    if ($hasLock -and $mutex) { $mutex.ReleaseMutex() }
    if ($mutex) { $mutex.Dispose() }
}
