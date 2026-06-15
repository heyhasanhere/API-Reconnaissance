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
