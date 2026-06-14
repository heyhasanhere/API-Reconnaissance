// Package shape classifies HTTP responses into a Shape the rest of
// api-recon can act on. The classifier is the brain that powers
// sibling discovery (graph), informed fuzzing (fuzz), the download
// interpreter (download), and REPL heuristic suggestions.
//
// The classifier is stateless. Construct with New, share across
// goroutines, call Classify as often as you like.
package shape

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

// Kind is the broad type of response.
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
	KindRedirect    Kind = "redirect"
	KindError       Kind = "error"
	KindForm        Kind = "form"
)

// String returns the kind name. Used for tag values like
// "shape:json_list".
func (k Kind) String() string { return string(k) }

// Shape is the classifier's output. Only fields relevant to the Kind
// are populated; the rest are zero values.
type Shape struct {
	Kind         Kind
	ContentType  string
	Status       int

	// For lists: count and detected ID field(s).
	ItemCount int
	IDFields  []string

	// For pagination: the param name and observed value.
	PaginationParam string
	PaginationValue string

	// For HLS: the variant path or the segment list size.
	VariantPath  string
	SegmentCount int

	// For errors: the literal message and any extracted "missing value."
	ErrorMessage  string
	MissingValues []string

	// For HTML forms: the action URL, method, and field names.
	FormAction string
	FormMethod string
	FormFields []string

	// Cross-host: the host in the body that differs from the request.
	CrossHost string

	// Human-readable reasoning. The REPL shows this when the shape
	// is unusual: "detected cross-host URL to prox.anikage.cc."
	Reasoning string
}

// Classifier is stateless. Construct once with New(), pass everywhere.
type Classifier struct {
	missingValueRE *regexp.Regexp
	streamInfRE    *regexp.Regexp
	segmentLineRE  *regexp.Regexp
	formActionRE   *regexp.Regexp
	formInputRE    *regexp.Regexp
	hlsURLRE       *regexp.Regexp
}

// New returns a fresh Classifier with precompiled regexes.
func New() *Classifier {
	return &Classifier{
		// "provider {X} not found", "missing {X}", "no {X} available",
		// "{X} is required", etc. Greedy enough to be useful; the
		// caller is expected to filter implausible values.
		missingValueRE: regexp.MustCompile(`(?i)(?:provider|missing|requires?|needs?)\s+["']?([A-Za-z0-9_.-]+)["']?`),
		streamInfRE:    regexp.MustCompile(`(?m)^#EXT-X-STREAM-INF`),
		segmentLineRE:  regexp.MustCompile(`(?m)^/stream/`),
		formActionRE:   regexp.MustCompile(`(?is)<form[^>]*\saction=["']([^"']+)["']`),
		formInputRE:    regexp.MustCompile(`(?is)<input[^>]*\sname=["']([^"']+)["']`),
		// HLS segment URLs are usually /stream/<key> or absolute URLs.
		hlsURLRE: regexp.MustCompile(`(?m)^(?:https?://[^\s]+|/stream/[^\s]+)$`),
	}
}

// Classify is the one entry point. The response's Body is consumed
// by the caller; the classifier reads the bytes and does not touch
// the body. The response itself is read for headers and status only.
//
// requestURL is the URL that produced this response (used to detect
// cross-host URLs in the body). May be empty.
func (c *Classifier) Classify(resp *http.Response, body []byte, requestURL string) Shape {
	if resp == nil {
		return Shape{Kind: KindUnknown, Reasoning: "nil response"}
	}
	ct := contentType(resp.Header.Get("Content-Type"))
	status := resp.StatusCode

	// Rule 1: redirects.
	if status >= 300 && status < 400 {
		loc := resp.Header.Get("Location")
		return Shape{
			Kind:        KindRedirect,
			ContentType: ct,
			Status:      status,
			Reasoning:   "redirect to " + loc,
		}
	}

	// Rule 2: 5xx and 4xx.
	if status >= 400 {
		return c.classifyError(status, ct, body)
	}

	// Rule 3 & 4: HLS / DASH.
	if strings.HasPrefix(ct, "application/vnd.apple.mpegurl") || bytes.Contains(body, []byte("#EXTM3U")) {
		return c.classifyHLS(status, ct, body)
	}
	if strings.HasPrefix(ct, "application/dash+xml") {
		return Shape{
			Kind:        KindDASH,
			ContentType: ct,
			Status:      status,
			Reasoning:   "DASH manifest",
		}
	}

	// Rule 5: HTML / Form.
	if strings.HasPrefix(ct, "text/html") || bytes.HasPrefix(body, []byte("<!DOCTYPE")) || bytes.HasPrefix(body, []byte("<html")) {
		return c.classifyHTML(status, ct, body)
	}

	// Rule 6: JSON.
	if strings.HasPrefix(ct, "application/json") || looksLikeJSON(body) {
		return c.classifyJSON(status, ct, body, requestURL)
	}

	// Rule 7: direct file by extension.
	if isDirectFile(requestURL) {
		return Shape{
			Kind:        KindDirect,
			ContentType: ct,
			Status:      status,
			Reasoning:   "URL has video extension",
		}
	}

	// Rule 8: HLS as JSON (array of URL strings).
	if looksLikeJSONArray(body) && allItemsLookLikeURLs(body) {
		var urls []string
		_ = json.Unmarshal(body, &urls)
		return Shape{
			Kind:         KindSegmentList,
			ContentType:  ct,
			Status:       status,
			ItemCount:    len(urls),
			SegmentCount: len(urls),
			Reasoning:    "JSON array of URL strings",
		}
	}

	return Shape{
		Kind:        KindUnknown,
		ContentType: ct,
		Status:      status,
		Reasoning:   "no rule matched",
	}
}

