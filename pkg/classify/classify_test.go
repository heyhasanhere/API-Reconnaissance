package classify

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/heyhasanhere/API-Reconnaissance/pkg/probe"
)

// loadFixture reads testdata/<name>.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

func probeOf(status int, ct string, body []byte) *probe.Response {
	h := http.Header{}
	if ct != "" {
		h.Set("Content-Type", ct)
	}
	return &probe.Response{
		Status:  status,
		Headers: h,
		Body:    body,
	}
}

func TestClassify_AnikageEpisodes(t *testing.T) {
	body := loadFixture(t, "anikage_episodes.json")
	s := Classify(probeOf(200, "application/json", body), "https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes")
	if s.Kind != KindJSONList {
		t.Fatalf("Kind = %s, want %s", s.Kind, KindJSONList)
	}
	if s.ItemCount != 28 {
		t.Errorf("ItemCount = %d, want 28", s.ItemCount)
	}
	if !contains(s.IDFields, "id") || !contains(s.IDFields, "number") {
		t.Errorf("IDFields = %v, want id and number", s.IDFields)
	}
}

func TestClassify_AnikageServers(t *testing.T) {
	body := loadFixture(t, "anikage_servers.json")
	s := Classify(probeOf(200, "application/json", body), "https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/1/servers")
	if s.Kind != KindJSONList {
		t.Fatalf("Kind = %s, want %s", s.Kind, KindJSONList)
	}
	want := []string{"megg", "kiss", "miko", "verse"}
	if !equalStrings(s.ProviderList, want) {
		t.Errorf("ProviderList = %v, want %v", s.ProviderList, want)
	}
}

func TestClassify_AnikageSourcesMiko(t *testing.T) {
	body := loadFixture(t, "anikage_sources_miko.json")
	s := Classify(probeOf(200, "application/json", body), "https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/1/sources?provider=miko&lang=sub")
	if s.Kind != KindJSON {
		t.Fatalf("Kind = %s, want %s", s.Kind, KindJSON)
	}
	if !s.StreamIsM3U8 {
		t.Error("StreamIsM3U8 = false, want true")
	}
	if s.StreamIsEmbed {
		t.Error("StreamIsEmbed = true, want false (miko is HLS, not embed)")
	}
	if s.StreamKey == "" {
		t.Error("StreamKey empty, want the opaque url field")
	}
}

func TestClassify_AnikageSourcesKiss(t *testing.T) {
	body := loadFixture(t, "anikage_sources_kiss.json")
	s := Classify(probeOf(200, "application/json", body), "https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/1/sources?provider=kiss&lang=sub")
	if s.Kind != KindJSON {
		t.Fatalf("Kind = %s, want %s", s.Kind, KindJSON)
	}
	if !s.StreamIsEmbed {
		t.Error("StreamIsEmbed = false, want true (kiss returns embeds)")
	}
}

func TestClassify_HLSVariant(t *testing.T) {
	body := []byte("#EXTM3U\n#EXT-X-VERSION:3\n#EXTINF:4.5,\n/stream/seg1\n#EXTINF:5.0,\n/stream/seg2\n")
	s := Classify(probeOf(200, "application/vnd.apple.mpegurl", body), "https://cdn.example/playlist.m3u8")
	if s.Kind != KindHLSVariant {
		t.Errorf("Kind = %s, want %s", s.Kind, KindHLSVariant)
	}
}

func TestClassify_HLSMaster(t *testing.T) {
	body := []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=2000000\n720p.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=4000000\n1080p.m3u8\n")
	s := Classify(probeOf(200, "application/vnd.apple.mpegurl", body), "https://cdn.example/master.m3u8")
	if s.Kind != KindHLSMaster {
		t.Errorf("Kind = %s, want %s", s.Kind, KindHLSMaster)
	}
}

func TestClassify_DASH(t *testing.T) {
	body := []byte(`<?xml version="1.0"?><MPD xmlns="urn:mpeg:dash:schema:mpd:2011"></MPD>`)
	s := Classify(probeOf(200, "application/dash+xml", body), "https://cdn.example/manifest.mpd")
	if s.Kind != KindDASH {
		t.Errorf("Kind = %s, want %s", s.Kind, KindDASH)
	}
}

