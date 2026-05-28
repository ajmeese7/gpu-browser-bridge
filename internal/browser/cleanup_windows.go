package browser

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// killStaleChrome terminates any Chrome process still bound to userDataDir.
//
// chromedp launches Chrome as a child of bridge.exe. If the service is
// hard-killed (NSSM stop timing out, a crash, a reboot), that child can be
// orphaned while still holding the profile's singleton. The next launch then
// fails with "Opening in existing browser session" because the new Chrome
// just hands off to the orphan and exits. Clearing the orphan before we
// launch makes restarts reliable.
//
// The filter matches the bridge's exact user-data-dir, so it never touches a
// human's personal Chrome (which uses a different profile directory).
func killStaleChrome(userDataDir string, log *slog.Logger) {
	if userDataDir == "" {
		return
	}
	// Single-quote everything: embedded double quotes don't survive Go's
	// Windows argument escaping into powershell.exe -Command cleanly.
	pattern := strings.ReplaceAll(userDataDir, "'", "''")
	script := fmt.Sprintf(
		`$p = Get-CimInstance Win32_Process | `+
			`Where-Object { $_.Name -eq 'chrome.exe' -and $_.CommandLine -like '*%s*' }; `+
			`$p | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }; `+
			`($p | Measure-Object).Count`,
		pattern,
	)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.Output()
	if err != nil {
		log.Warn("stale-chrome cleanup failed", "err", err)
		return
	}
	if killed := strings.TrimSpace(string(out)); killed != "" && killed != "0" {
		log.Info("killed stale chrome holding the bridge profile", "count", killed)
		// Give Windows a moment to release the profile singleton before relaunch.
		time.Sleep(500 * time.Millisecond)
	}
}
