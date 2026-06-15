// Package recipe is the minimal on-disk store for discovered
// chains. v2 doesn't need v0.1.0's strict Validate/Fill — the
// chain engine re-derives placeholders from the recorded steps
// at replay time, so the recipe is just a structured log of what
// worked.
//
// On-disk format: pretty-printed JSON, mode 0600, atomic write.
// Path: <root>/<domain>.json where root defaults to
// $XDG_DATA_HOME/api-recon/recipes/ (or
// $HOME/.local/share/api-recon/recipes/).
package recipe

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// CurrentSchemaVersion is stamped into new recipes.
const CurrentSchemaVersion = 1

// Step is one URL in the discovered chain. URLTemplate is the
// path (or full URL) with placeholders in {braces}. Method is
// almost always GET. Placeholders is the list of names in
// alphabetical order, used to fill from positional args at
// replay time.
type Step struct {
	Name           string   `json:"name"`
	URLTemplate    string   `json:"url_template"`
	Method         string   `json:"method"`
	Placeholders   []string `json:"placeholders,omitempty"`
	RequiredParams []string `json:"required_params,omitempty"`
}

// Provider is one streaming provider discovered via /servers.
// IsHLS is set if the sources endpoint returns isM3U8:true for
// this provider.
type Provider struct {
	Name    string `json:"name"`
	IsHLS   bool   `json:"is_hls,omitempty"`
	IsEmbed bool   `json:"is_embed,omitempty"`
	Note    string `json:"note,omitempty"`
}

// Recipe is the on-disk artifact. The chain engine builds one of
// these as it discovers, and saves it to disk on success.
type Recipe struct {
	SchemaVersion  int               `json:"schema_version"`
	Domain         string            `json:"domain"`
	Discovered     time.Time         `json:"discovered"`
	Updated        time.Time         `json:"updated"`
	Chain          []Step            `json:"chain"`
	Providers      []Provider        `json:"providers,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	StreamTemplate string            `json:"stream_template,omitempty"`
	Note           string            `json:"note,omitempty"`
}

// Store is the file-based recipe store. Construct with NewStore or
// NewStoreAt.
type Store struct {
	root string
	mu   sync.Mutex
}

// NewStore returns a Store at the default XDG location, with the
// directory created (mode 0700) if it doesn't exist.
func NewStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("recipe: home dir: %w", err)
	}
	var root string
	if xdh := os.Getenv("XDG_DATA_HOME"); xdh != "" {
		root = filepath.Join(xdh, "api-recon", "recipes")
	} else {
		root = filepath.Join(home, ".local", "share", "api-recon", "recipes")
	}
	return NewStoreAt(root)
}

// NewStoreAt returns a Store at the given root directory.
func NewStoreAt(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("recipe: empty root")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("recipe: mkdir %s: %w", root, err)
	}
	return &Store{root: root}, nil
}

// Root returns the on-disk root directory.
func (s *Store) Root() string { return s.root }

// Path returns the on-disk path for domain without checking
// existence.
func (s *Store) Path(domain string) string {
	return filepath.Join(s.root, domain+".json")
}

// Save writes r to disk atomically. The file gets mode 0600.
// SchemaVersion is stamped if zero. Discovered is preserved across
// updates.
func (s *Store) Save(r *Recipe) error {
	if r == nil {
		return errors.New("recipe: nil recipe")
	}
	if r.Domain == "" {
		return errors.New("recipe: empty domain")
	}
	if !validDomain(r.Domain) {
		return fmt.Errorf("recipe: invalid domain %q", r.Domain)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if r.SchemaVersion == 0 {
		r.SchemaVersion = CurrentSchemaVersion
	}

	final := s.Path(r.Domain)
	if existing, err := loadFile(final); err == nil && !existing.Discovered.IsZero() {
		r.Discovered = existing.Discovered
	} else {
		if r.Discovered.IsZero() {
			r.Discovered = time.Now()
		}
	}
	r.Updated = time.Now()

	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("recipe: marshal: %w", err)
	}
	return atomicWrite(final, data)
}

// Load reads and parses a recipe for domain. Returns
// os.ErrNotExist if no recipe is stored.
func (s *Store) Load(domain string) (*Recipe, error) {
	if !validDomain(domain) {
		return nil, fmt.Errorf("recipe: invalid domain %q", domain)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return loadFile(s.Path(domain))
}

// List returns all known domains, sorted.
func (s *Store) List() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("recipe: list: %w", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		domain := strings.TrimSuffix(e.Name(), ".json")
		if validDomain(domain) {
			out = append(out, domain)
		}
	}
	sort.Strings(out)
	return out, nil
}

// FillTemplate substitutes {placeholder} tokens in template with
// values from the map. Returns an error if a placeholder is
// missing or if any key in values is not a placeholder. The
// replacement preserves URL safety — values are inserted
// literally without escaping (callers should not include
// untrusted data in the values).
func FillTemplate(template string, values map[string]string) (string, error) {
	var firstErr error
	out := placeholderRE.ReplaceAllStringFunc(template, func(token string) string {
		name := strings.Trim(token, "{}")
		v, ok := values[name]
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("missing placeholder %q", name)
			}
			return token
		}
		return v
	})
	if firstErr != nil {
		return "", firstErr
	}
	// Check for unknown placeholders (keys in values that didn't
	// appear in the template).
	for k := range values {
		if !strings.Contains(template, "{"+k+"}") {
			return "", fmt.Errorf("unknown placeholder %q", k)
		}
	}
	return out, nil
}

var placeholderRE = regexp.MustCompile(`\{[a-zA-Z0-9_]+\}`)

func loadFile(path string) (*Recipe, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Recipe
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("recipe: parse %s: %w", path, err)
	}
	return &r, nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".recipe-*.json.tmp")
	if err != nil {
		return fmt.Errorf("recipe: create tmp: %w", err)
	}
	tmpName := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("recipe: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("recipe: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("recipe: close: %w", err)
	}
	if err := os.Chmod(tmpName, 0600); err != nil {
		return fmt.Errorf("recipe: chmod: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("recipe: rename: %w", err)
	}
	success = true
	return nil
}

func validDomain(domain string) bool {
	if domain == "" || len(domain) > 253 {
		return false
	}
	if strings.ContainsAny(domain, "/\\\x00\n ") {
		return false
	}
	if !strings.Contains(domain, ".") {
		return false
	}
	return true
}
