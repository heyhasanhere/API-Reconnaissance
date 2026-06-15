// Package chain is the adaptive discovery engine. Given an
// entry URL on a video-streaming site, it runs a 7-step pipeline
// that probes, classifies, and decides what to do next:
//
//  1. ResolveEntry — fetch the entry URL, find the API base
//  2. DetectListEndpoint — find a JSON list endpoint
//  3. DrillIntoItem — derive an item URL from the first list entry
//  4. EnumerateSiblings — probe common sibling suffixes
//  5. EnumerateProviders — discover available providers
//  6. ResolveStream — find the CDN key and resolve it to a URL
//  7. ClassifyPlaylist — fetch the playlist, classify it
//
// Each step is implemented as a method on Discovery. Steps call
// helpers in strategies.go for adaptive decisions.
//
// The package is *not* an interface. The seven steps are called
// in order from Run; if any step returns an error, Run stops and
// returns the partial discovery.
package chain

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/heyhasanhere/API-Reconnaissance/pkg/auth"
	"github.com/heyhasanhere/API-Reconnaissance/pkg/classify"
	"github.com/heyhasanhere/API-Reconnaissance/pkg/probe"
)

// Discovery is the running state. Run builds it up across the 7
// steps. Callers can inspect Steps / Logs after Run returns.
type Discovery struct {
	EntryURL string
	APIBase  string // e.g. https://anikage.cc/api/media/anime/zMLNvt6MtV
	Auth     *auth.Store

	Steps []Step
	Logs  []string

	Episodes  []Episode
	Providers []Provider
	Final     *FinalState

	// Working state used across steps.
	resourceURL     string // /api/.../episodes/{n} once drilled
	listPath        string // e.g. /episodes — appended to APIBase in step 3
	streamKey       string // the opaque `url` field from /sources
	streamCDN       string // resolved CDN host, e.g. prox.anikage.cc
	lastSiblingURL  string
	lastSiblingBody []byte
	lastSiblingSha  classify.Shape
}

// Step records one probe and its outcome.
type Step struct {
	URL     string
	Method  string
	Shape   classify.Shape
	Outcome string // "ok" | "miss" | "abort" | "info"
	Note    string
}

// Episode is one item from a list endpoint. Number is the integer
// the path uses; ID is whatever the list returned (may be a UUID).
type Episode struct {
	Number int
	Title  string
	ID     string
}

// Provider is one streaming provider.
type Provider struct {
	Name      string
	IsHLS     bool
	IsEmbed   bool
	StreamKey string
	Note      string
}

// FinalState is what Run populates on success.
type FinalState struct {
	StreamURL    string
	StreamKind   classify.Kind
	StreamCDN    string
	Segments     int
	BytesEst     int64
	RequiredHdrs map[string]string
}

// Run executes the 7 steps and returns a populated Discovery.
// ctx is propagated to every probe.
func Run(ctx context.Context, entryURL string) (*Discovery, error) {
	if entryURL == "" {
		return nil, fmt.Errorf("chain: empty entry URL")
	}
	d := &Discovery{
		EntryURL: entryURL,
		Auth:     auth.New(),
	}

	if err := d.stepResolveEntry(ctx); err != nil {
		return d, err
	}
	if err := d.stepDetectListEndpoint(ctx); err != nil {
		return d, err
	}
	if err := d.stepDrillIntoItem(ctx); err != nil {
		return d, err
	}
	if err := d.stepEnumerateSiblings(ctx); err != nil {
		return d, err
	}
	if err := d.stepEnumerateProviders(ctx); err != nil {
		return d, err
	}
	if err := d.stepResolveStream(ctx); err != nil {
		return d, err
	}
	if err := d.stepClassifyPlaylist(ctx); err != nil {
		return d, err
	}

	return d, nil
}

// --- step 1: resolve the entry URL to an API base ---

