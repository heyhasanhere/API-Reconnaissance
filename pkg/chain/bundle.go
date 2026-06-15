// Bundle scanner: extracts API host candidates from the entry
// page's JS bundles. The chain engine calls this when the entry
// URL is an HTML page that doesn't have an inline /api/ script
// hint — i.e. a modern SPA where the API surface is documented in
// a separate modulepreload'd bundle.
//
// The scanner is deliberately minimal. It does three things:
//   1. extractScriptSrcs — pull <script src="..."> and
//      <link rel="modulepreload" href="..."> URLs out of the HTML
//   2. for each candidate URL (capped at 2), GET it (capped at
//      500 KB) and extractHostsFromBundle
//   3. return the deduplicated union of all hostnames found
//
// No JS parsing, no AST, no fetch-then-execute. The bundle is just
// text; we regex out the https://... literals and pick the
// different-host ones. DecideCandidateBases() in strategies.go
// ranks them.
package chain

import (
	"context"
	"net/url"
	"regexp"
	"strings"
)

// maxBundles is the cap on the number of bundles the scanner will
// download per entry URL. SPAs commonly have 50+ modulepreload
// links (one per route); the API client is usually in the first
// 10. 12 is a good balance — large enough to find the real API
// client buried in the middle, small enough to fail fast.
const maxBundles = 12

// maxBundleBytes is the cap on each bundle's body. 500 KB is more
// than enough for an API client (the largest real-world example
// I've seen is ~140 KB) but small enough to skip the framework
// vendor bundle (1.4 MB for React Router, etc.) without parsing.
const maxBundleBytes = 500 * 1024

// scriptSrcRE matches <script src="..."> and
// <script src='...'>. The src attribute can contain query
// strings and fragments; the closing quote terminates the match.
var scriptSrcRE = regexp.MustCompile(`<script[^>]+src=["']([^"']+)["']`)

// modulePreloadRE matches <link rel="modulepreload" href="...">
// and <link rel='modulepreload' href='...'>. The rel attribute
// is matched loosely so other rel values (preload, prefetch) on
// the same tag are tolerated.
var modulePreloadRE = regexp.MustCompile(`<link[^>]+rel=["']modulepreload["'][^>]+href=["']([^"']+)["']`)

// scriptOrModuleRE matches either form in document order. The
// first capture group is the script src, the second is the
// modulepreload href. Either is empty depending on which form
// matched.
var scriptOrModuleRE = regexp.MustCompile(
	`<script[^>]+src=["']([^"']+)["']` + "|" +
		`<link[^>]+rel=["']modulepreload["'][^>]+href=["']([^"']+)["']`,
)

// httpsURLRE matches https://host[:port][/path] literals in
// bundle text. We deliberately do NOT match http:// — the page
// is HTTPS, so any non-HTTPS host is suspect (and would be
// downgraded by the browser anyway).
var httpsURLRE = regexp.MustCompile(`https://([a-zA-Z0-9.\-]+)`)

// fetchURLRE matches https://host/path literals that appear as
// the URL argument to a fetch() call, e.g. fetch("https://x.com/y")
// or fetch(`https://x.com/${id}`). The window is small — up to
// 80 chars before the URL — to avoid matching fetch() calls in
// comments. fetchBaseURL is the first capture (host), used by
// extractHostsFromBundle to distinguish API bases (hosts that
// appear as fetch() targets) from stream-CDN hosts (hosts that
// appear in HOST_HANDLERS string templates).
var fetchURLRE = regexp.MustCompile(`fetch\(\s*["'\x60]https://([a-zA-Z0-9.\-]+)`)

// urlWithPathRE matches https://host/path[...] literals — the
// path must start with `/[a-zA-Z0-9_-]` so we can distinguish
// a URL-with-path from a bare-host literal. This catches the
// common "const e=`https://host/...`,r=await fetch(e,...)"
// pattern where the URL is assigned to a variable before
// fetch() is called. Bare-host literals (no path) are CDN
// bases like `const P="https://crs.24stream.xyz"`. The
// capture group is the full `https://host/...rest` so we can
// inspect the path component.
var urlWithPathRE = regexp.MustCompile(`https://([a-zA-Z0-9.\-]+)/[a-zA-Z0-9_\-][^"')\s\x60]*`)

// streamCDNPathRE matches paths that indicate a stream-CDN
// endpoint rather than an API endpoint. These are excluded
// from the "strong API candidate" set even if the host
// appears with a path — they're URL rewrite targets in
// HOST_HANDLERS maps, not fetch() bases.
var streamCDNPathRE = regexp.MustCompile(`^/(media|stream|storage|cdn-cgi|assets|static|img|images|fonts)/`)

