# ClaudGravityNext client installer for Windows.
#
# Usage (one-liner):
#   irm https://raw.githubusercontent.com/testAccountDeltas/ClaudGravityNext/main/install.ps1 | iex
#
# Non-interactive (CI / scripted):
#   $env:CLAUDGRAVITY_URL = "http://1.2.3.4:8898"
#   $env:CLAUDGRAVITY_KEY = "sk-..."
#   irm https://raw.githubusercontent.com/testAccountDeltas/ClaudGravityNext/main/install.ps1 | iex
#
# Proxy mode (the gateway answers Claude Code's own api.anthropic.com calls, e.g. /usage)
# is tried automatically and skipped with a warning if the gateway cannot provide it:
#   $env:CLAUDGRAVITY_PROXY = "1"   require it (or run the saved file with -Proxy)
#   $env:CLAUDGRAVITY_PROXY = "0"   skip it    (or -NoProxy)
#
# Uninstall (download first -- piped iex can't take switches):
#   irm https://raw.githubusercontent.com/testAccountDeltas/ClaudGravityNext/main/install.ps1 -OutFile install.ps1
#   .\install.ps1 -Uninstall
#
# NOTE for maintainers: this file is executed both as a saved .ps1 AND piped
# straight into `iex` as plain text. That second path means #Requires and a
# top-of-file param() block do NOT work here (iex runs the text as part of
# the caller's own scope, not as a script file) -- uninstall is instead
# detected via $env:CLAUDGRAVITY_UNINSTALL or a trailing -Uninstall arg.
# Keep this file plain ASCII: Invoke-RestMethod/iex and Windows PowerShell
# 5.1's no-BOM file reading disagree on non-ASCII encoding, and mixing the
# two silently corrupts the script either way it's invoked.

$IsUninstall = ($env:CLAUDGRAVITY_UNINSTALL -eq "1") -or ($args -contains "-Uninstall")

# Proxy mode: route Claude Code's own api.anthropic.com calls (the /usage
# panel, bootstrap, ...) through the gateway's CONNECT proxy so the gateway
# answers them instead of Anthropic. Needs the gateway's CONNECT port (API
# port + 1) reachable. Default "auto": try it, and fall back to plain HTTP with
# a warning if the gateway cannot provide it. "on" (-Proxy or
# $env:CLAUDGRAVITY_PROXY = "1") fails instead of falling back; "off"
# (-NoProxy or $env:CLAUDGRAVITY_PROXY = "0") skips it.
$ProxyMode = "auto"
if (($env:CLAUDGRAVITY_PROXY -eq "1") -or ($args -contains "-Proxy")) { $ProxyMode = "on" }
if (($env:CLAUDGRAVITY_PROXY -eq "0") -or ($args -contains "-NoProxy")) { $ProxyMode = "off" }
$UseProxy = $false

$ErrorActionPreference = "Stop"
$InstallDir = Join-Path $env:LOCALAPPDATA "ClaudGravityNext"
$MenuLabel = "ClaudGravityNext: Open Claude Code here"

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Ok($msg)   { Write-Host "    $msg" -ForegroundColor Green }
function Write-Warn($msg) { Write-Host "    $msg" -ForegroundColor Yellow }

# --- uninstall ---
if ($IsUninstall) {
    Write-Step "Removing ClaudGravityNext..."
    foreach ($base in @(
        "Registry::HKEY_CURRENT_USER\Software\Classes\Directory\Background\shell\ClaudGravityNext",
        "Registry::HKEY_CURRENT_USER\Software\Classes\Directory\shell\ClaudGravityNext"
    )) {
        if (Test-Path $base) { Remove-Item -Path $base -Recurse -Force -ErrorAction SilentlyContinue }
    }
    if (Test-Path $InstallDir) { Remove-Item -Path $InstallDir -Recurse -Force -ErrorAction SilentlyContinue }
    Write-Ok "Done. Context menu entry and $InstallDir removed."
    return
}

