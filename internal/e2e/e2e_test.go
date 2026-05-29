//go:build e2e

// Package e2e drives a real Chrome through the bridge end-to-end. It is gated
// behind the `e2e` build tag (so `go test ./...` and CI's unit job skip it) and
// needs a Chrome install. Run it on the GPU host for the full suite:
//
//	go test -tags e2e ./internal/e2e
//
// It launches Chrome via the actual browser supervisor, serves the bridge's own
// HTTP handler, and drives it over HTTP against controlled local pages (no
// internet), asserting on screenshots, eval, the WebGPU adapter, and session
// injection (cookies / headers / localStorage). The GPU-adapter check skips
// gracefully when no hardware adapter is present (e.g. headless CI).
package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ajmeese7/gpu-browser-bridge/internal/browser"
	"github.com/ajmeese7/gpu-browser-bridge/internal/config"
	"github.com/ajmeese7/gpu-browser-bridge/internal/server"
)

const token = "e2e-token-0123456789abcdef0123456789ab" // >= 32 chars

// findChrome locates a Chrome/Chromium binary on the current platform.
func findChrome(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("BRIDGE_CHROME_PATH"); p != "" {
		return p
	}
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		candidates = []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		}
	default:
		for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
			if p, err := exec.LookPath(name); err == nil {
				candidates = append(candidates, p)
			}
		}
		candidates = append(candidates, "/usr/bin/google-chrome", "/usr/bin/chromium")
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("no Chrome/Chromium found; skipping e2e")
	return ""
}

// startBridge launches Chrome via the real supervisor and serves the bridge's
// HTTP handler, returning its base URL.
func startBridge(t *testing.T) string {
	t.Helper()
	cfg := &config.Config{
		BindAddr:    "127.0.0.1:51234", // unused: we serve via httptest
		Token:       token,
		ChromePath:  findChrome(t),
		UserDataDir: t.TempDir(),
		LogPath:     filepath.Join(t.TempDir(), "e2e.log"),
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := browser.New(cfg, log)
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("start browser: %v", err)
	}
	t.Cleanup(b.Shutdown)

	ts := httptest.NewServer(server.New(cfg, b, log).Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}

// appServer serves the controlled pages the tests navigate to.
func appServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><html><body><h1>ok</h1></body></html>`)
	})
	mux.HandleFunc("/echo-header", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, r.Header.Get("X-Test"))
	})
	mux.HandleFunc("/color", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><html><body style="margin:0">`+
			`<div style="width:100vw;height:100vh;background:rgb(0,128,255)"></div></body></html>`)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// post sends an authenticated POST and returns the decoded JSON object.
func post(t *testing.T, base, path string, body any) map[string]json.RawMessage {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(buf))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s -> %d: %s", path, resp.StatusCode, raw)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v (%s)", path, err, raw)
	}
	return out
}

// evalString runs script on url and returns the string result.
func evalString(t *testing.T, bridge, url, script string, extra map[string]any) string {
	t.Helper()
	body := map[string]any{"url": url, "script": script, "timeout_ms": 30000}
	for k, v := range extra {
		body[k] = v
	}
	res := post(t, bridge, "/eval", body)
	var s string
	if err := json.Unmarshal(res["result"], &s); err != nil {
		t.Fatalf("result not a string: %s", res["result"])
	}
	return s
}

func TestE2E(t *testing.T) {
	bridge := startBridge(t)
	app := appServer(t)

	t.Run("healthz", func(t *testing.T) {
		resp, err := http.Get(bridge + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var h struct {
			OK          bool `json:"ok"`
			ChromeAlive bool `json:"chrome_alive"`
		}
		json.NewDecoder(resp.Body).Decode(&h)
		if !h.OK || !h.ChromeAlive {
			t.Fatalf("unhealthy: %+v", h)
		}
	})

	t.Run("eval_basic", func(t *testing.T) {
		res := post(t, bridge, "/eval", map[string]any{"url": app.URL + "/", "script": "6*7", "timeout_ms": 30000})
		if got := strings.TrimSpace(string(res["result"])); got != "42" {
			t.Fatalf("6*7 = %s, want 42", got)
		}
	})

	t.Run("screenshot_renders_pixels", func(t *testing.T) {
		res := post(t, bridge, "/screenshot", map[string]any{
			"url": app.URL + "/color", "viewport_w": 160, "viewport_h": 160, "settle_ms": 300, "timeout_ms": 30000,
		})
		var b64 string
		json.Unmarshal(res["png_b64"], &b64)
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(raw) == 0 {
			t.Fatalf("decode png: %v (len %d)", err, len(raw))
		}
		img, _, err := image.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("not a valid image: %v", err)
		}
		b := img.Bounds()
		r, g, bl, _ := img.At(b.Dx()/2, b.Dy()/2).RGBA()
		r8, g8, b8 := r>>8, g>>8, bl>>8
		// expect roughly rgb(0,128,255): blue-dominant, low red
		if b8 < 200 || r8 > 60 || g8 < 90 || g8 > 170 {
			t.Fatalf("center pixel rgb(%d,%d,%d) not ~rgb(0,128,255)", r8, g8, b8)
		}
	})

	t.Run("inject_header", func(t *testing.T) {
		got := evalString(t, bridge, app.URL+"/echo-header", "document.body.innerText", map[string]any{
			"headers": map[string]string{"X-Test": "hdr-value"},
		})
		if strings.TrimSpace(got) != "hdr-value" {
			t.Fatalf("echoed header = %q, want hdr-value", got)
		}
	})

	t.Run("inject_cookie", func(t *testing.T) {
		got := evalString(t, bridge, app.URL+"/", "document.cookie", map[string]any{
			"cookies": []map[string]any{{"name": "ck", "value": "cookieval", "url": app.URL}},
		})
		if !strings.Contains(got, "ck=cookieval") {
			t.Fatalf("document.cookie = %q, want it to contain ck=cookieval", got)
		}
	})

	t.Run("inject_localstorage", func(t *testing.T) {
		got := evalString(t, bridge, app.URL+"/", "localStorage.getItem('lsk')", map[string]any{
			"local_storage": map[string]string{"lsk": "lsv"},
		})
		if got != "lsv" {
			t.Fatalf("localStorage.getItem = %q, want lsv", got)
		}
	})

	t.Run("webgpu_adapter", func(t *testing.T) {
		// localhost is a secure context, so navigator.gpu is available.
		script := `(async()=>{if(!navigator.gpu)return 'no-gpu-api';` +
			`const a=await navigator.gpu.requestAdapter();if(!a)return 'no-adapter';` +
			`return (a.info&&a.info.vendor)||'adapter';})()`
		got := evalString(t, bridge, app.URL+"/", script, nil)
		if got == "no-gpu-api" || got == "no-adapter" {
			t.Skipf("no WebGPU adapter available (%s) - expected without a GPU desktop", got)
		}
		t.Logf("WebGPU adapter: %s", got)
	})
}
