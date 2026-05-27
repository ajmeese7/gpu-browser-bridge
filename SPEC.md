# gpu-browser-bridge — Spec (v0–v2)

## Problem

Headless development environments (cloud VMs, CI runners, air-gapped boxes, servers without a display) have no WebGPU adapter. Chromium silently falls back to WebGL2, which masks real WebGPU bugs and prevents verification of any code that branches on `navigator.gpu`.

The same problem applies, with less drama, to WebXR, WebGL extensions that require specific hardware, and visual regressions that only appear under real GPU compositing.

A motivating example: a 3D network-graph project using Babylon.js (WebGPU first, WebGL2 fallback) had a render bug that was invisible on every CI run and every headless verification pass — falling back to WebGL2 sidestepped the broken code path. The bug only surfaced when a developer opened the page in a real browser. Anyone with a coding agent running on a headless box has hit, or will hit, a variant of this.

We want headless callers to verify GPU-dependent UI on a workstation with a real GPU, on demand, without manual setup each time.

## Non-goals

- **Not** a general remote-browser-as-a-service. This is a one-host bridge used by trusted callers on a private network.
- **Not** a Playwright replacement. The service exposes Playwright/CDP, it does not abstract it.
- **Not** a public endpoint. CDP and the wrapper API are bound to localhost on the GPU host and reached over an SSH tunnel (v0) or Tailscale (v2+).
- **Not** cross-platform on the host side. Windows-only host (where the GPU Chrome lives). The caller can be anything that speaks HTTP.

## Architecture

```
+--------------------+          SSH reverse tunnel           +---------------------+
|  Headless caller   |  ── ssh -L 51234:localhost:51234 ──→  | GPU host (Windows)  |
|  (Linux / cloud)   |                                       |                     |
|                    |     POST http://localhost:51234/…     |  bridge.exe         |
|  gpu-browser CLI   | ────────────────────────────────────→ |  (Windows service)  |
|  or MCP client     | ←──────────── JSON ─────────────────  |        │            |
+--------------------+                                       |        ▼            |
                                                             |  Playwright / CDP   |
                                                             |        │            |
                                                             |        ▼            |
                                                             |  Chrome (persistent)|
                                                             |  127.0.0.1:9222 CDP |
                                                             |  WebGPU enabled     |
                                                             +---------------------+
```

**Why a wrapper, not raw CDP?** CDP has zero auth. Anything that can reach :9222 can read every cookie/password in that Chrome profile and execute arbitrary JS in any tab. The wrapper adds:

- Bearer-token auth
- An allowlist of operations (no arbitrary CDP method calls)
- Logging of every request
- Lifecycle management for Chrome (restart if it dies)

## Milestones

### v0 — Proof of concept (no code, ~1 hour)

Just enough to validate the workflow.

- Windows Task Scheduler entry: launch Chrome at user logon with `--remote-debugging-port=9222 --user-data-dir=%LOCALAPPDATA%\bridge-chrome --remote-allow-origins=*`, bound to `127.0.0.1` only.
- Manual SSH reverse tunnel from the headless host: `ssh -L 9222:localhost:9222 user@gpu-host` (kept open in a tmux window or via `autossh`).
- Caller writes a Playwright spec using `chromium.connectOverCDP("http://localhost:9222")` and runs it on the headless host.