func (d *Discovery) stepResolveEntry(ctx context.Context) error {
	resp, err := d.probe(ctx, d.EntryURL, "entry URL")
	if err != nil {
		return fmt.Errorf("step 1 (resolve entry): %w", err)
	}
	shape := classify.Classify(resp, d.EntryURL)
	d.recordStep(d.EntryURL, "GET", shape, "ok", "entry URL")

	if shape.Kind == classify.KindHTML || shape.Kind == classify.KindForm {
		// Look for API hints in the page.
		if shape.CrossHost != "" {
			// Found a /api/ path in a <script> src. Use its base.
			if api := apiBaseFromScript(shape.CrossHost); api != "" {
				d.APIBase = api
				d.log("detected API base from <script>: %s", d.APIBase)
				return nil
			}
		}
		// No script hint. Try the common /api prefix.
		if u, err := url.Parse(d.EntryURL); err == nil {
			d.APIBase = u.Scheme + "://" + u.Host + "/api"
			d.log("no <script> hint, trying %s as API base", d.APIBase)
			return nil
		}
		return fmt.Errorf("could not extract API base from HTML entry")
	}

	if shape.Kind == classify.KindJSONList {
		// The entry URL itself is a list endpoint. Save the
		// listPath (e.g. /episodes) so step 3 knows what to
		// append to the API base.
		u, _ := url.Parse(d.EntryURL)
		d.APIBase, d.listPath = splitListPath(d.EntryURL)
		_ = u
		d.log("entry URL is a list endpoint; API base = %s, list path = %s", d.APIBase, d.listPath)
		// Pre-populate episodes since the user gave us the list.
		d.populateEpisodesFromBody(resp.Body)
		return nil
	}

	if shape.Kind == classify.KindJSON {
		d.APIBase = trimToAPIBase(d.EntryURL)
		d.log("entry URL is a JSON endpoint; API base = %s", d.APIBase)
		return nil
	}

	return fmt.Errorf("entry URL returned %s — expected HTML, JSON, or JSON list", shape.Kind)
}

// --- step 2: find a list endpoint ---

func (d *Discovery) stepDetectListEndpoint(ctx context.Context) error {
	if len(d.Episodes) > 0 {
		// Step 1 already populated episodes. Skip.
		return nil
	}

	// Try common list suffixes.
	suffixes := []string{"/episodes", "/items", "/list", "/catalog", "/browse", "/videos", "/posts"}
	for _, suf := range suffixes {
		u := d.APIBase + suf
		resp, err := d.probe(ctx, u, "list endpoint candidate")
		if err != nil {
			continue
		}
		shape := classify.Classify(resp, u)
		if shape.Kind == classify.KindJSONList && shape.ItemCount > 0 {
			d.recordStep(u, "GET", shape, "ok", "found list endpoint")
			d.populateEpisodesFromBody(resp.Body)
			return nil
		}
		d.recordStep(u, "GET", shape, "miss", "not a list")
	}

	return fmt.Errorf("step 2: no list endpoint found under %s (tried %d suffixes)", d.APIBase, len(suffixes))
}

// --- step 3: drill into one item ---

