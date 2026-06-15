package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// readScript resolves inline JS, file JS, or neither; the inline and file forms
// are mutually exclusive (that case calls fatal/os.Exit, so it is not exercised
// here).
func TestReadScript(t *testing.T) {
	if got := readScript("", ""); got != "" {
		t.Errorf("neither: got %q, want empty", got)
	}
	if got := readScript("doThing()", ""); got != "doThing()" {
		t.Errorf("inline: got %q", got)
	}

	path := filepath.Join(t.TempDir(), "s.js")
	if err := os.WriteFile(path, []byte("await boot()\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readScript("", path); got != "await boot()\n" {
		t.Errorf("file: got %q", got)
	}
}

func TestParseClick(t *testing.T) {
	cases := []struct {
		in      string
		x, y    float64
		wantErr bool
	}{
		{in: "100,200", x: 100, y: 200},
		{in: " 40 , 55 ", x: 40, y: 55},
		{in: "12.5,7.25", x: 12.5, y: 7.25},
		{in: "100", wantErr: true},
		{in: "a,2", wantErr: true},
		{in: "1,b", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			x, y, err := parseClick(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseClick(%q) = (%v,%v), want error", tc.in, x, y)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseClick(%q): %v", tc.in, err)
			}
			if x != tc.x || y != tc.y {
				t.Errorf("parseClick(%q) = (%v,%v), want (%v,%v)", tc.in, x, y, tc.x, tc.y)
			}
		})
	}
}

// flags after a positional arg must still be parsed — the original bug
// was Go's flag package stopping at the first non-flag token, so
// `screenshot URL --ignore-https` silently dropped --ignore-https.
func TestParseInterspersed(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantIgnore bool
		wantOut    string
		wantPos    []string
	}{
		{
			name:       "flags after url (the reported bug)",
			args:       []string{"https://example.com:5173/", "--out", "x.png", "--ignore-https", "--settle", "3000"},
			wantIgnore: true,
			wantOut:    "x.png",
			wantPos:    []string{"https://example.com:5173/"},
		},
		{
			name:       "flags before url",
			args:       []string{"--ignore-https", "--out", "x.png", "https://example.com:5173/"},
			wantIgnore: true,
			wantOut:    "x.png",
			wantPos:    []string{"https://example.com:5173/"},
		},
		{
			name:       "flags interspersed with two positionals (eval)",
			args:       []string{"https://example.com:5173/", "--ignore-https", "return 1+1"},
			wantIgnore: true,
			wantPos:    []string{"https://example.com:5173/", "return 1+1"},
		},
		{
			name:       "no flags",
			args:       []string{"https://example.com/"},
			wantIgnore: false,
			wantPos:    []string{"https://example.com/"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			ignore := fs.Bool("ignore-https", false, "")
			out := fs.String("out", "", "")
			_ = fs.Int("settle", 0, "")
			pos := parseInterspersed(fs, tc.args)

			if *ignore != tc.wantIgnore {
				t.Errorf("ignore-https = %v, want %v", *ignore, tc.wantIgnore)
			}
			if *out != tc.wantOut {
				t.Errorf("out = %q, want %q", *out, tc.wantOut)
			}
			if len(pos) != len(tc.wantPos) {
				t.Fatalf("positional = %v, want %v", pos, tc.wantPos)
			}
			for i := range pos {
				if pos[i] != tc.wantPos[i] {
					t.Errorf("positional[%d] = %q, want %q", i, pos[i], tc.wantPos[i])
				}
			}
		})
	}
}
