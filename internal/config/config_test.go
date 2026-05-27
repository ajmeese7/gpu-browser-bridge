package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateToken_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")

	tok, err := GenerateToken(path)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if len(tok) != 64 {
		t.Fatalf("token length = %d, want 64", len(tok))
	}

	got, err := readToken(path)
	if err != nil {
		t.Fatalf("readToken: %v", err)
	}
	if got != tok {
		t.Fatalf("readToken = %q, want %q", got, tok)
	}
}

func TestValidate_RejectsNonLoopback(t *testing.T) {
	c := &Config{
		BindAddr:   "0.0.0.0:8765",
		Token:      "x" + string(make([]byte, 63)),
		ChromePath: `C:\fake\chrome.exe`,
	}
	if err := c.validate(); err == nil {
		t.Fatal("validate accepted 0.0.0.0 bind; want error")
	}
}

func TestValidate_RejectsShortToken(t *testing.T) {
	c := &Config{
		BindAddr:   "127.0.0.1:8765",
		Token:      "tooshort",
		ChromePath: `C:\fake\chrome.exe`,
	}
	if err := c.validate(); err == nil {
		t.Fatal("validate accepted short token; want error")
	}
}

func TestEnvOr(t *testing.T) {
	t.Setenv("BRIDGE_TEST_X", "from-env")
	if got := envOr("BRIDGE_TEST_X", "fallback"); got != "from-env" {
		t.Fatalf("envOr = %q, want from-env", got)
	}
	os.Unsetenv("BRIDGE_TEST_X")
	if got := envOr("BRIDGE_TEST_X", "fallback"); got != "fallback" {
		t.Fatalf("envOr = %q, want fallback", got)
	}
}
