# uninstall.ps1 - remove gpu-browser-bridge.
#
# Runs unelevated: removes the per-user logon task and stops the bridge. By
# default the token, Chrome profile, and logs are left in place so a re-install
# can reuse them. Pass -Purge to delete them.

[CmdletBinding()]
param(
  [switch]$Purge,
  [string]$TaskName = "gpu-browser-bridge"
)

$ErrorActionPreference = "Stop"

$appDir = "$env:LOCALAPPDATA\gpu-browser-bridge"

# Remove the logon task.
if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
  Write-Host "Removing task $TaskName ..."
  Stop-ScheduledTask       -TaskName $TaskName -ErrorAction SilentlyContinue
  Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
} else {
  Write-Host "Task $TaskName not found."
}

# Stop any running bridge.exe and its Chrome.
Get-Process bridge -ErrorAction SilentlyContinue | ForEach-Object {
  Write-Host "Stopping bridge.exe (PID $($_.Id)) ..."
  Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue
}
Get-CimInstance Win32_Process -Filter "Name='chrome.exe'" -ErrorAction SilentlyContinue |
  Where-Object { $_.CommandLine -and $_.CommandLine.Contains("gpu-browser-bridge") } |
  ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }

if ($Purge) {
  if (Test-Path $appDir) { Remove-Item -Recurse -Force $appDir; Write-Host "Purged $appDir" }
} else {
  Remove-Item (Join-Path $appDir "bridge.exe") -Force -ErrorAction SilentlyContinue
  Write-Host ""
  Write-Host "Token, Chrome profile, and logs preserved under $appDir. Pass -Purge to delete them."
}

Write-Host "Uninstall complete." -ForegroundColor Green
