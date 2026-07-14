param(
    [string]$HostName = "103.236.97.248",
    [int]$Port = 22348,
    [string]$User = "root",
    [string]$KeyPath = (Join-Path $HOME ".ssh\yanhuo"),
    [string]$RemoteRoot = "/opt/pansou-web",
    [string]$EnabledPlugins = "labi,zhizhen,shandian,duoduo,muou,qqpd,gying,weibo",
    [string]$FrontendRoot = "",
    [string]$BasePath = "/pansou/",
    [string]$BrowserGatewayUrl = "http://192.168.16.1:18789/fetch",
    [string]$PublicBaseUrl = "http://103.236.97.248:22350",
    [switch]$SkipNpmInstall
)

$ErrorActionPreference = "Stop"

$common = @{
    HostName = $HostName
    Port = $Port
    User = $User
    KeyPath = $KeyPath
    RemoteRoot = $RemoteRoot
    PublicBaseUrl = $PublicBaseUrl
}

& (Join-Path $PSScriptRoot "update-backend.ps1") @common -EnabledPlugins $EnabledPlugins -BrowserGatewayUrl $BrowserGatewayUrl
& (Join-Path $PSScriptRoot "update-frontend.ps1") @common -FrontendRoot $FrontendRoot -BasePath $BasePath -SkipNpmInstall:$SkipNpmInstall

Write-Host "Full deployment complete: $PublicBaseUrl/pansou/"
