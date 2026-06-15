// Package pickjson is a small helper for picking a value from a
// JSON list of provider objects and injecting it into a URL's
// query string. The chain engine uses it to mutate a /sources
// URL from ?provider=unknown to ?provider=miko after the user
// picks a provider.
package pickjson

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// ParseProviderList extracts the `id` field from each object in a
// JSON array. Used to turn a /servers response into a list of
// provider names.
func ParseProviderList(body []byte) []string {
	var arr []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &arr); err != nil {
		return nil
	}
	out := make([]string, len(arr))
	for i, x := range arr {
		out[i] = x.ID
	}
	return out
}

// WithQueryParam returns a copy of rawURL with ?key=value added (or
// replaced if key is already present). The original URL is
// unchanged.
func WithQueryParam(rawURL, key, value string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("pickjson: parse %q: %w", rawURL, err)
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// WithQueryParams returns a copy of rawURL with multiple params
// set in one call. Nil/empty values are skipped.
func WithQueryParams(rawURL string, params map[string]string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("pickjson: parse %q: %w", rawURL, err)
	}
	q := u.Query()
	for k, v := range params {
		if v == "" {
			continue
		}
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
