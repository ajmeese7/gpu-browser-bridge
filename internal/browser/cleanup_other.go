//go:build !windows

package browser

import "log/slog"

// killStaleChrome is a no-op off Windows; the service only runs on Windows.
func killStaleChrome(_ string, _ *slog.Logger) {}
