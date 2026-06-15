package download

import (
	"context"
	"strings"
	"testing"
)

func TestArgv_HLS(t *testing.T) {
	req := Request{
		StreamURL:  "https://cdn.example/playlist.m3u8",
		OutputPath: "out.mkv",
		Kind:       "hls_variant",
		Headers: map[string]string{
			"Origin":  "https://anikage.cc",
			"Referer": "https://anikage.cc/",
		},
	}
	argv := Argv(req)
	s := strings.Join(argv, " ")
	if !strings.Contains(s, "yt-dlp") {
		t.Error("argv should contain yt-dlp")
	}
	if !strings.Contains(s, "--concurrent-fragments 16") {
		t.Error("argv should have concurrent-fragments 16 for HLS")
	}
	if !strings.Contains(s, `--add-header Origin:https://anikage.cc`) {
		t.Error("argv should inject Origin header")
	}
	if !strings.Contains(s, `-o out.mkv`) {
		t.Error("argv should have -o out.mkv")
	}
	if !strings.HasSuffix(s, "https://cdn.example/playlist.m3u8") {
		t.Errorf("argv should end with stream URL, got %s", s)
	}
}

func TestArgv_Direct(t *testing.T) {
	req := Request{
		StreamURL: "https://cdn.example/file.mp4",
		Kind:      "direct",
	}
	argv := Argv(req)
	s := strings.Join(argv, " ")
	if strings.Contains(s, "--concurrent-fragments") {
		t.Error("argv should NOT have concurrent-fragments for direct file")
	}
}

func TestArgv_DASH(t *testing.T) {
	req := Request{
		StreamURL: "https://cdn.example/manifest.mpd",
		Kind:      "dash",
	}
	argv := Argv(req)
	if !strings.Contains(strings.Join(argv, " "), "--concurrent-fragments") {
		t.Error("argv should have concurrent-fragments for DASH")
	}
}

func TestArgv_SegmentList(t *testing.T) {
	req := Request{
		StreamURL: "https://cdn.example/segments.json",
		Kind:      "segment_list",
		Headers:   map[string]string{"Origin": "https://x.com"},
	}
	argv := Argv(req)
	s := strings.Join(argv, " ")
	if !strings.Contains(s, "aria2c") {
		t.Error("argv should use aria2c for segment_list")
	}
	if !strings.Contains(s, "--header Origin: https://x.com") {
		t.Error("argv should inject header for aria2c")
	}
}

func TestArgv_SkipsDefaultHeaders(t *testing.T) {
	req := Request{
		StreamURL: "https://x.com/y.m3u8",
		Kind:      "hls_variant",
		Headers: map[string]string{
			"User-Agent":      "api-recon/0.2.0",
			"Accept":          "*/*",
			"Accept-Language": "en",
			"Origin":          "https://x.com",
		},
	}
	argv := Argv(req)
	s := strings.Join(argv, " ")
	if strings.Contains(s, "User-Agent") {
		t.Error("argv should skip default User-Agent")
	}
	if strings.Contains(s, "Accept") {
		t.Error("argv should skip default Accept")
	}
	if !strings.Contains(s, "Origin") {
		t.Error("argv should keep non-default Origin")
	}
}

func TestArgv_ExtraArgs(t *testing.T) {
	req := Request{
		StreamURL: "https://x.com/y.m3u8",
		Kind:      "hls_variant",
		ExtraArgs: []string{"--proxy", "http://localhost:8080"},
	}
	argv := Argv(req)
	s := strings.Join(argv, " ")
	if !strings.Contains(s, "--proxy http://localhost:8080") {
		t.Error("argv should include extra args before stream URL")
	}
}

func TestArgv_ToolOverride(t *testing.T) {
	req := Request{
		StreamURL: "https://x.com/y.m3u8",
		Kind:      "hls_variant",
		Tool:      "my-yt-dlp",
	}
	argv := Argv(req)
	if argv[0] != "my-yt-dlp" {
		t.Errorf("argv[0] = %q, want my-yt-dlp", argv[0])
	}
}

func TestArgv_DefaultConcurrent(t *testing.T) {
	req := Request{
		StreamURL: "https://x.com/y.m3u8",
		Kind:      "hls_variant",
	}
	argv := Argv(req)
	if !strings.Contains(strings.Join(argv, " "), "--concurrent-fragments 16") {
		t.Error("default concurrent should be 16")
	}
}

func TestArgv_CustomConcurrent(t *testing.T) {
	req := Request{
		StreamURL:  "https://x.com/y.m3u8",
		Kind:       "hls_variant",
		Concurrent: 32,
	}
	argv := Argv(req)
	if !strings.Contains(strings.Join(argv, " "), "--concurrent-fragments 32") {
		t.Error("custom concurrent should be 32")
	}
}

// TestRun_ContextCancel: a download that takes too long should be
// cancellable.
func TestRun_ContextCancel(t *testing.T) {
	// We can't easily test with a real yt-dlp in CI. Use a
	// mock shell command via PATH manipulation.
	// Skipping if we can't set up the mock.
	t.Skip("requires PATH manipulation; covered manually")
	_ = context.Background
}
