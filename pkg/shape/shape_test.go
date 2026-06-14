package shape

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

// loadTestdata reads a fixture from the project-root testdata/ dir.
func loadTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return data
}

func resp(status int, contentType string) *http.Response {
	r := &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	return r
}

func TestClassifier_Redirect(t *testing.T) {
	r := resp(302, "")
	r.Header.Set("Location", "https://other.example/x")
	got := New().Classify(r, nil, "https://example.com/x")
	if got.Kind != KindRedirect {
		t.Errorf("Kind = %q, want %q", got.Kind, KindRedirect)
	}
	if !strings.Contains(got.Reasoning, "other.example") {
		t.Errorf("Reasoning should mention Location, got %q", got.Reasoning)
	}
}

func TestClassifier_ErrorEnvelope(t *testing.T) {
	r := resp(500, "application/json")
	body := loadTestdata(t, "anikage_error.json")
	got := New().Classify(r, body, "https://anikage.cc/api/x")
	if got.Kind != KindError {
		t.Errorf("Kind = %q, want %q", got.Kind, KindError)
	}
	if got.ErrorMessage != "No episodes found for provider pahe" {
		t.Errorf("ErrorMessage = %q", got.ErrorMessage)
	}
	if len(got.MissingValues) == 0 || got.MissingValues[0] != "pahe" {
		t.Errorf("MissingValues = %v, want [pahe]", got.MissingValues)
	}
}

func TestClassifier_PlainError(t *testing.T) {
	r := resp(404, "text/plain")
	got := New().Classify(r, []byte("not found"), "https://x/y")
	if got.Kind != KindError {
		t.Errorf("Kind = %q, want %q", got.Kind, KindError)
	}
	if got.ErrorMessage != "" {
		t.Errorf("ErrorMessage should be empty for plain text, got %q", got.ErrorMessage)
	}
}

func TestClassifier_HLSMaster(t *testing.T) {
	r := resp(200, "application/vnd.apple.mpegurl")
	body := loadTestdata(t, "hls_master.m3u8")
	got := New().Classify(r, body, "https://prox.anikage.cc/m3u8/abc")
	if got.Kind != KindHLSMaster {
		t.Errorf("Kind = %q, want %q", got.Kind, KindHLSMaster)
	}
	if !strings.HasPrefix(got.VariantPath, "/stream/") {
		t.Errorf("VariantPath = %q, want /stream/...", got.VariantPath)
	}
}

func TestClassifier_HLSVariant(t *testing.T) {
	r := resp(200, "application/vnd.apple.mpegurl")
	body := loadTestdata(t, "hls_variant.m3u8")
	got := New().Classify(r, body, "https://prox.anikage.cc/stream/abc")
	if got.Kind != KindHLSVariant {
		t.Errorf("Kind = %q, want %q", got.Kind, KindHLSVariant)
	}
	if got.SegmentCount != 5 {
		t.Errorf("SegmentCount = %d, want 5", got.SegmentCount)
	}
}

func TestClassifier_HTML(t *testing.T) {
	r := resp(200, "text/html")
	body := []byte(`<!DOCTYPE html><html><body><h1>hi</h1></body></html>`)
	got := New().Classify(r, body, "https://x/page")
	if got.Kind != KindHTML {
		t.Errorf("Kind = %q, want %q", got.Kind, KindHTML)
	}
}

func TestClassifier_HTMLWithForm(t *testing.T) {
	r := resp(200, "text/html")
	body := []byte(`<form action="/login" method="POST"><input name="user"><input name="pass" type="password"></form>`)
	got := New().Classify(r, body, "https://x/login")
	if got.Kind != KindForm {
		t.Errorf("Kind = %q, want %q", got.Kind, KindForm)
	}
	if got.FormAction != "/login" {
		t.Errorf("FormAction = %q", got.FormAction)
	}
	if got.FormMethod != "POST" {
		t.Errorf("FormMethod = %q", got.FormMethod)
	}
	if len(got.FormFields) != 2 {
		t.Errorf("FormFields = %v, want 2", got.FormFields)
	}
}

func TestClassifier_DASH(t *testing.T) {
	r := resp(200, "application/dash+xml")
	body := []byte(`<?xml version="1.0"?><MPD></MPD>`)
	got := New().Classify(r, body, "https://x/manifest.mpd")
	if got.Kind != KindDASH {
		t.Errorf("Kind = %q, want %q", got.Kind, KindDASH)
	}
}

func TestClassifier_JSONObject(t *testing.T) {
	r := resp(200, "application/json")
	body := []byte(`{"sources":[{"url":"https://prox.anikage.cc/stream/abc"}]}`)
	got := New().Classify(r, body, "https://anikage.cc/api/sources")
	if got.Kind != KindJSON {
		t.Errorf("Kind = %q, want %q", got.Kind, KindJSON)
	}
	if got.CrossHost != "prox.anikage.cc" {
		t.Errorf("CrossHost = %q, want prox.anikage.cc", got.CrossHost)
	}
}

func TestClassifier_JSONObject_SameHost(t *testing.T) {
	r := resp(200, "application/json")
	body := []byte(`{"sources":[{"url":"https://anikage.cc/other"}]}`)
	got := New().Classify(r, body, "https://anikage.cc/api/sources")
	if got.CrossHost != "" {
		t.Errorf("CrossHost should be empty for same-host URL, got %q", got.CrossHost)
	}
}

