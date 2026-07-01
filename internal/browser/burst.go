package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png" // register PNG decoder for image.Decode
	"math"
	"sort"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/security"
	"github.com/chromedp/chromedp"
)

// Burst capture defaults and analysis thresholds.
const (
	// pixelTol is the per-channel 8-bit delta below which two pixels count as
	// unchanged. Real GPU rendering is not bit-exact frame to frame (dithering,
	// sub-pixel AA), so a small tolerance keeps a static scene reading as 0%.
	pixelTol = 8
	// defaultFlickerThresholdPct is the max adjacent-frame changed-pixel
	// percentage above which a scene expected to be static is flagged as
	// flickering.
	defaultFlickerThresholdPct = 2.0
	// oscillationMinGaps is the minimum number of gaps between change events
	// (so >= oscillationMinGaps+1 events, i.e. several cycles) before the diff
	// series is called periodic.
	oscillationMinGaps = 3
	// oscillationMaxGapCV is the largest coefficient of variation the event gaps
	// may have and still count as regular; above it the crossings look like noise.
	oscillationMaxGapCV = 0.5
	// captureTimeoutBuffer is added on top of the capture span + settle when
	// deriving the request timeout, covering navigation, script, and encoding.
	captureTimeoutBuffer = 30 * time.Second
)

// Region is an optional crop applied to each captured frame before diffing, in
// CSS pixels. Restricting the diff to (say) just the 3D canvas keeps unrelated
// chrome repaints from swamping the flicker signal.
type Region struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// FrameDiff is the changed-pixel percentage between two adjacent frames.
type FrameDiff struct {
	From       int     `json:"from"`
	To         int     `json:"to"`
	ChangedPct float64 `json:"changed_pct"`
}

// Oscillation reports whether the diff series is periodic and, if so, its
// dominant period.
type Oscillation struct {
	Detected       bool `json:"detected"`
	ApproxPeriodMS int  `json:"approx_period_ms,omitempty"`
}

// BurstRequest is the JSON body for /burst.
type BurstRequest struct {
	URL string `json:"url"`
	// Script, if set, runs and is awaited on the foregrounded tab before the
	// first frame, so the agent can seed state or drive an interaction first.
	Script string `json:"script,omitempty"`
	// Click, if set, is a real pointer pick dispatched after Script and before
	// capture.
	Click   *ClickPoint `json:"click,omitempty"`
	WaitFor string      `json:"wait_for,omitempty"`
	// Frames is how many frames to capture (>= 2). IntervalMS is the wait
	// between captures.
	Frames     int `json:"frames"`
	IntervalMS int `json:"interval_ms"`
	// Region, if set, crops each frame before diffing.
	Region *Region `json:"region,omitempty"`
	// ReturnFrames includes the captured PNGs in the response (base64). The
	// diff is computed server-side regardless; frames are only shipped back when
	// the caller wants to write them to disk.
	ReturnFrames bool `json:"return_frames,omitempty"`
	// FlickerThresholdPct overrides defaultFlickerThresholdPct when > 0.
	FlickerThresholdPct float64 `json:"flicker_threshold_pct,omitempty"`
	ViewportW           int     `json:"viewport_w,omitempty"`
	ViewportH           int     `json:"viewport_h,omitempty"`
	TimeoutMS           int     `json:"timeout_ms,omitempty"`
	IgnoreHTTPS         bool    `json:"ignore_https_errors,omitempty"`
	SettleMillis        int     `json:"settle_ms,omitempty"`
	SessionContext
}

// BurstResult is the /burst response. FramePNGs is populated only when
// ReturnFrames was set; the analysis fields are always present.
type BurstResult struct {
	Frames int `json:"frames"`
	// IntervalMS is the requested sleep between captures. FrameIntervalMS is the
	// measured median wall-clock gap between frames, which includes the capture
	// cost itself (Page.captureScreenshot can take ~100ms on a real GPU) and is
	// what the oscillation period is derived from.
	IntervalMS      int         `json:"interval_ms"`
	FrameIntervalMS int         `json:"frame_interval_ms"`
	Region          []int       `json:"region,omitempty"` // [x,y,w,h] actually diffed
	Diffs           []FrameDiff `json:"diffs"`
	MaxChangedPct   float64     `json:"max_changed_pct"`
	MeanChangedPct  float64     `json:"mean_changed_pct"`
	Flicker         bool        `json:"flicker"`
	Oscillation     Oscillation `json:"oscillation"`
	FramePNGs       [][]byte    `json:"frame_pngs,omitempty"` // base64 per frame
	// ScriptResult is the optional Script's return value, mirroring the
	// screenshot path so one call can yield both analysis and assertion data.
	ScriptResult   json.RawMessage `json:"script_result,omitempty"`
	Console        []ConsoleEntry  `json:"console"`
	FailedRequests []FailedRequest `json:"failed_requests"`
}