// BundleScan returns the hostnames referenced by https://...
// literals in the entry page's JS bundles, deduplicated and
// lowercased. The list is unordered; DecideCandidateBases()
// ranks. The getter is injected so tests can stub network I/O
// without a real server.
//
// We scan up to maxBundles bundles and accumulate ALL hosts
// across them. The first few bundles in a typical SPA are
// framework/UI (React, vendor) and contain framework-host
// strings (react.dev, reactrouter.com) that aren't API bases.
// The real API client is usually a later bundle. We rely on
// DecideCandidateBases to filter and rank — it de-prioritizes
// the framework hosts via prefix matching.
func BundleScan(ctx context.Context, entryHTML []byte,
	pageURL string, getter func(ctx context.Context, rawURL string) ([]byte, error),
) []string {
	if len(entryHTML) == 0 || getter == nil {
		return nil
	}
	srcs := extractScriptSrcs(entryHTML, pageURL)
	if len(srcs) == 0 {
		return nil
	}

	seen := map[string]bool{}
	var hosts []string
	for _, src := range srcs {
		if len(hosts) >= maxBundles*4 {
			// Already have a healthy list; stop pulling bundles.
			break
		}
		body, err := getter(ctx, src)
		if err != nil || len(body) == 0 {
			continue
		}
		if len(body) > maxBundleBytes {
			// Probably not an API client — skip.
			continue
		}
		for _, h := range extractHostsFromBundle(body) {
			if !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
	}
	return hosts
}

// extractScriptSrcs returns the URLs of all <script src="...">
// and <link rel="modulepreload" href="..."> in the HTML, in
// document order. Relative URLs are resolved against pageURL.
// The list is capped at maxBundles — we only need the first
// few, not all 50+ that a typical SPA references.
func extractScriptSrcs(html []byte, pageURL string) []string {
	page, err := url.Parse(pageURL)
	if err != nil {
		return nil
	}
	// Walk the HTML in one pass, finding both kinds in document
	// order. The single combined regex finds whichever appears
	// next; we record what kind it was by trying both captures.
	var out []string
	combined := scriptOrModuleRE
	for _, m := range combined.FindAllStringSubmatch(string(html), -1) {
		// m[1] is the script src (or empty), m[2] is the modulepreload href (or empty).
		ref := m[1]
		if ref == "" {
			ref = m[2]
		}
		if u := resolveRef(ref, page); u != "" {
			out = append(out, u)
			if len(out) >= maxBundles {
				return out
			}
		}
	}
	return out
}

// extractHostsFromBundle returns the hostnames of all https://...
// literals in the bundle text, deduplicated and lowercased, in
// the order they first appear. Junk hostnames (empty, no dot,
// IP addresses, hostnames that are actually paths) are filtered
// out.
//
// We also distinguish API-base hosts from stream-CDN hosts:
//   - An API-base is a host that appears as the URL argument
//     to a fetch() call, OR appears with a path that's not a
//     known stream-CDN path (/media/, /stream/, /storage/, ...).
//     The fetch() pattern catches fetch("https://x/...") and
//     fetch(`https://x/...`). The path pattern catches the
//     common `const e=\`https://x/...\`,r=await fetch(e,...)`
//     indirection where the URL is assigned to a variable
//     before being fetched.
//   - A stream-CDN host appears only with bare-host literals
//     (e.g. const P="https://crs.24stream.xyz") or with paths
//     like /media/, /stream/, /storage/. These are the rewrite
//     targets in HOST_HANDLERS maps, never fetched directly.
//
// The first group is returned first; the second group follows.
func extractHostsFromBundle(bundle []byte) []string {
	text := string(bundle)

	// Set of hosts seen with API-likely context (fetch() arg,
	// or with a non-CDN path).
	apiSet := map[string]bool{}
	for _, m := range fetchURLRE.FindAllStringSubmatch(text, -1) {
		h := strings.ToLower(m[1])
		if isPlausibleHost(h) {
			apiSet[h] = true
		}
	}
	for _, m := range urlWithPathRE.FindAllStringSubmatch(text, -1) {
		h := strings.ToLower(m[1])
		// path is m[0] minus the host+"/" prefix. The
		// urlWithPathRE already guarantees a path starts
		// with "/" + [a-zA-Z0-9_-]. The stream-CDN filter
		// removes /media/, /stream/, /storage/, etc.
		rest := m[0][len("https://")+len(h):]
		if streamCDNPathRE.MatchString(rest) {
			continue
		}
		if isPlausibleHost(h) {
			apiSet[h] = true
		}
	}

	// Second pass: all https:// host literals, in order, but
	// tagged with whether they were seen as a fetch() target
	// or with an API-likely path.
	seen := map[string]bool{}
	var api, cdn []string
	for _, m := range httpsURLRE.FindAllStringSubmatch(text, -1) {
		h := strings.ToLower(m[1])
		if !isPlausibleHost(h) {
			continue
		}
		if seen[h] {
			continue
		}
		seen[h] = true
		if apiSet[h] {
			api = append(api, h)
		} else {
			cdn = append(cdn, h)
		}
	}
	return append(api, cdn...)
}

// isPlausibleHost filters out regex false positives: empty
// strings, single-label names, and obviously-not-a-host
// fragments. We keep IPs (some streaming sites use them).
func isPlausibleHost(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	if !strings.Contains(h, ".") {
		return false
	}
	for _, r := range h {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// resolveRef resolves a script/modulepreload URL against the
// page URL. Absolute URLs pass through unchanged; relative URLs
// (starting with /) are resolved against the page's scheme+host.
// Anything else (protocol-relative, query-only, fragment-only)
// is returned as-is or rejected.
func resolveRef(ref string, page *url.URL) string {
	if ref == "" {
		return ""
	}
	// Absolute URL.
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	// Protocol-relative: //cdn.example.com/x.js — reject (we
	// only follow https:// in the bundle extractor, and the
	// page is https).
	if strings.HasPrefix(ref, "//") {
		return ""
	}
	// Root-relative: /assets/api.js.
	if strings.HasPrefix(ref, "/") {
		return page.Scheme + "://" + page.Host + ref
	}
	// Bare path: assets/api.js — resolve against page path.
	if page != nil {
		base := *page
		base.Path, base.RawQuery, base.Fragment = "", "", ""
		if u, err := base.Parse(ref); err == nil {
			return u.String()
		}
	}
	return ""
}
