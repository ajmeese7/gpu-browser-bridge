# gpu-browser-bridge - Agent Context

## Project

HTTP API wrapping a GPU-backed Chrome on Windows, so headless callers (CI, coding agents, cloud VMs) can take screenshots and run JS against real WebGPU/WebGL code paths without falling back to software rendering.

## Constraints

- **Host OS**: Windows only (where the GPU lives). Caller can be any OS.
- **Language**: Go 1.26+, single static binary for both service and CLI.
- **CDP driver**: chromedp (pure Go, no Node sidecar).
- **Process supervision**: an **interactive-session logon Scheduled Task** (`windows/install.ps1`), NOT a Windows service. A service runs in Session 0, which has no GPU desktop and hangs on WebGPU pages - see Known Issues. **Install and run need no elevation**: everything (binary, token, profile, log) lives under `%LocalAppData%\gpu-browser-bridge`, and a user can register a self-scoped logon task unelevated. (A non-elevated process cannot replace an admin-created task, so migrating off an old admin/service install needs one elevated `uninstall.ps1` first.)
- **Port**: 51234 (IANA dynamic/private range).
- **PowerShell .ps1 files must be ASCII-only** - Windows reads them as Windows-1252 by default. No em-dashes, arrows, smart quotes, or any non-ASCII character.
- **Never use PowerShell Add-Content for line-oriented files** - it does not prepend a newline separator. Read-modify-write with explicit newlines instead.
- **No auto-commits** - always wait for explicit user authorization before any git commit.

## Build

```bash
go build -ldflags "-H=windowsgui" -o bridge.exe ./cmd/bridge   # GUI subsystem: no console window (logs go to a file)
GOOS=linux GOARCH=amd64 go build -o gpu-browser ./cmd/gpu-browser
go test ./...
```

## Architecture

```
Caller (headless Linux)                          GPU Host (Windows)
+---------------------------+                    +---------------------------+
| gpu-browser CLI           |  SSH -L tunnel     | bridge.exe (logon task)   |
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

### cmd/bridge (bridge.exe) - GPU-host process

Entry point for the GPU host. Wires config + browser + HTTP server.

- Console mode: `bridge.exe` (this is how the logon task runs it). Built with the GUI subsystem (`-H=windowsgui`) so there is no console window; logs to `%LOCALAPPDATA%\gpu-browser-bridge\bridge.log` (truncated each launch), tee'd to stderr for foreground dev runs.
- Token generator: `bridge.exe gen-token <path>` (prints to stdout; the GUI-subsystem build does not reach a parent console, so `install.ps1` reads the token back from the file).
- Deployed via `windows/install.ps1` as an interactive-session logon task (see Process supervision). A `bridge.exe service` SCM path still exists in the code but is unused - a Session-0 service hangs on WebGPU.

### cmd/gpu-browser - Caller CLI

Cross-platform CLI that talks to bridge.exe over HTTP.

- `gpu-browser healthz` - check bridge status
- `gpu-browser screenshot <url> [flags]` - capture PNG
- `gpu-browser eval <url> <script> [flags]` - run JS, return result

Config from env (`BRIDGE_URL`, `BRIDGE_TOKEN`) or `~/.config/gpu-browser/config`.

### internal/config

Loads `Config` struct from env vars + on-disk token file. Key defaults (all
under the user's profile so install/run need no elevation):
- `BRIDGE_BIND_ADDR`: `127.0.0.1:51234`
- `BRIDGE_TOKEN_PATH`: `%LocalAppData%\gpu-browser-bridge\token`
- `BRIDGE_CHROME_PATH`: auto-detected from `Program Files`
- `BRIDGE_USER_DATA_DIR`: `%LocalAppData%\gpu-browser-bridge\chrome-profile`
- `BRIDGE_LOG_PATH`: `%LocalAppData%\gpu-browser-bridge\bridge.log`

Validates: bind address must be loopback, token must be >= 32 chars.

### internal/browser

Supervises one persistent Chrome process via chromedp.

Key design decisions:
- Builds exec options from scratch instead of `chromedp.DefaultExecAllocatorOptions`, which includes `DisableGPU` (fatal for WebGPU) and OLD headless. Uses NEW headless (`--headless=new`) explicitly: no window at all, but keeps the real GPU - verified `navigator.gpu.requestAdapter()` returns the AMD RDNA-2 adapter and a WebGPU sample renders to a non-black screenshot.
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

- `install.ps1` - **No admin.** Builds bridge.exe (GUI subsystem), installs to `%LocalAppData%`, generates the token, registers and starts the self-scoped interactive logon task (runs bridge.exe directly, new-headless Chrome - no window). Errors with a one-time migration hint if a legacy admin task blocks it.
- `uninstall.ps1` - Removes the task and per-user binary unelevated; best-effort (needs admin) for legacy service / `%ProgramFiles%` binary / `%ProgramData%` token. `-Purge` also deletes token, Chrome profile, and logs.
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

The token is printed at the end of `install.ps1` output. It can also be read from `%LocalAppData%\gpu-browser-bridge\token` on the GPU host.

## SSH Tunnel Setup

Windows OpenSSH has a critical gotcha for Administrator accounts: it ignores the per-user `~/.ssh/authorized_keys` and instead reads `C:\ProgramData\ssh\administrators_authorized_keys`. The `authorize-key.ps1` script handles this automatically.

### Setting up the tunnel

The tunnel must use `-L` (local forward), NOT `-R` (remote forward).

From the **caller** (the headless box you run the CLI on):
```bash
ssh -N -L 51234:127.0.0.1:51234 "<windows-username>"@<windows-ip>
```

This makes the caller's `localhost:51234` forward to the GPU host's `127.0.0.1:51234` through the SSH connection. The bridge is then reachable at `http://localhost:51234` on the caller.