// contentType returns the media type, lowercased and stripped of
// parameters ("text/html; charset=utf-8" → "text/html").
func contentType(h string) string {
	if i := strings.Index(h, ";"); i >= 0 {
		h = h[:i]
	}
	return strings.ToLower(strings.TrimSpace(h))
}

// classifyError handles 4xx and 5xx. We try to parse the body as
// JSON and extract any "missing value" patterns from the error
// message.
func (c *Classifier) classifyError(status int, ct string, body []byte) Shape {
	s := Shape{
		Kind:        KindError,
		ContentType: ct,
		Status:      status,
	}

	// Try to find an error message in JSON envelopes.
	if message := extractErrorMessage(body); message != "" {
		s.ErrorMessage = message
		s.MissingValues = c.missingValueRE.FindAllString(message, -1)
		// The regex returns the full match ("provider pahe"). Strip
		// the prefix to leave just the value.
		var values []string
		for _, m := range s.MissingValues {
			parts := strings.Fields(m)
			if len(parts) > 0 {
				values = append(values, parts[len(parts)-1])
			}
		}
		s.MissingValues = values
		s.Reasoning = "error: " + truncate(message, 80)
	} else {
		s.Reasoning = "error with no parseable message"
	}
	return s
}

// extractErrorMessage looks for `error.message`, `message`, or `error`
// fields in a JSON envelope and returns the deepest string it can
// find. Returns "" if nothing useful.
func extractErrorMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var any any
	if err := json.Unmarshal(body, &any); err != nil {
		return ""
	}
	return findString(any, []string{"error.message", "error", "message", "msg"})
}

// findString walks a JSON value along the given dotted path. If
// any path resolves to a string, returns it. Tries each path in
// order; returns the first hit.
func findString(v any, paths []string) string {
	for _, p := range paths {
		if s, ok := walk(v, p); ok {
			return s
		}
	}
	return ""
}

func walk(v any, path string) (string, bool) {
	parts := strings.Split(path, ".")
	cur := v
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = m[p]
		if !ok {
			return "", false
		}
	}
	if s, ok := cur.(string); ok {
		return s, true
	}
	return "", false
}

// classifyHLS differentiates master from variant.
func (c *Classifier) classifyHLS(status int, ct string, body []byte) Shape {
	if c.streamInfRE.Match(body) {
		// Master: extract the first /stream/... line.
		variant := ""
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "/stream/") {
				variant = line
				break
			}
		}
		return Shape{
			Kind:        KindHLSMaster,
			ContentType: ct,
			Status:      status,
			VariantPath: variant,
			Reasoning:   "HLS master playlist",
		}
	}
	// Variant: count segments.
	count := 0
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if c.hlsURLRE.MatchString(line) {
			count++
		}
	}
	return Shape{
		Kind:         KindHLSVariant,
		ContentType:  ct,
		Status:       status,
		SegmentCount: count,
		Reasoning:    "HLS variant playlist",
	}
}

// classifyHTML detects forms.
func (c *Classifier) classifyHTML(status int, ct string, body []byte) Shape {
	s := Shape{
		Kind:        KindHTML,
		ContentType: ct,
		Status:      status,
		Reasoning:   "HTML",
	}
	if m := c.formActionRE.FindStringSubmatch(string(body)); len(m) >= 2 {
		s.Kind = KindForm
		s.FormAction = m[1]
		// Default to GET if no method attribute.
		s.FormMethod = "GET"
		if re := regexp.MustCompile(`(?is)<form[^>]*\smethod=["']([^"']+)["']`); re != nil {
			if mm := re.FindStringSubmatch(string(body)); len(mm) >= 2 {
				s.FormMethod = strings.ToUpper(mm[1])
			}
		}
		// Extract field names.
		seen := map[string]bool{}
		for _, m := range c.formInputRE.FindAllStringSubmatch(string(body), -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				s.FormFields = append(s.FormFields, m[1])
			}
		}
		s.Reasoning = "HTML form with " + itoa(len(s.FormFields)) + " fields"
	}
	return s
}