func TestClassify_404JSON(t *testing.T) {
	body := []byte(`{"message":"Not found"}`)
	s := Classify(probeOf(404, "application/json", body), "https://example.com/x")
	if s.Kind != KindError {
		t.Errorf("Kind = %s, want %s", s.Kind, KindError)
	}
	if !strings.Contains(s.ErrorMessage, "Not found") {
		t.Errorf("ErrorMessage = %q, want it to contain 'Not found'", s.ErrorMessage)
	}
}

func TestClassify_403ForbiddenOrigin(t *testing.T) {
	body := []byte("forbidden origin")
	s := Classify(probeOf(403, "text/plain", body), "https://cdn.example/stream/seg")
	if s.Kind != KindError {
		t.Errorf("Kind = %s, want %s", s.Kind, KindError)
	}
	if !strings.Contains(s.Reasoning, "forbidden origin") {
		t.Errorf("Reasoning = %q, want it to mention forbidden origin", s.Reasoning)
	}
}

func TestClassify_ProviderInErrorMessage(t *testing.T) {
	body := []byte(`{"success":false,"error":{"message":"No episodes found for provider pahe"}}`)
	s := Classify(probeOf(500, "application/json", body), "https://example.com/x")
	if s.Kind != KindError {
		t.Errorf("Kind = %s, want %s", s.Kind, KindError)
	}
	if !contains(s.MissingValues, "pahe") {
		t.Errorf("MissingValues = %v, want it to contain 'pahe'", s.MissingValues)
	}
}

func TestClassify_ProviderRequired(t *testing.T) {
	body := []byte(`{"message":"provider query param is required"}`)
	s := Classify(probeOf(400, "application/json", body), "https://example.com/sources")
	if !contains(s.MissingValues, "provider") {
		t.Errorf("MissingValues = %v, want it to contain 'provider'", s.MissingValues)
	}
}

func TestClassify_HTML(t *testing.T) {
	body := []byte(`<!doctype html><html><body>hello</body></html>`)
	s := Classify(probeOf(200, "text/html", body), "https://example.com/")
	if s.Kind != KindHTML {
		t.Errorf("Kind = %s, want %s", s.Kind, KindHTML)
	}
}

func TestClassify_Redirect(t *testing.T) {
	h := http.Header{}
	h.Set("Location", "/elsewhere")
	resp := &probe.Response{Status: 302, Headers: h, Body: nil}
	s := Classify(resp, "https://example.com/x")
	if s.Kind != KindRedirect {
		t.Errorf("Kind = %s, want %s", s.Kind, KindRedirect)
	}
}

func TestClassify_Direct(t *testing.T) {
	// 2 MiB body with .mp4 extension.
	big := make([]byte, 2<<20)
	for i := range big {
		big[i] = 'x'
	}
	s := Classify(probeOf(200, "video/mp4", big), "https://cdn.example/video.mp4")
	if s.Kind != KindDirect {
		t.Errorf("Kind = %s, want %s", s.Kind, KindDirect)
	}
}

func TestClassify_Form(t *testing.T) {
	body := []byte(`<!doctype html><html><body><form action="/login" method="post"><input name="user"><input name="pass"></form></body></html>`)
	s := Classify(probeOf(200, "text/html", body), "https://example.com/login")
	if s.Kind != KindForm {
		t.Errorf("Kind = %s, want %s", s.Kind, KindForm)
	}
	if s.FormAction != "/login" {
		t.Errorf("FormAction = %q, want /login", s.FormAction)
	}
	if !contains(s.FormFields, "user") || !contains(s.FormFields, "pass") {
		t.Errorf("FormFields = %v, want user and pass", s.FormFields)
	}
}

func TestClassify_RealHTTPServer(t *testing.T) {
	// Round-trip: spin up a server, probe it, classify. Sanity
	// check that probe + classify integrate.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`[{"id":1,"name":"a"},{"id":2,"name":"b"}]`))
	}))
	defer srv.Close()

	resp, err := probe.Do(t.Context(), probe.Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	s := Classify(resp, srv.URL)
	if s.Kind != KindJSONList {
		t.Errorf("Kind = %s, want %s", s.Kind, KindJSONList)
	}
	if s.ItemCount != 2 {
		t.Errorf("ItemCount = %d, want 2", s.ItemCount)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
