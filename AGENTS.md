# gpu-browser-bridge - Agent Context

## Project

HTTP API wrapping a GPU-backed Chrome on Windows, so headless callers (CI, coding agents, cloud VMs) can take screenshots and run JS against real WebGPU/WebGL code paths without falling back to software rendering.

## Constraints

- **Host OS**: Windows only (where the GPU lives). Caller can be any OS.
- **Language**: Go 1.26+, single static binary for both service and CLI.
- **CDP driver**: chromedp (pure Go, no Node sidecar).
- **Service wrapper**: NSSM (via Chocolatey: `choco install nssm -y`).
- **Port**: 51234 (IANA dynamic/private range).
- **PowerShell .ps1 files must be ASCII-only** - Windows reads them as Windows-1252 by default. No em-dashes, arrows, smart quotes, or any non-ASCII character.
- **Never use PowerShell Add-Content for line-oriented files** - it does not prepend a newline separator. Read-modify-write with explicit newlines instead.
- **No auto-commits** - always wait for explicit user authorization before any git commit.

## Build

```bash
go build -o bridge.exe ./cmd/bridge
GOOS=linux GOARCH=amd64 go build -o gpu-browser ./cmd/gpu-browser
go test ./...
```

## Architecture

```
Caller (headless Linux)                          GPU Host (Windows)
+---------------------------+                    +---------------------------+
| gpu-browser CLI           |  SSH -L tunnel     | bridge.exe (NSSM service) |
|   or curl + bearer token  | -----------------> |   |                       |
|                           | <--- JSON -------- |   v                       |
+---------------------------+                    | chromedp (CDP)            |
                                                 |   |                       |
                                                 |   v                       |
                                                 | Chrome 148+               |
                                                 | WebGPU + real GPU         |
                                                 +---------------------------+
```

## Components

### cmd/bridge (bridge.exe) - Windows service

Entry point for the GPU host. Wires config + browser + HTTP server.

- Console mode: `bridge.exe` (foreground, for dev)
- Service mode: `bridge.exe service` (native SCM, for sc.exe users)
- NSSM mode: NSSM runs `bridge.exe` in console mode (recommended)
- Token generator: `bridge.exe gen-token <path>`

### cmd/gpu-browser - Caller CLI

Cross-platform CLI that talks to bridge.exe over HTTP.

- `gpu-browser healthz` - check bridge status
- `gpu-browser screenshot <url> [flags]` - capture PNG
- `gpu-browser eval <url> <script> [flags]` - run JS, return result

Config from env (`BRIDGE_URL`, `BRIDGE_TOKEN`) or `~/.config/gpu-browser/config`.

### internal/config

Loads `Config` struct from env vars + on-disk token file. Key defaults:
- `BRIDGE_BIND_ADDR`: `127.0.0.1:51234`
- `BRIDGE_TOKEN_PATH`: `%ProgramData%\gpu-browser-bridge\token`
- `BRIDGE_CHROME_PATH`: auto-detected from `Program Files`
- `BRIDGE_USER_DATA_DIR`: `%LocalAppData%\gpu-browser-bridge\chrome-profile`

Validates: bind address must be loopback, token must be >= 32 chars.

### internal/browser

Supervises one persistent Chrome process via chromedp.

Key design decisions:
- Does NOT use `chromedp.DefaultExecAllocatorOptions` because it includes `Headless` and `DisableGPU`, both fatal for WebGPU.
- Keeps an "anchor tab" (the browserCtx from `chromedp.NewContext`) alive for the lifetime of the service. Closing the anchor tab would close Chrome.
- The launch timeout uses a goroutine + `time.After` instead of `context.WithTimeout(browserCtx, ...)` because chromedp ties tab lifetime to whichever context the first `Run` uses. Wrapping it in a derived context and cancelling that context kills the anchor tab.
- Per-request operations (`Screenshot`, `Eval`) create a fresh tab via `chromedp.NewContext(browserCtx)`, do their work, then cancel the tab context.
- If Chrome dies between requests, `newTab()` detects `browserCtx.Err() != nil` and relaunches.

Console and network listeners (`listeners.go`) capture `runtime.EventConsoleAPICalled`, `runtime.EventExceptionThrown`, `network.EventResponseReceived` (>= 400), and `network.EventLoadingFailed`.

