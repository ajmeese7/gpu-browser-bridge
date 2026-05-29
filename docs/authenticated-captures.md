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

## Notes

- The bridge brings each per-request tab to the foreground before capturing, so apps that paint via `requestAnimationFrame` (React, Babylon, ...) render rather than hanging.
- The session lives in the bridge's persistent Chrome profile. To drop it, clear the profile (see [windows/README.md](../windows/README.md)) or clear the origin's site data via `/eval`.
- `--ignore-https` / `ignore_https_errors` accepts self-signed certs, e.g. a local HTTPS dev server.