**Exit criterion:** caller takes one screenshot of any WebGPU-using URL through GPU-backed Chrome and gets a PNG back. Confirm the page reports the WebGPU code path (e.g. Babylon's renderer badge) rather than WebGL2.

This proves networking + CDP + WebGPU. If it works, build v1. If it doesn't, the rest of this spec is moot.

### v1 — The actual service

A small Go HTTP server (`bridge.exe`) wrapping Chrome + a CDP driver, installed as a Windows service via NSSM.

#### Endpoints

All require `Authorization: Bearer <token>` header. Token loaded from `%PROGRAMDATA%\gpu-browser-bridge\token` on service start.

| Method | Path | Body | Returns |
|--------|------|------|---------|
| `GET`  | `/healthz` | — | `{ok: true, chrome_alive: bool, uptime_s: int}` (no auth) |
| `POST` | `/screenshot` | `{url, viewport?, wait_for?, full_page?}` | `{png_b64, console[], failed_requests[]}` |
| `POST` | `/eval` | `{url, script, wait_for?}` | `{result, console[], failed_requests[]}` |
| `POST` | `/trace` | `{url, actions[]}` | `{trace_zip_b64, screenshots[], console[]}` |

`script` for `/eval` runs in page context after `wait_for`. Result must be JSON-serializable. The caller cannot pass arbitrary CDP commands — only the above shapes.

#### Chrome lifecycle

- On service start: launch Chrome with persistent `--user-data-dir` (preserves login state between requests).
- Health check every 30s: if Chrome's `/json/version` fails, kill and relaunch.
- Each request gets its own incognito browser context inside the persistent Chrome to isolate cookies between callers if we ever add more than one.

#### Why Go

- Single static binary, no runtime install.
- `chromedp` or `playwright-community/playwright-go` for the driver.
- Matches the existing `.gitignore` (Go-flavored) — assume that signal.

If `playwright-go` proves flaky on Windows we fall back to `chromedp` (pure Go, CDP-native, no node sidecar).

#### Installation

```powershell
# One-time:
.\install.ps1
# Generates a token, registers NSSM service "gpu-browser-bridge",
# adds Defender exclusion for the bridge-chrome profile dir,
# prints token + setup instructions for the remote.
```

#### Repo layout for v1

```
gpu-browser-bridge/
├── README.md                    # User-facing: install + use
├── SPEC.md                      # This file
├── go.mod / go.sum
├── cmd/
│   ├── bridge/                  # The Windows service (bridge.exe)
│   │   └── main.go
│   └── gpu-browser/             # Remote CLI (built for Linux/macOS)
│       └── main.go
├── internal/
│   ├── browser/                 # Chrome lifecycle, CDP driver glue
│   ├── server/                  # HTTP handlers, auth, logging
│   └── config/                  # token loading, paths
├── windows/
│   ├── install.ps1              # NSSM register, token gen, firewall
│   ├── uninstall.ps1
│   └── README.md                # Windows-specific notes
└── docs/
    ├── networking.md            # SSH tunnel + Tailscale recipes
    └── security.md              # Threat model, what's protected and not
```

### v2 — MCP shim (optional)

Once v1 works, wrap the HTTP API as an MCP server so coding agents call `browser_screenshot` and `browser_eval` as native tools instead of shelling out to a CLI. The MCP server itself can run on the headless host and proxy to the bridge — no extra Windows install.

```
headless host:
  ~/.claude/.mcp.json:
    "gpu-browser": {
      "command": "gpu-browser-mcp",
      "env": {
        "BRIDGE_URL": "http://localhost:51234",
        "BRIDGE_TOKEN": "..."
      }
    }
```

Tools exposed: `screenshot(url, ...)`, `eval(url, script, ...)`, `trace(...)`.

## Security model

| Threat | Mitigation |
|--------|------------|
| Someone on the LAN reaches the bridge port | Bridge binds `127.0.0.1` only; reached only via SSH reverse tunnel (v0/v1) or Tailscale ACL (v2) |
| Stolen bearer token | Token rotation script; token in `%PROGRAMDATA%`, ACL'd to admins + service account |
| Malicious `eval` script reads cookies from user's real browser | Bridge Chrome uses dedicated `--user-data-dir`, separate from user's daily Chrome; logged into only test accounts |
| `eval` script exfiltrates data to an attacker domain | Out of scope for v1; the threat is "the trusted caller went rogue," which we accept. Could add an outbound domain allowlist later if needed. |
| `bridge.exe` is replaced by an attacker | NSSM service runs as a low-priv account, binary path ACL'd, signed binary in CI if we get fancy |

**What we are NOT protecting:** the bridge Chrome profile. Anything logged into it (admin accounts on staging boxes, dev credentials) is reachable by any authenticated bridge caller. Treat the profile like a shared test account, not a personal one.

## Networking decision (v0 → v2)

- **v0/v1: SSH reverse tunnel from the headless host.** Tunnel command in a systemd unit or cron `@reboot` on the headless host. If the tunnel dies, `autossh` restarts it. Bridge port never leaves Windows localhost.
- **v2 (if needed): Tailscale.** Both machines on a personal tailnet, bridge binds to the tailscale interface, ACL restricts to specific nodes. Removes the tunnel maintenance burden; adds Tailscale dependency.

Start at v0. Only graduate to Tailscale when working from outside the LAN becomes a real need.

## Open questions for review

1. **Driver choice — `chromedp` (pure Go, CDP-native) vs `playwright-go` (mirrors what many existing E2E test suites use).** Lean `chromedp` for fewer moving parts; `playwright-go` if symmetry with existing Playwright tests in your projects matters more than dependency simplicity.
2. **Does the bridge manage one Chrome process or one per request?** Spec currently says one persistent Chrome with per-request incognito contexts — faster, but a crash in one request can wedge them all. Per-request Chrome is safer but every call pays ~1s startup.
3. **`/trace` — worth it for v1, or defer?** Playwright traces are great for debugging multi-step flows but add complexity. Defer if v0 + screenshots answer most questions.
4. **MCP or CLI first?** CLI is faster to build and a coding agent can drive it the same way it drives `kubectl`. MCP is nicer long-term but adds a surface to maintain. Recommend: ship the CLI in v1, add MCP in v2 only if the CLI feels awkward in practice.
5. **Should `install.ps1` also install Chrome?** Easier UX, but means we have to track Chrome's installer URL. Probably: assume Chrome is installed, fail loudly with a link if not.

## What v0 looks like concretely

To pressure-test the workflow before any Go code is written. Replace the placeholders (`<GPU_HOST>`, `<USER>`, `<HEADLESS_HOST>`) with your own.

**On the GPU host (Windows):**

```powershell
# Start Chrome with CDP, profile isolated from your daily browser.
& "C:\Program Files\Google\Chrome\Application\chrome.exe" `
  --remote-debugging-port=9222 `
  --remote-debugging-address=127.0.0.1 `
  --user-data-dir="$env:LOCALAPPDATA\bridge-chrome" `
  --no-first-run --no-default-browser-check
```

**On the headless host (caller):**

```bash
# Open reverse tunnel so the GPU host's CDP appears as localhost:9222 here.
# Add -N to skip a remote shell, autossh in production.
ssh -N -R 9222:localhost:9222 <USER>@<GPU_HOST>
```

```typescript
// verify-webgpu.spec.ts
import { test, chromium } from "@playwright/test";

test("renders via real WebGPU", async () => {
  const browser = await chromium.connectOverCDP("http://localhost:9222");
  const context = await browser.newContext({ ignoreHTTPSErrors: true });
  const page = await context.newPage();
  await page.goto("https://your-app.example/scene");
  // ... drive the page, assert WebGPU path was taken, screenshot, etc.
});
```

**Success criterion:** a screenshot returns and the app reports the WebGPU code path was taken (renderer badge, `console.log` of `navigator.gpu` being defined, whatever signal your app exposes).

If that works, v1 is just "wrap the Chrome launch + the Playwright call in a Go service with an API key in front." If it doesn't, debug the network / firewall layer before building anything bigger.
