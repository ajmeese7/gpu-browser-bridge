package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ajmeese7/gpu-browser-bridge/internal/config"
)

func newTestServer(token string) *Server {
	cfg := &config.Config{Token: token}
	return New(cfg, nil, slogDiscard())
}

func TestRequireAuth_MissingToken(t *testing.T) {
	s := newTestServer("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	h := s.Handler()

	req := httptest.NewRequest(http.MethodPost, "/screenshot", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRequireAuth_WrongToken(t *testing.T) {
	s := newTestServer("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	h := s.Handler()

	req := httptest.NewRequest(http.MethodPost, "/screenshot", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHealthz_NoAuthRequired(t *testing.T) {
	s := newTestServer("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	h := s.Handler()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		body, _ := io.ReadAll(rec.Body)
		t.Fatalf("status = %d, body = %s", rec.Code, body)
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