func (d *Discovery) stepDrillIntoItem(ctx context.Context) error {
	if len(d.Episodes) == 0 {
		return fmt.Errorf("step 3: no episodes to drill into")
	}
	first := d.Episodes[0]
	d.log("drilling into episode %d (%q)", first.Number, first.Title)

	// Try the various ID fields in order: number, id, slug.
	candidates := []struct {
		name  string
		value string
	}{
		{"number", fmt.Sprintf("%d", first.Number)},
		{"id", first.ID},
	}

	// resourceURL is base + first episode's identifier. If we
	// know the list path (e.g. /episodes), include it.
	prefix := d.APIBase
	if d.listPath != "" {
		prefix = prefix + d.listPath
	}
	for _, c := range candidates {
		if c.value == "" {
			continue
		}
		u := prefix + "/" + c.value
		resp, err := d.probe(ctx, u, "drill candidate")
		if err != nil {
			continue
		}
		shape := classify.Classify(resp, u)
		if shape.Kind == classify.KindError {
			d.recordStep(u, "GET", shape, "miss", fmt.Sprintf("drill on %s returned 4xx/5xx", c.name))
			continue
		}
		// 200 with JSON, HTML, or list — that's the resource URL.
		d.resourceURL = u
		d.recordStep(u, "GET", shape, "ok", fmt.Sprintf("drilled via %s", c.name))
		d.log("resource URL: %s", d.resourceURL)
		return nil
	}

	// Fallback: some APIs (like anikage) don't expose a resource
	// endpoint at all — only its children. In that case, the
	// "resource URL" is the synthetic prefix that the children
	// share. Set it to the prefix we just tried (with the
	// first working id) and let step 4 probe siblings.
	for _, c := range candidates {
		if c.value == "" {
			continue
		}
		d.resourceURL = prefix + "/" + c.value
		d.log("no resource endpoint; treating %s as synthetic base for siblings", d.resourceURL)
		return nil
	}

	return fmt.Errorf("step 3: drill failed for episode %d on all id fields", first.Number)
}

// --- step 4: enumerate siblings ---

func (d *Discovery) stepEnumerateSiblings(ctx context.Context) error {
	if d.resourceURL == "" {
		return fmt.Errorf("step 4: no resource URL")
	}

	siblings := []string{"/sources", "/servers", "/streams", "/downloads", "/subtitles"}
	for _, suf := range siblings {
		u := d.resourceURL + suf
		resp, err := d.probe(ctx, u, "sibling candidate")
		if err != nil {
			continue
		}
		shape := classify.Classify(resp, u)
		if shape.Kind == classify.KindError {
			d.recordStep(u, "GET", shape, "miss", "sibling returned error")
			continue
		}
		if shape.Kind == classify.KindJSON || shape.Kind == classify.KindJSONList {
			// Found a useful sibling. Record the URL for step 5.
			d.recordStep(u, "GET", shape, "ok", "sibling found")
			d.lastSiblingURL = u
			d.lastSiblingSha = shape
			d.lastSiblingBody = resp.Body
			return nil
		}
		d.recordStep(u, "GET", shape, "miss", fmt.Sprintf("sibling returned %s", shape.Kind))
	}

	return fmt.Errorf("step 4: no usable sibling under %s (tried %d)", d.resourceURL, len(siblings))
}

// lastSiblingURL / lastSiblingShape / lastSiblingBody are the
// fields step 4 leaves for step 5 to use.

// --- step 5: enumerate providers ---

func (d *Discovery) stepEnumerateProviders(ctx context.Context) error {
	if d.lastSiblingURL == "" {
		return fmt.Errorf("step 5: no sibling to inspect")
	}

	// If the sibling is a list of short-id objects, treat as
	// provider list. The classifier populates ProviderList.
	if d.lastSiblingSha.ProviderList != nil {
		for _, name := range d.lastSiblingSha.ProviderList {
			d.Providers = append(d.Providers, Provider{Name: name})
		}
		d.log("found %d providers from sibling: %s", len(d.Providers), joinNames(d.Providers))
		return nil
	}

	// Otherwise the sibling is a single sources object. Treat
	// the URL as a sources endpoint that needs ?provider=.
	// Discover providers by hitting /servers separately.
	serversURL := d.resourceURL + "/servers"
	resp, err := d.probe(ctx, serversURL, "servers fallback")
	if err == nil {
		shape := classify.Classify(resp, serversURL)
		if shape.Kind == classify.KindJSONList && len(shape.ProviderList) > 0 {
			for _, name := range shape.ProviderList {
				d.Providers = append(d.Providers, Provider{Name: name})
			}
			d.recordStep(serversURL, "GET", shape, "ok", "providers from /servers")
			return nil
		}
	}

	return fmt.Errorf("step 5: could not enumerate providers")
}

