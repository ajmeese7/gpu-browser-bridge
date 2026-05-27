# authorize-key.ps1 - add a caller's SSH public key to the right
# authorized_keys file on this Windows host, with correct ACLs.
#
# Background: Windows OpenSSH ignores per-user authorized_keys for
# accounts in the local Administrators group, and instead consults
#   C:\ProgramData\ssh\administrators_authorized_keys
# This script detects which file applies and writes to the right one.
#
# Usage (elevated PowerShell):
#   .\windows\authorize-key.ps1 -PublicKey 'ssh-rsa AAAA... user@host'
#   .\windows\authorize-key.ps1 -KeyFile  C:\path\to\caller.pub
#   .\windows\authorize-key.ps1                # reads from clipboard
#
# Idempotent: skips if the key (matched by base64 body) is already present.

[CmdletBinding()]
param(
  [string]$PublicKey = "",
  [string]$KeyFile   = ""
)

$ErrorActionPreference = "Stop"

function Require-Admin {
  $id = [Security.Principal.WindowsIdentity]::GetCurrent()
  $p  = New-Object Security.Principal.WindowsPrincipal($id)
  if (-not $p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "authorize-key.ps1 must be run as Administrator."
  }
}

function Get-IsLocalAdmin {
  # SID S-1-5-32-544 = BUILTIN\Administrators
  return [bool](whoami /groups | Select-String -SimpleMatch "S-1-5-32-544")
}

Require-Admin

# Resolve the key text from one of: -PublicKey, -KeyFile, or clipboard.
if ($PublicKey -eq "" -and $KeyFile -ne "") {
  if (-not (Test-Path $KeyFile)) { throw "Key file not found: $KeyFile" }
  $PublicKey = (Get-Content -Raw -Path $KeyFile).Trim()
}
if ($PublicKey -eq "") {
  $PublicKey = (Get-Clipboard -Raw).Trim()
}
if ($PublicKey -eq "" -or $PublicKey -notmatch '^(ssh-(rsa|ed25519|dss)|ecdsa-sha2-\S+)\s+\S+') {
  throw "No valid public key supplied. Pass -PublicKey, -KeyFile, or copy a key to the clipboard."
}

# Decide which authorized_keys file to write.
if (Get-IsLocalAdmin) {
  $keysFile = "C:\ProgramData\ssh\administrators_authorized_keys"
  $aclGrant = @("Administrators:F", "SYSTEM:F")
  Write-Host "Account is in Administrators group -> using $keysFile"
} else {
  $keysFile = Join-Path $env:USERPROFILE ".ssh\authorized_keys"
  $aclGrant = @("$env:USERNAME:F")
  Write-Host "Standard user account -> using $keysFile"
}

New-Item -ItemType Directory -Force -Path (Split-Path -Parent $keysFile) | Out-Null

# Read existing keys (if any). Split on any line ending so a file written
# with CR-only or no trailing newline still parses correctly.
$existingLines = @()
if (Test-Path $keysFile) {
  $raw = [System.IO.File]::ReadAllText($keysFile)
  $existingLines = $raw -split "`r?`n" | Where-Object { $_ -ne "" }
}

# Idempotency check: compare on the base64 body (field 2 of the key line),
# ignoring the trailing comment so re-runs with the same key don't duplicate.
$newBody = ($PublicKey -split '\s+')[1]
$alreadyPresent = $existingLines | Where-Object {
  $parts = $_ -split '\s+'
  $parts.Length -ge 2 -and $parts[1] -eq $newBody
}

if ($alreadyPresent) {
  Write-Host "Key already authorized in $keysFile. Nothing to do." -ForegroundColor Yellow
} else {
  # Rewrite the whole file with explicit LF separators and ASCII encoding.
  # Add-Content's append-without-leading-newline behavior previously
  # concatenated keys on one line when the existing file lacked a trailing
  # newline, producing a corrupt "key" that sshd silently rejected.
  $allLines = @($existingLines + $PublicKey)
  $content  = ($allLines -join "`n") + "`n"
  [System.IO.File]::WriteAllText($keysFile, $content, [System.Text.Encoding]::ASCII)
  Write-Host "Appended key to $keysFile (now contains $($allLines.Count) keys)" -ForegroundColor Green
}

# Re-tighten ACLs unconditionally - OpenSSH silently ignores the file if
# permissions are too loose (e.g. if you ever edited it from a non-admin
# session and inheritance crept back in).
& icacls.exe $keysFile /inheritance:r /grant:r @aclGrant | Out-Null
if ($LASTEXITCODE -ne 0) { throw "icacls failed to lock down $keysFile" }
Write-Host "ACL: removed inheritance, granted $($aclGrant -join ', ')"

Write-Host ""
Write-Host "Done. sshd reads authorized_keys on each connection - no restart needed."
Write-Host 'From the caller, retry:  ssh -N -L <port>:localhost:<port> <user>@<this-machine>'
