# gpu-browser-bridge

Drive a real GPU-backed Chrome on a remote workstation from a headless host, so WebGPU / WebGL / WebXR code paths can be verified without falling back to software rendering.

**Status:** Spec only. See [SPEC.md](./SPEC.md).

## Why

Headless Chromium has no WebGPU adapter, so any code that branches on `navigator.gpu` either silently falls back to WebGL2 or fails in ways that are invisible to the headless caller. This means:

- Coding agents running on cloud or air-gapped boxes can't verify WebGPU features they just wrote.
- Visual regression in CI doesn't catch GPU-only rendering bugs.
- Anyone doing browser-based ML inference, 3D rendering, or WebXR work on a GPU-less server has no good way to "see what the user sees."

This bridge exposes a Windows workstation's real Chrome (with a GPU) to remote callers over an authenticated HTTP API, so the headless caller can take screenshots, run Playwright specs, and inspect runtime state against the same code path a developer sees in their own browser.

## Shape

- **Windows service** (`bridge.exe`) — runs Chrome with CDP, exposes authenticated HTTP API on `127.0.0.1`.
- **Remote CLI / MCP server** — talks to the bridge over a reverse SSH tunnel or Tailscale.

See [SPEC.md](./SPEC.md) for the full design.
