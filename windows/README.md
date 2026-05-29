# Windows install notes

## No elevation needed

`install.ps1` and `uninstall.ps1` run as a normal user. Everything installs under your user profile (`%LocalAppData%\gpu-browser-bridge`) and the logon Scheduled Task is registered for the current user — none of which needs administrator rights. The bridge then runs in your interactive desktop session, which is what gives Chrome a real GPU.

`go build` and running `bridge.exe` in the foreground for development also need no elevation.

## Prerequisites

- [Google Chrome](https://www.google.com/chrome/) installed at one of the standard `Program Files` locations.
- Go 1.26+ if building from source (otherwise pass `-SkipBuild` and drop a prebuilt `bridge.exe` in the repo root).

No service wrapper is needed: a Windows service runs in Session 0, which has no GPU desktop and hangs on WebGPU, so the bridge runs as an interactive logon task instead.

## Install

In a normal (non-elevated) PowerShell:

```powershell
cd C:\path\to\gpu-browser-bridge
.\windows\install.ps1
```

The script builds `bridge.exe` (GUI subsystem, so no console window), installs it and the token under `%LocalAppData%\gpu-browser-bridge\`, registers a logon Scheduled Task called `gpu-browser-bridge` that runs the bridge in your session, and starts it. Chrome runs in new headless mode (no window), so the whole thing is invisible on the desktop.

At the end it prints the headless-host setup snippet (SSH tunnel + CLI config). Copy that to the caller machine.

## Auto-logon (unattended reboots)

A logon task only runs once a user is logged on. For a headless GPU host that reboots unattended, enable auto-logon so a desktop session — and the bridge — come back on their own.

Recommended: [Sysinternals Autologon](https://learn.microsoft.com/sysinternals/downloads/autologon), which stores the password as an encrypted LSA secret rather than plaintext:

```powershell
.\Autologon.exe "<user>" $env:COMPUTERNAME "<password>" /accepteula
```

Fallback (registry) — WARNING: this writes the password in cleartext to the registry; only acceptable on a locked-down host you control:

```powershell
$wl = "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon"
Set-ItemProperty $wl AutoAdminLogon "1"
Set-ItemProperty $wl DefaultUserName "<user>"
Set-ItemProperty $wl DefaultDomainName $env:COMPUTERNAME
Set-ItemProperty $wl DefaultPassword "<password>"
```

After enabling it, reboot and confirm the bridge comes back in Session >= 1 (`Get-Process bridge | Select-Object SessionId`).

## Verify

```powershell
curl http://127.0.0.1:51234/healthz
# {"ok":true,"chrome_alive":true,"uptime_s":7}
Get-Process bridge | Select-Object Id, SessionId   # SessionId must be >= 1, not 0
```

If `chrome_alive` is `false`, check `%LocalAppData%\gpu-browser-bridge\bridge.log`.

## Uninstall

```powershell
.\windows\uninstall.ps1            # leaves token + profile + logs in place
.\windows\uninstall.ps1 -Purge     # removes everything
```

## Token rotation

The token lives in your profile, so no ACL juggling is needed — regenerate and restart the task:

```powershell
$tok = "$env:LocalAppData\gpu-browser-bridge\token"
& "$env:LocalAppData\gpu-browser-bridge\bridge.exe" gen-token $tok | Out-Null
Stop-ScheduledTask  -TaskName gpu-browser-bridge
Start-ScheduledTask -TaskName gpu-browser-bridge
```

Then re-distribute the new token to the headless host's `~/.config/gpu-browser/config`.
