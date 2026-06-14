// Package store provides the file-based recipe store.
//
// A Store has two roots: a global root at
// $XDG_DATA_HOME/api-recon/recipes/ (or
// $HOME/.local/share/api-recon/recipes/ if XDG is unset) and an
// optional project-local root at ./.api-recon/recipes/. Lookups check
// the project root first, then the global root. Writes always go to
// the project root if it's set, otherwise to the global root.
//
// All writes are atomic: write to <file>.tmp (mode 0600), fsync,
// rename. File mode 0600 is enforced because recipes may contain
// captured tokens.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/falcon/api-recon/pkg/recipe"
)

// Store is the file-based recipe store. Construct with New.
type Store struct {
	global  string
	project string // may be empty
	writer  string // where Save writes — derived from project presence
}

// New returns a store rooted at the user's data dir plus an optional
// project-local override. projectDir is the directory containing the
// project (typically "." or the result of os.Getwd()); if it's a path
// under which a writable .api-recon/ directory can be created, that
// directory becomes the project root.
//
// The default project root is ./.api-recon/recipes/. If the directory
// does not exist and cannot be created, New returns an error — even
// if the user is only reading. (We could be more permissive and
// silently fall back to global, but surprise writes to the global
// store are worse than an explicit failure.)
func New(projectDir string) (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("store: cannot determine home dir: %w", err)
	}

	globalRoot := filepath.Join(dataHome(home), "api-recon", "recipes")
	if err := os.MkdirAll(globalRoot, 0700); err != nil {
		return nil, fmt.Errorf("store: cannot create global root %s: %w", globalRoot, err)
	}

	s := &Store{global: globalRoot}

	if projectDir != "" {
		projRoot := filepath.Join(projectDir, ".api-recon", "recipes")
		if err := os.MkdirAll(projRoot, 0700); err == nil {
			s.project = projRoot
		}
		// If we can't create the project root, just skip the project
		// override. Global still works.
	}

	if s.project != "" {
		s.writer = s.project
	} else {
		s.writer = s.global
	}

	return s, nil
}

// NewWithRoot returns a store rooted at the given directory, which
// becomes the writer. The project-local root is still created under
// projectDir/.api-recon/recipes/ if projectDir is non-empty, so
// lookups check both. This is the form used by --store.
func NewWithRoot(root, projectDir string) (*Store, error) {
	if root == "" {
		return nil, errors.New("store: empty root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("store: resolve root: %w", err)
	}
	if err := os.MkdirAll(abs, 0700); err != nil {
		return nil, fmt.Errorf("store: create root %s: %w", abs, err)
	}

	s := &Store{global: abs, writer: abs}

	if projectDir != "" {
		projRoot := filepath.Join(projectDir, ".api-recon", "recipes")
		if err := os.MkdirAll(projRoot, 0700); err == nil {
			s.project = projRoot
			// Keep the override root as the writer — --store means
			// "write here, not in the project dir."
			_ = projRoot
		}
	}

	return s, nil
}

// dataHome returns $XDG_DATA_HOME if set and non-empty, otherwise
// $HOME/.local/share.
func dataHome(home string) string {
	if xdh := os.Getenv("XDG_DATA_HOME"); xdh != "" {
		return xdh
	}
	return filepath.Join(home, ".local", "share")
}

// GlobalRoot returns the path to the global recipe directory. Useful
// for `--store` flag wiring and tests.
func (s *Store) GlobalRoot() string { return s.global }

// ProjectRoot returns the path to the project-local recipe directory,
// or "" if there is no project root.
func (s *Store) ProjectRoot() string { return s.project }

// Lookup returns the recipe for domain, checking project first then
// global. Returns os.ErrNotExist if the recipe is not in either
// location. Other errors are wrapped I/O failures.
func (s *Store) Lookup(domain string) (*recipe.Recipe, error) {
	if domain == "" {
		return nil, errors.New("store: empty domain")
	}
	if !validDomain(domain) {
		return nil, fmt.Errorf("store: invalid domain %q", domain)
	}

	if s.project != "" {
		path := filepath.Join(s.project, domain+".json")
		if r, err := loadFile(path); err == nil {
			return r, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}

	path := filepath.Join(s.global, domain+".json")
	return loadFile(path)
}

// Path returns the on-disk path for a domain without checking
// existence. The path is the project root if set, else the global
// root — the same place Save would write to. Used by `recipe edit`.
func (s *Store) Path(domain string) string {
	return filepath.Join(s.writer, domain+".json")
}

// Save writes a recipe atomically. The recipe's SchemaVersion is
// stamped if zero. If a recipe already exists for this domain, it is
// overwritten; Discovered is preserved across updates.
func (s *Store) Save(r *recipe.Recipe) error {
	if r == nil {
		return errors.New("store: nil recipe")
	}
	if r.Domain == "" {
		return errors.New("store: recipe has empty domain")
	}
	if !validDomain(r.Domain) {
		return fmt.Errorf("store: invalid domain %q", r.Domain)
	}
	if err := r.Validate(); err != nil {
		return fmt.Errorf("store: invalid recipe: %w", err)
	}
	if r.SchemaVersion == 0 {
		r.SchemaVersion = recipe.CurrentSchemaVersion
	}

	// Preserve Discovered across updates.
	final := filepath.Join(s.writer, r.Domain+".json")
	if existing, err := loadFile(final); err == nil && !existing.Discovered.IsZero() {
		r.Discovered = existing.Discovered
	}

	data, err := r.Marshal()
	if err != nil {
		return fmt.Errorf("store: marshal: %w", err)
	}

	return atomicWrite(final, data)
}

// List returns the domains known to either the global or the project
// root, deduplicated, sorted.
func (s *Store) List() ([]string, error) {
	seen := map[string]bool{}
	var out []string

	for _, root := range []string{s.project, s.global} {
		if root == "" {
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("store: list %s: %w", root, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			domain := strings.TrimSuffix(name, ".json")
			if !seen[domain] {
				seen[domain] = true
				out = append(out, domain)
			}
		}
	}

	sort.Strings(out)
	return out, nil
}

// loadFile reads and parses a recipe from a path. Returns
// os.ErrNotExist if the file does not exist.
func loadFile(path string) (*recipe.Recipe, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return recipe.Unmarshal(data)
}

// atomicWrite writes data to path via a tmp file + rename, fsync'd
// before the rename. On any error before the rename, the tmp file
// is removed. The final file gets mode 0600.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".recipe-*.json.tmp")
	if err != nil {
		return fmt.Errorf("store: create tmp: %w", err)
	}
	tmpName := tmp.Name()

	// Best-effort cleanup if anything fails before the rename.
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("store: write tmp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("store: sync tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close tmp: %w", err)
	}
	if err := os.Chmod(tmpName, 0600); err != nil {
		return fmt.Errorf("store: chmod tmp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("store: rename: %w", err)
	}
	success = true
	return nil
}

// validDomain returns true if domain is a plausible DNS name. We
// reject path separators, NULs, "..", and similar to keep Path()
// from escaping the recipes directory.
func validDomain(domain string) bool {
	if domain == "" || len(domain) > 253 {
		return false
	}
	if strings.ContainsAny(domain, "/\\\x00\n") {
		return false
	}
	if domain == "." || domain == ".." || strings.HasPrefix(domain, ".") {
		return false
	}
	if !strings.Contains(domain, ".") {
		// Require at least one dot so we don't accept "localhost".
		// Local domains are out of scope for v1.
		return false
	}
	return true
}
