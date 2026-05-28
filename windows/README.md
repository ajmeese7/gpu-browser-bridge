# Windows install notes

## Elevation

**`install.ps1` needs no administrator rights.** It installs everything under your user
profile (`%LocalAppData%\gpu-browser-bridge`) and registers a per-user logon Scheduled
Task, all of which a standard user can do. The bridge then runs **non-elevated** in your
interactive desktop session (that is what gives Chrome a real GPU).

The only thing that needs an elevated PowerShell is cleaning up a **legacy** admin/service
install (a Session-0 service or an admin-created task from an older version): a non-elevated
process cannot remove an admin-created task. `uninstall.ps1` does the normal per-user
removal unelevated and attempts the legacy bits best-effort, telling you if elevation is
needed.

## Prerequisites

- [Google Chrome](https://www.google.com/chrome/) installed at one of the standard
  `Program Files` locations.
- Go 1.26+ if building from source (otherwise pass `-SkipBuild` and drop a prebuilt
  `bridge.exe` in the repo root).

No service wrapper (NSSM/sc.exe) is needed — a Windows service runs in Session 0, which
has no GPU desktop and hangs on WebGPU. The bridge runs as an interactive logon task
instead. See [../docs/fix-session0-gpu-hang.md](../docs/fix-session0-gpu-hang.md).

## Install

In a normal (non-elevated) PowerShell:

```powershell
cd C:\path\to\gpu-browser-bridge
.\windows\install.ps1
```

The script builds `bridge.exe` (GUI subsystem, so no console window), installs it and the
token under `%LocalAppData%\gpu-browser-bridge\`, registers a logon Scheduled Task called
`gpu-browser-bridge` that runs the bridge non-elevated in your session, and starts it.
Chrome's window is parked off-screen, so the whole thing is invisible on the desktop.

A logon task only runs once you are logged on; for unattended reboots enable auto-logon
(see the fix doc above).

At the end it prints the headless-host setup snippet (SSH tunnel + CLI config). Copy that
to the caller machine.

> **Upgrading from an older admin/service install?** Run `.\windows\uninstall.ps1` once in
> an elevated PowerShell to clear the old admin-created task/service, then run
> `install.ps1` normally (no admin).

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

The token lives in your profile, so no ACL juggling is needed — regenerate and restart the
task:

```powershell
$tok = "$env:LocalAppData\gpu-browser-bridge\token"
& "$env:LocalAppData\gpu-browser-bridge\bridge.exe" gen-token $tok | Out-Null
Stop-ScheduledTask  -TaskName gpu-browser-bridge
Start-ScheduledTask -TaskName gpu-browser-bridge
```

Then re-distribute the new token to the headless host's `~/.config/gpu-browser/config`.