`-R` does the opposite (makes a port on the remote forward back to the local) and is NOT what we want here.

**Use `127.0.0.1`, not `localhost`, for the remote target.** On many hosts `localhost` resolves to IPv6 `::1` first; sshd then forwards to `::1:51234`, and if the bridge isn't answering on IPv6 the channel closes and the caller sees `Empty reply from server` / `EOF`. The bridge binds both IPv4 and IPv6 loopback (`internal/server.ListenAndServe`), so `localhost` works against current builds, but `127.0.0.1` is unambiguous and also works against older binaries.

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

- **No Chrome window appears**: the bridge runs Chrome in new headless mode (`--headless=new`), so there is no window on the desktop or taskbar - nothing to see and nothing for a user to accidentally close - while the real GPU is still used. (Earlier headful builds showed a window; closing it killed Chrome. `newTab` now also relaunches Chrome if the browser context died, so a crash self-heals on the next request.)
- **`engineCount: 3` in Babylon.js apps**: React StrictMode / Vite HMR double-invokes effects, creating orphan Babylon engines. Only the last one drives rendering. Not a bridge bug.
- **WebGPU/GPU-heavy pages time out (`context deadline exceeded`) when the bridge runs as a Session-0 service**: This is the real cause behind the old "webgpureport.org is just a slow SPA" guess. A Windows service (NSSM or `sc.exe`) always runs in **Session 0**, which has no interactive GPU desktop, so GPU-bound renders never settle and `chromedp.Navigate` dies at the timeout. Static pages (example.com) still rasterize, which makes it look like a per-site problem. The identical `bridge.exe` run in an **interactive session** screenshots the same WebGPU page in ~0.5s. Fix: deploy with `windows/install-interactive.ps1` (logon Scheduled Task in the user's session), not the Session-0 service. See `docs/fix-session0-gpu-hang.md`. Verify the session with `Get-Process bridge | Select SessionId` (must be >= 1, not 0).

## Security Model

See `docs/security.md`. Short version: bridge binds loopback only, reached via SSH tunnel or Tailscale ACL. Bearer token for API auth. The bridge Chrome profile should only contain test/dev credentials, not personal ones.
