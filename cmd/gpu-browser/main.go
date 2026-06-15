// gpu-browser — caller-side CLI. Talks to bridge.exe over HTTP.
//
// Usage:
//
//	gpu-browser healthz
//	gpu-browser screenshot URL [--out FILE] [--full] [--script JS | --script-file PATH] [--click X,Y] [--wait-for SELECTOR] [--viewport WxH] [--ignore-https] [--header "K: V"] [--cookie name=value] [--local-storage k=v]
//	gpu-browser eval URL SCRIPT [--click X,Y] [--wait-for SELECTOR] [--ignore-https] [--header "K: V"] [--cookie name=value] [--local-storage k=v]
//
// Configuration is via environment variables:
//
//	BRIDGE_URL    e.g. http://localhost:51234
//	BRIDGE_TOKEN  bearer token printed by install.ps1
//
// or ~/.config/gpu-browser/config (KEY=value, one per line).
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type clientConfig struct {
	URL   string
	Token string
}

func loadClientConfig() (*clientConfig, error) {
	c := &clientConfig{
		URL:   os.Getenv("BRIDGE_URL"),
		Token: os.Getenv("BRIDGE_TOKEN"),
	}
	if c.URL != "" && c.Token != "" {
		return c, nil
	}

	home, err := os.UserHomeDir()
	if err == nil {
		path := filepath.Join(home, ".config", "gpu-browser", "config")
		if b, err := os.ReadFile(path); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				k, v, ok := strings.Cut(line, "=")
				if !ok {
					continue
				}
				k, v = strings.TrimSpace(k), strings.TrimSpace(v)
				switch k {
				case "BRIDGE_URL":
					if c.URL == "" {
						c.URL = v
					}
				case "BRIDGE_TOKEN":
					if c.Token == "" {
						c.Token = v
					}
				}
			}
		}
	}

	if c.URL == "" {
		c.URL = "http://localhost:51234"
	}
	if c.Token == "" {
		return nil, fmt.Errorf("no BRIDGE_TOKEN set (env or ~/.config/gpu-browser/config)")
	}
	return c, nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "healthz":
		runHealthz()
	case "screenshot":
		runScreenshot(args)
	case "eval":
		runEval(args)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `gpu-browser - drive a remote GPU-backed Chrome via the bridge service

commands:
  healthz                         report bridge status
  screenshot URL [flags]          capture a screenshot of URL
  eval URL SCRIPT [flags]         run JS in URL, return result

screenshot flags:
  --out FILE                      write PNG here (default: stdout)
  --full                          full-page screenshot
  --script JS                     run JS on the page after load, before capture
  --script-file PATH              read the pre-capture JS from a file (vs --script)
  --click X,Y                     dispatch a real pointer pick at viewport coords
  --wait-for SELECTOR             wait for CSS selector before capture
  --viewport WxH                  e.g. 1440x900
  --ignore-https                  accept invalid certs
  --settle MS                     extra wait after load (ms)
  --header "Key: Value"           add an HTTP header, e.g. Authorization (repeatable)
  --cookie "name=value"           set a cookie for the target URL (repeatable)
  --local-storage "key=value"     seed localStorage for the target origin (repeatable)

eval flags:
  --wait-for SELECTOR             wait for CSS selector before script
  --click X,Y                     dispatch a real pointer pick before the script
  --ignore-https                  accept invalid certs
  --settle MS                     extra wait after load (ms)
  --header "Key: Value"           add an HTTP header, e.g. Authorization (repeatable)
  --cookie "name=value"           set a cookie for the target URL (repeatable)
  --local-storage "key=value"     seed localStorage for the target origin (repeatable)

env:
  BRIDGE_URL                      default http://localhost:51234
  BRIDGE_TOKEN                    required`)
}

