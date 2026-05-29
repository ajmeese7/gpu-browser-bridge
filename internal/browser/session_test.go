package browser

import (
	"testing"

	"github.com/chromedp/cdproto/network"
)

func TestToCookieParams(t *testing.T) {
	if toCookieParams(nil) != nil {
		t.Fatal("nil cookies should map to nil params")
	}

	params := toCookieParams([]Cookie{{
		Name:     "session",
		Value:    "abc123",
		URL:      "https://example.com/",
		Secure:   true,
		HTTPOnly: true,
		SameSite: "Lax",
	}})
	if len(params) != 1 {
		t.Fatalf("got %d params, want 1", len(params))
	}
	p := params[0]
	if p.Name != "session" || p.Value != "abc123" || p.URL != "https://example.com/" {
		t.Errorf("name/value/url mismatch: %+v", p)
	}
	if !p.Secure || !p.HTTPOnly {
		t.Errorf("secure/httpOnly not propagated: %+v", p)
	}
	if p.SameSite != network.CookieSameSiteLax {
		t.Errorf("same_site = %q, want Lax", p.SameSite)
	}
}

func TestToCookieParamsEmptySameSite(t *testing.T) {
	// Empty same_site must stay empty (not coerced to a bogus enum value).
	p := toCookieParams([]Cookie{{Name: "a", Value: "b"}})[0]
	if p.SameSite != "" {
		t.Errorf("same_site = %q, want empty", p.SameSite)
	}
}

func TestLocalStorageScript(t *testing.T) {
	// Single entry is deterministic; also checks JS-string escaping of a value
	// containing a double quote.
	got := localStorageScript(map[string]string{"tok": `a"b`})
	want := `try{localStorage.setItem("tok","a\"b");}catch(e){}`
	if got != want {
		t.Errorf("localStorageScript:\n got %q\nwant %q", got, want)
	}
}

func TestToHeaders(t *testing.T) {
	h := toHeaders(map[string]string{"Authorization": "Bearer x"})
	if h["Authorization"] != "Bearer x" {
		t.Errorf("header not propagated: %+v", h)
	}
}

func TestPreNavigateActionsCount(t *testing.T) {
	// Empty session => no actions; each populated field adds one action.
	if n := len((SessionContext{}).preNavigateActions()); n != 0 {
		t.Errorf("empty session produced %d actions, want 0", n)
	}
	full := SessionContext{
		Cookies:      []Cookie{{Name: "a", Value: "b"}},
		Headers:      map[string]string{"X": "y"},
		LocalStorage: map[string]string{"k": "v"},
	}
	if n := len(full.preNavigateActions()); n != 3 {
		t.Errorf("full session produced %d actions, want 3", n)
	}
}
