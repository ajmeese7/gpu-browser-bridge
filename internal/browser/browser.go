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
	"sync"
	"time"

	"github.com/ajmeese7/gpu-browser-bridge/internal/config"
	"github.com/chromedp/cdproto/network"
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

// ScreenshotRequest is the JSON body for /screenshot.
type ScreenshotRequest struct {
	URL          string `json:"url"`
	WaitFor      string `json:"wait_for,omitempty"` // CSS selector to wait for
	FullPage     bool   `json:"full_page,omitempty"`
	ViewportW    int    `json:"viewport_w,omitempty"`
	ViewportH    int    `json:"viewport_h,omitempty"`
	TimeoutMS    int    `json:"timeout_ms,omitempty"`
	IgnoreHTTPS  bool   `json:"ignore_https_errors,omitempty"`
	SettleMillis int    `json:"settle_ms,omitempty"` // extra wait after load
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
	actions = append(actions, network.Enable(), chromedp.Navigate(req.URL))
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
	URL          string `json:"url"`
	Script       string `json:"script"`             // JS expression; last expression value is returned
	WaitFor      string `json:"wait_for,omitempty"` // CSS selector before running script
	TimeoutMS    int    `json:"timeout_ms,omitempty"`
	IgnoreHTTPS  bool   `json:"ignore_https_errors,omitempty"`
	SettleMillis int    `json:"settle_ms,omitempty"`
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
	actions = append(actions, chromedp.Navigate(req.URL))
	if req.WaitFor != "" {
		actions = append(actions, chromedp.WaitVisible(req.WaitFor))
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
