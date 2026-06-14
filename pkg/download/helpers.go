package download

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/falcon/api-recon/pkg/recipe"
)

// writeSegmentList writes a JSON array of URL strings (one per
// line) to path for aria2c's --input-file.
func writeSegmentList(path string, body []byte) error {
	var urls []string
	if err := json.Unmarshal(body, &urls); err != nil {
		return fmt.Errorf("body is not a JSON array of strings: %w", err)
	}
	if len(urls) == 0 {
		return fmt.Errorf("no URLs in segment list")
	}
	var b strings.Builder
	for _, u := range urls {
		b.WriteString(u)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0600)
}

// findHTMLLinks extracts href and download targets from <a> tags.
// Returns the first "interesting" link — a download attribute or a
// link with a non-page extension (.zip, .mp4, .tar, .pdf, etc.).
func findHTMLLinks(html string) []string {
	// First try <a download> — strong signal.
	downloadRe := regexp.MustCompile(`(?is)<a[^>]+href=["']([^"']+)["'][^>]*download`)
	if m := downloadRe.FindStringSubmatch(html); len(m) >= 2 {
		return []string{m[1]}
	}
	// Then <a href> with a downloadable extension.
	hrefRe := regexp.MustCompile(`(?is)<a[^>]+href=["']([^"']+)["']`)
	extensions := []string{".zip", ".tar.gz", ".tgz", ".mp4", ".mkv", ".pdf", ".exe", ".dmg"}
	seen := map[string]bool{}
	var out []string
	for _, m := range hrefRe.FindAllStringSubmatch(html, -1) {
		href := m[1]
		if seen[href] {
			continue
		}
		seen[href] = true
		lower := strings.ToLower(href)
		for _, ext := range extensions {
			if strings.HasSuffix(lower, ext) {
				out = append(out, href)
				break
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	// Fall back: any <a href> that's not just a fragment.
	for _, m := range hrefRe.FindAllStringSubmatch(html, -1) {
		href := m[1]
		if !strings.HasPrefix(href, "#") && !strings.HasPrefix(href, "javascript:") {
			return []string{href}
		}
	}
	return nil
}

// extractStreamKey finds the first sources[].url value in a JSON
// body and returns it along with the cross-host (if any) it
// resolves to. The "url" is often base64-ish (a key into a CDN
// proxy), not a real URL.
func extractStreamKey(body []byte, crossHost string) (key, host string, err error) {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", fmt.Errorf("body is not a JSON object: %w", err)
	}
	sourcesRaw, ok := parsed["sources"]
	if !ok {
		return "", "", fmt.Errorf("no 'sources' key in body")
	}
	sources, ok := sourcesRaw.([]any)
	if !ok || len(sources) == 0 {
		return "", "", fmt.Errorf("'sources' is not a non-empty array")
	}
	first, ok := sources[0].(map[string]any)
	if !ok {
		return "", "", fmt.Errorf("sources[0] is not an object")
	}
	urlVal, ok := first["url"].(string)
	if !ok {
		return "", "", fmt.Errorf("sources[0].url is not a string")
	}
	return urlVal, crossHost, nil
}

// buildStreamURL constructs a real URL from a stream key, host,
// and auth. The pattern (from the anikage case) is:
//   https://<host>/m3u8/<key>   for the master playlist
//   https://<host>/stream/<key>  for the variant
//
// We default to /m3u8/ and let the caller's HLS strategy fetch the
// master + variant.
func buildStreamURL(key, host string, auth recipe.Auth) string {
	if host == "" {
		// Fall back to the auth's host or anikage's known CDN.
		host = "prox.anikage.cc"
	}
	return fmt.Sprintf("https://%s/m3u8/%s", host, key)
}

// isLikelyHLSKey returns true if the stream key looks like a
// base64-encoded HLS path (the anikage case). We use a simple
// heuristic: the key is short (< 200 chars) and contains only
// base64-safe characters.
func isLikelyHLSKey(key string) bool {
	if key == "" || len(key) > 200 {
		return false
	}
	for _, r := range key {
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '+' || r == '/' || r == '=') {
			return false
		}
	}
	return true
}