Write-Host ""
Write-Host "  ClaudGravityNext client installer" -ForegroundColor Magenta
Write-Host "  Claude Code <-> ClaudGravityNext gateway client setup"
Write-Host ""

# --- 1. gateway url + key ---
Write-Step "Gateway settings"

$GatewayUrl = $env:CLAUDGRAVITY_URL
if (-not $GatewayUrl) {
    $GatewayUrl = Read-Host "Gateway address (e.g. 203.0.113.10 or 203.0.113.10:8898)"
}
$GatewayUrl = $GatewayUrl.Trim().TrimEnd('/')
$GatewayUrl = $GatewayUrl -replace '^https?://', ''

$ApiKey = $env:CLAUDGRAVITY_KEY
if (-not $ApiKey) {
    $ApiKey = Read-Host "API key (sk-...)"
}
$ApiKey = $ApiKey.Trim()

if ($GatewayUrl -notmatch ':\d+$') {
    $GatewayUrl = "$GatewayUrl`:8898"
}
$GatewayHost, $GatewayPort = $GatewayUrl -split ':', 2
$BaseUrl = "http://$GatewayHost`:$GatewayPort"

# Plain HTTP to the gateway's API port: no CONNECT proxy and no CA are needed.
# (HTTPS_PROXY / SSL_CERT_FILE are left empty in the launcher on purpose.)
Write-Step "Checking the gateway ($BaseUrl)..."
try {
    $null = Invoke-RestMethod -Uri "$BaseUrl/v1/models" -Headers @{ "x-api-key" = $ApiKey } -Method Get -TimeoutSec 10
} catch {
    $status = $null
    if ($_.Exception.Response) { $status = [int]$_.Exception.Response.StatusCode }
    if ($status -eq 401) {
        Write-Host "The gateway rejected the API key (HTTP 401)." -ForegroundColor Red
    } else {
        Write-Host "Could not reach the gateway at $BaseUrl. Check the address, port and firewall: $_" -ForegroundColor Red
    }
    # throw, not exit: under "irm | iex" an exit would close the user's PowerShell window
    throw "Installation aborted (see the message above)."
}
Write-Ok "OK -- gateway reachable, key accepted"

# --- 1b. proxy mode: fetch the CA, proxy secret and CONNECT port ---
$ProxyUrl = ""
$CaPath = ""
if ($ProxyMode -ne "off") {
    Write-Step "Proxy mode: reading the gateway's proxy settings"
    try {
        try {
            $boot = Invoke-RestMethod -Uri "$BaseUrl/api/bootstrap" -Headers @{ "Authorization" = "Bearer $ApiKey" } -Method Get -TimeoutSec 10
        } catch {
            throw "could not read $BaseUrl/api/bootstrap ($($_.Exception.Message))"
        }
        if (-not $boot.proxy_secret -or -not $boot.ca_pem -or -not $boot.connect_port) {
            throw "the gateway did not return a proxy secret, CA and CONNECT port (is proxy_secret set in its config.json?)"
        }
        $tcp = New-Object System.Net.Sockets.TcpClient
        try {
            $iar = $tcp.BeginConnect($GatewayHost, [int]$boot.connect_port, $null, $null)
            if (-not $iar.AsyncWaitHandle.WaitOne(5000) -or -not $tcp.Connected) {
                throw "the CONNECT proxy port $GatewayHost`:$($boot.connect_port) is not reachable (open it in the firewall: it is the API port + 1)"
            }
        } finally {
            $tcp.Close()
        }
        $secretEnc = [uri]::EscapeDataString([string]$boot.proxy_secret)
        $ProxyUrl = "http://$secretEnc`:x@$GatewayHost`:$($boot.connect_port)"
        $UseProxy = $true
        Write-Ok "CONNECT proxy reachable on port $($boot.connect_port)"
    } catch {
        if ($ProxyMode -eq "on") {
            Write-Host "Proxy mode was requested but is not available: $_" -ForegroundColor Red
            throw "Installation aborted (see the message above)."
        }
        Write-Warn "Proxy mode is not available ($_). Continuing with plain HTTP: everything works except the per-model line in /usage."
    }
}

