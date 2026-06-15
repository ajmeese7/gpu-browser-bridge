# Capturing pages behind a login

`/screenshot` and `/eval` are stateless per request, but the bridge drives a **single long-lived Chrome with a persistent profile**, so cookies and storage set during one request carry over to later ones. That gives two ways to capture pages that require an authenticated session.

## Option 1 - inject session material (stateless)

If you already have the session as a cookie, bearer token, or storage value, pass it on the request and the bridge applies it before navigating - no prior login needed.

CLI:

```bash
gpu-browser screenshot "<app-url>/dashboard" \
  --header "Authorization: Bearer <token>" \
  --cookie "session=<value>" \
  --local-storage "auth=<value>"
```

HTTP body (same fields on `/screenshot` and `/eval`):

```json
{
  "url": "<app-url>/dashboard",
  "headers": { "Authorization": "Bearer <token>" },
  "cookies": [ { "name": "session", "value": "<value>", "url": "<app-url>" } ],
  "local_storage": { "auth": "<value>" }
}
```

`cookies` accepts `{name, value, url?|domain?, path?, secure?, http_only?, same_site?}`; giving `url` lets Chrome infer domain/path/secure. Headers are sent with every request; `local_storage` is seeded into the target origin before its scripts run. This suits token/cookie auth, but not interactive login flows you must click through.

## Option 2 - drive a form login via /eval (session persists in the profile)

For apps whose login is a JS form that sets an **httpOnly** cookie (which you can neither read nor inject), log in once with `/eval`. The cookie Chrome receives persists in the bridge's profile, so the **next** `/screenshot` is already authenticated.

Step 1 - submit the login form. Frameworks like React ignore a plain `input.value =` assignment, so set the value through the native setter and dispatch an `input` event:

```bash
gpu-browser eval "<app-url>/login" "$(cat <<'JS'
(async () => {
  const set = (el, v) => {
    const d = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
    d.call(el, v);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  };
  set(document.querySelector('#username'), '<user>');
  set(document.querySelector('#password'), '<password>');
  document.querySelector('button[type=submit]').click();
  await new Promise(r => setTimeout(r, 5000));
  return location.pathname; // expect to have left /login on success
})()
JS
)" --ignore-https
```

Step 2 - capture the authenticated page (same bridge, same profile, so the cookie is already there):

```bash
gpu-browser screenshot "<app-url>/dashboard" --ignore-https --settle 9000 --out out.png
```

Use a dedicated test account and pass credentials at call time - never commit them.

## Capturing a view that only exists after an interaction

Some views live only in client memory - an expanded graph node, an applied filter, a selection - so a fresh navigation always shows the pre-interaction state. `/screenshot` takes an optional `script` that runs against the live page after navigation (with the tab foregrounded, so `requestAnimationFrame` is active) and before `wait_for`/`settle_ms`/capture. The script performs the interaction in-page, then the same tab is screenshotted.

Combine it with stateless session injection so the capture is reproducible and needs no prior login. Inline the script with `--script`, or read it from a file with `--script-file` to avoid shell-quoting a multi-line script:

```bash
gpu-browser screenshot "<app-url>/projects/<id>/graph" \
  --ignore-https \
  --cookie "session=<value>" \
  --script "window.dispatchEvent(new CustomEvent('graph:expand-subnet', { detail: { cidr: '<cidr>', siteId: '<id>' } }))" \
  --settle 5000 \
  --out after.png
```

```bash
# multi-line script from a file
gpu-browser screenshot "<app-url>/projects/<id>/graph" \
  --ignore-https --cookie "session=<value>" \
  --script-file expand.js --settle 5000 --out after.png
```

The script may be `async`; its Promise is awaited, so it can `await` the app's readiness (or the interaction's own settle) before returning. Its return value is discarded - the screenshot is the result. To confirm the feature is working, diff `after.png` against a no-`--script` baseline of the same URL: they should differ.

HTTP body:

```json
{
  "url": "<app-url>/projects/<id>/graph",
  "ignore_https_errors": true,
  "cookies": [ { "name": "session", "value": "<value>", "url": "<app-url>" } ],
  "script": "window.dispatchEvent(new CustomEvent('graph:expand-subnet', { detail: { cidr: '<cidr>', siteId: '<id>' } }))",
  "settle_ms": 5000
}
```

`/eval` also foregrounds its tab, so a `script` there can drive `requestAnimationFrame` work and read back the result - use `/eval` when you want the JSON value, `/screenshot --script` when you want the image.

## Notes

- The bridge brings each per-request tab to the foreground before capturing, so apps that paint via `requestAnimationFrame` (React, Babylon, ...) render rather than hanging.
- The session lives in the bridge's persistent Chrome profile. To drop it, clear the profile (see [windows/README.md](../windows/README.md)) or clear the origin's site data via `/eval`.
- `--ignore-https` / `ignore_https_errors` accepts self-signed certs, e.g. a local HTTPS dev server.
