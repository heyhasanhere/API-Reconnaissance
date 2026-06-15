// Package classify turns a probe.Response into a Shape. The Shape
// is what the chain engine pattern-matches on: "is this a list of
// episodes? a list of providers? a sources object with a CDN key?
// a 403 forbidden origin tell? an m3u8 playlist?"
//
// The classifier is a single pure function. It does not probe
// anything itself. It does not know about downloads, recipes, or
// the chain engine. It only knows about response shapes.
package classify

import (
	"encoding/json"
	"mime"
	"net/http"
	"regexp"
	"strings"

	"github.com/heyhasanhere/API-Reconnaissance/pkg/probe"
)

// Kind is the high-level shape of a response.
type Kind string

const (
	KindUnknown     Kind = "unknown"
	KindHTML        Kind = "html"
	KindJSON        Kind = "json"
	KindJSONList    Kind = "json_list"
	KindHLSMaster   Kind = "hls_master"
	KindHLSVariant  Kind = "hls_variant"
	KindDASH        Kind = "dash"
	KindSegmentList Kind = "segment_list"
	KindDirect      Kind = "direct"
	KindError       Kind = "error"
	KindRedirect    Kind = "redirect"
	KindForm        Kind = "form"
)

// Shape is the result of classifying a probe.Response. Fields are
// optional — only the ones relevant to the detected Kind are
// populated.
type Shape struct {
	Kind          Kind
	ContentType   string
	ItemCount     int      // for json_list
	IDFields      []string // candidate fields for drilling
	ProviderList  []string // for /servers-style responses
	StreamKey     string   // the opaque `url` field from /sources
	StreamIsM3U8  bool
	StreamIsEmbed bool
	CrossHost     string // extracted host from a discovered URL field
	ErrorMessage  string
	MissingValues []string // values lifted from error messages
	FormAction    string
	FormFields    []string
	Reasoning     string // human-readable explanation
}

// providerMissingRE matches common "provider X is required / not
// found / unsupported" error messages. The capture group is the
// provider name.
var providerMissingRE = regexp.MustCompile(`(?i)provider\s+["']?([a-z0-9_-]+)["']?\s+(?:is\s+required|not\s+found|unsupported|invalid)`)

// providerParamRequiredRE matches the "provider query param is
// required" tell — it doesn't name a provider, just tells us we
// need to enumerate via /servers. The chain engine uses the
// boolean hit on this as a separate signal.
var providerParamRequiredRE = regexp.MustCompile(`(?i)provider\s+query\s+param\s+is\s+required`)

// episodeMissingRE matches "episode N not found" / "no episodes for
// provider X" — used to extract provider names from server errors.
var episodeMissingRE = regexp.MustCompile(`(?i)no\s+episodes\s+(?:found\s+)?for\s+provider\s+["']?([a-z0-9_-]+)["']?`)

// forbiddenOriginRE matches the 403 forbidden origin tell.
var forbiddenOriginRE = regexp.MustCompile(`(?i)forbidden\s+origin`)

// m3u8VariantMarker is the playlist-line marker that says "this is
// a variant, not a master."
const m3u8VariantMarker = "#EXTINF"

// m3u8MasterMarker is the playlist-line marker that says "this is
// a master."
const m3u8MasterMarker = "#EXT-X-STREAM-INF"

// dashMarker is the manifest root element for DASH.
const dashMarker = "<MPD"