func runHealthz() {
	cfg, err := loadClientConfig()
	if err != nil {
		fatal("%v", err)
	}
	resp, err := http.Get(cfg.URL + "/healthz")
	if err != nil {
		fatal("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(os.Stdout, resp.Body)
	if resp.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}

// repeatedFlag collects a string flag that may be given multiple times.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ", ") }
func (r *repeatedFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// sessionFlags registers the session-injection flags shared by screenshot/eval.
func sessionFlags(fs *flag.FlagSet) (headers, cookies, localStorage *repeatedFlag) {
	headers, cookies, localStorage = &repeatedFlag{}, &repeatedFlag{}, &repeatedFlag{}
	fs.Var(headers, "header", "")
	fs.Var(cookies, "cookie", "")
	fs.Var(localStorage, "local-storage", "")
	return
}

// applySession folds --header/--cookie/--local-storage into the request body.
// Cookies default their URL to targetURL so Chrome infers domain/path/secure.
func applySession(body map[string]any, headers, cookies, localStorage *repeatedFlag, targetURL string) {
	if len(*headers) > 0 {
		h := map[string]string{}
		for _, kv := range *headers {
			k, v, ok := strings.Cut(kv, ":")
			if !ok {
				fatal("--header must be 'Key: Value' (got %q)", kv)
			}
			h[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		body["headers"] = h
	}
	if len(*cookies) > 0 {
		cs := make([]map[string]any, 0, len(*cookies))
		for _, kv := range *cookies {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				fatal("--cookie must be 'name=value' (got %q)", kv)
			}
			cs = append(cs, map[string]any{"name": strings.TrimSpace(k), "value": v, "url": targetURL})
		}
		body["cookies"] = cs
	}
	if len(*localStorage) > 0 {
		m := map[string]string{}
		for _, kv := range *localStorage {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				fatal("--local-storage must be 'key=value' (got %q)", kv)
			}
			m[strings.TrimSpace(k)] = v
		}
		body["local_storage"] = m
	}
}

// readScript resolves the screenshot pre-script from --script (inline) or
// --script-file (path). The file form avoids shell-quoting pain for multi-line
// scripts. The two are mutually exclusive; supplying neither yields "".
func readScript(inline, file string) string {
	if inline != "" && file != "" {
		fatal("--script and --script-file are mutually exclusive")
	}
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			fatal("read --script-file %s: %v", file, err)
		}
		return string(b)
	}
	return inline
}

// parseClick parses a "X,Y" pair into viewport CSS coordinates for a real
// pointer pick. Accepts integers or decimals.
func parseClick(s string) (x, y float64, err error) {
	xs, ys, ok := strings.Cut(s, ",")
	if !ok {
		return 0, 0, fmt.Errorf("click must be X,Y (got %q)", s)
	}
	if x, err = strconv.ParseFloat(strings.TrimSpace(xs), 64); err != nil {
		return 0, 0, fmt.Errorf("click X: %w", err)
	}
	if y, err = strconv.ParseFloat(strings.TrimSpace(ys), 64); err != nil {
		return 0, 0, fmt.Errorf("click Y: %w", err)
	}
	return x, y, nil
}

// parseInterspersed parses flags that may appear before, after, or
// between positional arguments. Go's flag package stops at the first
// non-flag token, so a command like `screenshot URL --ignore-https`
// would otherwise silently drop every flag after the URL. This permutes
// the arguments so flag position no longer matters, then returns the
// collected positional arguments.
func parseInterspersed(fs *flag.FlagSet, args []string) []string {
	var positional []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			fatal("parse flags: %v", err)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	return positional
}

func runScreenshot(args []string) {
	fs := flag.NewFlagSet("screenshot", flag.ExitOnError)
	out := fs.String("out", "", "")
	full := fs.Bool("full", false, "")
	waitFor := fs.String("wait-for", "", "")
	viewport := fs.String("viewport", "", "")
	ignoreHTTPS := fs.Bool("ignore-https", false, "")
	settle := fs.Int("settle", 0, "")
	script := fs.String("script", "", "")
	scriptFile := fs.String("script-file", "", "")
	click := fs.String("click", "", "")
	headers, cookies, localStorage := sessionFlags(fs)
	pos := parseInterspersed(fs, args)
	if len(pos) < 1 {
		fatal("screenshot requires a URL")
	}

	body := map[string]any{
		"url":                 pos[0],
		"full_page":           *full,
		"ignore_https_errors": *ignoreHTTPS,
	}
	if s := readScript(*script, *scriptFile); s != "" {
		body["script"] = s
	}
	if *click != "" {
		x, y, err := parseClick(*click)
		if err != nil {
			fatal("%v", err)
		}
		body["click"] = map[string]float64{"x": x, "y": y}
	}
	if *waitFor != "" {
		body["wait_for"] = *waitFor
	}
	if *settle > 0 {
		body["settle_ms"] = *settle
	}
	if *viewport != "" {
		w, h, err := parseViewport(*viewport)
		if err != nil {
			fatal("%v", err)
		}
		body["viewport_w"] = w
		body["viewport_h"] = h
	}
	applySession(body, headers, cookies, localStorage, pos[0])

	var result struct {
		PNG            string          `json:"png_b64"`
		ScriptResult   json.RawMessage `json:"script_result"`
		Console        json.RawMessage `json:"console"`
		FailedRequests json.RawMessage `json:"failed_requests"`
	}
	if err := postJSON("/screenshot", body, &result); err != nil {
		fatal("%v", err)
	}
	png, err := base64.StdEncoding.DecodeString(result.PNG)
	if err != nil {
		fatal("decode png: %v", err)
	}
	if *out == "" {
		_, _ = os.Stdout.Write(png)
	} else {
		if err := os.WriteFile(*out, png, 0o644); err != nil {
			fatal("write %s: %v", *out, err)
		}
		fmt.Fprintf(os.Stderr, "wrote %d bytes to %s\n", len(png), *out)
	}
	// PNG is the stdout payload; the script's return value and diagnostics go to
	// stderr so stdout stays a clean image stream.
	if len(result.ScriptResult) > 0 && string(result.ScriptResult) != "null" {
		fmt.Fprintf(os.Stderr, "script_result: %s\n", result.ScriptResult)
	}
	fmt.Fprintf(os.Stderr, "console: %s\nfailed: %s\n", result.Console, result.FailedRequests)
}

func runEval(args []string) {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	waitFor := fs.String("wait-for", "", "")
	ignoreHTTPS := fs.Bool("ignore-https", false, "")
	settle := fs.Int("settle", 0, "")
	click := fs.String("click", "", "")
	headers, cookies, localStorage := sessionFlags(fs)
	pos := parseInterspersed(fs, args)
	if len(pos) < 2 {
		fatal("eval requires a URL and a JS script")
	}

	body := map[string]any{
		"url":                 pos[0],
		"script":              pos[1],
		"ignore_https_errors": *ignoreHTTPS,
	}
	if *waitFor != "" {
		body["wait_for"] = *waitFor
	}
	if *click != "" {
		x, y, err := parseClick(*click)
		if err != nil {
			fatal("%v", err)
		}
		body["click"] = map[string]float64{"x": x, "y": y}
	}
	if *settle > 0 {
		body["settle_ms"] = *settle
	}
	applySession(body, headers, cookies, localStorage, pos[0])

	var result json.RawMessage
	if err := postJSON("/eval", body, &result); err != nil {
		fatal("%v", err)
	}
	_, _ = os.Stdout.Write(result)
	fmt.Println()
}

func postJSON(path string, body any, out any) error {
	cfg, err := loadClientConfig()
	if err != nil {
		return err
	}
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, cfg.URL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST %s: %s: %s", path, resp.Status, errBody)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func parseViewport(s string) (int, int, error) {
	w, h, ok := strings.Cut(s, "x")
	if !ok {
		return 0, 0, fmt.Errorf("viewport must be WxH (got %q)", s)
	}
	wi, err := strconv.Atoi(w)
	if err != nil {
		return 0, 0, fmt.Errorf("viewport width: %w", err)
	}
	hi, err := strconv.Atoi(h)
	if err != nil {
		return 0, 0, fmt.Errorf("viewport height: %w", err)
	}
	return wi, hi, nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
