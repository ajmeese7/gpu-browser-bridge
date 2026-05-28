package server

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// TestListenAndServe_DualStack verifies the server answers on both IPv4 and
// IPv6 loopback. Binding only one family is what caused the SSH tunnel to
// fail with "empty reply from server" when sshd resolved localhost to ::1.
func TestListenAndServe_DualStack(t *testing.T) {
	port := freePort(t)
	cfg := &config.Config{
		BindAddr: net.JoinHostPort("127.0.0.1", port),
		Token:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	s := New(cfg, nil, slogDiscard())

	go func() { _ = s.ListenAndServe(t.Context()) }()

	for _, host := range []string{"127.0.0.1", "[::1]"} {
		url := fmt.Sprintf("http://%s/healthz", net.JoinHostPort(strings.Trim(host, "[]"), port))
		if err := waitForOK(url); err != nil {
			t.Fatalf("%s: %v", host, err)
		}
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port
}

func waitForOK(url string) error {
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
		lastErr = fmt.Errorf("status %d", resp.StatusCode)
		time.Sleep(20 * time.Millisecond)
	}
	return lastErr
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