// Classify inspects resp and returns a Shape. reqURL is the URL
// that was probed (for cross-host extraction and reasoning).
func Classify(resp *probe.Response, reqURL string) Shape {
	if resp == nil {
		return Shape{Kind: KindUnknown, Reasoning: "nil response"}
	}

	ct := canonicalContentType(resp.Headers.Get("Content-Type"))

	// 1. Redirects — by status code, before content-type sniffing.
	if resp.Status >= 300 && resp.Status < 400 {
		return Shape{
			Kind:        KindRedirect,
			ContentType: ct,
			Reasoning:   "3xx status; Location: " + resp.Headers.Get("Location"),
		}
	}

	// 2. 4xx/5xx — error envelope. The body might still be useful
	// (it's almost always JSON with a message we can mine for
	// hints).
	if resp.Status >= 400 {
		return classifyError(resp, ct, reqURL)
	}

	// 3. m3u8 — by content-type first, then by body sniff, then
	// by path (the CDN sometimes returns video/* with a /m3u8/
	// path; trust the path, not the content-type).
	if isM3U8(ct) || looksLikeM3U8(resp.Body) || hasM3U8Path(reqURL) {
		return classifyM3U8(resp, ct, reqURL)
	}

	// 4. DASH manifest.
	if isDASH(ct) || looksLikeDASH(resp.Body) {
		return Shape{
			Kind:        KindDASH,
			ContentType: ct,
			Reasoning:   "DASH manifest",
		}
	}

	// 5. JSON — object or list?
	if isJSON(ct) || looksLikeJSON(resp.Body) {
		return classifyJSON(resp, ct, reqURL)
	}

	// 6. HTML / form.
	if isHTML(ct) || looksLikeHTML(resp.Body) {
		return classifyHTML(resp, ct, reqURL)
	}

	// 7. Direct file — known video extension with large body.
	if isDirect(resp, reqURL) {
		return Shape{
			Kind:        KindDirect,
			ContentType: ct,
			Reasoning:   "video extension with " + humanBytes(int64(len(resp.Body))),
		}
	}

	return Shape{
		Kind:        KindUnknown,
		ContentType: ct,
		Reasoning:   "no rule matched; content-type=" + ct,
	}
}

// classifyError handles 4xx/5xx. The body is usually JSON; we
// extract the message and look for provider names.
func classifyError(resp *probe.Response, ct, reqURL string) Shape {
	s := Shape{
		Kind:        KindError,
		ContentType: ct,
	}

	// Mine the body for useful information regardless of
	// content-type.
	body := string(resp.Body)
	s.ErrorMessage = extractErrorMessage(resp.Body)

	// Provider name in error message → candidate provider.
	if m := providerMissingRE.FindStringSubmatch(body); len(m) >= 2 {
		s.MissingValues = append(s.MissingValues, m[1])
	}
	if m := episodeMissingRE.FindStringSubmatch(body); len(m) >= 2 {
		s.MissingValues = append(s.MissingValues, m[1])
	}
	// "provider query param is required" — surface as a literal
	// MissingValues entry so the chain engine can branch on it.
	if providerParamRequiredRE.MatchString(body) {
		s.MissingValues = append(s.MissingValues, "provider")
	}

	// 403 forbidden origin is a special tell.
	if resp.Status == 403 && forbiddenOriginRE.MatchString(body) {
		s.Reasoning = "403 forbidden origin — add Origin/Referer headers for the page host"
	} else {
		s.Reasoning = "error response: " + s.ErrorMessage
	}
	return s
}

