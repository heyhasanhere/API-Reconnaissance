// Package recipe defines the on-disk representation of a domain's
// API: endpoints, captured auth, sibling relationships, and the
// download shape. Recipes are the source of truth in api-recon; the
// REPL harvests them, `run` replays them, `verify` re-checks them.
//
// A recipe is a JSON file at:
//
//	$XDG_DATA_HOME/api-recon/recipes/<domain>.json  (default)
//	./.api-recon/recipes/<domain>.json             (project-local override)
//
// File mode is 0600 because recipes may contain captured tokens.
package recipe

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// CurrentSchemaVersion is the schema version this code emits and
// understands. Bump when the on-disk format changes incompatibly.
const CurrentSchemaVersion = 1

// Recipe is the top-level type. A Recipe is keyed by domain (e.g.
// "anikage.cc"). Endpoints are named (e.g. "episodes", "sources",
// "downloads") and may contain {placeholders} in their URL that
// Fill substitutes at run time.
type Recipe struct {
	SchemaVersion int                    `json:"schema_version"`
	Domain        string                 `json:"domain"`
	Discovered    time.Time              `json:"discovered"`
	Updated       time.Time              `json:"updated"`
	Notes         string                 `json:"notes,omitempty"`
	Endpoints     map[string]Endpoint    `json:"endpoints"`
	Siblings      map[string]Sibling     `json:"siblings,omitempty"`
	Auth          Auth                   `json:"auth"`
	CDN           *CDN                   `json:"cdn,omitempty"`
	Download      *Download              `json:"download,omitempty"`
	Failures      []Failure              `json:"failures,omitempty"`
}

// Endpoint is a single API endpoint. URL may contain {placeholders}
// like {slug} or {n}; use Fill to substitute them at call time.
type Endpoint struct {
	URL    string   `json:"url"`
	Method string   `json:"method"`
	Params []string `json:"params,omitempty"`
	Shape  string   `json:"shape"`
	Notes  string   `json:"notes,omitempty"`
}

