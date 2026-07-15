#Requires -RunAsAdministrator

[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [string]$TaskName = 'PanSou PostgreSQL Backup Pull',
    [string]$PullScriptPath = (Join-Path $PSScriptRoot 'pull-postgres-backups.ps1'),
    [string]$LocalBackupDir = 'D:\Backups\PanSou\PostgreSQL',
    [switch]$RunNow
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if (-not (Test-Path -LiteralPath $PullScriptPath -PathType Leaf)) {
    throw "备份拉取脚本不存在：$PullScriptPath"
}

$resolvedScript = (Resolve-Path -LiteralPath $PullScriptPath).Path
$powerShellExe = Join-Path $PSHOME 'powershell.exe'
$arguments = '-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "{0}" -LocalBackupDir "{1}"' -f $resolvedScript, $LocalBackupDir
$action = New-ScheduledTaskAction -Execute $powerShellExe -Argument $arguments -WorkingDirectory (Split-Path -Parent $resolvedScript)
$triggers = @(
    (New-ScheduledTaskTrigger -Daily -At '00:20')
    (New-ScheduledTaskTrigger -Daily -At '06:20')
    (New-ScheduledTaskTrigger -Daily -At '12:20')
    (New-ScheduledTaskTrigger -Daily -At '18:20')
)
$settings = New-ScheduledTaskSettingsSet `
    -StartWhenAvailable `
    -MultipleInstances IgnoreNew `
    -ExecutionTimeLimit (New-TimeSpan -Hours 2) `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries
$currentUser = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
$principal = New-ScheduledTaskPrincipal -UserId $currentUser -LogonType Interactive -RunLevel Limited

if ($PSCmdlet.ShouldProcess($TaskName, '注册或更新 Windows 计划任务')) {
    Register-ScheduledTask `
        -TaskName $TaskName `
        -Action $action `
        -Trigger $triggers `
        -Settings $settings `
        -Principal $principal `
        -Description '每 6 小时通过 SSH/SCP 拉取并校验 PanSou PostgreSQL 服务器备份。' `
        -Force | Out-Null

    Write-Host "计划任务已注册：$TaskName"
    if ($RunNow) {
        Start-ScheduledTask -TaskName $TaskName
        Write-Host '已触发首次运行。'
    }
}
