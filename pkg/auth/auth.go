// Package auth captures and injects HTTP headers per host.
//
// The Store records headers it sees on responses (so it can replay
// them on the next request to the same host), and tracks which URL
// each header was first observed on (so the chain engine can show
// the user a useful "this header came from <url>" message).
//
// On 403 with a "forbidden origin" body, the chain engine calls
// InjectForbiddenOrigin — the Store records the page's Origin and
// Referer for the failed host, and every subsequent probe to that
// host gets the headers automatically.
package auth

import (
	"net/http"
	"strings"
	"sync"
)

// DefaultHeaders are applied to every request unless overridden.
var DefaultHeaders = map[string]string{
	"User-Agent": "api-recon/0.2.0",
	"Accept":     "application/json, */*;q=0.5",
}

// Store is a thread-safe per-host header recorder.
type Store struct {
	mu      sync.Mutex
	perHost map[string]map[string]string // host -> header -> value
	sources map[string]string            // host+header -> "first seen at URL"
}

// New returns an empty Store with the package's default headers
// pre-loaded. The defaults are returned by HeadersFor so callers
// can apply them per request.
func New() *Store {
	return &Store{
		perHost: map[string]map[string]string{},
		sources: map[string]string{},
	}
}

// HeadersFor returns the headers to apply for a request to host.
// The result includes the package's DefaultHeaders as a base;
// per-host captured headers override on conflict. Returns a fresh
// map each call.
func (s *Store) HeadersFor(host string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make(map[string]string, len(DefaultHeaders))
	for k, v := range DefaultHeaders {
		out[k] = v
	}
	if host != "" {
		if m, ok := s.perHost[host]; ok {
			for k, v := range m {
				out[k] = v
			}
		}
	}
	return out
}

// Record captures a header that the server used (e.g. Set-Cookie
// reflected as an authentication requirement) and stores it under
// the given host. The first URL the header is seen on is recorded
// in the sources map.
func (s *Store) Record(host, header, value, firstSeenURL string) {
	if host == "" || header == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perHost[host] == nil {
		s.perHost[host] = map[string]string{}
	}
	s.perHost[host][header] = value
	if firstSeenURL != "" {
		s.sources[host+"|"+header] = firstSeenURL
	}
}

// RecordFromResponse captures useful headers from an http.Response.
// Currently this is a no-op (we only inject on 403) but the method
// exists for future use — for example, sites that issue Set-Cookie
// for session establishment.
func (s *Store) RecordFromResponse(host, firstSeenURL string, resp *http.Response) {
	if resp == nil {
		return
	}
	// Record Authorization hints from response headers.
	if v := resp.Header.Get("WWW-Authenticate"); v != "" {
		s.Record(host, "WWW-Authenticate", v, firstSeenURL)
	}
}

// InjectForbiddenOrigin is called when a host returns 403 with
// "forbidden origin" in the body. It records Origin and Referer
// (synthesized from the page host) for the failed host, so the
// next probe to that host will include them.
func (s *Store) InjectForbiddenOrigin(host, pageOrigin string) {
	if host == "" || pageOrigin == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perHost[host] == nil {
		s.perHost[host] = map[string]string{}
	}
	// Only set if not already present.
	if _, ok := s.perHost[host]["Origin"]; !ok {
		s.perHost[host]["Origin"] = pageOrigin
	}
	if _, ok := s.perHost[host]["Referer"]; !ok {
		s.perHost[host]["Referer"] = strings.TrimSuffix(pageOrigin, "/") + "/"
	}
}

// HasForHost returns true if any non-default header is recorded for
// host. Used by the chain engine to decide whether to log a
// "injected custom header" line.
func (s *Store) HasForHost(host string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.perHost[host]
	return ok
}

// HostOf pulls the host out of a URL. Pure string parse — we don't
// import net/url to keep this package dependency-free.
func HostOf(rawURL string) string {
	rest := rawURL
	switch {
	case strings.HasPrefix(rest, "https://"):
		rest = rest[len("https://"):]
	case strings.HasPrefix(rest, "http://"):
		rest = rest[len("http://"):]
	default:
		return ""
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}
