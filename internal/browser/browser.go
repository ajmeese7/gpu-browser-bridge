// Package browser owns the supervised Chrome process and exposes
// per-request operations (Screenshot, Eval) that run in a fresh tab.
//
// One Chrome runs for the lifetime of the bridge service. Each public
// operation opens a new tab (chromedp context) inside that Chrome,
// performs its work, and closes the tab. If Chrome dies, the supervisor
// relaunches it on the next operation.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ajmeese7/gpu-browser-bridge/internal/config"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/security"
	"github.com/chromedp/chromedp"
)

// Browser supervises a Chrome process and serves per-request operations.
type Browser struct {
	cfg *config.Config
	log *slog.Logger

	mu            sync.Mutex
	allocCtx      context.Context
	allocCancel   context.CancelFunc
	browserCtx    context.Context // long-lived: holds the "anchor" tab that keeps Chrome alive
	browserCancel context.CancelFunc
}

func New(cfg *config.Config, log *slog.Logger) *Browser {
	return &Browser{cfg: cfg, log: log}
}

// Start launches Chrome and waits until it accepts CDP commands.
// Returns nil once the browser is ready to serve requests. Blocks up to
// 30s; the caller-provided context bounds nothing during launch on
// purpose (Start is called once at boot and the launch is atomic).
func (b *Browser) Start(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.launchLocked()
}

func (b *Browser) launchLocked() error {
	// Clear any Chrome orphaned by a prior hard-kill/crash that still holds
	// this profile's singleton; otherwise our launch hands off to it and
	// exits with "Opening in existing browser session".
	killStaleChrome(b.cfg.UserDataDir, b.log)

	// NOTE: we do NOT extend chromedp.DefaultExecAllocatorOptions because it
	// includes DisableGPU (fatal for WebGPU) and OLD headless. We set NEW
	// headless (--headless=new) explicitly below, which keeps the real GPU.
	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(b.cfg.ChromePath),
		chromedp.UserDataDir(b.cfg.UserDataDir),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.NoSandbox, // service account can't easily use sandbox
		chromedp.Flag("enable-unsafe-webgpu", true),
		chromedp.Flag("enable-features", "Vulkan,WebGPU"),
		chromedp.Flag("disable-features", "Translate,OptimizationHints"),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-popup-blocking", true),
		chromedp.Flag("disable-prompt-on-repost", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-sync", true),
		// New headless mode: the full browser with NO window at all, so nothing
		// shows on the desktop or taskbar and a user cannot accidentally close
		// it. Unlike OLD headless it keeps the real GPU - verified
		// navigator.gpu.requestAdapter() returns the AMD RDNA-2 adapter and a
		// WebGPU sample renders to a non-black screenshot. We still run this via
		// an interactive logon session (see windows/install.ps1) - the
		// configuration proven to deliver the real GPU.
		chromedp.Flag("headless", "new"),
	}

	// Use a background-anchored context so the browser outlives any per-request
	// context that triggered Start.
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)

	// The first chromedp.NewContext on the allocator launches Chrome AND
	// opens its initial tab. We keep this tab alive for the lifetime of the
	// service — closing it would close Chrome's last window and kill the
	// process. Per-request tabs are NewContext children of this one.
	browserCtx, browserCancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(func(s string, args ...any) {
		b.log.Debug("chromedp", "msg", fmt.Sprintf(s, args...))
	}))

	// Force-launch Chrome by running a trivial action on the anchor tab.
	// IMPORTANT: do not wrap browserCtx in a derived timeout context here —
	// chromedp ties the tab's lifetime to whichever context the first Run
	// uses, and cancelling that context closes the anchor tab, killing
	// Chrome when no other tabs remain. We instead use a goroutine to
	// enforce a launch timeout without touching the chromedp context.
	launchDone := make(chan error, 1)
	go func() {
		launchDone <- chromedp.Run(browserCtx, chromedp.Navigate("about:blank"))
	}()
	select {
	case err := <-launchDone:
		if err != nil {
			browserCancel()
			allocCancel()
			return fmt.Errorf("start chrome: %w", err)
		}
	case <-time.After(30 * time.Second):
		browserCancel()
		allocCancel()
		return fmt.Errorf("start chrome: timed out after 30s")
	}

	b.allocCtx = allocCtx
	b.allocCancel = allocCancel
	b.browserCtx = browserCtx
	b.browserCancel = browserCancel
	b.log.Info("chrome ready", "exec", b.cfg.ChromePath, "profile", b.cfg.UserDataDir)
	return nil
}