// classifyJSON handles top-level array vs object, ID detection, and
// cross-host URL detection.
func (c *Classifier) classifyJSON(status int, ct string, body []byte, requestURL string) Shape {
	s := Shape{
		ContentType: ct,
		Status:      status,
	}

	// Try to parse.
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		s.Kind = KindUnknown
		s.Reasoning = "JSON failed to parse: " + truncate(err.Error(), 60)
		return s
	}

	switch v := parsed.(type) {
	case []any:
		// A JSON array of URL strings is a segment list, not a list of records.
		if len(v) > 0 && allStringsAreURLs(v) {
			s.Kind = KindSegmentList
			s.ItemCount = len(v)
			s.SegmentCount = len(v)
			s.Reasoning = "json array of URL strings (" + itoa(len(v)) + " items)"
			return s
		}
		s.Kind = KindJSONList
		s.ItemCount = len(v)
		s.IDFields, s.PaginationParam, s.PaginationValue = describeList(v, body)
		if s.PaginationParam != "" {
			s.Reasoning = "list of " + itoa(s.ItemCount) + " items; pagination param " + s.PaginationParam
		} else {
			s.Reasoning = "list of " + itoa(s.ItemCount) + " items"
		}
	case map[string]any:
		s.Kind = KindJSON
		// Cross-host detection: if there's a `sources` array with
		// objects containing a `url`, and that URL's host differs
		// from the request host, set CrossHost.
		if requestURL != "" {
			if ch := findCrossHost(v, requestURL); ch != "" {
				s.CrossHost = ch
				s.Reasoning = "json with sources pointing at " + ch
			} else {
				s.Reasoning = "json object"
			}
		} else {
			s.Reasoning = "json object"
		}
	default:
		s.Kind = KindUnknown
		s.Reasoning = "json with non-list, non-object root"
	}
	return s
}

// describeList inspects a list's first object to find ID fields and
// detect pagination params in the request URL. requestURL is unused
// here; pagination is detected by scanning for common field names.
func describeList(items []any, _ /*body*/ []byte) (idFields []string, pageParam, pageValue string) {
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		// Priority order for ID fields.
		for _, name := range []string{"id", "uuid", "slug", "name", "key", "_id"} {
			if _, has := m[name]; has {
				idFields = append(idFields, name)
				break
			}
		}
		// If we have at least one item, also look for any field
		// that ends in "_id" or "Id".
		for k := range m {
			if k == "id" || k == "uuid" || k == "slug" {
				continue
			}
			if strings.HasSuffix(k, "_id") || strings.HasSuffix(k, "Id") {
				if !contains(idFields, k) {
					idFields = append(idFields, k)
				}
			}
		}
		break // only inspect first item
	}
	return
}

// findCrossHost looks for a "sources" key with a list of objects
// each containing a "url" key, and returns the host of the first
// URL that differs from requestHost.
func findCrossHost(v map[string]any, requestURL string) string {
	requestHost := hostOf(requestURL)
	sources, ok := v["sources"].([]any)
	if !ok {
		// Also try "data" and "results" — common API shapes.
		for _, k := range []string{"data", "results", "items"} {
			if list, ok := v[k].([]any); ok {
				sources = list
				break
			}
		}
	}
	for _, s := range sources {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		u, ok := m["url"].(string)
		if !ok {
			continue
		}
		h := hostOf(u)
		if h != "" && h != requestHost {
			return h
		}
	}
	return ""
}

// hostOf extracts the host from a URL string. Returns "" for empty
// or unparseable input.
func hostOf(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	// Fast path: find "://" and the next "/".
	i := strings.Index(rawURL, "://")
	if i < 0 {
		return ""
	}
	rest := rawURL[i+3:]
	if j := strings.Index(rest, "/"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// looksLikeJSON returns true if body starts with '{' or '[' after
// trimming whitespace, OR if it parses as JSON. Used as a fallback
// when Content-Type is missing or wrong.
func looksLikeJSON(body []byte) bool {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}

func looksLikeJSONArray(body []byte) bool {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '['
}

// allItemsLookLikeURLs returns true if body is a JSON array whose
// every element is a string starting with http://, https://, or /.
func allItemsLookLikeURLs(body []byte) bool {
	var arr []any
	if err := json.Unmarshal(body, &arr); err != nil {
		return false
	}
	if len(arr) == 0 {
		return false
	}
	return allStringsAreURLs(arr)
}

// allStringsAreURLs is the in-memory version of allItemsLookLikeURLs,
// called after we already parsed the JSON.
func allStringsAreURLs(arr []any) bool {
	if len(arr) == 0 {
		return false
	}
	for _, v := range arr {
		s, ok := v.(string)
		if !ok {
			return false
		}
		if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "/") {
			return false
		}
	}
	return true
}

// isDirectFile returns true if the URL ends in a known video
// extension. The download interpreter will use this to pick a
// strategy. We don't check content-length here — the caller can
// add that if needed.
func isDirectFile(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	for _, ext := range []string{".mp4", ".mkv", ".webm", ".mov", ".m4v", ".avi"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// itoa is a small helper to avoid importing strconv just for tests.
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

// truncate returns s shortened to n bytes, with "…" appended if
// truncation happened.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return s[:n-1] + "…"
}

// contains is a tiny helper.
func contains(s []string, want string) bool {
	for _, x := range s {
		if x == want {
			return true
		}
	}
	return false
}
