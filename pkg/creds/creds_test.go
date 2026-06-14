package creds

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/falcon/api-recon/pkg/recipe"
)

func TestBearerCapture(t *testing.T) {
	s := New()
	req, _ := http.NewRequest("GET", "https://api.example/v1/users", nil)
	req.Header.Set("Authorization", "Bearer abc.def.ghi")
	s.Observe(req, nil)

	if s.Bearer != "abc.def.ghi" {
		t.Errorf("Bearer = %q, want abc.def.ghi", s.Bearer)
	}
	if s.SourceURL("Authorization") != "https://api.example/v1/users" {
		t.Errorf("SourceURL(Authorization) = %q", s.SourceURL("Authorization"))
	}
}

func TestBearerCaseInsensitive(t *testing.T) {
	s := New()
	req, _ := http.NewRequest("GET", "https://x", nil)
	req.Header.Set("Authorization", "bearer xyz")
	s.Observe(req, nil)
	if s.Bearer != "xyz" {
		t.Errorf("Bearer = %q, want xyz", s.Bearer)
	}
}

func TestSetCookieCapture(t *testing.T) {
	s := New()
	resp := &http.Response{
		Header: http.Header{},
		Request: &http.Request{URL: mustURL("https://x/login")},
	}
	resp.Header.Add("Set-Cookie", "session=abc123; Path=/; HttpOnly")
	resp.Header.Add("Set-Cookie", "csrf=xyz; Path=/")
	s.Observe(nil, resp)

	if s.Cookies["session"] != "abc123" {
		t.Errorf("session = %q, want abc123", s.Cookies["session"])
	}
	if s.Cookies["csrf"] != "xyz" {
		t.Errorf("csrf = %q, want xyz", s.Cookies["csrf"])
	}
}

func TestCookieRequestCapture(t *testing.T) {
	s := New()
	req, _ := http.NewRequest("GET", "https://x/page", nil)
	req.Header.Set("Cookie", "a=1; b=2")
	s.Observe(req, nil)
	if s.Cookies["a"] != "1" || s.Cookies["b"] != "2" {
		t.Errorf("Cookies = %v, want {a:1, b:2}", s.Cookies)
	}
}

func TestAPIKeyCapture(t *testing.T) {
	s := New()
	req, _ := http.NewRequest("GET", "https://x", nil)
	req.Header.Set("X-API-Key", "secret-key-12345")
	s.Observe(req, nil)
	if s.APIKey != "secret-key-12345" {
		t.Errorf("APIKey = %q", s.APIKey)
	}
}

func TestRequiredHeaders(t *testing.T) {
	s := New()
	req, _ := http.NewRequest("GET", "https://anikage.cc/api", nil)
	req.Header.Set("Origin", "https://anikage.cc")
	req.Header.Set("Referer", "https://anikage.cc/")
	req.Header.Set("User-Agent", "test")
	s.Observe(req, nil)
	if s.Required["Origin"] != "https://anikage.cc" {
		t.Errorf("Required[Origin] = %q", s.Required["Origin"])
	}
	if s.Required["Referer"] != "https://anikage.cc/" {
		t.Errorf("Required[Referer] = %q", s.Required["Referer"])
	}
}

func TestInject(t *testing.T) {
	s := New()
	req, _ := http.NewRequest("GET", "https://anikage.cc/api", nil)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Origin", "https://anikage.cc")
	req.Header.Set("Cookie", "s=1")
	s.Observe(req, nil)

	base := http.Header{"X-Custom": []string{"y"}}
	out := s.Inject(base, mustURL("https://prox.anikage.cc/stream/abc"))

	if out.Get("Authorization") != "Bearer tok" {
		t.Errorf("Authorization = %q", out.Get("Authorization"))
	}
	if out.Get("Origin") != "https://anikage.cc" {
		t.Errorf("Origin = %q", out.Get("Origin"))
	}
	if out.Get("Cookie") != "s=1" {
		t.Errorf("Cookie = %q", out.Get("Cookie"))
	}
	if out.Get("X-Custom") != "y" {
		t.Errorf("X-Custom (caller's own) should be preserved, got %q", out.Get("X-Custom"))
	}
	// Required headers don't trample caller-set values.
	base2 := http.Header{"Origin": []string{"https://other"}}
	out2 := s.Inject(base2, nil)
	if out2.Get("Origin") != "https://other" {
		t.Errorf("Origin should not be overridden, got %q", out2.Get("Origin"))
	}
}

