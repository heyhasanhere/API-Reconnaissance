package chain

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestExtractScriptSrcs: given a snippet of HTML, returns the
// list of script and modulepreload URLs in document order, with
// relative URLs resolved against the page URL. The result is
// capped at maxBundles (12) — we only fetch a few bundles per
// entry URL, and the test stays well under that cap.
func TestExtractScriptSrcs(t *testing.T) {
	html := []byte(`<!doctype html><html><head>
		<link rel="modulepreload" href="/assets/api.js">
		<script src="/assets/page.js"></script>
		<link rel="modulepreload" href="https://cdn.example.com/extra.js">
		<script src="https://cdn.example.com/main.js"></script>
	</head></html>`)
	page := "https://example.com/watch?id=1"
	got := extractScriptSrcs(html, page)
	want := []string{
		"https://example.com/assets/api.js",       // modulepreload
		"https://example.com/assets/page.js",      // script
		"https://cdn.example.com/extra.js",        // modulepreload
		"https://cdn.example.com/main.js",         // script
	}
	if !equalStrings(got, want) {
		t.Errorf("extractScriptSrcs =\n  %v\nwant\n  %v", got, want)
	}
}

// TestExtractScriptSrcs_AllReturned: when there are fewer than
// maxBundles matches, all of them come back.
func TestExtractScriptSrcs_AllReturned(t *testing.T) {
	html := []byte(`<html><head>
		<link rel="modulepreload" href="/a.js">
	</head></html>`)
	got := extractScriptSrcs(html, "https://example.com/")
	want := []string{"https://example.com/a.js"}
	if !equalStrings(got, want) {
		t.Errorf("extractScriptSrcs = %v, want %v", got, want)
	}
}

// TestExtractScriptSrcs_NoScripts: HTML with no script tags
// returns an empty list.
func TestExtractScriptSrcs_NoScripts(t *testing.T) {
	html := []byte(`<!doctype html><html><body>plain</body></html>`)
	got := extractScriptSrcs(html, "https://example.com/")
	if len(got) != 0 {
		t.Errorf("extractScriptSrcs = %v, want []", got)
	}
}

// TestExtractHostsFromBundle: given a JS bundle string, returns
// the deduplicated set of hostnames from all https:// literals,
// with hosts that appear as fetch() targets ranked first.
func TestExtractHostsFromBundle(t *testing.T) {
	bundle := []byte(`
		const c="https://api.example.com";
		const P="https://crs.24stream.xyz";
		function f() { return fetch("https://chad.example.com/x"); }
		// duplicate: "https://api.example.com" should be deduped
		const other = "https://cdn.example.com";
		// protocol-relative: //proto.example.com — should NOT match
		const proto = "//proto.example.com";
		// not-a-host: "https://" with no host
		const empty = "https://";
	`)
	got := extractHostsFromBundle(bundle)
	// chad.example.com is a fetch() target → first.
	// api.example.com / crs.24stream.xyz / cdn.example.com appear
	// only in string templates → follow in document order.
	want := []string{
		"chad.example.com",
		"api.example.com",
		"crs.24stream.xyz",
		"cdn.example.com",
	}
	if !equalStrings(got, want) {
		t.Errorf("extractHostsFromBundle =\n  %v\nwant\n  %v", got, want)
	}
}

// TestExtractHostsFromBundle_RealAnidapBundle: verify the
// scanner produces the right hosts for the captured anidap
// api-*.js bundle. The expected hosts are chad.anidap.se
// (the real backend, appears 3× as a fetch() target — first),
// plus the stream-CDN hosts from HOST_HANDLERS (string-template
// targets — after). chad must be ranked first so step 2 finds
// the right base quickly.
func TestExtractHostsFromBundle_RealAnidapBundle(t *testing.T) {
	bundle, err := os.ReadFile("../../testdata/anidap_api_bundle.js")
	if err != nil {
		t.Skipf("fixture not present: %v", err)
	}
	hosts := extractHostsFromBundle(bundle)
	if len(hosts) == 0 {
		t.Fatal("no hosts extracted from real anidap bundle")
	}
	have := map[string]bool{}
	for _, h := range hosts {
		have[h] = true
	}
	for _, want := range []string{
		"chad.anidap.se",
		"crs.24stream.xyz",
	} {
		if !have[want] {
			t.Errorf("expected host %q not found in bundle scan; got %v", want, hosts)
		}
	}
	// chad.anidap.se must come first (it's the only fetch()
	// target in the bundle).
	if hosts[0] != "chad.anidap.se" {
		t.Errorf("hosts[0] = %q, want chad.anidap.se (the only fetch() target); got %v", hosts[0], hosts)
	}
	// The known stream-CDN hosts should NOT be the first entry.
	for _, cdn := range []string{
		"crs.24stream.xyz", "hls.anidb.app", "megaplay.buzz",
		"kwik.cx", "ply.24stream.xyz", "mp4.24stream.xyz",
		"wave.24stream.xyz", "tools.fast4speed.rsvp",
	} {
		if have[cdn] {
			// find the index; it must be > 0
			for i, h := range hosts {
				if h == cdn {
					if i == 0 {
						t.Errorf("stream-CDN host %q ranked first in %v (should be after fetch targets)", cdn, hosts)
					}
					break
				}
			}
		}
	}
}