func TestClassifier_JSONList(t *testing.T) {
	r := resp(200, "application/json")
	body := loadTestdata(t, "anikage_episodes.json")
	got := New().Classify(r, body, "https://anikage.cc/api/episodes")
	if got.Kind != KindJSONList {
		t.Errorf("Kind = %q, want %q", got.Kind, KindJSON)
	}
	if got.ItemCount != 3 {
		t.Errorf("ItemCount = %d, want 3", got.ItemCount)
	}
	if len(got.IDFields) == 0 || got.IDFields[0] != "id" {
		t.Errorf("IDFields = %v, want [id ...]", got.IDFields)
	}
}

func TestClassifier_JSONList_NoIDField(t *testing.T) {
	r := resp(200, "application/json")
	body := []byte(`[{"value":"a"},{"value":"b"}]`)
	got := New().Classify(r, body, "https://x/list")
	if got.Kind != KindJSONList {
		t.Errorf("Kind = %q", got.Kind)
	}
	if len(got.IDFields) != 0 {
		t.Errorf("IDFields should be empty, got %v", got.IDFields)
	}
}

func TestClassifier_DirectFile(t *testing.T) {
	r := resp(200, "video/mp4")
	got := New().Classify(r, []byte("binary"), "https://cdn.example.com/movie.mp4")
	if got.Kind != KindDirect {
		t.Errorf("Kind = %q, want %q", got.Kind, KindDirect)
	}
}

func TestClassifier_SegmentList(t *testing.T) {
	r := resp(200, "application/json")
	body := []byte(`["https://x.com/seg1","https://x.com/seg2","https://x.com/seg3"]`)
	got := New().Classify(r, body, "https://x/segments")
	if got.Kind != KindSegmentList {
		t.Errorf("Kind = %q, want %q", got.Kind, KindSegmentList)
	}
	if got.ItemCount != 3 {
		t.Errorf("ItemCount = %d, want 3", got.ItemCount)
	}
}

func TestClassifier_Unknown(t *testing.T) {
	r := resp(200, "text/plain")
	got := New().Classify(r, []byte("hi"), "https://x/y")
	if got.Kind != KindUnknown {
		t.Errorf("Kind = %q, want %q", got.Kind, KindUnknown)
	}
}

func TestClassifier_ContentTypeWithCharset(t *testing.T) {
	r := resp(200, "application/json; charset=utf-8")
	body := []byte(`{"a":1}`)
	got := New().Classify(r, body, "https://x/y")
	if got.ContentType != "application/json" {
		t.Errorf("ContentType = %q, want application/json", got.ContentType)
	}
	if got.Kind != KindJSON {
		t.Errorf("Kind = %q, want %q", got.Kind, KindJSON)
	}
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"":                                    "",
		"not a url":                           "",
		"https://example.com/x":               "example.com",
		"https://example.com":                 "example.com",
		"http://a.b.c:8080/path":              "a.b.c:8080",
		"https://prox.anikage.cc/m3u8/abc123": "prox.anikage.cc",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := hostOf(in); got != want {
				t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

func TestLooksLikeJSON(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{`{"a":1}`, true},
		{"  [1,2]", true},
		{"\n\n{\"a\":1}", true},
		{"plain text", false},
		{"", false},
		{"<html>", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := looksLikeJSON([]byte(tc.in)); got != tc.want {
				t.Errorf("looksLikeJSON(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestAllItemsLookLikeURLs(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{`["https://x/a","https://x/b"]`, true},
		{`["/path/a","/path/b"]`, true},
		{`["https://x/a","not a url"]`, false},
		{`[1,2,3]`, false},
		{`{}`, false},
		{`[]`, false}, // empty array — we don't accept it as a segment list
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := allItemsLookLikeURLs([]byte(tc.in)); got != tc.want {
				t.Errorf("allItemsLookLikeURLs(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsDirectFile(t *testing.T) {
	cases := map[string]bool{
		"https://x/movie.mp4":    true,
		"https://x/movie.MKV":    true,
		"https://x/movie.webm":   true,
		"https://x/movie.avi":    true,
		"https://x/movie.txt":    false,
		"https://x/movie":        false,
		"https://x/movie.m3u8":   false,
		"https://x/manifest.mpd": false,
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := isDirectFile(in); got != want {
				t.Errorf("isDirectFile(%q) = %v, want %v", in, got, want)
			}
		})
	}
}

func TestExtractErrorMessage(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"error.message envelope", `{"error":{"message":"bad things"}}`, "bad things"},
		{"top-level error string", `{"error":"bad things"}`, "bad things"},
		{"top-level message", `{"message":"bad things"}`, "bad things"},
		{"msg field", `{"msg":"bad things"}`, "bad things"},
		{"plain text", `not json`, ""},
		{"empty", ``, ""},
		{"no error field", `{"data":{}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractErrorMessage([]byte(tc.body)); got != tc.want {
				t.Errorf("extractErrorMessage = %q, want %q", got, tc.want)
			}
		})
	}
}
