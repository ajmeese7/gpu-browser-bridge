# uninstall.ps1 - remove gpu-browser-bridge.
#
# Runs UNELEVATED for a normal per-user install (logon task + %LOCALAPPDATA%).
# Legacy admin installs (a Session-0 service, an admin-created task, or a binary
# under %ProgramFiles%) can only be cleaned up from an ELEVATED PowerShell; this
# script attempts them best-effort and tells you if elevation is needed.
#
# By default the token, Chrome profile, and logs are left in place so a
# re-install can reuse them. Pass -Purge to delete them.

[CmdletBinding()]
param(
  [switch]$Purge,
  [string]$TaskName = "gpu-browser-bridge"
)

$ErrorActionPreference = "Stop"

$elevated = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
$appDir       = "$env:LOCALAPPDATA\gpu-browser-bridge"
$legacyInstall= "$env:ProgramFiles\gpu-browser-bridge"
$legacyData   = "$env:ProgramData\gpu-browser-bridge"
$needAdmin    = $false

# Remove the logon task.
if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
  Write-Host "Removing task $TaskName ..."
  try {
    Stop-ScheduledTask       -TaskName $TaskName -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction Stop
  } catch {
    Write-Host "  could not remove task: $($_.Exception.Message)" -ForegroundColor Yellow
    $needAdmin = $true
  }
} else {
  Write-Host "Task $TaskName not found."
}

# Legacy Session-0 service from older installs (needs admin).
if (Get-Service $TaskName -ErrorAction SilentlyContinue) {
  Write-Host "Removing legacy service $TaskName ..."
  if ($elevated) {
    & sc.exe stop   $TaskName | Out-Null
    Start-Sleep -Seconds 3
    & sc.exe delete $TaskName | Out-Null
  } else {
    Write-Host "  legacy service present but removal needs elevation." -ForegroundColor Yellow
    $needAdmin = $true
  }
}

# Stop any running bridge.exe and its Chrome.
Get-Process bridge -ErrorAction SilentlyContinue | ForEach-Object {
  Write-Host "Stopping bridge.exe (PID $($_.Id)) ..."
  Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue
}
Get-CimInstance Win32_Process -Filter "Name='chrome.exe'" -ErrorAction SilentlyContinue |
  Where-Object { $_.CommandLine -and $_.CommandLine.Contains("gpu-browser-bridge") } |
  ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }

# Remove the per-user binary (keep data unless -Purge).
if (Test-Path (Join-Path $appDir "bridge.exe")) { Remove-Item -Force (Join-Path $appDir "bridge.exe") }
if (Test-Path (Join-Path $appDir "run-bridge.cmd")) { Remove-Item -Force (Join-Path $appDir "run-bridge.cmd") } # stale launcher from an old build

# Legacy %ProgramFiles% binary dir (needs admin).
if (Test-Path $legacyInstall) {
  if ($elevated) { Write-Host "Removing $legacyInstall ..."; Remove-Item -Recurse -Force $legacyInstall }
  else { Write-Host "Legacy $legacyInstall present but removal needs elevation." -ForegroundColor Yellow; $needAdmin = $true }
}

if ($Purge) {
  if (Test-Path $appDir) { Remove-Item -Recurse -Force $appDir; Write-Host "Purged $appDir" }
  if (Test-Path $legacyData) {
    if ($elevated) { Remove-Item -Recurse -Force $legacyData; Write-Host "Purged $legacyData" }
    else { Write-Host "Legacy $legacyData present but removal needs elevation." -ForegroundColor Yellow; $needAdmin = $true }
  }
} else {
  Write-Host ""
  Write-Host "Token, Chrome profile, and logs preserved under $appDir. Pass -Purge to delete them."
}

Write-Host ""
if ($needAdmin) {
  Write-Host "Some legacy admin-installed pieces remain. Re-run this in an ELEVATED PowerShell to finish." -ForegroundColor Yellow
} else {
  Write-Host "Uninstall complete." -ForegroundColor Green
}
