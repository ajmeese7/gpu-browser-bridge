# Networking

The bridge binds loopback `:51234` on the GPU host (both IPv4 `127.0.0.1` and IPv6 `::1`). To call it from a headless host (the caller), you need to make that port reachable on the caller's loopback as well.

This doc covers two ways: SSH local-forward tunnel (recommended, works on any network) and Tailscale (zero-tunnel-maintenance, requires Tailscale on both ends).

## Option A -- SSH local-forward tunnel (recommended)

The caller opens an SSH connection TO the GPU host and creates a local forward (`-L`), making the bridge's port appear on the caller's own loopback. Everything stays on loopback on both ends.

### One-shot (foreground)

On the caller:

```bash
ssh -N -L 51234:127.0.0.1:51234 <user>@<gpu-host>
```

`-N` = no remote shell, `-L local:host:remote` = listen on the caller's localhost:51234 and forward to the GPU host's 127.0.0.1:51234 through the SSH connection. Use `127.0.0.1` (not `localhost`) for the remote target -- see Notes. Leave it running; in another terminal:

```bash
curl http://localhost:51234/healthz
```

### Persistent (autossh + systemd)

Install [`autossh`](https://github.com/Autossh/autossh) on the caller and create a user systemd unit at `~/.config/systemd/user/gpu-bridge-tunnel.service`:

```ini
[Unit]
Description=GPU browser bridge tunnel
After=network-online.target

[Service]
Type=simple
ExecStart=/usr/bin/autossh -M 0 -N \
  -o "ServerAliveInterval 30" \
  -o "ServerAliveCountMax 3" \
  -o "ExitOnForwardFailure yes" \
  -L 51234:127.0.0.1:51234 \
  <user>@<gpu-host>
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload
systemctl --user enable --now gpu-bridge-tunnel
loginctl enable-linger $USER     # keep the tunnel up across logouts
```

Verify:

```bash
systemctl --user status gpu-bridge-tunnel
curl http://localhost:51234/healthz
```

### Notes

- The tunnel must originate from the caller, not the GPU host. SSH has to know how to reach the GPU host; if it can't, set up a normal SSH connection first (key auth, known_hosts, etc.).
- **Target the remote forward at `127.0.0.1`, not `localhost`.** `localhost` resolves to IPv6 `::1` on many hosts; the GPU host's sshd then tries to connect the forward to `::1:51234`, and if the bridge isn't answering on IPv6 the channel is closed and the caller sees `Empty reply from server` / `EOF`. The bridge now binds both IPv4 and IPv6 loopback, but `127.0.0.1` in the forward spec removes the ambiguity entirely and works against older bridge builds too.
- `ExitOnForwardFailure yes` means autossh will tear the tunnel down (and restart) if the port is already bound — useful when the GPU host reboots out from under the tunnel.
- The GPU host's `sshd` config must allow port forwarding (`AllowTcpForwarding yes`, which is the default).

## Option B — Tailscale

Both machines join the same tailnet. Bridge binds the tailscale interface; caller talks directly.

### GPU host (Windows)

1. Install [Tailscale for Windows](https://tailscale.com/download/windows), join the tailnet.
2. Set the bind address in the run-as user's environment and restart the logon task
   (the task inherits the user environment at launch; other paths use their defaults):

```powershell
[Environment]::SetEnvironmentVariable("BRIDGE_BIND_ADDR", "<tailscale-ip>:51234", "User")
Stop-ScheduledTask  -TaskName gpu-browser-bridge
Start-ScheduledTask -TaskName gpu-browser-bridge
```

Important: the bridge's `config.validate()` currently rejects non-loopback `BindAddr` to prevent accidental exposure. Tailscale support is a planned change — see issue tracker, or override the check by binding `127.0.0.1` and using Tailscale's `tailscale serve` to expose loopback to the tailnet:

```powershell
tailscale serve --bg --tcp 51234 tcp://127.0.0.1:51234
```

### Caller

```bash
# Find the GPU host's tailscale name from `tailscale status`
export BRIDGE_URL=http://<gpu-host>.<tailnet>.ts.net:51234
gpu-browser healthz
```

### Tailnet ACL

Restrict who can hit the bridge port:

```jsonc
// in your tailnet ACL
{
  "acls": [
    { "action": "accept",
      "src":    ["tag:bridge-caller"],
      "dst":    ["tag:bridge-host:51234"] }
  ]
}
```

Tag the GPU host with `bridge-host` and the caller with `bridge-caller`. Nothing else on the tailnet can reach the bridge.

## Which to pick

| Need | Pick |
|------|------|
| Caller and GPU host on the same LAN, never need access from elsewhere | SSH local-forward tunnel |
| Caller and GPU host on different networks (cloud VM, remote office) | Tailscale |
| Already running an SSH connection between them | SSH local-forward tunnel (it's free) |
| Multiple callers need bridge access | Tailscale with ACL |

Start with the SSH tunnel. Move to Tailscale only when the tunnel becomes a hassle to maintain.