// Burst captures N composited frames interval ms apart and returns a per-frame
// pixel-diff analysis. It reuses the screenshot path's capture primitive
// (Page.captureScreenshot, which captures real WebGPU pixels), so no screencast
// plumbing is needed and frame timing is precise.
func (b *Browser) Burst(ctx context.Context, req BurstRequest) (*BurstResult, error) {
	if req.URL == "" {
		return nil, errors.New("url is required")
	}
	if req.Frames < 2 {
		return nil, errors.New("frames must be >= 2")
	}
	if req.IntervalMS < 0 {
		return nil, errors.New("interval_ms must be >= 0")
	}

	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	captureSpan := time.Duration(req.Frames*req.IntervalMS) * time.Millisecond
	settle := time.Duration(req.SettleMillis) * time.Millisecond
	if needed := captureSpan + settle + captureTimeoutBuffer; timeout < needed {
		timeout = needed
	}

	tabCtx, cancelTab, err := b.newTab(ctx)
	if err != nil {
		return nil, err
	}
	defer cancelTab()

	runCtx, cancelRun := context.WithTimeout(tabCtx, timeout)
	defer cancelRun()

	console, failed := attachListeners(runCtx)

	frames := make([][]byte, req.Frames)
	times := make([]time.Time, req.Frames)
	var scriptResult json.RawMessage

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
	// Foreground the per-request tab: headless Chrome pauses rAF and does not
	// composite background tabs, so captureScreenshot would hang or return a
	// frozen frame. Same fix the screenshot/eval paths rely on.
	actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
		return page.BringToFront().Do(ctx)
	}))
	if req.Script != "" {
		actions = append(actions, chromedp.Evaluate(req.Script, &scriptResult, evalAwait))
	}
	if req.Click != nil {
		actions = append(actions, chromedp.MouseClickXY(req.Click.X, req.Click.Y))
	}
	if req.WaitFor != "" {
		actions = append(actions, chromedp.WaitVisible(req.WaitFor))
	}
	if req.SettleMillis > 0 {
		actions = append(actions, chromedp.Sleep(settle))
	}
	// Capture loop: one frame, then sleep interval, repeated. Sleeping between
	// (not before) captures means the first frame is grabbed right after settle.
	for i := range req.Frames {
		idx := i
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			buf, err := page.CaptureScreenshot().Do(ctx)
			if err != nil {
				return err
			}
			frames[idx] = buf
			times[idx] = time.Now()
			return nil
		}))
		if i < req.Frames-1 && req.IntervalMS > 0 {
			actions = append(actions, chromedp.Sleep(time.Duration(req.IntervalMS)*time.Millisecond))
		}
	}

	if err := chromedp.Run(runCtx, actions...); err != nil {
		return nil, fmt.Errorf("burst: %w", err)
	}

	result, err := analyzeFrames(frames, req, medianIntervalMS(times))
	if err != nil {
		return nil, fmt.Errorf("burst: %w", err)
	}
	result.ScriptResult = scriptResult
	result.Console = console.snapshot()
	result.FailedRequests = failed.snapshot()
	return result, nil
}

// analyzeFrames decodes the captured PNG frames, computes adjacent-frame diffs
// over the optional crop, and derives the flicker/oscillation summary.
// frameIntervalMS is the measured median gap between frames, used for the
// oscillation period (nominal req.IntervalMS undercounts by the capture cost).
func analyzeFrames(frames [][]byte, req BurstRequest, frameIntervalMS int) (*BurstResult, error) {
	imgs := make([]image.Image, len(frames))
	for i, raw := range frames {
		if len(raw) == 0 {
			return nil, fmt.Errorf("frame %d is empty", i)
		}
		img, _, err := image.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("decode frame %d: %w", i, err)
		}
		imgs[i] = img
	}

	// Diff rectangle: the requested crop clamped to the frame bounds, or the
	// full frame when no region was given.
	rect := imgs[0].Bounds()
	var regionOut []int
	if req.Region != nil {
		crop := image.Rect(req.Region.X, req.Region.Y, req.Region.X+req.Region.W, req.Region.Y+req.Region.H)
		rect = rect.Intersect(crop)
		if rect.Empty() {
			return nil, errors.New("region does not overlap the frame")
		}
		regionOut = []int{rect.Min.X, rect.Min.Y, rect.Dx(), rect.Dy()}
	}

	diffs := make([]FrameDiff, 0, len(imgs)-1)
	series := make([]float64, 0, len(imgs)-1)
	var sum, max float64
	for i := 1; i < len(imgs); i++ {
		pct := changedPct(imgs[i-1], imgs[i], rect, pixelTol)
		diffs = append(diffs, FrameDiff{From: i - 1, To: i, ChangedPct: round1(pct)})
		series = append(series, pct)
		sum += pct
		if pct > max {
			max = pct
		}
	}
	mean := sum / float64(len(series))

	threshold := req.FlickerThresholdPct
	if threshold <= 0 {
		threshold = defaultFlickerThresholdPct
	}

	res := &BurstResult{
		Frames:          len(imgs),
		IntervalMS:      req.IntervalMS,
		FrameIntervalMS: frameIntervalMS,
		Region:          regionOut,
		Diffs:           diffs,
		MaxChangedPct:   round1(max),
		MeanChangedPct:  round1(mean),
		Flicker:         max >= threshold,
		Oscillation:     detectOscillation(series, frameIntervalMS),
	}
	if req.ReturnFrames {
		res.FramePNGs = frames
	}
	return res, nil
}

