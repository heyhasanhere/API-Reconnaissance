package recipe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFillTemplate_OK(t *testing.T) {
	got, err := FillTemplate("/api/{slug}/episodes/{n}",
		map[string]string{"slug": "abc", "n": "1"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "/api/abc/episodes/1" {
		t.Errorf("got %q, want /api/abc/episodes/1", got)
	}
}

func TestFillTemplate_Missing(t *testing.T) {
	_, err := FillTemplate("/api/{slug}/episodes/{n}",
		map[string]string{"slug": "abc"})
	if err == nil {
		t.Error("expected error for missing placeholder {n}")
	}
}

func TestFillTemplate_Unknown(t *testing.T) {
	_, err := FillTemplate("/api/{slug}/x",
		map[string]string{"slug": "abc", "extra": "y"})
	if err == nil {
		t.Error("expected error for unknown placeholder")
	}
}

func TestSave_LoadRoundtrip(t *testing.T) {
	root := t.TempDir()
	s, err := NewStoreAt(root)
	if err != nil {
		t.Fatalf("NewStoreAt: %v", err)
	}
	r := &Recipe{
		Domain: "example.com",
		Chain: []Step{
			{Name: "episodes", URLTemplate: "/api/{slug}/episodes", Method: "GET"},
			{Name: "sources", URLTemplate: "/api/{slug}/episodes/{n}/sources?provider={p}", Method: "GET",
				Placeholders: []string{"slug", "n", "p"}, RequiredParams: []string{"provider"}},
		},
		Providers: []Provider{
			{Name: "miko", IsHLS: true},
		},
		Headers:        map[string]string{"Origin": "https://example.com"},
		StreamTemplate: "https://cdn.example/m3u8/{key}",
	}
	if err := s.Save(r); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Check mode 0600.
	info, err := os.Stat(s.Path("example.com"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("perm = %o, want 0600", perm)
	}

	got, err := s.Load("example.com")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Domain != "example.com" {
		t.Errorf("Domain = %q, want example.com", got.Domain)
	}
	if len(got.Chain) != 2 {
		t.Errorf("Chain length = %d, want 2", len(got.Chain))
	}
	if got.StreamTemplate != "https://cdn.example/m3u8/{key}" {
		t.Errorf("StreamTemplate = %q", got.StreamTemplate)
	}
}

func TestSave_PreservesDiscovered(t *testing.T) {
	root := t.TempDir()
	s, _ := NewStoreAt(root)
	first := &Recipe{Domain: "example.com", Chain: []Step{{Name: "a", URLTemplate: "/a"}}}
	if err := s.Save(first); err != nil {
		t.Fatalf("first save: %v", err)
	}
	loaded, _ := s.Load("example.com")
	originalDiscovered := loaded.Discovered

	// Second save with a different chain.
	second := &Recipe{Domain: "example.com", Chain: []Step{{Name: "b", URLTemplate: "/b"}}}
	if err := s.Save(second); err != nil {
		t.Fatalf("second save: %v", err)
	}
	loaded2, _ := s.Load("example.com")
	if !loaded2.Discovered.Equal(originalDiscovered) {
		t.Errorf("Discovered changed: was %v, now %v", originalDiscovered, loaded2.Discovered)
	}
}

func TestList_Empty(t *testing.T) {
	root := t.TempDir()
	s, _ := NewStoreAt(root)
	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List = %v, want []", got)
	}
}

func TestList_Sorted(t *testing.T) {
	root := t.TempDir()
	s, _ := NewStoreAt(root)
	for _, d := range []string{"zeta.com", "alpha.com", "mike.com"} {
		_ = s.Save(&Recipe{Domain: d, Chain: []Step{{Name: "x", URLTemplate: "/x"}}})
	}
	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"alpha.com", "mike.com", "zeta.com"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("List = %v, want %v", got, want)
	}
}

func TestLoad_Missing(t *testing.T) {
	root := t.TempDir()
	s, _ := NewStoreAt(root)
	_, err := s.Load("nope.com")
	if err == nil {
		t.Error("expected error for missing recipe")
	}
}

func TestSave_InvalidDomain(t *testing.T) {
	root := t.TempDir()
	s, _ := NewStoreAt(root)
	if err := s.Save(&Recipe{Domain: ""}); err == nil {
		t.Error("expected error for empty domain")
	}
	if err := s.Save(&Recipe{Domain: "../escape"}); err == nil {
		t.Error("expected error for path-traversal domain")
	}
}

func TestAtomicWrite_NoTempLeftovers(t *testing.T) {
	root := t.TempDir()
	s, _ := NewStoreAt(root)
	for i := 0; i < 5; i++ {
		_ = s.Save(&Recipe{Domain: "example.com", Chain: []Step{{Name: "x", URLTemplate: "/x"}}})
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover tmp file: %s", e.Name())
		}
	}
	// Should be exactly the one .json file.
	jsons := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			jsons++
		}
	}
	if jsons != 1 {
		t.Errorf("json count = %d, want 1", jsons)
	}
}

func TestValidDomain(t *testing.T) {
	good := []string{"example.com", "a.b.c", "sub.domain.example.com"}
	bad := []string{"", "no-dot", "../escape", "/abs/path", "with space.com"}
	for _, d := range good {
		if !validDomain(d) {
			t.Errorf("validDomain(%q) = false, want true", d)
		}
	}
	for _, d := range bad {
		if validDomain(d) {
			t.Errorf("validDomain(%q) = true, want false", d)
		}
	}
}

func TestNewStoreAt_EmptyRoot(t *testing.T) {
	if _, err := NewStoreAt(""); err == nil {
		t.Error("expected error for empty root")
	}
}

func TestNewStoreAt_CreatesDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "recipes")
	s, err := NewStoreAt(root)
	if err != nil {
		t.Fatalf("NewStoreAt: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("dir not created: %v", err)
	}
	_ = s
}
