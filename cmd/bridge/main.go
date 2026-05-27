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
	"log/slog"
	"os"
	"os/signal"
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
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, log); err != nil {
		log.Error("bridge exited with error", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	return runWithConfig(ctx, log, cfg)
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
