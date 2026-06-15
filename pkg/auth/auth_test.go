package auth

import (
	"net/http"
	"strings"
	"testing"
)

func TestNew_HeadersFor(t *testing.T) {
	s := New()
	h := s.HeadersFor("example.com")
	if h["User-Agent"] == "" {
		t.Error("default User-Agent missing")
	}
	if h["Accept"] == "" {
		t.Error("default Accept missing")
	}
}

func TestRecord_OverridesDefault(t *testing.T) {
	s := New()
	s.Record("cdn.example", "User-Agent", "custom-agent", "https://cdn.example/seg")
	h := s.HeadersFor("cdn.example")
	if h["User-Agent"] != "custom-agent" {
		t.Errorf("User-Agent = %q, want custom-agent", h["User-Agent"])
	}
	// A different host should still get the default.
	h2 := s.HeadersFor("other.example")
	if h2["User-Agent"] == "custom-agent" {
		t.Errorf("default leaked across hosts: %q", h2["User-Agent"])
	}
}

func TestInjectForbiddenOrigin(t *testing.T) {
	s := New()
	s.InjectForbiddenOrigin("cdn.example", "https://anikage.cc")
	h := s.HeadersFor("cdn.example")
	if h["Origin"] != "https://anikage.cc" {
		t.Errorf("Origin = %q, want https://anikage.cc", h["Origin"])
	}
	if h["Referer"] != "https://anikage.cc/" {
		t.Errorf("Referer = %q, want https://anikage.cc/", h["Referer"])
	}
}

func TestInjectForbiddenOrigin_DoesNotOverwrite(t *testing.T) {
	s := New()
	s.Record("cdn.example", "Origin", "https://first.com", "https://x")
	s.InjectForbiddenOrigin("cdn.example", "https://second.com")
	h := s.HeadersFor("cdn.example")
	if h["Origin"] != "https://first.com" {
		t.Errorf("Origin = %q, want first.com (should not be overwritten)", h["Origin"])
	}
}

func TestHasForHost(t *testing.T) {
	s := New()
	if s.HasForHost("anywhere") {
		t.Error("HasForHost = true on empty store")
	}
	s.Record("anywhere", "X-Foo", "bar", "https://anywhere/x")
	if !s.HasForHost("anywhere") {
		t.Error("HasForHost = false after Record")
	}
}

func TestRecordFromResponse(t *testing.T) {
	s := New()
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("WWW-Authenticate", "Bearer")
	s.RecordFromResponse("api.example", "https://api.example/x", resp)
	if !s.HasForHost("api.example") {
		t.Error("HasForHost = false after RecordFromResponse")
	}
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"https://example.com/path":  "example.com",
		"http://example.com":        "example.com",
		"https://a.b.c:8080/x?y=z":  "a.b.c:8080",
		"not a url":                 "",
		"":                          "",
	}
	for in, want := range cases {
		got := HostOf(in)
		if got != want {
			t.Errorf("HostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHeadersFor_EmptyHost(t *testing.T) {
	s := New()
	s.Record("cdn.example", "X-Foo", "bar", "u")
	h := s.HeadersFor("")
	if !strings.HasPrefix(h["User-Agent"], "api-recon") {
		t.Errorf("empty host should still get default User-Agent, got %q", h["User-Agent"])
	}
	if _, ok := h["X-Foo"]; ok {
		t.Error("X-Foo leaked from cdn.example to empty host")
	}
}
