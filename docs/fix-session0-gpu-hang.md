# Fix: WebGPU pages hang (`context deadline exceeded`) on the bridge

## What was actually wrong

The bridge was deployed as an **NSSM Windows service running as `LocalSystem` in Session 0**.
A Windows service always lives in Session 0, which has **no interactive GPU desktop**.
Chrome can still rasterize plain pages there (that's why `example.com` screenshots
worked), but **GPU/WebGPU-heavy pages never finish rendering**, so `chromedp.Navigate`
waits for a load/settle that never comes and the request dies at the 30 s timeout:
`{"error":"screenshot: context deadline exceeded"}`.

This was **not** a network, route, IPv6, TLS, cert, or code problem. Proven on the host:

| Test (run on the bridge host `192.168.1.245`)                          | Result          |
| ---------------------------------------------------------------------- | --------------- |
| `curl https://192.168.1.250:5173/`                                     | HTTP 200, 15 ms |
| `chrome.exe --dump-dom https://192.168.1.250:5173/.../graph`           | loads, ~750 ms  |
| identical `bridge.exe` in an **interactive session**, `ignore_https=on`| PNG, ~500 ms    |
| same, repeated 7x on a persistent profile w/ service worker registered | all OK          |
| the **Session-0 service** Chrome (PIDs all `SessionId 0`)              | hangs to 30 s   |

The only difference between "works" and "hangs" is the **session**: the service and all
its Chrome processes run in Session 0; the working instance runs in interactive Session 1.

## The fix

Run the bridge in the logged-on user's **interactive session** via a logon Scheduled Task
instead of a Session-0 service. The caller side (SSH tunnel, token, `http://...:51234`)
is unchanged.

### Step 1 - Install the interactive logon task (no admin)

In a normal PowerShell on the GPU host:

```powershell
cd C:\Users\Aaron Meese\Documents\gpu-browser-bridge
.\windows\install.ps1
```

`install.ps1` builds/places `bridge.exe` under `%LocalAppData%\gpu-browser-bridge`,
generates the token there, registers a per-user logon task (`Run only when user is logged
on`, **non-elevated** - the configuration proven to work; running Chrome elevated from a
task breaks the CDP handshake), starts it, and waits up to ~30s for `healthz`. `bridge.exe`
is built with the GUI subsystem (no console window) and the task runs it directly; it
writes its own log to `%LocalAppData%\gpu-browser-bridge\bridge.log`, and Chrome runs in new
headless mode (no window at all, but still real GPU), so nothing is visible on the desktop
and there is no window to accidentally close. On success it prints the
session the bridge is running in - it must be **>= 1**, not 0. On failure it prints the tail
of the log so you can see the actual error.

**Migrating from an older admin/service install** (a Session-0 service or an admin-created
task)? A non-elevated process cannot remove an admin-created task, so `install.ps1` will
stop with a migration hint. Clear the old install once in an **elevated** PowerShell, then
re-run `install.ps1` normally:

```powershell
.\windows\uninstall.ps1        # elevated: removes the old admin task/service
.\windows\install.ps1          # normal: per-user, no admin
```

### Step 2 - Confirm the session (admin or normal PowerShell)

```powershell
Get-Process bridge | Select-Object Id, SessionId
# SessionId must be 1 (or higher). If it shows 0, the task ran in Session 0 - see Troubleshooting.
Invoke-WebRequest http://127.0.0.1:51234/healthz -UseBasicParsing | Select-Object -Expand Content
```

### Step 3 - Verify end-to-end from the caller (cyiq)

With the SSH tunnel up (`ssh -N -L 51234:127.0.0.1:51234 "aaron meese"@192.168.1.245`):

```bash
gpu-browser screenshot "https://192.168.1.250:5173/" --out /tmp/root.png --ignore-https --settle 3000
gpu-browser screenshot "https://192.168.1.250:5173/projects/ccf43040-5018-4a1a-9460-3cc026fb6f1e/graph" \
  --out /tmp/labels_check.png --ignore-https --settle 9000 --viewport 1600x900
```

Both should now return PNGs instead of `context deadline exceeded`.

### Step 4 (optional but recommended) - Survive reboots

A logon task only runs once someone is logged in. For an unattended GPU host, enable
auto-logon so a desktop session exists after every reboot.

**Recommended - Sysinternals Autologon** (stores the password as an encrypted LSA secret,
not plaintext):

```powershell
# Download once from https://learn.microsoft.com/sysinternals/downloads/autologon
# then either run the GUI, or non-interactively:
.\Autologon.exe "aaron meese" $env:COMPUTERNAME "<password>" /accepteula
```

**Fallback - registry auto-logon** (WARNING: writes the password in cleartext to the
registry; only acceptable on a locked-down host you control):

```powershell
$wl = "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon"
Set-ItemProperty $wl AutoAdminLogon "1"
Set-ItemProperty $wl DefaultUserName "aaron meese"
Set-ItemProperty $wl DefaultDomainName $env:COMPUTERNAME
Set-ItemProperty $wl DefaultPassword "<password>"
```

After enabling auto-logon, reboot and re-run Step 2 to confirm the bridge comes back in
Session >= 1 on its own.

## Rolling back

```powershell
cd C:\Users\Aaron Meese\Documents\gpu-browser-bridge
.\windows\uninstall.ps1          # removes the task + binary (and any legacy service); keeps token + profile + logs
.\windows\uninstall.ps1 -Purge   # also deletes token, Chrome profile, and logs
```

## Troubleshooting

- **`Get-Process bridge` shows `SessionId 0`**: the task ran in Session 0. Make sure the
  task's principal is `LogonType Interactive` (the script sets this) and that you started
  it while logged on, not via "run whether user is logged on or not".
- **healthz never comes up**: check Task Scheduler -> `gpu-browser-bridge` -> Last Run
  Result, and `%LocalAppData%\gpu-browser-bridge\bridge.log`.
- **`install.ps1` errors that the task can't be removed unelevated**: a legacy
  admin/service install is present. Run `uninstall.ps1` once elevated (see Step 1
  migration), then re-run `install.ps1` normally.
- **Chrome window opens but bridge never reports healthy (no log output)**: this was the
  symptom of running the task elevated (`RunLevel Highest`). The task runs non-elevated
  (`RunLevel Limited`); make sure it shows `Run only when user is logged on` and is **not**
  set to highest privileges.
