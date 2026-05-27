# uninstall.ps1 - remove gpu-browser-bridge service.
#
# By default the token and Chrome profile are left in place so a re-install
# can pick them back up. Pass -Purge to delete them.

[CmdletBinding()]
param(
  [switch]$Purge,
  [string]$ServiceName = "gpu-browser-bridge",
  [string]$InstallDir  = "$env:ProgramFiles\gpu-browser-bridge"
)

$ErrorActionPreference = "Stop"

function Require-Admin {
  $id = [Security.Principal.WindowsIdentity]::GetCurrent()
  $p  = New-Object Security.Principal.WindowsPrincipal($id)
  if (-not $p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "uninstall.ps1 must be run as Administrator."
  }
}

Require-Admin

if (Get-Service $ServiceName -ErrorAction SilentlyContinue) {
  Write-Host "Stopping and removing service $ServiceName ..."
  if (Get-Command nssm.exe -ErrorAction SilentlyContinue) {
    & nssm.exe stop   $ServiceName confirm | Out-Null
    & nssm.exe remove $ServiceName confirm | Out-Null
  } else {
    & sc.exe stop   $ServiceName | Out-Null
    & sc.exe delete $ServiceName | Out-Null
  }
} else {
  Write-Host "Service $ServiceName not found; nothing to stop."
}

if (Test-Path $InstallDir) {
  Write-Host "Removing $InstallDir ..."
  Remove-Item -Recurse -Force $InstallDir
}

if ($Purge) {
  $tokenDir   = "$env:ProgramData\gpu-browser-bridge"
  $profileDir = "$env:LOCALAPPDATA\gpu-browser-bridge"
  if (Test-Path $tokenDir)   { Remove-Item -Recurse -Force $tokenDir   ; Write-Host "Purged $tokenDir" }
  if (Test-Path $profileDir) { Remove-Item -Recurse -Force $profileDir ; Write-Host "Purged $profileDir" }
} else {
  Write-Host ""
  Write-Host "Token and Chrome profile preserved. Pass -Purge to delete them."
}

Write-Host "Uninstall complete." -ForegroundColor Green