# --- 2. save local files ---
Write-Step "Saving configuration to $InstallDir"
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
if ($UseProxy) {
    $CaPath = Join-Path $InstallDir "ca.pem"
    Set-Content -Path $CaPath -Value ([string]$boot.ca_pem) -Encoding ascii
    Write-Ok "ca.pem saved"
}

$configObj = @{
    base_url = $BaseUrl
    api_key  = $ApiKey
}
$configObj | ConvertTo-Json | Set-Content -Path (Join-Path $InstallDir "config.json") -Encoding utf8
Write-Ok "config.json saved"

# --- 3. find claude ---
# Search order, deliberately: VS Code extension (the most common way people
# end up with Claude Code installed) -> PATH/environment -> each OS's
# default native-install location.
Write-Step "Looking for a Claude Code install..."
$ClaudeCmd = $null

function Find-InVSCodeExtensions {
    $extRoot = Join-Path $env:USERPROFILE ".vscode\extensions"
    if (-not (Test-Path $extRoot)) { return $null }
    $dirs = Get-ChildItem -Path $extRoot -Directory -Filter "anthropic.claude-code-*-win32-x64" -ErrorAction SilentlyContinue |
        Sort-Object Name -Descending
    foreach ($d in $dirs) {
        $candidate = Join-Path $d.FullName "resources\native-binary\claude.exe"
        if (Test-Path $candidate) { return $candidate }
    }
    return $null
}

function Find-InPathOrEnv {
    $cmd = Get-Command claude -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    if ($env:CLAUDGRAVITY_CLAUDE_PATH -and (Test-Path $env:CLAUDGRAVITY_CLAUDE_PATH)) {
        return $env:CLAUDGRAVITY_CLAUDE_PATH
    }
    return $null
}

function Find-NativeInstall {
    $candidates = @(
        (Join-Path $env:USERPROFILE ".local\bin\claude.exe"),
        (Join-Path $env:LOCALAPPDATA "Programs\claude-code\claude.exe"),
        (Join-Path $env:APPDATA "npm\claude.cmd"),
        (Join-Path $env:APPDATA "npm\claude.exe")
    )
    foreach ($c in $candidates) {
        if (Test-Path $c) { return $c }
    }
    # npm global root, if npm is on PATH
    $npmCmd = Get-Command npm -ErrorAction SilentlyContinue
    if ($npmCmd) {
        try {
            $npmRoot = (& npm root -g 2>$null).Trim()
            if ($npmRoot) {
                foreach ($name in @("claude.cmd", "claude.exe", "claude")) {
                    $c = Join-Path $npmRoot "..\$name"
                    if (Test-Path $c) { return (Resolve-Path $c).Path }
                }
            }
        } catch {}
    }
    return $null
}

$ClaudeCmd = Find-InVSCodeExtensions
$foundVia = "VS Code extension"
if (-not $ClaudeCmd) { $ClaudeCmd = Find-InPathOrEnv; $foundVia = "PATH/environment" }
if (-not $ClaudeCmd) { $ClaudeCmd = Find-NativeInstall; $foundVia = "native install" }

if ($ClaudeCmd) {
    Write-Ok "Found ($foundVia): $ClaudeCmd"
} else {
    Write-Warn "Claude Code was not found automatically. launch.cmd will fall back to 'claude' on PATH -- install Claude Code, or edit $InstallDir\launch.cmd by hand."
    $ClaudeCmd = "claude"
    $foundVia = "not found automatically, falling back to 'claude' on PATH"
}

# --- 4. detect Windows Terminal ---
$WtPath = (Get-Command wt.exe -ErrorAction SilentlyContinue).Source
if ($WtPath) {
    Write-Ok "Windows Terminal found -- will be used to launch"
} else {
    Write-Warn "Windows Terminal not found -- the context menu will open plain cmd.exe"
}