// --- step 6: resolve the stream URL ---

func (d *Discovery) stepResolveStream(ctx context.Context) error {
	if len(d.Providers) == 0 {
		return fmt.Errorf("step 6: no providers to probe")
	}

	// For each provider, fetch the sources endpoint and try to
	// resolve a stream URL. Stop at the first HLS provider; for
	// other types, record what we found and continue.
	for i := range d.Providers {
		p := &d.Providers[i]
		sourcesURL := d.lastSiblingURL
		// If the sibling is /servers (i.e. we just got the list),
		// we need /sources instead. Fall back to a derived URL.
		if strings.HasSuffix(sourcesURL, "/servers") {
			sourcesURL = strings.TrimSuffix(sourcesURL, "/servers") + "/sources"
		}
		// Inject ?provider= into the URL.
		sourcesURL = injectQueryParam(sourcesURL, "provider", p.Name)

		resp, err := d.probe(ctx, sourcesURL, fmt.Sprintf("sources for %s", p.Name))
		if err != nil {
			d.log("provider %s: probe error %v", p.Name, err)
			continue
		}
		shape := classify.Classify(resp, sourcesURL)
		if shape.Kind == classify.KindError {
			d.recordStep(sourcesURL, "GET", shape, "miss", shape.ErrorMessage)
			// Mine the error message for a provider name.
			for _, v := range shape.MissingValues {
				if v == p.Name || isProviderName(v) {
					d.log("error hints at provider %q", v)
				}
			}
			continue
		}
		if shape.Kind != classify.KindJSON {
			d.recordStep(sourcesURL, "GET", shape, "miss", fmt.Sprintf("expected JSON, got %s", shape.Kind))
			continue
		}
		if shape.StreamIsEmbed {
			p.IsEmbed = true
			p.Note = "third-party embeds"
			d.recordStep(sourcesURL, "GET", shape, "info", "embeds, not a stream")
			continue
		}
		if shape.StreamKey == "" {
			p.Note = "no url field"
			d.recordStep(sourcesURL, "GET", shape, "miss", "no url field")
			continue
		}

		// We have a stream key. Resolve to a URL.
		p.StreamKey = shape.StreamKey
		p.IsHLS = shape.StreamIsM3U8
		d.streamKey = shape.StreamKey
		d.streamCDN = shape.CrossHost

		// If the key is already a URL, use it.
		if strings.HasPrefix(shape.StreamKey, "http://") || strings.HasPrefix(shape.StreamKey, "https://") {
			d.recordStep(sourcesURL, "GET", shape, "ok", fmt.Sprintf("stream URL: %s", shape.StreamKey))
			return nil
		}

		// Otherwise resolve via CDN host. The CDN host may be in
		// shape.CrossHost (from a discovered URL field), or we
		// can guess from the page host.
		cdn := d.streamCDN
		if cdn == "" {
			cdn = guessCDNHost(d.EntryURL)
		}
		if cdn == "" {
			d.recordStep(sourcesURL, "GET", shape, "miss", "no CDN host discovered")
			continue
		}
		// Try /m3u8/{key} then /stream/{key}.
		for _, prefix := range []string{"/m3u8/", "/stream/"} {
			candidate := "https://" + cdn + prefix + shape.StreamKey
			r, err := d.probeWithAuth(ctx, candidate, "stream candidate")
			if err != nil {
				continue
			}
			if r.Status >= 200 && r.Status < 400 {
				d.streamCDN = cdn
				d.recordStep(candidate, "GET", classify.Classify(r, candidate), "ok", "stream resolved")
				return nil
			}
		}
		d.recordStep(sourcesURL, "GET", shape, "miss", "CDN resolve failed")
	}

	return fmt.Errorf("step 6: no provider produced a working stream URL")
}

// --- step 7: classify the playlist ---

