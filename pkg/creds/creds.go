// Package creds captures, stores, and re-injects authentication
// material found in HTTP traffic. The store is post-process: callers
// (the capture pipeline, the harvest driver) hand it
// (request, response) pairs, and the store picks out Authorization,
// Set-Cookie, X-API-Key, and "required" non-auth headers (Origin,
// Referer). On replay, Inject assembles them back into a header map.
//
// The store is concurrency-safe.
package creds

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/heyhasanhere/API-Reconnaissance/pkg/recipe"
)

// Store holds captured credentials. Construct with New, share across
// goroutines, call Observe / ObserveHeaders as traffic flows, then
// Inject to apply them to a new request.
type Store struct {
	mu       sync.Mutex
	Bearer   string
	Cookies  map[string]string
	APIKey   string
	Required map[string]string // Origin, Referer, etc.
	Sources  map[string]string // header name → "first seen at URL"
}

// New returns an empty Store.
func New() *Store {
	return &Store{
		Cookies:  map[string]string{},
		Required: map[string]string{},
		Sources:  map[string]string{},
	}
}

// Observe records credentials seen in a captured request/response
// pair. Both may be nil (in which case the corresponding half is
// skipped). Idempotent: last write wins for credentials of the
// same kind. The first URL each credential was seen at is kept in
// Sources so the REPL can say "this token came from /api/login —
// try refreshing there on 401."
func (s *Store) Observe(req *http.Request, resp *http.Response) {
	if req != nil {
		s.ObserveHeaders(req.Header, nil, req.URL.String())
	}
	if resp != nil {
		s.ObserveHeaders(nil, resp.Header, lastReqURL(resp))
	}
}

// ObserveHeaders is a lighter entry point for the Playwright JSONL
// pipeline, where we have header maps but not full *http.Request /
// *http.Response values. Either may be nil.
func (s *Store) ObserveHeaders(reqH, respH http.Header, reqURL string) {
	if reqH != nil {
		s.extractFromRequest(reqH, reqURL)
	}
	if respH != nil {
		s.extractFromResponse(respH, reqURL)
	}
}

func (s *Store) extractFromRequest(h http.Header, reqURL string) {
	// Authorization: Bearer <token>
	if v := h.Get("Authorization"); v != "" {
		s.setBearer(extractBearer(v), reqURL)
	}
	// X-API-Key: <key>
	if v := h.Get("X-API-Key"); v != "" {
		s.setAPIKey(v, reqURL)
	}
	// Cookie: a=1; b=2
	if v := h.Get("Cookie"); v != "" {
		for name, val := range parseCookies(v) {
			s.setCookie(name, val, reqURL)
		}
	}
	// Required non-auth headers: Origin, Referer, and a few others
	// that targets commonly insist on.
	for _, name := range []string{"Origin", "Referer", "X-Requested-With", "User-Agent"} {
		if v := h.Get(name); v != "" {
			s.setRequired(name, v, reqURL)
		}
	}
}

func (s *Store) extractFromResponse(h http.Header, reqURL string) {
	// Set-Cookie: name=value; ... — Go's Header.Values gives one
	// string per Set-Cookie line, which is what we want.
	for _, line := range h.Values("Set-Cookie") {
		name, val, ok := parseSetCookie(line)
		if !ok {
			continue
		}
		s.setCookie(name, val, reqURL)
	}
	// Some APIs return a token in a response header.
	if v := h.Get("X-API-Key"); v != "" {
		s.setAPIKey(v, reqURL)
	}
	if v := h.Get("Authorization"); v != "" {
		s.setBearer(extractBearer(v), reqURL)
	}
}

// setBearer stores a bearer token. Newer, longer tokens win (so
// refreshes supersede bootstraps).
func (s *Store) setBearer(token, source string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Bearer == "" || len(token) >= len(s.Bearer) {
		s.Bearer = token
	}
	if source != "" && s.Sources["Authorization"] == "" {
		s.Sources["Authorization"] = source
	}
}

func (s *Store) setAPIKey(key, source string) {
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.APIKey == "" || len(key) >= len(s.APIKey) {
		s.APIKey = key
	}
	if source != "" && s.Sources["X-API-Key"] == "" {
		s.Sources["X-API-Key"] = source
	}
}

func (s *Store) setCookie(name, value, source string) {
	if name == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.Cookies[name]; !ok || len(value) >= len(existing) {
		s.Cookies[name] = value
	}
	if source != "" && s.Sources["Cookie:"+name] == "" {
		s.Sources["Cookie:"+name] = source
	}
}

func (s *Store) setRequired(name, value, source string) {
	if name == "" || value == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Required[name] = value
	if source != "" && s.Sources[name] == "" {
		s.Sources[name] = source
	}
}

