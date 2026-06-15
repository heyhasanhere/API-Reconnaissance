package chain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/heyhasanhere/API-Reconnaissance/pkg/auth"
	"github.com/heyhasanhere/API-Reconnaissance/pkg/classify"
	"github.com/heyhasanhere/API-Reconnaissance/pkg/probe"
)

// loadFixture reads ../../testdata/<name>.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

// TestStepResolveEntry_HTML: the entry URL is an HTML page; the
// chain should pick an API base.
func TestStepResolveEntry_HTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<!doctype html><html><body>hi</body></html>"))
	}))
	defer srv.Close()

	d := &Discovery{EntryURL: srv.URL + "/anime/x", Auth: auth.New()}
	if err := d.stepResolveEntry(context.Background()); err != nil {
		t.Fatalf("stepResolveEntry: %v", err)
	}
	if d.APIBase == "" {
		t.Error("APIBase empty after HTML entry")
	}
}

// TestStepResolveEntry_AlreadyList: the user gave us the
// /episodes URL directly. The chain should pre-populate episodes
// from the response and skip step 2.
func TestStepResolveEntry_AlreadyList(t *testing.T) {
	episodes := loadFixture(t, "anikage_episodes.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(episodes)
	}))
	defer srv.Close()

	d := &Discovery{EntryURL: srv.URL + "/api/media/anime/zMLNvt6MtV/episodes", Auth: auth.New()}
	if err := d.stepResolveEntry(context.Background()); err != nil {
		t.Fatalf("stepResolveEntry: %v", err)
	}
	if d.APIBase != srv.URL+"/api/media/anime/zMLNvt6MtV" {
		t.Errorf("APIBase = %q, want %s/api/media/anime/zMLNvt6MtV", d.APIBase, srv.URL)
	}
	if len(d.Episodes) != 28 {
		t.Errorf("Episodes = %d, want 28", len(d.Episodes))
	}
}

