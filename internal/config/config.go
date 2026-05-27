// Package config resolves runtime settings for bridge.exe from environment
// variables and on-disk files. All paths default to Windows conventions
// (%PROGRAMDATA%, %LOCALAPPDATA%) but can be overridden for tests.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	// BindAddr is the TCP address the HTTP server listens on.
	// Always loopback; the reverse SSH tunnel is what crosses the network.
	BindAddr string

	// Token is the bearer token required on every authenticated request.
	Token string

	// ChromePath is the path to the Chrome executable.
	ChromePath string

	// UserDataDir is the persistent Chrome profile directory.
	// Isolated from the user's daily browser profile.
	UserDataDir string

	// LogPath is where the service writes structured logs.
	LogPath string
}

// Load resolves a Config from environment + on-disk files.
// Env vars override defaults. Token is loaded from disk only.
func Load() (*Config, error) {
	c := &Config{
		BindAddr:    envOr("BRIDGE_BIND_ADDR", "127.0.0.1:51234"),
		ChromePath:  envOr("BRIDGE_CHROME_PATH", defaultChromePath()),
		UserDataDir: envOr("BRIDGE_USER_DATA_DIR", defaultUserDataDir()),
		LogPath:     envOr("BRIDGE_LOG_PATH", defaultLogPath()),
	}

	tokenPath := envOr("BRIDGE_TOKEN_PATH", defaultTokenPath())
	tok, err := readToken(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("load token from %s: %w", tokenPath, err)
	}
	c.Token = tok

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	if !strings.HasPrefix(c.BindAddr, "127.0.0.1:") && !strings.HasPrefix(c.BindAddr, "localhost:") {
		return fmt.Errorf("BindAddr must be loopback (got %q); exposing the bridge to non-loopback is not supported", c.BindAddr)
	}
	if len(c.Token) < 32 {
		return errors.New("token is too short (need >= 32 chars); regenerate with install.ps1")
	}
	if c.ChromePath == "" {
		return errors.New("ChromePath is empty; set BRIDGE_CHROME_PATH or install Chrome to the default location")
	}
	return nil
}

func readToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// GenerateToken writes a fresh 64-char hex token to path, creating parent
// dirs as needed. Used by install.ps1 (via `bridge.exe gen-token`).
func GenerateToken(path string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func defaultChromePath() string {
	candidates := []string{
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func defaultUserDataDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "gpu-browser-bridge", "chrome-profile")
}

func defaultTokenPath() string {
	base := os.Getenv("PROGRAMDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "gpu-browser-bridge", "token")
}

func defaultLogPath() string {
	base := os.Getenv("PROGRAMDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "gpu-browser-bridge", "bridge.log")
}
