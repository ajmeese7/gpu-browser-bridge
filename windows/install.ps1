# install.ps1 - install gpu-browser-bridge as an interactive-session logon task.
#
# NO ADMIN REQUIRED. Everything lives in the current user's profile
# (%LOCALAPPDATA%\gpu-browser-bridge) and the logon task is registered for the
# current user, so this script runs unelevated. That keeps the trust barrier
# low: you never hand an install script Administrator rights.
#
# WHY A LOGON TASK (and not a Windows service):
#   A Windows service always runs in Session 0, which has no interactive GPU
#   desktop. Chrome rasterizes static pages there, but GPU/WebGPU-heavy pages
#   never finish rendering, so chromedp.Navigate hangs and the request dies at
#   the 30s timeout. Running bridge.exe in the logged-on user's interactive
#   session (Session 1+) gives Chrome the real GPU/DWM desktop and fixes it.
#
#   bridge.exe is built with the Windows GUI subsystem (-H windowsgui) so it has
#   no console window, and Chrome runs in new headless mode (--headless=new: no
#   window, but still real GPU), so the deployment is fully invisible on the
#   desktop while still GPU-accelerated.
#
# Prerequisites:
#   - Go toolchain on PATH (unless -SkipBuild and bridge.exe is in the repo root).
#   - Chrome installed (auto-detected under Program Files).
#
# Usage:
#   .\install.ps1                  # build, generate token, register + start task
#   .\install.ps1 -Token <hex>     # use a specific token instead of generating
#   .\install.ps1 -SkipBuild       # use existing bridge.exe in repo root

[CmdletBinding()]
param(
  [string]$Token = "",
  [switch]$SkipBuild,
  [string]$TaskName = "gpu-browser-bridge"
)

$ErrorActionPreference = "Stop"

function Find-Chrome {
  $candidates = @(
    "C:\Program Files\Google\Chrome\Application\chrome.exe",
    "C:\Program Files (x86)\Google\Chrome\Application\chrome.exe"
  )
  foreach ($p in $candidates) { if (Test-Path $p) { return $p } }
  throw "Google Chrome not found. Install it from https://www.google.com/chrome/ and re-run."
}

$chrome = Find-Chrome
Write-Host "Chrome found at $chrome"

$user       = "$env:USERDOMAIN\$env:USERNAME"
$appDir     = "$env:LOCALAPPDATA\gpu-browser-bridge"
$binaryDest = Join-Path $appDir "bridge.exe"
$tokenPath  = Join-Path $appDir "token"
$profileDir = Join-Path $appDir "chrome-profile"
$logPath    = Join-Path $appDir "bridge.log"
$repoRoot   = Split-Path -Parent $PSScriptRoot
$binarySrc  = Join-Path $repoRoot "bridge.exe"

# 1. Build (unless skipped). GUI subsystem => no console window.
if (-not $SkipBuild) {
  Write-Host "Building bridge.exe (GUI subsystem, no console window) ..."
  Push-Location $repoRoot
  try {
    & go build -ldflags "-H=windowsgui" -o bridge.exe ./cmd/bridge
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }
  } finally { Pop-Location }
}
if (-not (Test-Path $binarySrc)) {
  throw "bridge.exe not found at $binarySrc. Build first or omit -SkipBuild."
}

# 2. Remove any prior instance of this task, and stop a running bridge.exe + its
#    Chrome so the port and profile are free.
if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
  Write-Host "Stopping and removing existing task $TaskName ..."
  Stop-ScheduledTask       -TaskName $TaskName -ErrorAction SilentlyContinue
  Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
}
Get-Process bridge -ErrorAction SilentlyContinue | ForEach-Object {
  Write-Host "Stopping running bridge.exe (PID $($_.Id)) ..."
  Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue
}
Get-CimInstance Win32_Process -Filter "Name='chrome.exe'" -ErrorAction SilentlyContinue |
  Where-Object { $_.CommandLine -and $_.CommandLine.Contains("gpu-browser-bridge") } |
  ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
Start-Sleep -Seconds 1

# 3. Place binary + create the per-user data dir.
Write-Host "Installing to $appDir ..."
New-Item -ItemType Directory -Force -Path $appDir     | Out-Null
New-Item -ItemType Directory -Force -Path $profileDir | Out-Null
Copy-Item -Force $binarySrc $binaryDest

# 4. Token: explicit -Token wins; otherwise reuse an existing token (so reinstalls
#    do not invalidate the caller's config) or generate a fresh one. No ACL step
#    is needed - the file sits in the user's NTFS-isolated profile.
if ($Token -ne "") {
  Set-Content -Path $tokenPath -Value $Token -NoNewline -Encoding Ascii
} elseif (Test-Path $tokenPath) {
  Write-Host "Reusing existing token at $tokenPath"
  $Token = (Get-Content $tokenPath -Raw).Trim()
} else {
  Write-Host "Generating fresh token ..."
  # GUI-subsystem bridge.exe does not write to the parent console, so read the
  # token back from the file gen-token writes rather than capturing stdout.
  & $binaryDest gen-token $tokenPath | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "gen-token failed" }
  $Token = (Get-Content $tokenPath -Raw).Trim()
}

# 5. Register the interactive logon task for the current user. LogonType
#    Interactive + AtLogOn + RunLevel Limited = runs in this user's Session 1+
#    desktop, non-elevated. No admin needed to register a self-scoped task.
Write-Host "Registering logon task $TaskName for $user ..."
$action    = New-ScheduledTaskAction -Execute $binaryDest -WorkingDirectory $appDir
$trigger   = New-ScheduledTaskTrigger -AtLogOn -User $user
$principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited
$settings  = New-ScheduledTaskSettingsSet `
  -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable `
  -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) `
  -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew
Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
  -Principal $principal -Settings $settings -Force | Out-Null

# 6. Start now and wait for health (first launch is a cold Chrome start).
Write-Host "Starting task ..."
Start-ScheduledTask -TaskName $TaskName
$ok = $false
for ($i = 0; $i -lt 40; $i++) {
  Start-Sleep -Milliseconds 750
  try {
    $h = (Invoke-WebRequest "http://127.0.0.1:51234/healthz" -UseBasicParsing -TimeoutSec 3).Content
    if ($h -match '"ok":true') { $ok = $true; break }
  } catch { }
}

Write-Host ""
if ($ok) {
  $sid = (Get-Process bridge -ErrorAction SilentlyContinue | Select-Object -First 1).SessionId
  Write-Host "Install complete (no admin used)." -ForegroundColor Green
  Write-Host "bridge.exe is running in Session $sid (must be >= 1; Session 0 would hang on WebGPU)."
  Write-Host "Installed under $appDir. Listens on http://127.0.0.1:51234 (loopback only)."
  Write-Host ""
  Write-Host "On the caller (headless host):"
  Write-Host "  ssh -N -L 51234:127.0.0.1:51234 <user>@<this-machine>"
  Write-Host "  # ~/.config/gpu-browser/config:"
  Write-Host "  #   BRIDGE_URL=http://localhost:51234"
  Write-Host "  #   BRIDGE_TOKEN=$Token"
  Write-Host "  gpu-browser healthz"
  Write-Host ""
  Write-Host "NOTE: the logon task only runs once you are logged on. For unattended"
  Write-Host "reboots, enable auto-logon (see windows/README.md)."
} else {
  Write-Host "WARNING: bridge did not report healthy on 127.0.0.1:51234 within ~30s." -ForegroundColor Yellow
  Write-Host "Last lines of ${logPath}:" -ForegroundColor Yellow
  if (Test-Path $logPath) { Get-Content $logPath -Tail 15 }
}