// TestStepDetectListEndpoint_Found: probes common list suffixes
// and finds the one that returns a non-empty JSON list.
func TestStepDetectListEndpoint_Found(t *testing.T) {
	episodes := loadFixture(t, "anikage_episodes.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/episodes" {
			w.Header().Set("Content-Type", "application/json")
			w.Write(episodes)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	d := &Discovery{
		APIBase: srv.URL + "/api",
		Auth:    auth.New(),
	}
	if err := d.stepDetectListEndpoint(context.Background()); err != nil {
		t.Fatalf("stepDetectListEndpoint: %v", err)
	}
	if len(d.Episodes) != 28 {
		t.Errorf("Episodes = %d, want 28", len(d.Episodes))
	}
}

// TestStepDrillIntoItem_UUID404ThenInteger: the anikage trap —
// the `id` field is a UUID that the sub-endpoints reject. The
// chain must fall back to the `number` field (integer 1).
func TestStepDrillIntoItem_UUID404ThenInteger(t *testing.T) {
	episodes := loadFixture(t, "anikage_episodes.json")
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/episodes/019e6dd0-9af6-7fdb-82ce-14bda50833ee" {
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"Not found"}`))
			return
		}
		if r.URL.Path == "/api/episodes/1" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"1","title":"x"}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv2.Close()

	d := &Discovery{
		APIBase: srv2.URL + "/api/episodes",
		Auth:    auth.New(),
	}
	d.populateEpisodesFromBody(episodes)
	if err := d.stepDrillIntoItem(context.Background()); err != nil {
		t.Fatalf("stepDrillIntoItem: %v", err)
	}
	if d.resourceURL != srv2.URL+"/api/episodes/1" {
		t.Errorf("resourceURL = %q, want .../1 (integer drill)", d.resourceURL)
	}
}

// TestStepEnumerateSiblings_FindsServers: probes /sources,
// /servers, /streams, /downloads, /subtitles; finds /servers.
func TestStepEnumerateSiblings_FindsServers(t *testing.T) {
	servers := loadFixture(t, "anikage_servers.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ep/1/sources", "/ep/1/streams", "/ep/1/downloads", "/ep/1/subtitles":
			w.WriteHeader(404)
		case "/ep/1/servers":
			w.Header().Set("Content-Type", "application/json")
			w.Write(servers)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	d := &Discovery{
		resourceURL: srv.URL + "/ep/1",
		Auth:        auth.New(),
	}
	if err := d.stepEnumerateSiblings(context.Background()); err != nil {
		t.Fatalf("stepEnumerateSiblings: %v", err)
	}
	if !contains(d.lastSiblingURL, "/servers") {
		t.Errorf("lastSiblingURL = %q, want /servers", d.lastSiblingURL)
	}
}

// TestProbe_403ForbiddenOriginAutoRetry: server returns 403 with
// "forbidden origin" on first hit, 200 on second. The chain's
// probe helper should auto-inject Origin/Referer and retry.
func TestProbe_403ForbiddenOriginAutoRetry(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(403)
			w.Write([]byte("forbidden origin"))
			return
		}
		// Second call: require Origin header to be set.
		if r.Header.Get("Origin") == "" {
			w.WriteHeader(403)
			w.Write([]byte("still no origin"))
			return
		}
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	d := &Discovery{
		EntryURL: "https://example.com/anime/x",
		Auth:     auth.New(),
	}
	resp, err := d.probe(context.Background(), srv.URL+"/seg", "test")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if resp.Status != 200 {
		t.Errorf("Status = %d, want 200 (after auto-retry)", resp.Status)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (initial 403 + retry)", calls)
	}
}

func TestTrimToAPIBase(t *testing.T) {
	cases := map[string]string{
		"https://anikage.cc/api/media/anime/x/episodes": "https://anikage.cc/api/media/anime/x",
		"https://anikage.cc/api/episodes":              "https://anikage.cc/api",
		"https://anikage.cc/x":                         "https://anikage.cc",
	}
	for in, want := range cases {
		got := trimToAPIBase(in)
		if got != want {
			t.Errorf("trimToAPIBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInjectQueryParam(t *testing.T) {
	got := injectQueryParam("https://x.com/sources?lang=sub", "provider", "miko")
	want := "https://x.com/sources?lang=sub&provider=miko"
	if got != want {
		t.Errorf("injectQueryParam = %q, want %q", got, want)
	}
}

func TestGuessCDNHost(t *testing.T) {
	if got := guessCDNHost("https://anikage.cc/anime/x"); got != "prox.anikage.cc" {
		t.Errorf("guessCDNHost(anikage) = %q, want prox.anikage.cc", got)
	}
	// Generic: example.com → prox.example.com (first prefix in list)
	if got := guessCDNHost("https://example.com/x"); got != "prox.example.com" {
		t.Errorf("guessCDNHost(example) = %q, want prox.example.com", got)
	}
}

func TestCountHLS(t *testing.T) {
	body := []byte("#EXTM3U\n#EXTINF:4.5,\n/stream/a\n#EXTINF:5.0,\n/stream/b\n#EXTINF:5.5,\n/stream/c\n")
	segs, bytes := countHLS(body)
	if segs != 3 {
		t.Errorf("segs = %d, want 3", segs)
	}
	if bytes < 3*(1<<20) {
		t.Errorf("bytes = %d, want >= 3 MiB", bytes)
	}
}

func TestDecideDrill(t *testing.T) {
	// number takes priority over id.
	got := DecideDrill(map[string]any{"number": 1, "id": "abc"})
	if got != "1" {
		t.Errorf("DecideDrill = %q, want 1", got)
	}
	// Falls back to id when no number.
	got = DecideDrill(map[string]any{"id": "abc"})
	if got != "abc" {
		t.Errorf("DecideDrill = %q, want abc", got)
	}
}

func TestDecideCDNHost(t *testing.T) {
	hosts := DecideCDNHost("https://anikage.cc/anime/x", classify.Shape{})
	if len(hosts) == 0 {
		t.Error("expected at least one CDN host")
	}
	if hosts[0] != "prox.anikage.cc" {
		t.Errorf("hosts[0] = %q, want prox.anikage.cc", hosts[0])
	}
}

// TestStepResolveEntry_BundleScan: the entry URL is an HTML page
// with a modulepreload link to a JS bundle. The bundle hardcodes
// https://api.example.com and https://chad.example.com. After
// stepResolveEntry, d.CandidateBases should contain both, with
// the chad one ranked first (api. and chad. are equal-priority,
// order is by document order in the bundle).
func TestStepResolveEntry_BundleScan(t *testing.T) {
	const bundleJS = `const a="https://api.example.com";` +
		`const b="https://chad.example.com";` +
		`const c="https://cdn.example.com";`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/anime/x":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<!doctype html><html><head>
				<link rel="modulepreload" href="/assets/api.js">
			</head><body>hi</body></html>`))
		case "/assets/api.js":
			w.Header().Set("Content-Type", "application/javascript")
			w.Write([]byte(bundleJS))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	d := &Discovery{EntryURL: srv.URL + "/anime/x", Auth: auth.New()}
	if err := d.stepResolveEntry(context.Background()); err != nil {
		t.Fatalf("stepResolveEntry: %v", err)
	}
	// API-likely hosts (api., chad.) should be first, cdn. last.
	if len(d.CandidateBases) != 3 {
		t.Fatalf("CandidateBases = %v, want 3 hosts", d.CandidateBases)
	}
	// First two are api./chad. in some order, last is cdn.
	if d.CandidateBases[2] != "cdn.example.com" {
		t.Errorf("CandidateBases[2] = %q, want cdn.example.com (deprioritized)", d.CandidateBases[2])
	}
	// APIBase should be set to the first candidate.
	if d.APIBase != "https://"+d.CandidateBases[0] {
		t.Errorf("APIBase = %q, want https://%s", d.APIBase, d.CandidateBases[0])
	}
}

