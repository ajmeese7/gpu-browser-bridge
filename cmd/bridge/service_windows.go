//go:build windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/ajmeese7/gpu-browser-bridge/internal/config"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
)

const serviceName = "gpu-browser-bridge"

// runService is the entry point when bridge.exe is invoked by the Windows
// Service Control Manager (or by NSSM acting as one).
func runService() {
	// Open Application event log; ignore failure (registry may not be set up).
	elog, err := eventlog.Open(serviceName)
	if err != nil {
		// Fall back to a file logger next to the binary.
		exe, _ := os.Executable()
		fallback := filepath.Join(filepath.Dir(exe), "bridge-service.log")
		f, ferr := os.OpenFile(fallback, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if ferr == nil {
			defer f.Close()
			log := slog.New(slog.NewJSONHandler(f, nil))
			svc.Run(serviceName, &serviceHandler{log: log})
			return
		}
		fmt.Fprintln(os.Stderr, "service start failed: no event log and no fallback log")
		os.Exit(1)
	}
	defer elog.Close()

	log := slog.New(slog.NewJSONHandler(&eventlogWriter{el: elog}, nil))
	if err := svc.Run(serviceName, &serviceHandler{log: log}); err != nil {
		_ = elog.Error(1, fmt.Sprintf("service run failed: %v", err))
		os.Exit(1)
	}
}

type serviceHandler struct {
	log *slog.Logger
}

func (h *serviceHandler) Execute(_ []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		h.log.Error("load config", "err", err)
		return false, 1
	}

	runErr := make(chan error, 1)
	go func() {
		runErr <- runWithConfig(ctx, h.log, cfg)
	}()

	status <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case req := <-r:
			switch req.Cmd {
			case svc.Interrogate:
				status <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				err := <-runErr
				if err != nil {
					h.log.Error("shutdown", "err", err)
				}
				return false, 0
			}
		case err := <-runErr:
			if err != nil {
				h.log.Error("run", "err", err)
				return false, 1
			}
			return false, 0
		}
	}
}

// eventlogWriter routes slog output to the Windows event log as Info events.
type eventlogWriter struct{ el *eventlog.Log }

func (w *eventlogWriter) Write(p []byte) (int, error) {
	_ = w.el.Info(1, string(p))
	return len(p), nil
}
