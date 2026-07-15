[CmdletBinding()]
param(
    [string]$LocalBackupDir = 'D:\Backups\PanSou\PostgreSQL',
    [string]$RemoteHost = 'root@103.236.97.248',
    [ValidateRange(1, 65535)]
    [int]$SshPort = 22348,
    [string]$IdentityFile = "$env:USERPROFILE\.ssh\yanhuo",
    [string]$RemoteBackupDir = '/opt/pansou-web/backups',
    [ValidateRange(1, 3650)]
    [int]$RetentionDays = 30,
    [ValidateRange(1, 10)]
    [int]$MaxRetries = 3
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$mutex = $null
$hasLock = $false
$logFile = $null

function Write-BackupLog {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Message,
        [ValidateSet('INFO', 'WARN', 'ERROR')]
        [string]$Level = 'INFO'
    )

    $line = '{0} [{1}] {2}' -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $Level, $Message
    Write-Host $line
    if ($script:logFile) {
        Add-Content -LiteralPath $script:logFile -Value $line -Encoding UTF8
    }
}

function Invoke-SshCommand {
    param([Parameter(Mandatory = $true)][string]$Command)

    $output = & $script:sshPath @script:sshOptions $RemoteHost $Command 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "SSH 命令执行失败（退出码 $LASTEXITCODE）：$($output -join [Environment]::NewLine)"
    }
    return @($output)
}

function Get-RemoteSha256 {
    param([Parameter(Mandatory = $true)][string]$FileName)

    $output = Invoke-SshCommand -Command "sha256sum -- '$RemoteBackupDir/$FileName'"
    $match = [regex]::Match(($output -join "`n"), '(?im)^([0-9a-f]{64})\s+')
    if (-not $match.Success) {
        throw "无法读取远端文件 $FileName 的 SHA-256。"
    }
    return $match.Groups[1].Value.ToLowerInvariant()
}