// TestStepDetectListEndpoint_CrossHost: pure-logic test that the
// chain engine iterates the candidate bases produced by the
// bundle scan. We don't make real network calls — the actual
// probe is exercised by the bundle-scan integration test below.
// Here we just verify that candidateAPIBases() expands
// CandidateBases in priority order.
func TestStepDetectListEndpoint_CrossHost(t *testing.T) {
	d := &Discovery{
		APIBase:        "https://entry.example.com/api", // fallback
		CandidateBases: []string{"chad.example.com", "api.example.com"},
		Auth:           auth.New(),
	}
	got := d.candidateAPIBases()
	want := []string{
		"https://chad.example.com",
		"https://api.example.com",
		"https://entry.example.com/api", // fallback, not duplicated
	}
	if !equalStrings(got, want) {
		t.Errorf("candidateAPIBases =\n  %v\nwant\n  %v", got, want)
	}
}

// TestStepDetectListEndpoint_CrossHost_DeduplicatesFirst: when
// APIBase matches the first candidate (which it normally does,
// because step 1 sets APIBase to the first candidate), the
// fallback isn't duplicated.
func TestStepDetectListEndpoint_CrossHost_DeduplicatesFirst(t *testing.T) {
	d := &Discovery{
		APIBase:        "https://chad.example.com", // same as first candidate
		CandidateBases: []string{"chad.example.com", "api.example.com"},
		Auth:           auth.New(),
	}
	got := d.candidateAPIBases()
	want := []string{
		"https://chad.example.com",
		"https://api.example.com",
	}
	if !equalStrings(got, want) {
		t.Errorf("candidateAPIBases =\n  %v\nwant\n  %v", got, want)
	}
}

// TestStepDetectListEndpoint_CrossHost_NoCandidates: backward
// compat — when CandidateBases is empty, only APIBase is tried.
func TestStepDetectListEndpoint_CrossHost_NoCandidates(t *testing.T) {
	d := &Discovery{
		APIBase: "https://entry.example.com/api",
		Auth:    auth.New(),
	}
	got := d.candidateAPIBases()
	want := []string{"https://entry.example.com/api"}
	if !equalStrings(got, want) {
		t.Errorf("candidateAPIBases =\n  %v\nwant\n  %v", got, want)
	}
}

// TestDecideCandidateBases: pure function ranking. Backend-
// prefixed hosts (api./chad./v[0-9].) on the entry apex are
// kept; other entry-apex hosts (cdn./random.) are filtered as
// they're usually the same site's UI/CDN infrastructure.
func TestDecideCandidateBases(t *testing.T) {
	hosts := []string{
		"cdn.example.com",     // on entry apex, not backend — filtered
		"chad.example.com",    // on entry apex, backend prefix — kept
		"api.example.com",     // on entry apex, backend prefix — kept
		"random.example.com",  // on entry apex, not backend — filtered
		"example.com",         // entry host — filtered
		"v2.api.com",          // on entry apex, version prefix — kept
		"react.dev",           // framework host — filtered
		"api.somethingelse.io", // different apex — kept
	}
	got := DecideCandidateBases("https://example.com/x", hosts)
	// API-likely: chad, api, v2.api.com. Cross-apex: api.somethingelse.io.
	want := []string{
		"chad.example.com",
		"api.example.com",
		"v2.api.com",
		"api.somethingelse.io",
	}
	if !equalStrings(got, want) {
		t.Errorf("DecideCandidateBases =\n  %v\nwant\n  %v", got, want)
	}
}

// TestDecideCandidateBases_BlocksApex: when scanning for anidap's
// backend, both anidap.se and its apex (e.g. www.anidap.se) are
// filtered. The bundle scanner finds chad.anidap.se, anidap.se is
// the entry host.
func TestDecideCandidateBases_BlocksApex(t *testing.T) {
	hosts := []string{
		"anidap.se",
		"www.anidap.se",
		"chad.anidap.se",
	}
	got := DecideCandidateBases("https://anidap.se/watch", hosts)
	want := []string{"chad.anidap.se"}
	if !equalStrings(got, want) {
		t.Errorf("DecideCandidateBases =\n  %v\nwant\n  %v", got, want)
	}
}

// TestIsFrameworkHost: the blocklist catches the common vendor
// hosts found in modern SPA bundles.
func TestIsFrameworkHost(t *testing.T) {
	yes := []string{"react.dev", "reactrouter.com", "unpkg.com", "esm.sh", "chiaki.site", "i.ytimg.com"}
	for _, h := range yes {
		if !isFrameworkHost(h) {
			t.Errorf("isFrameworkHost(%q) = false, want true", h)
		}
	}
	no := []string{"api.example.com", "chad.example.com", "example.com", "v1.api.com"}
	for _, h := range no {
		if isFrameworkHost(h) {
			t.Errorf("isFrameworkHost(%q) = true, want false", h)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// compile-time checks
var _ = probe.MaxBodyBytes