# --- 5. write launch.cmd ---
Write-Step "Writing launch.cmd"
$LaunchPath = Join-Path $InstallDir "launch.cmd"
# In proxy mode the CA is trusted through NODE_EXTRA_CA_CERTS and HTTPS_PROXY
# carries the proxy secret; "%" must be doubled inside a .cmd file.
$httpsProxyValue = $ProxyUrl -replace '%', '%%'
$caLines = @()
if ($UseProxy) { $caLines = @("set ""NODE_EXTRA_CA_CERTS=$CaPath""") }
$launchLines = @(
    "@echo off",
    "setlocal",
    "",
    "set ""CLAUDE_CONFIG_DIR=$InstallDir\profile""",
    "set ""ANTHROPIC_BASE_URL=$BaseUrl""",
    "set ""HTTP_PROXY=""",
    "set ""HTTPS_PROXY=$httpsProxyValue""",
    "set ""ALL_PROXY=""") + $caLines + @(
    "set ""ANTHROPIC_AUTH_TOKEN=""",
    "set ""ANTHROPIC_API_KEY=""",
    "set ""CLAUDE_CODE_OAUTH_TOKEN=$ApiKey""",
    "set ""CLAUDE_CODE_OAUTH_SCOPES=user:inference user:profile""",
    "set ""CLAUDE_CODE_SUBSCRIPTION_TYPE=max""",
    "set ""CLAUDE_CODE_RATE_LIMIT_TIER=default""",
    "set ""CLAUDE_CODE_SIMPLE=""",
    "set ""CLAUDE_CODE_ENABLE_TODO_TOOLS=1""",
    "set ""CLAUDE_CODE_USE_POWERSHELL_TOOL=1""",
    "set ""CLAUDE_CODE_ATTRIBUTION_HEADER=0""",
    "set ""CLAUDE_CODE_EFFORT_LEVEL=xhigh""",
    "set ""CLAUDE_CODE_GATEWAY_HINT_HEADERS=1""",
    "set ""_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL=1""",
    "set ""DISABLE_AUTOUPDATER=1""",
    "",
    """$ClaudeCmd"" %*",
    "endlocal"
)
Set-Content -Path $LaunchPath -Value $launchLines -Encoding ascii
Write-Ok "launch.cmd written"

# --- 6. context menu ---
Write-Step "Registering the folder context menu"

if ($WtPath) {
    $menuCommandBg  = '"' + $WtPath + '" -d "%V" cmd /k "' + $LaunchPath + '"'
    $menuCommandDir = '"' + $WtPath + '" -d "%1" cmd /k "' + $LaunchPath + '"'
} else {
    $menuCommandBg  = 'cmd.exe /k "cd /d "%V" && "' + $LaunchPath + '""'
    $menuCommandDir = 'cmd.exe /k "cd /d "%1" && "' + $LaunchPath + '""'
}

function Set-ContextMenuEntry($baseKey, $command) {
    New-Item -Path $baseKey -Force | Out-Null
    Set-ItemProperty -Path $baseKey -Name "(Default)" -Value $MenuLabel
    Set-ItemProperty -Path $baseKey -Name "Icon" -Value "cmd.exe"
    $cmdKey = Join-Path $baseKey "command"
    New-Item -Path $cmdKey -Force | Out-Null
    Set-ItemProperty -Path $cmdKey -Name "(Default)" -Value $command
}

Set-ContextMenuEntry "Registry::HKEY_CURRENT_USER\Software\Classes\Directory\Background\shell\ClaudGravityNext" $menuCommandBg
Set-ContextMenuEntry "Registry::HKEY_CURRENT_USER\Software\Classes\Directory\shell\ClaudGravityNext" $menuCommandDir
Write-Ok "'$MenuLabel' added to the folder context menu"

Write-Host ""
Write-Host "  Done!" -ForegroundColor Magenta
Write-Host "  Right-click any folder -> '$MenuLabel'"
Write-Host "  Or run directly: $LaunchPath"
Write-Host "  Uninstall: download this script, then run '.\install.ps1 -Uninstall'"
Write-Host ""