// Shutdown kills Chrome and releases resources.
func (b *Browser) Shutdown() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.browserCancel != nil {
		b.browserCancel()
		b.browserCancel = nil
	}
	if b.allocCancel != nil {
		b.allocCancel()
		b.allocCancel = nil
	}
}

// Healthy reports whether Chrome is currently up. Checks the browser
// context — the allocator can outlive the actual Chrome process, so
// checking allocCtx alone gives false positives.
func (b *Browser) Healthy() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.allocCtx == nil || b.allocCtx.Err() != nil {
		return false
	}
	if b.browserCtx == nil || b.browserCtx.Err() != nil {
		return false
	}
	return true
}

// newTab opens a fresh tab inside the supervised browser. The returned
// cancel must be called by the caller to close the tab.
func (b *Browser) newTab(_ context.Context) (context.Context, context.CancelFunc, error) {
	b.mu.Lock()
	if b.allocCtx == nil || b.allocCtx.Err() != nil ||
		b.browserCtx == nil || b.browserCtx.Err() != nil {
		// Chrome is gone (process exited/crashed, or its last window/anchor tab
		// was closed). The allocator can outlive the Chrome process, so we must
		// check browserCtx too, not just allocCtx - otherwise we hand out a
		// canceled context and every request fails with "context canceled".
		// Tear down the stale contexts before relaunching to avoid leaking them.
		if b.browserCancel != nil {
			b.browserCancel()
			b.browserCancel = nil
		}
		if b.allocCancel != nil {
			b.allocCancel()
			b.allocCancel = nil
		}
		if err := b.launchLocked(); err != nil {
			b.mu.Unlock()
			return nil, nil, err
		}
	}
	parent := b.browserCtx
	b.mu.Unlock()

	tabCtx, cancel := chromedp.NewContext(parent)
	return tabCtx, cancel, nil
}

// ConsoleEntry mirrors a JS console.log/warn/error/info call.
type ConsoleEntry struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// FailedRequest mirrors a >=400 response or a network failure.
type FailedRequest struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
	Error  string `json:"error,omitempty"`
}

// SessionContext is optional per-request session material applied to the tab
// before navigation, so the bridge can screenshot or eval pages that need an
// authenticated session. All fields are optional. It is embedded in both
// request types, so its fields appear at the top level of the JSON body.
type SessionContext struct {
	// Cookies are set via CDP Network.setCookies before navigation.
	Cookies []Cookie `json:"cookies,omitempty"`
	// Headers are added to every request the page makes (e.g. Authorization).
	Headers map[string]string `json:"headers,omitempty"`
	// LocalStorage entries are seeded into the page's origin before its own
	// scripts run.
	LocalStorage map[string]string `json:"local_storage,omitempty"`
}

// ClickPoint is an optional real pointer pick: a mousePressed + mouseReleased
// pair dispatched at the given viewport CSS coordinates (Input.dispatchMouseEvent
// via chromedp.MouseClickXY). Unlike a synthetic click event built in JS, this
// drives the page's actual pointer path - hit-testing, capture, drag thresholds
// - which is what canvas pickers (Babylon, WebGPU) react to. It is applied on
// the foregrounded tab after any script and before the result is observed
// (screenshot capture or eval return).
type ClickPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Cookie mirrors the subset of CDP CookieParam we expose. Provide either URL
// (Chrome infers domain/path/secure from it) or an explicit Domain.
type Cookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	URL      string `json:"url,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Path     string `json:"path,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
	HTTPOnly bool   `json:"http_only,omitempty"`
	SameSite string `json:"same_site,omitempty"` // Strict | Lax | None
}

// preNavigateActions returns the CDP actions that apply this session material.
// They MUST run after network.Enable() and before chromedp.Navigate.
func (s SessionContext) preNavigateActions() []chromedp.Action {
	var acts []chromedp.Action
	if len(s.Headers) > 0 {
		acts = append(acts, network.SetExtraHTTPHeaders(toHeaders(s.Headers)))
	}
	if params := toCookieParams(s.Cookies); len(params) > 0 {
		acts = append(acts, network.SetCookies(params))
	}
	if len(s.LocalStorage) > 0 {
		script := localStorageScript(s.LocalStorage)
		acts = append(acts, chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(script).Do(ctx)
			return err
		}))
	}
	return acts
}