try {
    $mutex = New-Object System.Threading.Mutex($false, 'Local\PanSouPostgresBackupPull')
    try {
        $hasLock = $mutex.WaitOne(0, $false)
    }
    catch [System.Threading.AbandonedMutexException] {
        $hasLock = $true
    }

    if (-not $hasLock) {
        Write-Host '已有一个 PanSou PostgreSQL 备份拉取任务正在运行，本次退出。'
        exit 0
    }

    if ($RemoteBackupDir -notmatch '^/[A-Za-z0-9._/-]+$') {
        throw 'RemoteBackupDir 含有不支持的字符。'
    }
    if (-not (Test-Path -LiteralPath $IdentityFile -PathType Leaf)) {
        throw "SSH 私钥不存在：$IdentityFile"
    }

    $sshCommand = Get-Command ssh.exe -ErrorAction Stop
    $scpCommand = Get-Command scp.exe -ErrorAction Stop
    $script:sshPath = $sshCommand.Source
    $script:scpPath = $scpCommand.Source

    New-Item -ItemType Directory -Path $LocalBackupDir -Force | Out-Null
    $localRoot = [System.IO.Path]::GetPathRoot((Get-Item -LiteralPath $LocalBackupDir).FullName)
    $driveName = $localRoot.TrimEnd('\').TrimEnd(':')
    $drive = Get-PSDrive -Name $driveName -ErrorAction Stop
    if ($drive.Free -lt 2GB) {
        throw ('本地备份盘剩余空间不足 2 GiB，当前仅 {0:N2} GiB。' -f ($drive.Free / 1GB))
    }

    $logDir = Join-Path $LocalBackupDir '_logs'
    New-Item -ItemType Directory -Path $logDir -Force | Out-Null
    $script:logFile = Join-Path $logDir ('pull-{0}.log' -f (Get-Date -Format 'yyyy-MM'))

    $script:sshOptions = @(
        '-i', $IdentityFile,
        '-p', $SshPort,
        '-o', 'BatchMode=yes',
        '-o', 'ConnectTimeout=15',
        '-o', 'ServerAliveInterval=15',
        '-o', 'ServerAliveCountMax=3',
        '-o', 'StrictHostKeyChecking=accept-new'
    )
    $scpOptions = @(
        '-i', $IdentityFile,
        '-P', $SshPort,
        '-p',
        '-o', 'BatchMode=yes',
        '-o', 'ConnectTimeout=15',
        '-o', 'ServerAliveInterval=15',
        '-o', 'ServerAliveCountMax=3',
        '-o', 'StrictHostKeyChecking=accept-new'
    )

    Write-BackupLog ('开始检查远端备份；本地剩余空间 {0:N2} GiB。' -f ($drive.Free / 1GB))
    $listCommand = "find '$RemoteBackupDir' -maxdepth 1 -type f -name 'pansou-*.dump' -size +0c -printf '%f\t%s\n' | sort"
    $remoteLines = Invoke-SshCommand -Command $listCommand
    $remoteFiles = @()

    foreach ($line in $remoteLines) {
        $match = [regex]::Match([string]$line, '^(pansou-[A-Za-z0-9._-]+\.dump)\t([0-9]+)$')
        if ($match.Success) {
            $remoteFiles += [pscustomobject]@{
                Name = $match.Groups[1].Value
                Size = [long]$match.Groups[2].Value
            }
        }
    }

    if ($remoteFiles.Count -eq 0) {
        throw "远端目录 $RemoteBackupDir 中没有找到非空的 pansou-*.dump。"
    }

    $downloaded = 0
    $verified = 0
    foreach ($remoteFile in $remoteFiles) {
        $destination = Join-Path $LocalBackupDir $remoteFile.Name
        $hashSidecar = "$destination.sha256"
        $partFile = "$destination.part"

        if (Test-Path -LiteralPath $destination -PathType Leaf) {
            $localSize = (Get-Item -LiteralPath $destination).Length
            if ($localSize -eq $remoteFile.Size -and (Test-Path -LiteralPath $hashSidecar -PathType Leaf)) {
                $storedHash = (Get-Content -LiteralPath $hashSidecar -Raw).Trim().ToLowerInvariant()
                if ($storedHash -match '^[0-9a-f]{64}$') {
                    $localHash = (Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash.ToLowerInvariant()
                    if ($localHash -eq $storedHash) {
                        Write-BackupLog "已存在且本地校验通过，跳过：$($remoteFile.Name)"
                        continue
                    }
                }
                Write-BackupLog "本地 SHA-256 记录无效或文件校验失败，将与远端重新核对：$($remoteFile.Name)" 'WARN'
            }

            if ($localSize -eq $remoteFile.Size) {
                $remoteHash = Get-RemoteSha256 -FileName $remoteFile.Name
                $localHash = (Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash.ToLowerInvariant()
                if ($localHash -eq $remoteHash) {
                    Set-Content -LiteralPath $hashSidecar -Value $remoteHash -Encoding ASCII
                    $verified++
                    Write-BackupLog "已有文件校验通过：$($remoteFile.Name)"
                    continue
                }
            }

            $quarantine = '{0}.invalid-{1}' -f $destination, (Get-Date -Format 'yyyyMMddHHmmss')
            Move-Item -LiteralPath $destination -Destination $quarantine
            Remove-Item -LiteralPath $hashSidecar -Force -ErrorAction SilentlyContinue
            Write-BackupLog "已有文件大小或哈希不匹配，已移至：$quarantine" 'WARN'
        }

        $success = $false
        for ($attempt = 1; $attempt -le $MaxRetries; $attempt++) {
            try {
                Remove-Item -LiteralPath $partFile -Force -ErrorAction SilentlyContinue
                $remoteHash = Get-RemoteSha256 -FileName $remoteFile.Name
                Write-BackupLog "下载 $($remoteFile.Name)，第 $attempt/$MaxRetries 次。"
                $remoteSource = '{0}:{1}/{2}' -f $RemoteHost, $RemoteBackupDir, $remoteFile.Name
                $scpOutput = & $script:scpPath @scpOptions $remoteSource $partFile 2>&1
                if ($LASTEXITCODE -ne 0) {
                    throw "SCP 退出码 $LASTEXITCODE：$($scpOutput -join [Environment]::NewLine)"
                }

                $partSize = (Get-Item -LiteralPath $partFile).Length
                if ($partSize -ne $remoteFile.Size) {
                    throw "文件大小不一致，远端 $($remoteFile.Size) 字节，本地 $partSize 字节。"
                }
                $partHash = (Get-FileHash -LiteralPath $partFile -Algorithm SHA256).Hash.ToLowerInvariant()
                if ($partHash -ne $remoteHash) {
                    throw "SHA-256 不一致，远端 $remoteHash，本地 $partHash。"
                }

                Move-Item -LiteralPath $partFile -Destination $destination
                Set-Content -LiteralPath $hashSidecar -Value $remoteHash -Encoding ASCII
                $downloaded++
                $success = $true
                Write-BackupLog "下载并校验通过：$($remoteFile.Name)"
                break
            }
            catch {
                Remove-Item -LiteralPath $partFile -Force -ErrorAction SilentlyContinue
                Write-BackupLog "下载失败：$($remoteFile.Name)；$($_.Exception.Message)" 'WARN'
                if ($attempt -lt $MaxRetries) {
                    Start-Sleep -Seconds ([math]::Min(10, $attempt * 3))
                }
            }
        }

        if (-not $success) {
            throw "文件 $($remoteFile.Name) 连续 $MaxRetries 次下载或校验失败。"
        }
    }

    $cutoff = (Get-Date).AddDays(-$RetentionDays)
    $removed = 0
    Get-ChildItem -LiteralPath $LocalBackupDir -Filter 'pansou-*.dump' -File | Where-Object {
        $_.LastWriteTime -lt $cutoff
    } | ForEach-Object {
        $oldDump = $_.FullName
        Remove-Item -LiteralPath $oldDump -Force
        Remove-Item -LiteralPath "$oldDump.sha256" -Force -ErrorAction SilentlyContinue
        $removed++
        Write-BackupLog "已清理超过 $RetentionDays 天的本地备份：$($_.Name)"
    }

    Write-BackupLog "备份拉取完成：远端 $($remoteFiles.Count) 个，新下载 $downloaded 个，补充校验 $verified 个，清理 $removed 个。"
}
catch {
    Write-BackupLog $_.Exception.Message 'ERROR'
    exit 1
}
finally {
    if ($hasLock -and $mutex) {
        $mutex.ReleaseMutex()
    }
    if ($mutex) {
        $mutex.Dispose()
    }
}
