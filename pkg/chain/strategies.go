// Strategies are pure functions that decide what to do next based
// on observable signals in a probe response. They are *advisory*
// — the chain engine in chain.go does the actual probing, but
// calls these to pick the next URL/parameter to try.
//
// The strategy table is what makes the engine "adaptive" — every
// decision is driven by a pattern match on the response, never a
// hardcoded "if host is X" check.
package chain

import (
	"strings"

	"github.com/heyhasanhere/API-Reconnaissance/pkg/classify"
)

// Signal is the result of running a strategy. The chain engine
// uses Continue/Hint/Stop to decide what to probe next.
type Signal struct {
	Continue bool   // true = keep going, false = stop (success or failure)
	Hint     string // human-readable explanation
	NextURL  string // optional: override the next URL the engine would try
}

// DecideDrill returns the next id candidate to drill on, based on
// the list response. It implements the priority order documented
// in the design: number first, then id, then integer fallback.
func DecideDrill(item map[string]any) string {
	if n, ok := asInt(item["number"]); ok {
		return intToString(n)
	}
	if n, ok := asInt(item["episode"]); ok {
		return intToString(n)
	}
	if n, ok := asInt(item["order"]); ok {
		return intToString(n)
	}
	if s, ok := item["id"].(string); ok && s != "" {
		return s
	}
	return ""
}

// DecideSiblingSuffixes returns the suffixes to probe, in priority
// order, given the kind of resource we're looking at. For
// "episode" resources, /sources and /servers come first; for
// "movie" resources, /streams and /downloads.
func DecideSiblingSuffixes(resourceKind string) []string {
	switch resourceKind {
	case "movie", "film":
		return []string{"/streams", "/sources", "/downloads", "/subtitles"}
	default:
		// "episode", "chapter", "track" — the anikage pattern.
		return []string{"/sources", "/servers", "/streams", "/downloads", "/subtitles"}
	}
}

// DecideCDNHost returns candidate CDN hosts to try resolving a
// stream key against. The first non-empty candidate that's
// reachable is used. entryURL may be a full URL or a bare host.
func DecideCDNHost(entryURL string, shape classify.Shape) []string {
	var out []string
	// 1. CDN host found in the response itself (the classifier
	// populates CrossHost when a URL field is present).
	if shape.CrossHost != "" {
		out = append(out, shape.CrossHost)
	}
	// 2. Page-host-derived guesses.
	host := entryURL
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	if host != "" {
		// Known pattern: anikage.cc → prox.anikage.cc
		if host == "anikage.cc" {
			out = append(out, "prox.anikage.cc")
		}
		if i := strings.Index(host, "."); i > 0 {
			base := host[i+1:]
			for _, prefix := range []string{"prox.", "cdn.", "stream.", "media."} {
				out = append(out, prefix+base)
			}
		}
	}
	return out
}

// DecideStreamPath returns the path templates to try appending a
// stream key to. The order is: /m3u8/ (HLS master/variant), then
// /stream/ (some CDNs alias).
func DecideStreamPath() []string {
	return []string{"/m3u8/", "/stream/"}
}

// DecideOnError returns a hint for the chain engine when a probe
// returns an error shape. Currently this is informational — the
// engine logs the hint and continues with the next provider.
func DecideOnError(shape classify.Shape) Signal {
	if len(shape.MissingValues) == 0 {
		return Signal{Continue: true, Hint: "error: " + shape.ErrorMessage}
	}
	// "provider" → go look at /servers
	for _, v := range shape.MissingValues {
		if v == "provider" {
			return Signal{
				Continue: true,
				Hint:     "missing 'provider' query param; probing /servers for the provider list",
			}
		}
	}
	// A specific provider name in the error → try it next.
	return Signal{
		Continue: true,
		Hint:     "error mentions provider " + strings.Join(shape.MissingValues, ", ") + " — trying it",
	}
}