// extractErrorMessage pulls a human message from common JSON error
// envelopes. Returns the body itself if no envelope is found.
func extractErrorMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}

	// Try {"message": "..."}
	var m1 struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &m1) == nil && m1.Message != "" {
		return m1.Message
	}

	// Try {"error": "..."} or {"error": {"message": "..."}}
	var m2 struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(body, &m2) == nil && m2.Error != nil {
		if s, ok := m2.Error.(string); ok {
			return s
		}
		var m2b struct {
			Message string `json:"message"`
		}
		b, _ := json.Marshal(m2.Error)
		if json.Unmarshal(b, &m2b) == nil && m2b.Message != "" {
			return m2b.Message
		}
	}

	// Try {"success": false, "error": {"message": "..."}}
	var m3 struct {
		Success bool `json:"success"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &m3) == nil && !m3.Success && m3.Error.Message != "" {
		return m3.Error.Message
	}

	// Fall back to the raw body, trimmed.
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// classifyM3U8 distinguishes master from variant by looking for the
// EXT-X-STREAM-INF marker.
func classifyM3U8(resp *probe.Response, ct, reqURL string) Shape {
	body := string(resp.Body)
	if strings.Contains(body, m3u8MasterMarker) {
		return Shape{
			Kind:        KindHLSMaster,
			ContentType: ct,
			Reasoning:   "master playlist (contains EXT-X-STREAM-INF)",
		}
	}
	if strings.Contains(body, m3u8VariantMarker) {
		return Shape{
			Kind:        KindHLSVariant,
			ContentType: ct,
			Reasoning:   "variant playlist (contains EXTINF)",
		}
	}
	// m3u8 with no markers — treat as variant anyway; the
	// downloader will fail clearly if it's malformed.
	return Shape{
		Kind:        KindHLSVariant,
		ContentType: ct,
		Reasoning:   "m3u8 with no markers — treating as variant",
	}
}

// classifyJSON handles application/json responses. A top-level array
// is a list endpoint; an object is inspected for sources, providers,
// or episode sub-objects.
func classifyJSON(resp *probe.Response, ct, reqURL string) Shape {
	body := resp.Body
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) == 0 {
		return Shape{Kind: KindJSON, ContentType: ct, Reasoning: "empty JSON body"}
	}

	// Top-level array → list.
	if trimmed[0] == '[' {
		return classifyJSONList(body, ct, reqURL)
	}

	// Object.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return Shape{Kind: KindJSON, ContentType: ct, Reasoning: "object parse error: " + err.Error()}
	}

	s := Shape{Kind: KindJSON, ContentType: ct}

	// Sources object — the anikage /sources shape.
	if raw, ok := obj["sources"]; ok {
		populateSourcesFields(&s, raw)
	}

	// Provider list at top level — uncommon but possible.
	if raw, ok := obj["providers"]; ok {
		var pl []struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &pl) == nil {
			for _, p := range pl {
				s.ProviderList = append(s.ProviderList, p.ID)
			}
		}
	}

	// Cross-host — if any field is a URL, lift the host.
	for _, v := range obj {
		var str string
		if json.Unmarshal(v, &str) == nil && strings.HasPrefix(str, "http") {
			s.CrossHost = hostOf(str)
			break
		}
	}

	s.Reasoning = "JSON object"
	return s
}

// classifyJSONList handles a top-level JSON array. We pick the
// first object and look for id-like fields.
func classifyJSONList(body []byte, ct, reqURL string) Shape {
	var arr []json.RawMessage
	if err := json.Unmarshal(body, &arr); err != nil {
		return Shape{Kind: KindJSONList, ContentType: ct, Reasoning: "array parse error: " + err.Error()}
	}

	s := Shape{
		Kind:        KindJSONList,
		ContentType: ct,
		ItemCount:   len(arr),
	}

	// Inspect the first element for id-like fields and the
	// provider-list shape.
	if len(arr) > 0 {
		var first map[string]json.RawMessage
		if json.Unmarshal(arr[0], &first) == nil {
			// Provider list shape: [{id, default?, label?, ...},
			// ...] with short ids (≤ 24 chars, no UUIDs) and ≤ 5
			// fields per object. The anikage /servers response
			// fits; the anikage /episodes response has 10+ fields
			// per object with UUIDs, so it doesn't.
			if _, hasID := first["id"]; hasID && len(arr) <= 32 && len(first) <= 5 {
				var provs []struct {
					ID      string `json:"id"`
					Default bool   `json:"default"`
				}
				if json.Unmarshal(body, &provs) == nil && len(provs) == len(arr) {
					allShort := true
					for _, p := range provs {
						if len(p.ID) > 24 {
							allShort = false
							break
						}
					}
					if allShort {
						for _, p := range provs {
							s.ProviderList = append(s.ProviderList, p.ID)
						}
						s.Reasoning = "looks like a provider list (short id, ≤ 5 fields per item)"
						return s
					}
				}
			}

			// Generic id field detection.
			for _, name := range []string{"id", "uuid", "number", "episode", "order", "slug"} {
				if _, ok := first[name]; ok {
					s.IDFields = append(s.IDFields, name)
				}
			}
		}
	}

	s.Reasoning = "JSON list with " + itoa(s.ItemCount) + " items"
	return s
}

// populateSourcesFields reads the `sources` array and populates
// StreamKey, StreamIsM3U8, StreamIsEmbed on s.
func populateSourcesFields(s *Shape, raw json.RawMessage) {
	var srcs []struct {
		URL      string `json:"url"`
		IsM3U8   bool   `json:"isM3U8"`
		Type     string `json:"type"`
		EmbedURL string `json:"embedUrl"`
	}
	if err := json.Unmarshal(raw, &srcs); err != nil || len(srcs) == 0 {
		return
	}
	first := srcs[0]
	s.StreamKey = first.URL
	s.StreamIsM3U8 = first.IsM3U8
	s.StreamIsEmbed = first.EmbedURL != "" || first.Type == "embed"
}

// classifyHTML handles text/html. If there's a <form>, upgrade to
// KindForm.
func classifyHTML(resp *probe.Response, ct, reqURL string) Shape {
	s := Shape{Kind: KindHTML, ContentType: ct}
	body := string(resp.Body)
	lower := strings.ToLower(body)

	// Extract form action.
	if m := formActionRE.FindStringSubmatch(lower); len(m) >= 2 {
		s.Kind = KindForm
		s.FormAction = m[1]
		// Extract input names.
		for _, m := range inputNameRE.FindAllStringSubmatch(lower, -1) {
			s.FormFields = append(s.FormFields, m[1])
		}
	}

	// Pull any <script src="/api/..."> hints.
	if api := scriptAPIHint(lower); api != "" {
		s.CrossHost = hostOf(api)
	}

	s.Reasoning = "HTML"
	return s
}

var (
	formActionRE = regexp.MustCompile(`<form[^>]+action=["']([^"']+)["']`)
	inputNameRE  = regexp.MustCompile(`<input[^>]+name=["']([^"']+)["']`)
	scriptSrcRE  = regexp.MustCompile(`<script[^>]+src=["']([^"']+)["']`)
)

func scriptAPIHint(lower string) string {
	for _, m := range scriptSrcRE.FindAllStringSubmatch(lower, -1) {
		if strings.Contains(m[1], "/api/") {
			return m[1]
		}
	}
	return ""
}

// isDirect returns true if the URL ends in a known video extension
// and the body is large.
func isDirect(resp *probe.Response, reqURL string) bool {
	if len(resp.Body) < 1<<20 {
		return false
	}
	lower := strings.ToLower(reqURL)
	for _, ext := range []string{".mp4", ".mkv", ".webm", ".mov", ".m4v", ".ts"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// canonicalContentType strips parameters from Content-Type.
func canonicalContentType(ct string) string {
	if ct == "" {
		return ""
	}
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.TrimSpace(strings.ToLower(ct))
}

func isM3U8(ct string) bool {
	return ct == "application/vnd.apple.mpegurl" || ct == "application/x-mpegurl"
}

// hasM3U8Path returns true if the URL path contains /m3u8/ —
// used as a fallback signal when the content-type is wrong
// (some CDNs return video/* for what is actually a playlist).
func hasM3U8Path(rawURL string) bool {
	// Cheap parse: look for /m3u8/ in the path part only.
	i := strings.Index(rawURL, "?")
	path := rawURL
	if i >= 0 {
		path = rawURL[:i]
	}
	return strings.Contains(path, "/m3u8/") || strings.HasSuffix(path, "/m3u8")
}

func isDASH(ct string) bool {
	return ct == "application/dash+xml"
}

func isJSON(ct string) bool {
	return ct == "application/json" || strings.HasSuffix(ct, "+json")
}

func isHTML(ct string) bool {
	return ct == "text/html" || ct == "application/xhtml+xml"
}

// looksLikeM3U8 / looksLikeDASH / looksLikeJSON / looksLikeHTML
// sniff the body when the content-type is missing or wrong.
func looksLikeM3U8(body []byte) bool {
	s := strings.TrimSpace(string(body))
	return strings.HasPrefix(s, "#EXTM3U")
}

func looksLikeDASH(body []byte) bool {
	s := strings.TrimSpace(string(body))
	return strings.HasPrefix(s, dashMarker)
}

func looksLikeJSON(body []byte) bool {
	s := strings.TrimSpace(string(body))
	return s != "" && (s[0] == '{' || s[0] == '[')
}

func looksLikeHTML(body []byte) bool {
	s := strings.TrimSpace(string(body))
	lower := strings.ToLower(s)
	return strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html")
}

func hostOf(rawURL string) string {
	// We don't import net/url here to keep the package's import
	// graph tight; manual scheme/host parse is enough.
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return ""
	}
	rest := rawURL
	if strings.HasPrefix(rest, "https://") {
		rest = rest[len("https://"):]
	} else {
		rest = rest[len("http://"):]
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func humanBytes(n int64) string {
	const (
		KiB = 1024
		MiB = 1024 * 1024
	)
	switch {
	case n >= MiB:
		return itoa(int(n/MiB)) + " MiB"
	case n >= KiB:
		return itoa(int(n/KiB)) + " KiB"
	default:
		return itoa(int(n)) + " B"
	}
}

// Compile-time check that mime is used; we import it for future
// use but currently parse content-types manually.
var _ = mime.FormatMediaType
var _ = http.StatusOK