### internal/server

Standard `net/http` server with:
- `GET /healthz` - unauthenticated, returns `{ok, chrome_alive, uptime_s}`
- `POST /screenshot` - authenticated, drives Chrome to a URL and captures PNG
- `POST /eval` - authenticated, runs JS in page context, returns result
- Bearer-token auth middleware with `crypto/subtle.ConstantTimeCompare`
- Request logging via `slog`

### windows/ - Install scripts

- `install.ps1` - Builds bridge.exe, generates token, registers NSSM service. Must run elevated.
- `uninstall.ps1` - Stops service, removes binary. `-Purge` also deletes token + Chrome profile.
- `authorize-key.ps1` - Adds a caller's SSH public key to the correct `authorized_keys` file (admin vs standard user detection).

## Deploying the CLI to the Caller

The `gpu-browser` CLI must be cross-compiled for the caller's OS and copied over.

```bash
# On the GPU host (Windows), cross-compile for Linux:
GOOS=linux GOARCH=amd64 go build -o gpu-browser ./cmd/gpu-browser

# Copy to the caller via scp (needs SSH access to the caller):
scp gpu-browser <user>@<caller>:~/gpu-browser
ssh <user>@<caller> "sudo mv ~/gpu-browser /usr/local/bin/gpu-browser && sudo chmod +x /usr/local/bin/gpu-browser"

# Set up the CLI config on the caller:
ssh <user>@<caller> "mkdir -p ~/.config/gpu-browser && cat > ~/.config/gpu-browser/config << EOF
BRIDGE_URL=http://localhost:51234
BRIDGE_TOKEN=<token from install.ps1 output>
EOF"
```

The token is printed at the end of `install.ps1` output. It can also be read from `%ProgramData%\gpu-browser-bridge\token` on the GPU host.

## SSH Tunnel Setup

Windows OpenSSH has a critical gotcha for Administrator accounts: it ignores the per-user `~/.ssh/authorized_keys` and instead reads `C:\ProgramData\ssh\administrators_authorized_keys`. The `authorize-key.ps1` script handles this automatically.

### Setting up the tunnel

The tunnel must use `-L` (local forward), NOT `-R` (remote forward).

From the **caller** (e.g. cyiq):
```bash
ssh -N -L 51234:localhost:51234 "<windows-username>"@<windows-ip>
```

This makes the caller's `localhost:51234` forward to the GPU host's `localhost:51234` through the SSH connection. The bridge is then reachable at `http://localhost:51234` on the caller.

`-R` does the opposite (makes a port on the remote forward back to the local) and is NOT what we want here.

### Authorizing the caller's SSH key

On the GPU host (elevated PowerShell):
```powershell
.\windows\authorize-key.ps1 -PublicKey 'ssh-rsa AAAA... user@host'
# or copy the key to clipboard first:
.\windows\authorize-key.ps1
```

The script detects whether the current user is an Administrator and writes to the correct file (`C:\ProgramData\ssh\administrators_authorized_keys` for admins, `~/.ssh/authorized_keys` for standard users), then locks down ACLs.

### Verifying the tunnel

On the caller:
```bash
gpu-browser healthz
# Should return: {"ok":true,"chrome_alive":true,"uptime_s":...}
```

## Known Issues

- **Chrome window appears and disappears on launch**: Expected behavior. chromedp launches Chrome with a visible window but the window closes once the anchor tab loads about:blank. The Chrome process stays alive and CDP works fine - screenshots and eval both succeed.
- **`engineCount: 3` in Babylon.js apps**: React StrictMode / Vite HMR double-invokes effects, creating orphan Babylon engines. Only the last one drives rendering. Not a bridge bug.
- **webgpureport.org times out**: Heavy SPA that takes > 30s to fully initialize. Increase `timeout_ms` or use `/eval` to query `navigator.gpu.requestAdapter()` directly for WebGPU verification.

## Security Model

See `docs/security.md`. Short version: bridge binds loopback only, reached via SSH tunnel or Tailscale ACL. Bearer token for API auth. The bridge Chrome profile should only contain test/dev credentials, not personal ones.