// Inject returns a copy of base with all stored credentials and
// required headers applied. Does not mutate base. forURL is the URL
// the new request will target; if non-nil, the Inject may scope
// required headers by host (e.g. only send Origin/Referer to the
// original domain).
func (s *Store) Inject(base http.Header, forURL *url.URL) http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := http.Header{}
	for k, v := range base {
		out[k] = append([]string(nil), v...)
	}
	if s.Bearer != "" {
		out.Set("Authorization", "Bearer "+s.Bearer)
	}
	if s.APIKey != "" {
		out.Set("X-API-Key", s.APIKey)
	}
	if len(s.Cookies) > 0 {
		// Reassemble the Cookie header.
		names := make([]string, 0, len(s.Cookies))
		for k := range s.Cookies {
			names = append(names, k)
		}
		sort.Strings(names)
		var parts []string
		for _, n := range names {
			parts = append(parts, n+"="+s.Cookies[n])
		}
		out.Set("Cookie", strings.Join(parts, "; "))
	}
	for k, v := range s.Required {
		// Don't trample a header the caller explicitly set.
		if out.Get(k) == "" {
			out.Set(k, v)
		}
	}
	return out
}

// AsRecipeAuth converts the store contents to recipe.Auth for
// saving. Captures every Required header, every cookie as a single
// session_cookie string, the bearer, and the API key.
func (s *Store) AsRecipeAuth() recipe.Auth {
	s.mu.Lock()
	defer s.mu.Unlock()
	auth := recipe.Auth{
		RequiredHeaders: map[string]string{},
		BearerToken:     s.Bearer,
		APIKey:          s.APIKey,
	}
	for k, v := range s.Required {
		auth.RequiredHeaders[k] = v
	}
	if len(s.Cookies) > 0 {
		names := make([]string, 0, len(s.Cookies))
		for k := range s.Cookies {
			names = append(names, k)
		}
		sort.Strings(names)
		var parts []string
		for _, n := range names {
			parts = append(parts, n+"="+s.Cookies[n])
		}
		auth.SessionCookie = strings.Join(parts, "; ")
	}
	return auth
}

// LoadFromRecipe hydrates a Store from a saved recipe.Auth.
func LoadFromRecipe(auth recipe.Auth) *Store {
	s := New()
	s.Bearer = auth.BearerToken
	s.APIKey = auth.APIKey
	if auth.SessionCookie != "" {
		for name, val := range parseCookies(auth.SessionCookie) {
			s.Cookies[name] = val
		}
	}
	for k, v := range auth.RequiredHeaders {
		s.Required[k] = v
	}
	return s
}

// HasCredentials returns true if the store has anything injectable.
func (s *Store) HasCredentials() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Bearer != "" || s.APIKey != "" || len(s.Cookies) > 0 || len(s.Required) > 0
}

// SourceURL returns the URL at which the given credential was first
// observed. Returns "" if not seen. Useful for the REPL suggestion
// "token came from /api/login — try refreshing there on 401."
func (s *Store) SourceURL(credential string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Sources[credential]
}

// extractBearer returns the token from "Bearer xxx" (case-insensitive).
// Returns v unchanged if it doesn't look like "Bearer X".
func extractBearer(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(strings.ToLower(v), "bearer ") {
		return strings.TrimSpace(v[len("bearer "):])
	}
	return v
}

// parseCookies parses a Cookie header into a name→value map. The
// format is "a=1; b=2" — domain, path, expires, etc. are not relevant
// here.
func parseCookies(header string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		i := strings.IndexByte(part, '=')
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(part[:i])
		val := strings.TrimSpace(part[i+1:])
		if name != "" {
			out[name] = val
		}
	}
	return out
}

// parseSetCookie parses a single Set-Cookie line. We ignore
// attributes (domain, path, expires, etc.) and only return the
// name=value pair.
func parseSetCookie(line string) (name, value string, ok bool) {
	// Split on ';' to get the name=value and then attributes.
	parts := strings.Split(line, ";")
	if len(parts) == 0 {
		return "", "", false
	}
	first := strings.TrimSpace(parts[0])
	i := strings.IndexByte(first, '=')
	if i < 0 {
		return "", "", false
	}
	name = strings.TrimSpace(first[:i])
	value = strings.TrimSpace(first[i+1:])
	if name == "" {
		return "", "", false
	}
	return name, value, true
}

// lastReqURL returns a placeholder for cases where the request URL
// is unknown. We don't have it in the response alone, so we use the
// Host header.
func lastReqURL(resp *http.Response) string {
	if resp == nil || resp.Request == nil {
		return ""
	}
	return resp.Request.URL.String()
}
