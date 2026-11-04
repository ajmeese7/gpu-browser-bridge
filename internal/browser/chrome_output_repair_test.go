package browser

import (
	"sync"
	"testing"
)

func TestChromeOutputConcurrentCapture(t *testing.T) {
	var output chromeOutputBuffer
	var writers sync.WaitGroup
	for i := 0; i < 4; i++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for j := 0; j < 100; j++ {
				if _, err := output.Write([]byte("x")); err != nil {
					t.Error(err)
				}
				_ = output.String()
			}
		}()
	}
	writers.Wait()
	if got := len(output.String()); got != 400 {
		t.Fatalf("captured %d bytes, want 400", got)
	}
}
