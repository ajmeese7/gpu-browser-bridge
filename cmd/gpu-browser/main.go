// gpu-browser — caller-side CLI. Talks to bridge.exe over HTTP.
//
// Usage:
//
//	gpu-browser healthz
//	gpu-browser screenshot URL [--out FILE] [--full] [--wait-for SELECTOR] [--viewport WxH] [--ignore-https]
//	gpu-browser eval URL SCRIPT [--wait-for SELECTOR] [--ignore-https]
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
  --wait-for SELECTOR             wait for CSS selector before capture
  --viewport WxH                  e.g. 1440x900
  --ignore-https                  accept invalid certs
  --settle MS                     extra wait after load (ms)

eval flags:
  --wait-for SELECTOR             wait for CSS selector before script
  --ignore-https                  accept invalid certs
  --settle MS                     extra wait after load (ms)

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

func runScreenshot(args []string) {
	fs := flag.NewFlagSet("screenshot", flag.ExitOnError)
	out := fs.String("out", "", "")
	full := fs.Bool("full", false, "")
	waitFor := fs.String("wait-for", "", "")
	viewport := fs.String("viewport", "", "")
	ignoreHTTPS := fs.Bool("ignore-https", false, "")
	settle := fs.Int("settle", 0, "")
	if err := fs.Parse(args); err != nil {
		fatal("parse flags: %v", err)
	}
	if fs.NArg() < 1 {
		fatal("screenshot requires a URL")
	}

	body := map[string]any{
		"url":                 fs.Arg(0),
		"full_page":           *full,
		"ignore_https_errors": *ignoreHTTPS,
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

	var result struct {
		PNG            string          `json:"png_b64"`
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
	fmt.Fprintf(os.Stderr, "console: %s\nfailed: %s\n", result.Console, result.FailedRequests)
}

func runEval(args []string) {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	waitFor := fs.String("wait-for", "", "")
	ignoreHTTPS := fs.Bool("ignore-https", false, "")
	settle := fs.Int("settle", 0, "")
	if err := fs.Parse(args); err != nil {
		fatal("parse flags: %v", err)
	}
	if fs.NArg() < 2 {
		fatal("eval requires a URL and a JS script")
	}

	body := map[string]any{
		"url":                 fs.Arg(0),
		"script":              fs.Arg(1),
		"ignore_https_errors": *ignoreHTTPS,
	}
	if *waitFor != "" {
		body["wait_for"] = *waitFor
	}
	if *settle > 0 {
		body["settle_ms"] = *settle
	}

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