func toHeaders(m map[string]string) network.Headers {
	h := network.Headers{}
	for k, v := range m {
		h[k] = v
	}
	return h
}

func toCookieParams(cs []Cookie) []*network.CookieParam {
	if len(cs) == 0 {
		return nil
	}
	params := make([]*network.CookieParam, 0, len(cs))
	for _, c := range cs {
		p := &network.CookieParam{
			Name:     c.Name,
			Value:    c.Value,
			URL:      c.URL,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HTTPOnly: c.HTTPOnly,
		}
		if c.SameSite != "" {
			p.SameSite = network.CookieSameSite(c.SameSite)
		}
		params = append(params, p)
	}
	return params
}

// localStorageScript builds JS that seeds localStorage entries. It runs via
// AddScriptToEvaluateOnNewDocument, i.e. in the target origin before the page's
// own scripts, so values are present when the app boots.
func localStorageScript(items map[string]string) string {
	var b strings.Builder
	b.WriteString("try{")
	for k, v := range items {
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(v)
		b.WriteString("localStorage.setItem(")
		b.Write(kb)
		b.WriteString(",")
		b.Write(vb)
		b.WriteString(");")
	}
	b.WriteString("}catch(e){}")
	return b.String()
}

// ScreenshotRequest is the JSON body for /screenshot.
type ScreenshotRequest struct {
	URL string `json:"url"`
	// Script, if set, is JS run against the live page after navigation (and
	// after the tab is foregrounded, so requestAnimationFrame is active) but
	// before WaitFor/Settle/capture. It may be async; its Promise is awaited.
	// This is how an interaction-driven view ("expand a node, then look") is
	// captured: the script performs the interaction in-page, then the same
	// live tab is screenshotted. The script's return value is discarded.
	Script string `json:"script,omitempty"`
	// Click, if set, is a real pointer pick dispatched after Script and before
	// WaitFor/Settle/capture.
	Click        *ClickPoint `json:"click,omitempty"`
	WaitFor      string      `json:"wait_for,omitempty"` // CSS selector to wait for
	FullPage     bool        `json:"full_page,omitempty"`
	ViewportW    int         `json:"viewport_w,omitempty"`
	ViewportH    int         `json:"viewport_h,omitempty"`
	TimeoutMS    int         `json:"timeout_ms,omitempty"`
	IgnoreHTTPS  bool        `json:"ignore_https_errors,omitempty"`
	SettleMillis int         `json:"settle_ms,omitempty"` // extra wait after load
	SessionContext
}

type ScreenshotResult struct {
	PNG            []byte          `json:"png_b64"` // marshaled as base64 by encoding/json
	Console        []ConsoleEntry  `json:"console"`
	FailedRequests []FailedRequest `json:"failed_requests"`
}

func (b *Browser) Screenshot(ctx context.Context, req ScreenshotRequest) (*ScreenshotResult, error) {
	if req.URL == "" {
		return nil, errors.New("url is required")
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	tabCtx, cancelTab, err := b.newTab(ctx)
	if err != nil {
		return nil, err
	}
	defer cancelTab()

	runCtx, cancelRun := context.WithTimeout(tabCtx, timeout)
	defer cancelRun()

	console, failed := attachListeners(runCtx)

	var png []byte
	actions := []chromedp.Action{}
	if req.ViewportW > 0 && req.ViewportH > 0 {
		actions = append(actions, chromedp.EmulateViewport(int64(req.ViewportW), int64(req.ViewportH)))
	}
	if req.IgnoreHTTPS {
		actions = append(actions, security.SetIgnoreCertificateErrors(true))
	}
	actions = append(actions, network.Enable())
	actions = append(actions, req.preNavigateActions()...)
	actions = append(actions, chromedp.Navigate(req.URL))
	// Bring this per-request tab to the foreground before it settles/captures.
	// The anchor tab otherwise holds the foreground, and headless Chrome does
	// not composite background tabs (requestAnimationFrame is paused there), so
	// Page.captureScreenshot hangs forever on pages that only paint once
	// foregrounded - e.g. apps that render via rAF (React, Babylon, ...).
	actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
		return page.BringToFront().Do(ctx)
	}))
	// Optional in-page interaction, run on the foregrounded tab so its rAF is
	// live, before WaitFor/Settle so those observe the post-interaction state.
	// The return value is discarded; console/network listeners still capture
	// anything the script logs or fetches.
	if req.Script != "" {
		var discard json.RawMessage
		actions = append(actions, chromedp.Evaluate(req.Script, &discard, evalAwait))
	}
	// Optional real pointer pick, after the script set up state and before
	// WaitFor/Settle so those observe the post-click result.
	if req.Click != nil {
		actions = append(actions, chromedp.MouseClickXY(req.Click.X, req.Click.Y))
	}
	if req.WaitFor != "" {
		actions = append(actions, chromedp.WaitVisible(req.WaitFor))
	}
	if req.SettleMillis > 0 {
		actions = append(actions, chromedp.Sleep(time.Duration(req.SettleMillis)*time.Millisecond))
	}
	if req.FullPage {
		actions = append(actions, chromedp.FullScreenshot(&png, 90))
	} else {
		actions = append(actions, chromedp.CaptureScreenshot(&png))
	}

	if err := chromedp.Run(runCtx, actions...); err != nil {
		return nil, fmt.Errorf("screenshot: %w", err)
	}

	return &ScreenshotResult{
		PNG:            png,
		Console:        console.snapshot(),
		FailedRequests: failed.snapshot(),
	}, nil
}