func TestInject_DoesNotMutateBase(t *testing.T) {
	s := New()
	req, _ := http.NewRequest("GET", "https://x", nil)
	req.Header.Set("Authorization", "Bearer tok")
	s.Observe(req, nil)

	base := http.Header{}
	_ = s.Inject(base, nil)
	if base.Get("Authorization") != "" {
		t.Errorf("Inject should not mutate base, but base.Authorization = %q", base.Get("Authorization"))
	}
}

func TestAsRecipeAuth(t *testing.T) {
	s := New()
	req, _ := http.NewRequest("GET", "https://x", nil)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Origin", "https://x")
	req.Header.Set("Cookie", "a=1; b=2")
	s.Observe(req, nil)

	auth := s.AsRecipeAuth()
	if auth.BearerToken != "tok" {
		t.Errorf("BearerToken = %q", auth.BearerToken)
	}
	if auth.RequiredHeaders["Origin"] != "https://x" {
		t.Errorf("RequiredHeaders[Origin] = %q", auth.RequiredHeaders["Origin"])
	}
	// Cookies round-trip into a single session_cookie string.
	if !strings.Contains(auth.SessionCookie, "a=1") || !strings.Contains(auth.SessionCookie, "b=2") {
		t.Errorf("SessionCookie = %q (should contain a=1 and b=2)", auth.SessionCookie)
	}
}

func TestLoadFromRecipe(t *testing.T) {
	auth := recipe.Auth{
		BearerToken:     "tok",
		APIKey:          "k",
		SessionCookie:   "a=1; b=2",
		RequiredHeaders: map[string]string{"Origin": "https://x"},
	}
	s := LoadFromRecipe(auth)
	if s.Bearer != "tok" {
		t.Errorf("Bearer = %q", s.Bearer)
	}
	if s.APIKey != "k" {
		t.Errorf("APIKey = %q", s.APIKey)
	}
	if s.Cookies["a"] != "1" || s.Cookies["b"] != "2" {
		t.Errorf("Cookies = %v", s.Cookies)
	}
	if s.Required["Origin"] != "https://x" {
		t.Errorf("Required[Origin] = %q", s.Required["Origin"])
	}
}

func TestRoundtripAuth(t *testing.T) {
	s := New()
	req, _ := http.NewRequest("GET", "https://anikage.cc/api", nil)
	req.Header.Set("Authorization", "Bearer orig-tok")
	req.Header.Set("Cookie", "s=session-val")
	req.Header.Set("Origin", "https://anikage.cc")
	s.Observe(req, nil)

	auth := s.AsRecipeAuth()
	loaded := LoadFromRecipe(auth)

	if !reflect.DeepEqual(s.Cookies, loaded.Cookies) {
		t.Errorf("Cookies roundtrip: %v → %v", s.Cookies, loaded.Cookies)
	}
	if s.Required["Origin"] != loaded.Required["Origin"] {
		t.Errorf("Origin roundtrip lost: %q → %q", s.Required["Origin"], loaded.Required["Origin"])
	}
}

func TestHasCredentials(t *testing.T) {
	s := New()
	if s.HasCredentials() {
		t.Error("empty store should not have credentials")
	}
	s.Bearer = "x"
	if !s.HasCredentials() {
		t.Error("store with bearer should have credentials")
	}
}

func TestObserveHeaders_LightEntry(t *testing.T) {
	s := New()
	reqH := http.Header{}
	reqH.Set("Authorization", "Bearer tok")
	respH := http.Header{}
	respH.Add("Set-Cookie", "session=abc")
	s.ObserveHeaders(reqH, respH, "https://x/y")
	if s.Bearer != "tok" {
		t.Errorf("Bearer = %q", s.Bearer)
	}
	if s.Cookies["session"] != "abc" {
		t.Errorf("session = %q", s.Cookies["session"])
	}
}

func TestBearerLongerWins(t *testing.T) {
	s := New()
	req, _ := http.NewRequest("GET", "https://x", nil)
	req.Header.Set("Authorization", "Bearer short")
	s.Observe(req, nil)
	req.Header.Set("Authorization", "Bearer a-much-longer-token")
	s.Observe(req, nil)
	if s.Bearer != "a-much-longer-token" {
		t.Errorf("longer bearer should win, got %q", s.Bearer)
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