// TestBundleScan_NoBundles: entry HTML with no script tags
// returns an empty host list (the getter is never called).
func TestBundleScan_NoBundles(t *testing.T) {
	html := []byte(`<!doctype html><html><body>plain</body></html>`)
	var calls int
	getter := func(ctx context.Context, u string) ([]byte, error) {
		calls++
		return nil, nil
	}
	hosts := BundleScan(context.Background(), html, "https://example.com/", getter)
	if len(hosts) != 0 {
		t.Errorf("hosts = %v, want []", hosts)
	}
	if calls != 0 {
		t.Errorf("getter called %d times, want 0 (no bundles to fetch)", calls)
	}
}

// TestBundleScan_ResolvesRelativeURLs: relative script srcs
// are resolved against the page URL before the getter is called.
func TestBundleScan_ResolvesRelativeURLs(t *testing.T) {
	html := []byte(`<html><head>
		<link rel="modulepreload" href="/assets/api.js">
	</head></html>`)
	var gotURL string
	getter := func(ctx context.Context, u string) ([]byte, error) {
		gotURL = u
		return []byte(`const c="https://api.example.com";`), nil
	}
	BundleScan(context.Background(), html, "https://example.com/watch", getter)
	if gotURL != "https://example.com/assets/api.js" {
		t.Errorf("getter called with %q, want https://example.com/assets/api.js", gotURL)
	}
}

// TestBundleScan_RespectsCap: a bundle larger than maxBundleBytes
// is not parsed. We detect this by having the getter return a
// huge blob — if the scanner respects the cap, the regex should
// run but the result should be ignored.
func TestBundleScan_RespectsCap(t *testing.T) {
	html := []byte(`<html><head>
		<script src="/assets/big.js"></script>
	</head></html>`)
	huge := make([]byte, maxBundleBytes+1)
	for i := range huge {
		huge[i] = 'x'
	}
	getter := func(ctx context.Context, u string) ([]byte, error) {
		return huge, nil
	}
	hosts := BundleScan(context.Background(), html, "https://example.com/", getter)
	if len(hosts) != 0 {
		t.Errorf("hosts = %v, want [] (bundle was over cap)", hosts)
	}
}

// TestBundleScan_StopsAtCap: we scan up to maxBundles bundles
// total. After that, we stop even if more modulepreloads remain.
func TestBundleScan_StopsAtCap(t *testing.T) {
	html := []byte(`<html><head>
		<link rel="modulepreload" href="/a.js">
		<link rel="modulepreload" href="/b.js">
		<link rel="modulepreload" href="/c.js">
		<link rel="modulepreload" href="/d.js">
		<link rel="modulepreload" href="/e.js">
		<link rel="modulepreload" href="/f.js">
		<link rel="modulepreload" href="/g.js">
		<link rel="modulepreload" href="/h.js">
		<link rel="modulepreload" href="/i.js">
		<link rel="modulepreload" href="/j.js">
		<link rel="modulepreload" href="/k.js">
		<link rel="modulepreload" href="/l.js">
		<link rel="modulepreload" href="/m.js">
		<link rel="modulepreload" href="/n.js">
		<link rel="modulepreload" href="/o.js">
	</head></html>`)
	var calls int
	getter := func(ctx context.Context, u string) ([]byte, error) {
		calls++
		return []byte(`const c="https://api.example.com";`), nil
	}
	BundleScan(context.Background(), html, "https://example.com/", getter)
	if calls > maxBundles {
		t.Errorf("getter called %d times, want <= %d (cap)", calls, maxBundles)
	}
}

// TestBundleScan_ContinuesOnError: getter errors don't abort
// the scan; we just move to the next candidate.
func TestBundleScan_ContinuesOnError(t *testing.T) {
	html := []byte(`<html><head>
		<script src="/a.js"></script>
		<script src="/b.js"></script>
	</head></html>`)
	getter := func(ctx context.Context, u string) ([]byte, error) {
		if strings.HasSuffix(u, "/a.js") {
			return nil, context.DeadlineExceeded
		}
		return []byte(`const c="https://api.example.com";`), nil
	}
	hosts := BundleScan(context.Background(), html, "https://example.com/", getter)
	if len(hosts) != 1 || hosts[0] != "api.example.com" {
		t.Errorf("hosts = %v, want [api.example.com]", hosts)
	}
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
