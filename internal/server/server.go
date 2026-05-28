// Package server exposes the bridge over HTTP on a loopback address.
// All non-/healthz routes require a bearer token matching cfg.Token.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ajmeese7/gpu-browser-bridge/internal/browser"
	"github.com/ajmeese7/gpu-browser-bridge/internal/config"
)

type Server struct {
	cfg     *config.Config
	browser *browser.Browser
	log     *slog.Logger
	started time.Time
}

func New(cfg *config.Config, b *browser.Browser, log *slog.Logger) *Server {
	return &Server{cfg: cfg, browser: b, log: log, started: time.Now()}
}

// Handler returns the root http.Handler, wired with auth + logging middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /screenshot", s.requireAuth(s.handleScreenshot))
	mux.HandleFunc("POST /eval", s.requireAuth(s.handleEval))
	return s.logRequests(mux)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	alive := false
	if s.browser != nil {
		alive = s.browser.Healthy()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"chrome_alive": alive,
		"uptime_s":     int(time.Since(s.started).Seconds()),
	})
}

func (s *Server) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	var req browser.ScreenshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	res, err := s.browser.Screenshot(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleEval(w http.ResponseWriter, r *http.Request) {
	var req browser.EvalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	res, err := s.browser.Eval(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// requireAuth wraps a handler with bearer-token authentication.
// Returns 401 with no body on missing/invalid token.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	expected := []byte(s.cfg.Token)
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		got, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), expected) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// logRequests writes one structured log line per request.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(ww, r)
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// ListenAndServe runs the server until ctx is cancelled.
//
// It binds both IPv4 (127.0.0.1) and IPv6 (::1) loopback on the configured
// port. The bridge is reached through an SSH -L tunnel, and sshd may resolve
// the forward's "localhost" target to either family; binding only one leaves
// the other dead, which surfaces to the caller as "empty reply from server".
// Binding both makes the tunnel work regardless of how localhost resolves.
func (s *Server) ListenAndServe(ctx context.Context) error {
	_, port, err := net.SplitHostPort(s.cfg.BindAddr)
	if err != nil {
		return fmt.Errorf("parse bind addr %q: %w", s.cfg.BindAddr, err)
	}

	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	// IPv4 loopback is required; IPv6 loopback is best-effort (some hosts
	// disable IPv6 entirely, in which case binding ::1 fails and we proceed
	// with IPv4 only).
	v4Addr := net.JoinHostPort("127.0.0.1", port)
	ln4, err := net.Listen("tcp4", v4Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", v4Addr, err)
	}

	errCh := make(chan error, 2)
	serve := func(ln net.Listener) {
		s.log.Info("listening", "addr", ln.Addr().String())
		errCh <- srv.Serve(ln)
	}
	go serve(ln4)

	v6Addr := net.JoinHostPort("::1", port)
	if ln6, err := net.Listen("tcp6", v6Addr); err != nil {
		s.log.Warn("IPv6 loopback unavailable, serving IPv4 only", "addr", v6Addr, "err", err)
	} else {
		go serve(ln6)
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
