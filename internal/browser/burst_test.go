package browser

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// solid returns a w x h image filled with c.
func solid(w, h int, c color.Color) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	return img
}

// pngBytes encodes img to PNG, mirroring what captureScreenshot returns.
func pngBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

var (
	black = color.NRGBA{0, 0, 0, 255}
	white = color.NRGBA{255, 255, 255, 255}
)

func TestChangedPct(t *testing.T) {
	a := solid(16, 16, black)
	full := a.Bounds()

	if pct := changedPct(a, a, full, pixelTol); pct != 0 {
		t.Errorf("identical images: changedPct = %v, want 0", pct)
	}
	if pct := changedPct(a, solid(16, 16, white), full, pixelTol); pct != 100 {
		t.Errorf("opposite images: changedPct = %v, want 100", pct)
	}

	// A single differing corner pixel is 1/256 = ~0.39% over the full frame.
	corner := image.NewNRGBA(full)
	for y := range 16 {
		for x := range 16 {
			corner.Set(x, y, black)
		}
	}
	corner.Set(0, 0, white)
	if pct := changedPct(a, corner, full, pixelTol); pct <= 0 || pct > 1 {
		t.Errorf("one changed pixel: changedPct = %v, want ~0.39", pct)
	}
	// A region that excludes the changed corner sees no change.
	if pct := changedPct(a, corner, image.Rect(8, 8, 16, 16), pixelTol); pct != 0 {
		t.Errorf("region excluding change: changedPct = %v, want 0", pct)
	}
}

func TestChangedPctTolerance(t *testing.T) {
	// A per-channel delta at or below pixelTol counts as unchanged (absorbs
	// non-bit-exact GPU rendering).
	a := solid(8, 8, color.NRGBA{100, 100, 100, 255})
	b := solid(8, 8, color.NRGBA{100 + pixelTol, 100, 100, 255})
	if pct := changedPct(a, b, a.Bounds(), pixelTol); pct != 0 {
		t.Errorf("within-tolerance delta: changedPct = %v, want 0", pct)
	}
	c := solid(8, 8, color.NRGBA{100 + pixelTol + 1, 100, 100, 255})
	if pct := changedPct(a, c, a.Bounds(), pixelTol); pct != 100 {
		t.Errorf("over-tolerance delta: changedPct = %v, want 100", pct)
	}
}

func TestDetectOscillation(t *testing.T) {
	interval := 50

	// Perfectly alternating series => period of 2 samples.
	alt := []float64{30, 0, 30, 0, 30, 0, 30, 0, 30, 0, 30, 0}
	if o := detectOscillation(alt, interval); !o.Detected || o.ApproxPeriodMS != 100 {
		t.Errorf("alternating: %+v, want detected period 100", o)
	}

	// Flat series has zero variance => not periodic.
	flat := []float64{5, 5, 5, 5, 5, 5, 5, 5}
	if o := detectOscillation(flat, interval); o.Detected {
		t.Errorf("flat: %+v, want not detected", o)
	}

	// A one-shot settle (single spike then flat) is not periodic.
	settle := []float64{40, 0, 0, 0, 0, 0, 0, 0}
	if o := detectOscillation(settle, interval); o.Detected {
		t.Errorf("settle: %+v, want not detected", o)
	}

	// Too few samples to judge periodicity.
	if o := detectOscillation([]float64{10, 0, 10}, interval); o.Detected {
		t.Errorf("short series: %+v, want not detected", o)
	}
}

func TestAnalyzeFramesFlicker(t *testing.T) {
	// Alternating black/white frames: every adjacent diff is 100% changed, so
	// flicker trips. The diff series is constant (100), so it is a steady change,
	// not a detectable oscillation.
	frames := [][]byte{
		pngBytes(t, solid(16, 16, black)),
		pngBytes(t, solid(16, 16, white)),
		pngBytes(t, solid(16, 16, black)),
		pngBytes(t, solid(16, 16, white)),
	}
	res, err := analyzeFrames(frames, BurstRequest{Frames: 4, IntervalMS: 50}, 50)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Flicker {
		t.Errorf("alternating frames: Flicker = false, want true")
	}
	if res.MaxChangedPct != 100 {
		t.Errorf("MaxChangedPct = %v, want 100", res.MaxChangedPct)
	}
	if len(res.Diffs) != 3 {
		t.Errorf("got %d diffs, want 3", len(res.Diffs))
	}
}

func TestAnalyzeFramesStatic(t *testing.T) {
	// Identical frames: no flicker, no oscillation.
	frame := pngBytes(t, solid(16, 16, black))
	frames := [][]byte{frame, frame, frame, frame, frame, frame}
	res, err := analyzeFrames(frames, BurstRequest{Frames: 6, IntervalMS: 50}, 50)
	if err != nil {
		t.Fatal(err)
	}
	if res.Flicker {
		t.Errorf("static frames: Flicker = true, want false")
	}
	if res.Oscillation.Detected {
		t.Errorf("static frames: oscillation detected, want none")
	}
	if res.MaxChangedPct != 0 || res.MeanChangedPct != 0 {
		t.Errorf("static frames: max=%v mean=%v, want 0/0", res.MaxChangedPct, res.MeanChangedPct)
	}
}

func TestAnalyzeFramesOscillation(t *testing.T) {
	// Two frames of one color then two of the other, repeating. Adjacent diffs
	// alternate 0,100,0,100,... which autocorrelates to a period of 2 samples.
	b := pngBytes(t, solid(16, 16, black))
	w := pngBytes(t, solid(16, 16, white))
	frames := [][]byte{b, b, w, w, b, b, w, w, b, b, w, w}
	res, err := analyzeFrames(frames, BurstRequest{Frames: len(frames), IntervalMS: 60}, 60)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Oscillation.Detected {
		t.Fatalf("expected oscillation, got %+v", res.Oscillation)
	}
	if res.Oscillation.ApproxPeriodMS != 120 {
		t.Errorf("ApproxPeriodMS = %d, want 120", res.Oscillation.ApproxPeriodMS)
	}
}

func TestAnalyzeFramesRegionClamp(t *testing.T) {
	// A region is echoed clamped to the frame bounds.
	frames := [][]byte{
		pngBytes(t, solid(20, 20, black)),
		pngBytes(t, solid(20, 20, black)),
	}
	res, err := analyzeFrames(frames, BurstRequest{
		Frames: 2, IntervalMS: 50,
		Region: &Region{X: 5, Y: 5, W: 100, H: 100}, // overflows 20x20
	}, 50)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{5, 5, 15, 15}
	if len(res.Region) != 4 {
		t.Fatalf("region = %v, want %v", res.Region, want)
	}
	for i := range want {
		if res.Region[i] != want[i] {
			t.Fatalf("region = %v, want %v", res.Region, want)
		}
	}
}

func TestAnalyzeFramesEmptyFrame(t *testing.T) {
	frames := [][]byte{pngBytes(t, solid(8, 8, black)), nil}
	if _, err := analyzeFrames(frames, BurstRequest{Frames: 2, IntervalMS: 50}, 50); err == nil {
		t.Error("expected error for empty frame, got nil")
	}
}
