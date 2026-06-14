package download

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/falcon/api-recon/pkg/recipe"
	"github.com/falcon/api-recon/pkg/shape"
)

func TestPlanFor_AnikageHLS(t *testing.T) {
	auth := recipe.Auth{
		RequiredHeaders: map[string]string{
			"Origin":  "https://anikage.cc",
			"Referer": "https://anikage.cc/",
		},
	}
	in := Input{
		URL: "https://prox.anikage.cc/stream/abc",
		Shape: shape.Shape{
			Kind:        shape.KindHLSMaster,
			ContentType: "application/vnd.apple.mpegurl",
			VariantPath: "/stream/abc",
		},
		Auth:       auth,
		OutputPath: "frieren_ep1.mp4",
	}
	plan, err := PlanFor(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	// First arg should be yt-dlp.
	if len(plan.Argv) < 1 || plan.Argv[0] != "yt-dlp" {
		t.Errorf("argv[0] = %q, want yt-dlp", plan.Argv[0])
	}
	// Should contain --concurrent-fragments 16.
	found := false
	for i, a := range plan.Argv {
		if a == "--concurrent-fragments" && i+1 < len(plan.Argv) && plan.Argv[i+1] == "16" {
			found = true
		}
	}
	if !found {
		t.Errorf("argv should contain --concurrent-fragments 16, got %v", plan.Argv)
	}
	// Should contain --add-header "Origin:https://anikage.cc" and
	// "Referer:https://anikage.cc/".
	marshaled := plan.Marshal()
	if !strings.Contains(marshaled, "--add-header Origin:https://anikage.cc") {
		t.Errorf("argv should contain Origin header, got %q", marshaled)
	}
	if !strings.Contains(marshaled, "--add-header Referer:https://anikage.cc/") {
		t.Errorf("argv should contain Referer header, got %q", marshaled)
	}
	// Should end with the URL.
	if !strings.HasSuffix(marshaled, "https://prox.anikage.cc/stream/abc") {
		t.Errorf("argv should end with the URL, got %q", marshaled)
	}
}

func TestPlanFor_DirectCurl(t *testing.T) {
	in := Input{
		URL:        "https://cdn.example/movie.mp4",
		Shape:      shape.Shape{Kind: shape.KindDirect},
		OutputPath: "movie.mp4",
	}
	plan, err := PlanFor(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Argv[0] != "curl" {
		t.Errorf("argv[0] = %q, want curl", plan.Argv[0])
	}
	if !contains(plan.Argv, "-L") {
		t.Errorf("curl should use -L, got %v", plan.Argv)
	}
}

func TestPlanFor_DASH(t *testing.T) {
	in := Input{
		URL:        "https://x.com/manifest.mpd",
		Shape:      shape.Shape{Kind: shape.KindDASH},
		OutputPath: "video.mp4",
	}
	plan, err := PlanFor(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Argv[0] != "yt-dlp" {
		t.Errorf("argv[0] = %q, want yt-dlp", plan.Argv[0])
	}
}

func TestPlanFor_UnknownShape(t *testing.T) {
	in := Input{
		URL:   "https://x/y",
		Shape: shape.Shape{Kind: shape.KindUnknown},
	}
	_, err := PlanFor(context.Background(), in)
	if err == nil {
		t.Error("expected error for unknown shape")
	}
}

func TestPlanFor_JSONStreamKey(t *testing.T) {
	body := []byte(`{"sources":[{"url":"aHR0cHM6Ly9wcm94LmFuaWthZ2UuY2MvbTN1OA=="}]}`)
	auth := recipe.Auth{
		RequiredHeaders: map[string]string{
			"Origin":  "https://anikage.cc",
			"Referer": "https://anikage.cc/",
		},
	}
	in := Input{
		URL:        "https://anikage.cc/api/media/anime/z/episodes/1/sources?provider=miko&lang=sub",
		Body:       body,
		Shape:      shape.Shape{Kind: shape.KindJSON, CrossHost: "prox.anikage.cc"},
		Auth:       auth,
		OutputPath: "ep1.mp4",
	}
	plan, err := PlanFor(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	// Should resolve to a yt-dlp plan against prox.anikage.cc.
	if plan.Argv[0] != "yt-dlp" {
		t.Errorf("argv[0] = %q, want yt-dlp", plan.Argv[0])
	}
	if !strings.Contains(plan.Marshal(), "prox.anikage.cc") {
		t.Errorf("argv should contain the cross-host URL, got %q", plan.Marshal())
	}
}

func TestPlanFor_HTMLLinks(t *testing.T) {
	body := []byte(`<html><body><a href="/file.zip">download</a></body></html>`)
	in := Input{
		URL:        "https://x/page",
		Body:       body,
		Shape:      shape.Shape{Kind: shape.KindHTML},
		OutputPath: "file.zip",
	}
	plan, err := PlanFor(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Argv[0] != "curl" {
		t.Errorf("argv[0] = %q, want curl", plan.Argv[0])
	}
}

func TestMarshal_Quoting(t *testing.T) {
	p := Plan{Argv: []string{"echo", "hello world", "it's fine"}}
	marshaled := p.Marshal()
	if !strings.Contains(marshaled, "'hello world'") {
		t.Errorf("space-containing arg should be quoted, got %q", marshaled)
	}
	if !strings.Contains(marshaled, `'it'\\''s fine'`) {
		t.Errorf("single quote should be escaped, got %q", marshaled)
	}
}

func TestWriteSegmentList(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/segments.txt"
	body := []byte(`["https://x.com/a","https://x.com/b"]`)
	if err := writeSegmentList(path, body); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "https://x.com/a\n") {
		t.Errorf("file should contain URL on its own line, got %q", data)
	}
}

func TestWriteSegmentList_NotArray(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/segments.txt"
	body := []byte(`{"a":1}`)
	if err := writeSegmentList(path, body); err == nil {
		t.Error("expected error for non-array body")
	}
}

func TestFindHTMLLinks_DownloadAttr(t *testing.T) {
	html := `<a href="/file.zip" download>get</a>`
	links := findHTMLLinks(html)
	if len(links) != 1 || links[0] != "/file.zip" {
		t.Errorf("links = %v, want [/file.zip]", links)
	}
}

func TestFindHTMLLinks_Extension(t *testing.T) {
	html := `<a href="/x">page</a><a href="/file.zip">get</a>`
	links := findHTMLLinks(html)
	if len(links) != 1 || links[0] != "/file.zip" {
		t.Errorf("links = %v, want [/file.zip]", links)
	}
}

func TestFindHTMLLinks_NoLinks(t *testing.T) {
	html := `<html><body>no links here</body></html>`
	if got := findHTMLLinks(html); got != nil {
		t.Errorf("expected no links, got %v", got)
	}
}

func TestExtractStreamKey(t *testing.T) {
	body := []byte(`{"sources":[{"url":"abc123","type":"m3u8"}]}`)
	key, host, err := extractStreamKey(body, "prox.anikage.cc")
	if err != nil {
		t.Fatal(err)
	}
	if key != "abc123" {
		t.Errorf("key = %q", key)
	}
	if host != "prox.anikage.cc" {
		t.Errorf("host = %q", host)
	}
}

func TestBuildStreamURL(t *testing.T) {
	got := buildStreamURL("abc", "prox.anikage.cc", recipe.Auth{})
	if got != "https://prox.anikage.cc/m3u8/abc" {
		t.Errorf("buildStreamURL = %q", got)
	}
}

func TestIsLikelyHLSKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"aHR0cHM6Ly9wcm94", true},
		{"bnVtMQ==/dmFyaWFudDE=", true},
		{"plain text with space", false},
		{"contains!special@chars", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			if got := isLikelyHLSKey(tc.key); got != tc.want {
				t.Errorf("isLikelyHLSKey(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