// DecideCandidateBases ranks hosts discovered by the page-bundle
// scan. It filters out:
//   - the entry host (we already tried it)
//   - the entry host's parent domain (e.g. anidap.se when
//     scanning for anidap's backend — the page already has a /api
//     path that we tried)
//   - well-known framework/UI/library hosts that show up as
//     literals in vendor bundles (react.dev, reactrouter.com,
//     jsdelivr.net, esm.sh, etc.) — these aren't API bases, they're
//     documentation pages or CDN endpoints.
//
// The rest are bucketed by subdomain prefix and returned in
// priority order: API-likely first (api./chad./rest./v[0-9].),
// then unknown, then CDN-likely (cdn./media./i./img./assets.).
// Order within a bucket is the input order (i.e. document order
// in the bundle).
func DecideCandidateBases(entryURL string, hosts []string) []string {
	entryHost := hostOf(entryURL)
	seen := map[string]bool{entryHost: true}
	apex := apexDomain(entryHost)
	hasBackendPrefix := func(h string) bool {
		// Hosts that look like a backend subdomain on the entry
		// apex are NOT blocked. E.g. anidap.se's real backend is
		// chad.anidap.se — same apex, but the chad. prefix is a
		// tell that it's a separate service.
		return strings.HasPrefix(h, "api.") ||
			strings.HasPrefix(h, "chad.") ||
			strings.HasPrefix(h, "rest.") ||
			strings.HasPrefix(h, "backend.") ||
			isVersionPrefix(h)
	}

	var apiLike, unknown, cdnLike []string
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || seen[h] {
			continue
		}
		// Block hosts on the entry apex UNLESS they look like a
		// backend subdomain. The page's own /api/... paths are
		// tried in step 1's HTML fallback — same-apex hosts
		// (www.x.com, cdn.x.com, etc.) are usually UI/CDN
		// infrastructure, not separate APIs.
		if apex != "" && (h == apex || strings.HasSuffix(h, "."+apex)) {
			if hasBackendPrefix(h) {
				// Backend prefix on the entry apex — keep it.
			} else {
				seen[h] = true
				continue
			}
		}
		seen[h] = true
		if isFrameworkHost(h) {
			continue
		}
		switch {
		case strings.HasPrefix(h, "api."),
			strings.HasPrefix(h, "chad."),
			strings.HasPrefix(h, "rest."),
			isVersionPrefix(h):
			apiLike = append(apiLike, h)
		case strings.HasPrefix(h, "cdn."),
			strings.HasPrefix(h, "media."),
			strings.HasPrefix(h, "i."),
			strings.HasPrefix(h, "img."),
			strings.HasPrefix(h, "assets."):
			cdnLike = append(cdnLike, h)
		default:
			unknown = append(unknown, h)
		}
	}
	return append(append(apiLike, unknown...), cdnLike...)
}

// isFrameworkHost returns true for known framework/UI/library
// hostnames that show up as literals in vendor bundles. These
// are not API bases — they're documentation pages, package CDNs,
// or analytics. The list is intentionally small and curated:
// real sites use these vendors, so we should not probe them as
// if they were the site's own API.
func isFrameworkHost(h string) bool {
	switch h {
	// React ecosystem.
	case "react.dev", "reactrouter.com", "reactjs.org":
		return true
	// NPM/CDN.
	case "unpkg.com", "cdn.jsdelivr.net", "cdn.skypack.dev",
		"esm.sh", "cdnjs.cloudflare.com", "cdn.esm.sh":
		return true
	// Analytics / tracking.
	case "www.googletagmanager.com", "static.cloudflareinsights.com",
		"doctusflaxman.com", "payeddrub.com", "acscdn.com":
		return true
	// Image hosts (anidap uses chiaki.site, anili.st, ak.jk for
	// cover art — not API hosts).
	case "chiaki.site", "img.anili.st", "s4.anilist.co", "i.ytimg.com",
		"artworks.thetvdb.com", "img.ak.jk":
		return true
	// Common CDN subdomains that aren't the page's API.
	case "fonts.gstatic.com", "fonts.googleapis.com":
		return true
	}
	return false
}

// isVersionPrefix returns true for hosts whose first label is a
// version tag (v1., v2., v3., etc.).
func isVersionPrefix(host string) bool {
	if len(host) < 4 || host[0] != 'v' {
		return false
	}
	for i := 1; i < len(host); i++ {
		if host[i] == '.' {
			return i > 1 // need at least one digit
		}
		if host[i] < '0' || host[i] > '9' {
			return false
		}
	}
	return false
}

// hostOf extracts the bare hostname from a URL, lowercased. Used
// for filtering and de-duplication; returns "" if the input isn't
// a URL with a host.
func hostOf(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	rawURL = strings.TrimPrefix(rawURL, "https://")
	rawURL = strings.TrimPrefix(rawURL, "http://")
	if i := strings.IndexByte(rawURL, '/'); i >= 0 {
		rawURL = rawURL[:i]
	}
	if i := strings.IndexByte(rawURL, ':'); i >= 0 {
		rawURL = rawURL[:i]
	}
	return strings.ToLower(rawURL)
}

// apexDomain returns the last two labels of a hostname
// (anidap.se → anidap.se, www.anidap.se → anidap.se,
// sub.deep.example.com → example.com). Returns "" if the
// hostname has fewer than 2 labels.
func apexDomain(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}
	parts := strings.Split(h, ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "." + parts[len(parts)-1]
}

// asInt tries to coerce a JSON-decoded number field into an int.
// JSON numbers come in as float64; this also handles int already
// (in case the upstream used UseNumber).
func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	}
	return 0, false
}

func intToString(n int) string {
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
