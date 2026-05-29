// bridge.exe — the GPU-host Windows process. Wraps Chrome + an HTTP API.
//
// Usage:
//
//	bridge                run in console mode (foreground; for dev)
//	bridge service        run as a Windows service (set by NSSM/SCM)
//	bridge gen-token PATH generate a fresh token at PATH and print it
//
// All other configuration is via environment variables, see internal/config.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/ajmeese7/gpu-browser-bridge/internal/browser"
	"github.com/ajmeese7/gpu-browser-bridge/internal/config"
	"github.com/ajmeese7/gpu-browser-bridge/internal/server"
)

func main() {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "gen-token":
			if len(os.Args) < 3 {
				fatal("gen-token requires a path argument")
			}
			tok, err := config.GenerateToken(os.Args[2])
			if err != nil {
				fatal("generate token: %v", err)
			}
			fmt.Println(tok)
			return
		case "service":
			runService()
			return
		}
	}
	runConsole()
}

func runConsole() {
	cfg, cfgErr := config.Load()

	// Console mode logs to a file so the interactive deployment still records
	// output: bridge.exe is built with the GUI subsystem (no console window)
	// and launched directly by a Scheduled Task, so there is no console and no
	// redirection. We tee to stderr too, which is harmless for a foreground
	// dev run (and a no-op when stderr is a dead handle under the GUI subsystem).
	logPath := fallbackLogPath()
	if cfg != nil && cfg.LogPath != "" {
		logPath = cfg.LogPath
	}
	log := slog.New(slog.NewTextHandler(logWriter(logPath), &slog.HandlerOptions{Level: slog.LevelInfo}))

	if cfgErr != nil {
		log.Error("bridge exited with error", "err", fmt.Errorf("load config: %w", cfgErr))
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runWithConfig(ctx, log, cfg); err != nil {
		log.Error("bridge exited with error", "err", err)
		os.Exit(1)
	}
}

// logWriter opens path (truncating to bound growth across launches) and tees
// to stderr. The file is listed first so it always receives the bytes even
// when stderr is a dead handle. Falls back to stderr alone if the file can't
// be opened.
func logWriter(path string) io.Writer {
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
				return io.MultiWriter(f, os.Stderr)
			}
		}
	}
	return os.Stderr
}

// fallbackLogPath mirrors config's default, used only when config.Load fails
// before we have a Config (so we can still record why it failed).
func fallbackLogPath() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "gpu-browser-bridge", "bridge.log")
}

func runWithConfig(ctx context.Context, log *slog.Logger, cfg *config.Config) error {
	b := browser.New(cfg, log)
	if err := b.Start(ctx); err != nil {
		return fmt.Errorf("start browser: %w", err)
	}
	defer b.Shutdown()

	srv := server.New(cfg, b, log)
	return srv.ListenAndServe(ctx)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
