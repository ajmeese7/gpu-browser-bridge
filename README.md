# gpu-browser-bridge

Drive a real GPU-backed Chrome on a remote Windows workstation from a headless host, so WebGPU / WebGL / WebXR code paths can be verified without falling back to software rendering.

**Status:** v1 working end-to-end. Tested on Chrome 148, AMD RDNA-2.

## Why

Headless Chromium has no WebGPU adapter, so any code that branches on `navigator.gpu` either silently falls back to WebGL2 or fails in ways that are invisible to the headless caller. This means:

- Coding agents running on cloud or air-gapped boxes can't verify WebGPU features they just wrote.
- Visual regression in CI doesn't catch GPU-only rendering bugs.
- Anyone doing browser-based ML inference, 3D rendering, or WebXR work on a GPU-less server has no good way to "see what the user sees."

This bridge exposes a Windows workstation's real Chrome (with a GPU) to remote callers over an authenticated HTTP API, so the headless caller can take screenshots, run Playwright specs, and inspect runtime state against the same code path a developer sees in their own browser.

## Architecture

```
+--------------------+        SSH reverse tunnel        +---------------------+
|  Headless caller   |  ── ssh -R 8765:localhost:8765 ─→| GPU host (Windows)  |
|                    |                                  |                     |
|  gpu-browser CLI   |   POST http://localhost:8765/…   |  bridge.exe (NSSM)  |
|                    | ───────────────────────────────→ |        │            |
+--------------------+ ←─────────── JSON ──────────────  |        ▼            |
                                                        |  Chrome + chromedp  |
                                                        |  127.0.0.1:8765     |
                                                        |  WebGPU enabled     |
                                                        +---------------------+
```

## Quick start

### GPU host (Windows)

```powershell
# 1. Install Google Chrome:    https://www.google.com/chrome/
# 2. Install NSSM (via Chocolatey):
choco install nssm -y          # https://community.chocolatey.org/packages/NSSM
# 3. Clone this repo, then:
.\windows\install.ps1
# Prints the bearer token and the caller-side setup snippet.
```

### Caller (Linux / macOS / WSL)

```bash
# 1. Open the reverse tunnel (autossh in production, see docs/networking.md)
ssh -N -R 8765:localhost:8765 <user>@<gpu-host>

# 2. Configure the CLI
mkdir -p ~/.config/gpu-browser
cat > ~/.config/gpu-browser/config <<EOF
BRIDGE_URL=http://localhost:8765
BRIDGE_TOKEN=<token from install.ps1>
EOF

# 3. Smoke test
gpu-browser healthz
gpu-browser screenshot https://example.com/ --out example.png
gpu-browser eval https://example.com/ \
  "(async () => { const a = await navigator.gpu?.requestAdapter(); return a?.info; })()"
```

## API

All POST endpoints require `Authorization: Bearer <token>`. `GET /healthz` is unauthenticated.

| Method | Path | Body | Returns |
|--------|------|------|---------|
| `GET`  | `/healthz` | — | `{ok, chrome_alive, uptime_s}` |
| `POST` | `/screenshot` | `{url, viewport_w?, viewport_h?, wait_for?, full_page?, ignore_https_errors?, settle_ms?, timeout_ms?}` | `{png_b64, console[], failed_requests[]}` |
| `POST` | `/eval` | `{url, script, wait_for?, ignore_https_errors?, settle_ms?, timeout_ms?}` | `{result, console[], failed_requests[]}` |

`script` runs in page context after navigation + optional wait; the final expression's value is returned. Promises are awaited.

## Docs

- [SPEC.md](./SPEC.md) — full design, milestones (v0–v2), open questions
- [docs/networking.md](./docs/networking.md) — reverse SSH tunnel and Tailscale recipes
- [docs/security.md](./docs/security.md) — threat model, what's protected and not
- [windows/README.md](./windows/README.md) — install / uninstall / token rotation

## Build from source

```bash
go build -o bridge.exe ./cmd/bridge        # Windows binary
go build -o gpu-browser  ./cmd/gpu-browser  # caller CLI (cross-platform)
```

Go 1.26+ required (pulled in by chromedp).
