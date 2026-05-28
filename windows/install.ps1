# install.ps1 - install gpu-browser-bridge as a Windows service.
#
# Prerequisites:
#   - Run elevated (Administrator).
#   - NSSM on PATH. Easiest: `choco install nssm -y`
#     (https://community.chocolatey.org/packages/NSSM)
#   - Chrome installed at one of:
#       C:\Program Files\Google\Chrome\Application\chrome.exe
#       C:\Program Files (x86)\Google\Chrome\Application\chrome.exe
#     Download: https://www.google.com/chrome/
#
# Usage:
#   .\install.ps1                     # builds, generates token, registers service
#   .\install.ps1 -Token <hex>        # use a specific token instead of generating
#   .\install.ps1 -SkipBuild          # use existing bridge.exe in repo root
#
# After install: bridge listens on 127.0.0.1:51234. Set up an SSH
# local-forward tunnel from the headless host to reach it.

[CmdletBinding()]
param(
  [string]$Token = "",
  [switch]$SkipBuild,
  [string]$ServiceName = "gpu-browser-bridge",
  [string]$InstallDir = "$env:ProgramFiles\gpu-browser-bridge"
)

$ErrorActionPreference = "Stop"

function Require-Admin {
  $id = [Security.Principal.WindowsIdentity]::GetCurrent()
  $p  = New-Object Security.Principal.WindowsPrincipal($id)
  if (-not $p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "install.ps1 must be run as Administrator."
  }
}

function Require-Command($name, $hint) {
  if (-not (Get-Command $name -ErrorAction SilentlyContinue)) {
    throw "$name not found on PATH. $hint"
  }
}

function Find-Chrome {
  $candidates = @(
    "C:\Program Files\Google\Chrome\Application\chrome.exe",
    "C:\Program Files (x86)\Google\Chrome\Application\chrome.exe"
  )
  foreach ($p in $candidates) {
    if (Test-Path $p) { return $p }
  }
  throw "Google Chrome not found. Download from https://www.google.com/chrome/ and re-run."
}

Require-Admin
Require-Command "nssm.exe" "Install NSSM with: choco install nssm -y  (see https://community.chocolatey.org/packages/NSSM)"
$chrome = Find-Chrome
Write-Host "Chrome found at $chrome"

$repoRoot   = Split-Path -Parent $PSScriptRoot
$binarySrc  = Join-Path $repoRoot "bridge.exe"
$tokenDir   = "$env:ProgramData\gpu-browser-bridge"
$tokenPath  = Join-Path $tokenDir "token"
$profileDir = "$env:LOCALAPPDATA\gpu-browser-bridge\chrome-profile"
$logPath    = Join-Path $tokenDir "bridge.log"

if (-not $SkipBuild) {
  Write-Host "Building bridge.exe ..."
  Push-Location $repoRoot
  try {
    & go build -o bridge.exe ./cmd/bridge
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }
  } finally {
    Pop-Location
  }
}
if (-not (Test-Path $binarySrc)) {
  throw "bridge.exe not found at $binarySrc. Build first or omit -SkipBuild."
}

# Stop any existing service BEFORE touching the install dir - the running
# bridge.exe holds an exclusive lock on the file and Copy-Item will fail.
# Also kill any orphan bridge.exe processes started outside the service
# (e.g. from a foreground dev run).
if (Get-Service $ServiceName -ErrorAction SilentlyContinue) {
  Write-Host "Stopping existing service ..."
  & nssm.exe stop   $ServiceName confirm | Out-Null
  & nssm.exe remove $ServiceName confirm | Out-Null
}
Get-Process bridge -ErrorAction SilentlyContinue | ForEach-Object {
  Write-Host "Killing orphan bridge.exe (PID $($_.Id), $($_.Path)) ..."
  Stop-Process -Id $_.Id -Force
}

Write-Host "Installing binary to $InstallDir ..."
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Copy-Item -Force $binarySrc (Join-Path $InstallDir "bridge.exe")
$binaryDest = Join-Path $InstallDir "bridge.exe"

Write-Host "Preparing token directory $tokenDir ..."
New-Item -ItemType Directory -Force -Path $tokenDir   | Out-Null
New-Item -ItemType Directory -Force -Path $profileDir | Out-Null

if ($Token -eq "") {
  Write-Host "Generating fresh token ..."
  $Token = & $binaryDest gen-token $tokenPath
  if ($LASTEXITCODE -ne 0) { throw "gen-token failed" }
} else {
  Set-Content -Path $tokenPath -Value $Token -NoNewline
}
# Lock down token: SYSTEM (read) + Administrators (full), no inherited
# permissions, no one else. icacls is less brittle than .NET ACL APIs.
& icacls.exe $tokenPath /inheritance:r /grant:r "SYSTEM:R" "Administrators:F" | Out-Null
if ($LASTEXITCODE -ne 0) { throw "icacls failed to lock down $tokenPath" }

Write-Host "Registering NSSM service $ServiceName ..."
# NSSM is the SCM wrapper, so bridge.exe must run in console mode (no
# 'service' arg). Passing 'service' would invoke our svc.Run path which
# tries to dialog with the SCM directly - and since NSSM owns that
# channel, the child crashes and NSSM reports SERVICE_PAUSED.
& nssm.exe install $ServiceName $binaryDest
& nssm.exe set $ServiceName AppDirectory      $InstallDir
& nssm.exe set $ServiceName DisplayName       "GPU Browser Bridge"
& nssm.exe set $ServiceName Description       "HTTP API around a GPU-backed Chrome via CDP."
& nssm.exe set $ServiceName Start             SERVICE_AUTO_START
& nssm.exe set $ServiceName AppStdout         $logPath
& nssm.exe set $ServiceName AppStderr         $logPath
& nssm.exe set $ServiceName AppRotateFiles    1
& nssm.exe set $ServiceName AppRotateBytes    10485760
& nssm.exe set $ServiceName AppEnvironmentExtra `
    "BRIDGE_CHROME_PATH=$chrome" `
    "BRIDGE_USER_DATA_DIR=$profileDir" `
    "BRIDGE_TOKEN_PATH=$tokenPath" `
    "BRIDGE_LOG_PATH=$logPath"

Write-Host "Starting service ..."
& nssm.exe start $ServiceName

Write-Host ""
Write-Host "Install complete." -ForegroundColor Green
Write-Host ""
Write-Host "Bridge listens on http://127.0.0.1:51234 (loopback only)."
Write-Host "Token saved to $tokenPath"
Write-Host ""
Write-Host "On the headless host (caller side):"
Write-Host ""
Write-Host "  # Open local-forward tunnel so the bridge appears as localhost:51234:"
Write-Host "  # (target 127.0.0.1, not localhost, to avoid IPv6 resolution issues)"
Write-Host "  ssh -N -L 51234:127.0.0.1:51234 <user>@<this-machine>"
Write-Host ""
Write-Host "  # Configure the CLI:"
Write-Host "  mkdir -p ~/.config/gpu-browser"
Write-Host "  cat > ~/.config/gpu-browser/config <<EOF"
Write-Host "  BRIDGE_URL=http://localhost:51234"
Write-Host "  BRIDGE_TOKEN=$Token"
Write-Host "  EOF"
Write-Host ""
Write-Host "  gpu-browser healthz"