// Sibling is a related endpoint that we know about. Used to record
// failed/working alternatives the user discovered.
type Sibling struct {
	Path   string `json:"path"`
	Status string `json:"status"` // "ok" | "broken" | "unknown"
	Since  string `json:"since,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Auth is the captured authentication state. RequiredHeaders holds
// non-auth headers like Origin/Referer that the target insists on.
// The bearer token, session cookie, and API key are stored as
// separate fields so they can be redacted differently on display.
type Auth struct {
	RequiredHeaders map[string]string `json:"required_headers,omitempty"`
	BearerToken     string            `json:"bearer_token,omitempty"`
	SessionCookie   string            `json:"session_cookie,omitempty"`
	APIKey          string            `json:"api_key,omitempty"`
	RefreshFrom     string            `json:"refresh_from,omitempty"` // endpoint name
}

// CDN describes a cross-host content delivery network. The download
// interpreter uses this to know when to apply the parent's auth
// headers to a different host.
type CDN struct {
	Host         string `json:"host"`
	InheritsAuth bool   `json:"inherits_auth"`
}

// Download describes how to download a recognized source. Shape is
// the recognized kind ("hls", "dash", "direct", "segment_list",
// "html"); Tool is the binary to invoke; Flags are pre-baked.
type Download struct {
	Shape string   `json:"shape"`
	Tool  string   `json:"tool"`
	Flags []string `json:"flags,omitempty"`
	Note  string   `json:"note,omitempty"`
}

// Failure records an endpoint that returned a non-2xx during
// discovery. Used by `verify` to report "still broken" and to keep
// negative knowledge from being lost.
type Failure struct {
	Endpoint string    `json:"endpoint"`
	Status   int       `json:"status"`
	Message  string    `json:"message,omitempty"`
	When     time.Time `json:"when"`
}

// New returns a Recipe with SchemaVersion stamped, Discovered and
// Updated set to now, and the Endpoints map initialized.
func New(domain string) *Recipe {
	now := time.Now().UTC()
	return &Recipe{
		SchemaVersion: CurrentSchemaVersion,
		Domain:        domain,
		Discovered:    now,
		Updated:       now,
		Endpoints:     map[string]Endpoint{},
	}
}

// Validate checks the recipe for obvious problems. It is called by
// store.Lookup before returning a loaded recipe and by `verify`
// before re-running.
func (r *Recipe) Validate() error {
	if r == nil {
		return errors.New("recipe is nil")
	}
	if r.SchemaVersion == 0 {
		return errors.New("recipe: schema_version is 0 (missing)")
	}
	if r.SchemaVersion > CurrentSchemaVersion {
		return fmt.Errorf("recipe: schema_version %d is newer than supported (%d)", r.SchemaVersion, CurrentSchemaVersion)
	}
	if r.Domain == "" {
		return errors.New("recipe: domain is empty")
	}
	if r.Endpoints == nil {
		return errors.New("recipe: endpoints map is nil")
	}
	for name, ep := range r.Endpoints {
		if ep.URL == "" {
			return fmt.Errorf("recipe: endpoint %q has empty URL", name)
		}
		if ep.Method == "" {
			return fmt.Errorf("recipe: endpoint %q has empty method", name)
		}
		if _, err := url.Parse(ep.URL); err != nil {
			return fmt.Errorf("recipe: endpoint %q URL does not parse: %w", name, err)
		}
	}
	return nil
}

// placeholderRE matches a {name} placeholder inside a URL. Names are
// restricted to [a-zA-Z0-9_]+ for forward-compat (so we can later
// support {name:type} or {name?default} without breaking old recipes).
var placeholderRE = regexp.MustCompile(`\{([a-zA-Z0-9_]+)\}`)

// Placeholders returns the set of {name} placeholders present in the
// endpoint URL, in the order they first appear.
func (e *Endpoint) Placeholders() []string {
	matches := placeholderRE.FindAllStringSubmatch(e.URL, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		name := m[1]
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// Fill substitutes {placeholders} in the endpoint URL with values
// from parts. It is STRICT: returns an error if any placeholder in
// the URL is missing from parts, or if any key in parts is not a
// placeholder in the URL.
//
// The returned URL has been url.QueryEscape'd per-value (not
// per-URL) — so a value of "a b/c" becomes "a+b%2Fc", which is the
// correct behavior for path segments. If you need to put a value
// into the query string, encode it before calling Fill.
func (e *Endpoint) Fill(parts map[string]string) (string, error) {
	want := e.Placeholders()
	have := map[string]bool{}
	for _, p := range want {
		_, ok := parts[p]
		if !ok {
			return "", fmt.Errorf("endpoint %q: missing placeholder {%s}", e.URL, p)
		}
		have[p] = true
	}
	for k := range parts {
		if !have[k] {
			return "", fmt.Errorf("endpoint %q: unknown placeholder {%s} in parts", e.URL, k)
		}
	}

	// Substitute. We use ReplaceAllStringFunc to avoid double-substitution
	// (the value itself won't contain braces if the caller passed clean
	// input; if it does, that's their problem). Since we already
	// validated above that all placeholders are present, this branch
	// only fires if the caller mutates parts concurrently — be safe
	// anyway.
	var missing []string
	out := placeholderRE.ReplaceAllStringFunc(e.URL, func(match string) string {
		name := match[1 : len(match)-1]
		v, ok := parts[name]
		if !ok {
			missing = append(missing, name)
			return match
		}
		return url.PathEscape(v)
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("endpoint %q: missing placeholders: %s", e.URL, strings.Join(missing, ", "))
	}
	return out, nil
}

// Marshal returns the pretty-printed JSON encoding. Used by the
// store for atomic writes.
func (r *Recipe) Marshal() ([]byte, error) {
	r.Updated = time.Now().UTC()
	return json.MarshalIndent(r, "", "  ")
}

// Unmarshal parses a recipe from JSON. It also stamps SchemaVersion
// if missing (for hand-written recipes).
func Unmarshal(data []byte) (*Recipe, error) {
	var r Recipe
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.SchemaVersion == 0 {
		r.SchemaVersion = CurrentSchemaVersion
	}
	if r.Endpoints == nil {
		r.Endpoints = map[string]Endpoint{}
	}
	return &r, nil
}
