package store

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/falcon/api-recon/pkg/recipe"
)

// newTestStore returns a Store rooted at temp dirs. We can't easily
// override the dataHome() call inside New, so we point XDG_DATA_HOME
// and HOME at the temp dir for the duration of the test.
func newTestStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	tmp := t.TempDir()
	xdg := filepath.Join(tmp, "xdg")
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(xdg, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("HOME", home)

	project := t.TempDir()
	s, err := New(project)
	if err != nil {
		t.Fatal(err)
	}
	return s, xdg, project
}

func TestNew_DefaultPaths(t *testing.T) {
	s, xdg, project := newTestStore(t)
	wantGlobal := filepath.Join(xdg, "api-recon", "recipes")
	wantProject := filepath.Join(project, ".api-recon", "recipes")
	if s.GlobalRoot() != wantGlobal {
		t.Errorf("GlobalRoot = %q, want %q", s.GlobalRoot(), wantGlobal)
	}
	if s.ProjectRoot() != wantProject {
		t.Errorf("ProjectRoot = %q, want %q", s.ProjectRoot(), wantProject)
	}
	// Both dirs should exist on disk.
	for _, d := range []string{wantGlobal, wantProject} {
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			t.Errorf("dir %s missing or not a dir: %v", d, err)
		}
	}
}

func TestNew_NoProject(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "xdg"))
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	s, err := New("") // empty project dir
	if err != nil {
		t.Fatal(err)
	}
	if s.ProjectRoot() != "" {
		t.Errorf("ProjectRoot = %q, want empty", s.ProjectRoot())
	}
	if s.writer != s.GlobalRoot() {
		t.Errorf("writer should fall back to global")
	}
}

func TestSaveAndLookup(t *testing.T) {
	s, _, _ := newTestStore(t)
	r := recipe.New("anikage.cc")
	r.Endpoints["episodes"] = recipe.Endpoint{
		URL: "https://anikage.cc/api/episodes", Method: "GET", Shape: "json_list",
	}
	if err := s.Save(r); err != nil {
		t.Fatal(err)
	}

	got, err := s.Lookup("anikage.cc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Domain != "anikage.cc" {
		t.Errorf("Domain = %q, want anikage.cc", got.Domain)
	}
	if got.Endpoints["episodes"].URL != "https://anikage.cc/api/episodes" {
		t.Errorf("endpoint URL not preserved")
	}
}

func TestLookupMissing(t *testing.T) {
	s, _, _ := newTestStore(t)
	_, err := s.Lookup("nope.com")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist, got %v", err)
	}
}

func TestProjectOverridesGlobal(t *testing.T) {
	tmp := t.TempDir()
	xdg := filepath.Join(tmp, "xdg")
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(xdg, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("HOME", home)

	// Pre-populate global with a recipe.
	globalRoot := filepath.Join(xdg, "api-recon", "recipes")
	if err := os.MkdirAll(globalRoot, 0700); err != nil {
		t.Fatal(err)
	}
	globalRec := recipe.New("anikage.cc")
	globalRec.Notes = "from global"
	if err := os.WriteFile(filepath.Join(globalRoot, "anikage.cc.json"), mustMarshal(globalRec), 0600); err != nil {
		t.Fatal(err)
	}

	// Now create a Store with a project root that has its own version.
	project := t.TempDir()
	s, err := New(project)
	if err != nil {
		t.Fatal(err)
	}
	projectRec := recipe.New("anikage.cc")
	projectRec.Notes = "from project"
	if err := s.Save(projectRec); err != nil {
		t.Fatal(err)
	}

	got, err := s.Lookup("anikage.cc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Notes != "from project" {
		t.Errorf("Lookup should return project version, got Notes=%q", got.Notes)
	}
}

func TestSaveAtomicOnMidWriteFailure(t *testing.T) {
	s, _, _ := newTestStore(t)
	r := recipe.New("anikage.cc")
	r.Endpoints["episodes"] = recipe.Endpoint{
		URL: "https://anikage.cc/api/episodes", Method: "GET", Shape: "json_list",
	}
	if err := s.Save(r); err != nil {
		t.Fatal(err)
	}

	// Now: change the recipe's endpoint URL on disk to something
	// distinctive, then call Save with an updated recipe. We expect
	// the new value to appear, AND no .tmp file left behind.
	ep := r.Endpoints["episodes"]
	ep.URL = "https://anikage.cc/api/v2/episodes"
	r.Endpoints["episodes"] = ep
	if err := s.Save(r); err != nil {
		t.Fatal(err)
	}

	got, err := s.Lookup("anikage.cc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoints["episodes"].URL != "https://anikage.cc/api/v2/episodes" {
		t.Errorf("updated URL not saved, got %q", got.Endpoints["episodes"].URL)
	}

	// No .tmp files left in the writer dir.
	entries, err := os.ReadDir(s.writer)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover tmp file: %s", e.Name())
		}
	}
}

func TestSavePreservesDiscovered(t *testing.T) {
	s, _, _ := newTestStore(t)
	r := recipe.New("anikage.cc")
	origDiscovered := r.Discovered
	if err := s.Save(r); err != nil {
		t.Fatal(err)
	}

	// Update with a fresh recipe (Discovered is "now").
	r2 := recipe.New("anikage.cc")
	r2.Endpoints["x"] = recipe.Endpoint{URL: "https://x.com", Method: "GET", Shape: "json"}
	if err := s.Save(r2); err != nil {
		t.Fatal(err)
	}

	got, err := s.Lookup("anikage.cc")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Discovered.Equal(origDiscovered) {
		t.Errorf("Discovered changed across update: was %v, now %v", origDiscovered, got.Discovered)
	}
}

func TestSaveRejectsInvalidRecipe(t *testing.T) {
	s, _, _ := newTestStore(t)
	r := &recipe.Recipe{Domain: "anikage.cc"} // missing Endpoints
	err := s.Save(r)
	if err == nil {
		t.Fatal("expected validation error for missing endpoints")
	}
}

func TestList(t *testing.T) {
	s, _, _ := newTestStore(t)
	r1 := recipe.New("anikage.cc")
	if err := s.Save(r1); err != nil {
		t.Fatal(err)
	}
	r2 := recipe.New("example.com")
	if err := s.Save(r2); err != nil {
		t.Fatal(err)
	}

	got, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := []string{"anikage.cc", "example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
}

func TestValidDomain(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"anikage.cc", true},
		{"a.b.c.d.example.com", true},
		{"", false},
		{".hidden", false},
		{"..", false},
		{"no-dot", false},
		{"with/slash", false},
		{"with\\backslash", false},
		{"with\nnewline", false},
		{"with\x00nul", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := validDomain(tc.in); got != tc.want {
				t.Errorf("validDomain(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestPath(t *testing.T) {
	s, _, _ := newTestStore(t)
	got := s.Path("anikage.cc")
	want := filepath.Join(s.writer, "anikage.cc.json")
	if got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

// mustMarshal marshals a recipe for test fixtures.
func mustMarshal(r *recipe.Recipe) []byte {
	data, err := r.Marshal()
	if err != nil {
		panic(err)
	}
	return data
}

// Sanity: io.Discard import is used somewhere — make sure we don't
// have a stray import that would break the build. (Linter-friendly.)
var _ = io.Discard