// medianIntervalMS is the median wall-clock gap between consecutive frame
// captures. The median (not mean) shrugs off the odd slow capture (GC pause,
// scheduler hiccup) that would skew the period estimate.
func medianIntervalMS(times []time.Time) int {
	if len(times) < 2 {
		return 0
	}
	deltas := make([]float64, 0, len(times)-1)
	for i := 1; i < len(times); i++ {
		deltas = append(deltas, float64(times[i].Sub(times[i-1]).Milliseconds()))
	}
	sort.Float64s(deltas)
	return int(deltas[len(deltas)/2] + 0.5)
}

// changedPct returns the percentage of pixels within rect that differ between a
// and b by more than tol on any 8-bit channel.
func changedPct(a, b image.Image, rect image.Rectangle, tol int) float64 {
	total := rect.Dx() * rect.Dy()
	if total == 0 {
		return 0
	}
	changed := 0
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			ar, ag, ab, _ := a.At(x, y).RGBA()
			br, bg, bb, _ := b.At(x, y).RGBA()
			// RGBA() returns 16-bit channels; >>8 brings them to 8-bit.
			if abs8(ar, br) > tol || abs8(ag, bg) > tol || abs8(ab, bb) > tol {
				changed++
			}
		}
	}
	return float64(changed) / float64(total) * 100
}

// abs8 is the absolute difference of two 16-bit channel values, in 8-bit units.
func abs8(a, b uint32) int {
	d := int(a>>8) - int(b>>8)
	if d < 0 {
		return -d
	}
	return d
}

// detectOscillation looks for a periodic redraw loop in the diff series by
// timing the "change events" (upward crossings of the series mean) and checking
// that the gaps between them are regular. This beats autocorrelation on a sparse
// spike train, which locks onto harmonics and overstates the period.
//
// A one-shot settle (a single change then flat) has too few events; a steady
// per-frame animation never dips below its mean, so it produces no crossings and
// is correctly not called an oscillation (it is just motion, which `flicker`
// already captures).
func detectOscillation(series []float64, intervalMS int) Oscillation {
	var mean float64
	for _, v := range series {
		mean += v
	}
	mean /= float64(len(series))

	// Rising crossings of the mean mark the onset of each change event.
	var events []int
	for i := 1; i < len(series); i++ {
		if series[i-1] < mean && series[i] >= mean {
			events = append(events, i)
		}
	}

	gaps := make([]int, 0, len(events))
	for i := 1; i < len(events); i++ {
		gaps = append(gaps, events[i]-events[i-1])
	}
	if len(gaps) < oscillationMinGaps {
		return Oscillation{}
	}

	// Regular gaps => a real period. A high spread means the crossings are noise,
	// not a loop.
	var gsum float64
	for _, g := range gaps {
		gsum += float64(g)
	}
	gmean := gsum / float64(len(gaps))
	if gmean <= 0 {
		return Oscillation{}
	}
	var gvar float64
	for _, g := range gaps {
		d := float64(g) - gmean
		gvar += d * d
	}
	if cv := math.Sqrt(gvar/float64(len(gaps))) / gmean; cv > oscillationMaxGapCV {
		return Oscillation{}
	}

	sorted := append([]int(nil), gaps...)
	sort.Ints(sorted)
	medianGap := sorted[len(sorted)/2]
	return Oscillation{Detected: true, ApproxPeriodMS: medianGap * intervalMS}
}

// round1 rounds to one decimal place so the JSON diffs stay readable.
func round1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}
