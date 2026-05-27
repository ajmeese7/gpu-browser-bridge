# Security model

What this service does and does not protect, and what an attacker can do if they breach each layer.

## What's protected

| Layer | Mechanism |
|-------|-----------|
| Network reachability | Bridge binds `127.0.0.1` only. Reached only via reverse SSH tunnel or Tailscale ACL. |
| API auth | Bearer token required on `/screenshot` and `/eval`. Constant-time comparison. |
| Token storage | `%ProgramData%\gpu-browser-bridge\token` with ACL set to SYSTEM + Administrators only. |
| Token strength | 256 bits of `crypto/rand`, hex-encoded (64 chars). |
| Auth log | Every request logged with method, path, status, duration, remote address. |

## What's NOT protected

| Threat | Why we accept it |
|--------|------------------|
| The bridge Chrome profile | Anything logged into the bridge's Chrome (admin accounts, staging credentials, dev tokens) is reachable by any authenticated caller via `/eval`. Treat the profile like a shared test account, not a personal one. |
| `eval` script content | We do not sandbox or sanitize the JS sent to `/eval`. A malicious caller with a valid token can read all cookies, localStorage, and IndexedDB in any origin the bridge Chrome has been to. |
| Outbound network from Chrome | Chrome can reach any URL. A malicious `eval` script could exfiltrate page contents to an attacker domain. |
| Compromised `bridge.exe` binary | If an attacker replaces the binary on disk, all bets are off. Mitigations: run the service as a low-priv account, ACL the install dir to admins only, sign the binary in CI. |
| Tunnel hijack | If the reverse SSH tunnel's SSH keys leak, an attacker can re-establish the same tunnel and reach the bridge with their own caller. Rotate SSH keys on any suspected compromise; the bridge token is a second factor. |

## Threat model in one sentence

The bridge is for use by a small number of trusted callers (you, your coding agent) on a private network. If a token leaks, an attacker can drive a GPU-backed Chrome and read anything that Chrome is logged into — so don't log it into anything sensitive.

## Things to do if you suspect compromise

1. Rotate the token: `bridge.exe gen-token <path>` then `Restart-Service gpu-browser-bridge`.
2. Wipe the Chrome profile: stop the service, `Remove-Item -Recurse %LocalAppData%\gpu-browser-bridge\chrome-profile`, restart.
3. Review logs: `%ProgramData%\gpu-browser-bridge\bridge.log`. Look for requests from unexpected `remote` addresses or unexpected `path`s.
4. If the SSH tunnel was the entry point: rotate SSH keys on both ends.

## Future hardening (not in v1)

- **Outbound URL allowlist** for `/screenshot` and `/eval` — bridge refuses URLs that don't match a configured pattern.
- **Origin allowlist for `/eval` results** — only return data from origins the caller declared.
- **Signed releases** of `bridge.exe` via cosign + GitHub OIDC.
- **Per-caller tokens** with scope (`screenshot-only`, `read-only`, etc.) instead of one global bearer.
- **Audit log shipping** to a remote sink so a local attacker can't erase their tracks.