// EvalRequest is the JSON body for /eval.
type EvalRequest struct {
	URL     string `json:"url"`
	Script  string `json:"script"`             // JS expression; last expression value is returned
	WaitFor string `json:"wait_for,omitempty"` // CSS selector before running script
	// Click, if set, is a real pointer pick dispatched after WaitFor and before
	// Settle/Script, so Script can read the post-click state.
	Click        *ClickPoint `json:"click,omitempty"`
	TimeoutMS    int         `json:"timeout_ms,omitempty"`
	IgnoreHTTPS  bool        `json:"ignore_https_errors,omitempty"`
	SettleMillis int         `json:"settle_ms,omitempty"`
	SessionContext
}

type EvalResult struct {
	Result         json.RawMessage `json:"result"`
	Console        []ConsoleEntry  `json:"console"`
	FailedRequests []FailedRequest `json:"failed_requests"`
}

func (b *Browser) Eval(ctx context.Context, req EvalRequest) (*EvalResult, error) {
	if req.URL == "" || req.Script == "" {
		return nil, errors.New("url and script are required")
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	tabCtx, cancelTab, err := b.newTab(ctx)
	if err != nil {
		return nil, err
	}
	defer cancelTab()

	runCtx, cancelRun := context.WithTimeout(tabCtx, timeout)
	defer cancelRun()

	console, failed := attachListeners(runCtx)

	var raw json.RawMessage
	actions := []chromedp.Action{network.Enable()}
	if req.IgnoreHTTPS {
		actions = append(actions, security.SetIgnoreCertificateErrors(true))
	}
	actions = append(actions, req.preNavigateActions()...)
	actions = append(actions, chromedp.Navigate(req.URL))
	// Foreground the per-request tab so its requestAnimationFrame runs: headless
	// Chrome pauses rAF in background tabs (the anchor tab otherwise holds the
	// foreground), which would make rAF-driven scripts observe ~1 frame over the
	// whole timeout. Mirrors the screenshot path's bringToFront.
	actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
		return page.BringToFront().Do(ctx)
	}))
	if req.WaitFor != "" {
		actions = append(actions, chromedp.WaitVisible(req.WaitFor))
	}
	// Optional real pointer pick before Settle/Script, so the script observes
	// the post-click state.
	if req.Click != nil {
		actions = append(actions, chromedp.MouseClickXY(req.Click.X, req.Click.Y))
	}
	if req.SettleMillis > 0 {
		actions = append(actions, chromedp.Sleep(time.Duration(req.SettleMillis)*time.Millisecond))
	}
	actions = append(actions, chromedp.Evaluate(req.Script, &raw, evalAwait))

	if err := chromedp.Run(runCtx, actions...); err != nil {
		return nil, fmt.Errorf("eval: %w", err)
	}

	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	return &EvalResult{
		Result:         raw,
		Console:        console.snapshot(),
		FailedRequests: failed.snapshot(),
	}, nil
}

// evalAwait makes chromedp's Evaluate await Promises before resolving.
func evalAwait(p *runtime.EvaluateParams) *runtime.EvaluateParams {
	return p.WithAwaitPromise(true).WithReturnByValue(true)
}