func (d *Discovery) stepClassifyPlaylist(ctx context.Context) error {
	if d.streamKey == "" {
		return fmt.Errorf("step 7: no stream key to resolve")
	}

	// Build the playlist URL from the resolved CDN.
	var playlistURL string
	if strings.HasPrefix(d.streamKey, "http://") || strings.HasPrefix(d.streamKey, "https://") {
		playlistURL = d.streamKey
	} else if d.streamCDN != "" {
		// We resolved via /m3u8/ or /stream/ in step 6; the
		// candidate that worked is what we want.
		playlistURL = "https://" + d.streamCDN + "/m3u8/" + d.streamKey
	} else {
		return fmt.Errorf("step 7: cannot build playlist URL")
	}

	resp, err := d.probeWithAuth(ctx, playlistURL, "playlist")
	if err != nil {
		return fmt.Errorf("step 7: %w", err)
	}
	shape := classify.Classify(resp, playlistURL)
	d.recordStep(playlistURL, "GET", shape, "ok", "playlist classified")

	final := &FinalState{
		StreamURL:    playlistURL,
		StreamKind:   shape.Kind,
		StreamCDN:    d.streamCDN,
		RequiredHdrs: d.Auth.HeadersFor(d.streamCDN),
	}

	// If HLS variant, count segments and estimate bytes.
	if shape.Kind == classify.KindHLSVariant {
		segs, totalBytes := countHLS(resp.Body)
		final.Segments = segs
		final.BytesEst = totalBytes
	}
	if shape.Kind == classify.KindHLSMaster {
		// For master, we don't have a fixed segment count; estimate
		// from a single probe of the highest-bandwidth variant.
		final.Segments = 0
		final.BytesEst = 0
	}
	d.Final = final
	d.log("playlist: kind=%s segments=%d est_bytes=%d",
		shape.Kind, final.Segments, final.BytesEst)
	return nil
}

// --- helpers ---

// probe does an HTTP GET with the auth headers for the URL's host
// and records the request/response in the auth store on 403.
func (d *Discovery) probe(ctx context.Context, rawURL, note string) (*probe.Response, error) {
	host := auth.HostOf(rawURL)
	headers := d.Auth.HeadersFor(host)
	resp, err := probe.Do(ctx, probe.Request{URL: rawURL, Headers: headers})
	if err != nil {
		d.log("probe %s: error %v", note, err)
		return nil, err
	}
	// 403 with forbidden origin → record page host as Origin/Referer.
	if resp.Status == 403 {
		body := string(resp.Body)
		if strings.Contains(strings.ToLower(body), "forbidden origin") {
			pageHost := auth.HostOf(d.EntryURL)
			pageOrigin := "https://" + pageHost
			d.Auth.InjectForbiddenOrigin(host, pageOrigin)
			d.log("403 forbidden origin on %s; injected Origin/Referer for next probe", host)
			// Retry once with the new headers.
			headers = d.Auth.HeadersFor(host)
			resp, err = probe.Do(ctx, probe.Request{URL: rawURL, Headers: headers})
			if err != nil {
				return nil, err
			}
		}
	}
	return resp, nil
}

// probeWithAuth is an alias for probe — kept distinct for the
// call sites that need to make the auth-injection intent clear.
func (d *Discovery) probeWithAuth(ctx context.Context, rawURL, note string) (*probe.Response, error) {
	return d.probe(ctx, rawURL, note)
}

// populateEpisodesFromBody parses a JSON list and fills d.Episodes.
func (d *Discovery) populateEpisodesFromBody(body []byte) {
	type item struct {
		ID      string `json:"id"`
		Number  int    `json:"number"`
		Episode int    `json:"episode"`
		Order   int    `json:"order"`
		Title   string `json:"title"`
		Name    string `json:"name"`
	}
	var arr []item
	if err := jsonUnmarshal(body, &arr); err != nil {
		return
	}
	for _, x := range arr {
		n := x.Number
		if n == 0 {
			n = x.Episode
		}
		if n == 0 {
			n = x.Order
		}
		title := x.Title
		if title == "" {
			title = x.Name
		}
		d.Episodes = append(d.Episodes, Episode{
			Number: n, Title: title, ID: x.ID,
		})
	}
}

