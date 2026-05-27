# Windows install notes

## Prerequisites

- Run scripts elevated (Administrator).
- [NSSM](https://community.chocolatey.org/packages/NSSM) on PATH. Easiest:
  ```powershell
  choco install nssm -y
  ```
  (NSSM gives us auto-restart on Chrome/bridge crash, environment-variable passthrough, and log rotation. Native `sc.exe` would work but loses log rotation.)
- [Google Chrome](https://www.google.com/chrome/) installed at one of the standard `Program Files` locations.
- Go 1.26+ if building from source (otherwise pass `-SkipBuild` and drop a prebuilt `bridge.exe` in the repo root).

## Install

```powershell
cd C:\path\to\gpu-browser-bridge
.\windows\install.ps1
```

The script builds `bridge.exe`, copies it to `%ProgramFiles%\gpu-browser-bridge\`, generates a 64-char hex token at `%ProgramData%\gpu-browser-bridge\token` (locked to SYSTEM + Administrators), registers an NSSM-managed service called `gpu-browser-bridge`, and starts it.

At the end it prints the headless-host setup snippet (reverse tunnel + CLI config). Copy that to the caller machine.

## Verify

```powershell
curl http://127.0.0.1:8765/healthz
# {"ok":true,"chrome_alive":true,"uptime_s":7}
```

If `chrome_alive` is `false`, check `%ProgramData%\gpu-browser-bridge\bridge.log`.

## Uninstall

```powershell
.\windows\uninstall.ps1            # leaves token + profile in place
.\windows\uninstall.ps1 -Purge     # removes everything
```

## Token rotation

```powershell
& "$env:ProgramFiles\gpu-browser-bridge\bridge.exe" gen-token "$env:ProgramData\gpu-browser-bridge\token"
Restart-Service gpu-browser-bridge
```

Then re-distribute the new token to the headless host's `~/.config/gpu-browser/config`.
