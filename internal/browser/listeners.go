package browser

import (
	"context"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// consoleSink collects console.* calls observed during a request.
type consoleSink struct {
	mu      sync.Mutex
	entries []ConsoleEntry
}

func (s *consoleSink) push(e ConsoleEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
}

func (s *consoleSink) snapshot() []ConsoleEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ConsoleEntry, len(s.entries))
	copy(out, s.entries)
	return out
}

// failureSink collects HTTP >=400 responses and network failures.
type failureSink struct {
	mu      sync.Mutex
	entries []FailedRequest
}

func (s *failureSink) push(e FailedRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
}

func (s *failureSink) snapshot() []FailedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]FailedRequest, len(s.entries))
	copy(out, s.entries)
	return out
}

// attachListeners wires CDP event listeners on the given tab context.
// Listeners stop automatically when the context is cancelled.
func attachListeners(ctx context.Context) (*consoleSink, *failureSink) {
	console := &consoleSink{}
	failed := &failureSink{}

	chromedp.ListenTarget(ctx, func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			console.push(ConsoleEntry{
				Type: string(e.Type),
				Text: joinArgs(e.Args),
			})
		case *runtime.EventExceptionThrown:
			text := e.ExceptionDetails.Text
			if e.ExceptionDetails.Exception != nil && e.ExceptionDetails.Exception.Description != "" {
				text = e.ExceptionDetails.Exception.Description
			}
			console.push(ConsoleEntry{Type: "pageerror", Text: text})
		case *network.EventResponseReceived:
			if e.Response != nil && e.Response.Status >= 400 {
				failed.push(FailedRequest{
					URL:    e.Response.URL,
					Status: int(e.Response.Status),
				})
			}
		case *network.EventLoadingFailed:
			failed.push(FailedRequest{Error: e.ErrorText})
		}
	})

	return console, failed
}

func joinArgs(args []*runtime.RemoteObject) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		switch {
		case a.Value != nil:
			parts = append(parts, string(a.Value))
		case a.Description != "":
			parts = append(parts, a.Description)
		default:
			parts = append(parts, string(a.Type))
		}
	}
	return strings.Join(parts, " ")
}