func (d *Discovery) recordStep(url, method string, shape classify.Shape, outcome, note string) {
	d.Steps = append(d.Steps, Step{
		URL: url, Method: method, Shape: shape, Outcome: outcome, Note: note,
	})
}

func (d *Discovery) log(format string, args ...any) {
	d.Logs = append(d.Logs, fmt.Sprintf(format, args...))
}

// --- pure helpers (also used by strategies.go) ---

// trimToAPIBase returns the URL up to the last path segment. For
// https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes it
// returns https://anikage.cc/api/media/anime/zMLNvt6MtV.
func trimToAPIBase(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) <= 1 {
		return u.Scheme + "://" + u.Host
	}
	u.Path = "/" + strings.Join(parts[:len(parts)-1], "/")
	return u.String()
}

// splitListPath returns (apiBase, listPath) where listPath is the
// last path segment of rawURL (e.g. "/episodes"). For
// /api/media/anime/zMLNvt6MtV/episodes →
//   (https://anikage.cc/api/media/anime/zMLNvt6MtV, /episodes)
func splitListPath(rawURL string) (string, string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) <= 1 {
		return u.Scheme + "://" + u.Host, ""
	}
	listPath := "/" + parts[len(parts)-1]
	parts = parts[:len(parts)-1]
	u.Path = "/" + strings.Join(parts, "/")
	return u.String(), listPath
}

// apiBaseFromScript extracts the API base from a /api/... path
// found in a <script src>.
func apiBaseFromScript(scriptPath string) string {
	if !strings.HasPrefix(scriptPath, "/") {
		return ""
	}
	i := strings.Index(scriptPath, "/api/")
	if i < 0 {
		return ""
	}
	return scriptPath[:i+len("/api")]
}

// injectQueryParam adds or replaces a query parameter.
func injectQueryParam(rawURL, key, value string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String()
}

// guessCDNHost returns a likely CDN host based on the page host.
// For anikage.cc → prox.anikage.cc. For example.com, the generic
// guess is prox.example.com (same base, different prefix). Returns
// "" if no guess can be made.
func guessCDNHost(entryURL string) string {
	host := auth.HostOf(entryURL)
	// Known patterns — keep small. The chain engine also tries
	// "<page-host with first label swapped for common prefixes>"
	// via the strategies.
	switch host {
	case "anikage.cc":
		return "prox.anikage.cc"
	}
	// Generic guess: same domain with a common CDN prefix.
	// We return just the first guess; the chain engine can
	// try the rest via DecideCDNHost.
	prefixes := []string{"prox.", "cdn.", "stream.", "media."}
	for _, prefix := range prefixes {
		return prefix + host
	}
	return ""
}

// isProviderName is a loose check on whether a string looks like
// a provider id.
func isProviderName(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func joinNames(ps []Provider) string {
	ns := make([]string, len(ps))
	for i, p := range ps {
		ns[i] = p.Name
	}
	return strings.Join(ns, ", ")
}

// countHLS counts #EXTINF lines and returns (count, estBytes)
// where estBytes is the sum of #EXT-X-BYTERANGE lengths or
// 0 if not declared. Used only for size estimates.
func countHLS(body []byte) (int, int64) {
	text := string(body)
	segs := strings.Count(text, "#EXTINF:")
	if segs == 0 {
		return 0, 0
	}
	// Rough estimate: 1 MiB per segment (anikage's actual size).
	const bytesPerSeg = 1 << 20
	return segs, int64(segs) * bytesPerSeg
}

// hlsVariantRE is exported via strategies.go.

// jsonUnmarshal is the standard library call wrapped so the import
// is local to this file.
var jsonUnmarshal = json.Unmarshal
