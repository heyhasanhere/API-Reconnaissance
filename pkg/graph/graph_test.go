package graph

import (
	"os"
	"sort"
	"strings"
	"testing"
)

func loadTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return data
}

func TestBuild_Empty(t *testing.T) {
	g := Build(nil)
	if got := g.Paths(); len(got) != 0 {
		t.Errorf("Paths = %v, want []", got)
	}
}

func TestObserve_AddsNode(t *testing.T) {
	g := New()
	g.Observe(Observation{URL: "https://anikage.cc/api/episodes", Method: "GET", Status: 200})

	paths := g.Paths()
	if len(paths) != 1 || paths[0] != "/api/episodes" {
		t.Errorf("Paths = %v", paths)
	}
	n, ok := g.Node("/api/episodes")
	if !ok {
		t.Fatal("expected node at /api/episodes")
	}
	if n.Status != 200 {
		t.Errorf("Status = %d, want 200", n.Status)
	}
	if n.HitCount != 1 {
		t.Errorf("HitCount = %d, want 1", n.HitCount)
	}
}

func TestObserve_UpdatesNode(t *testing.T) {
	g := New()
	g.Observe(Observation{URL: "https://anikage.cc/api/episodes", Method: "GET", Status: 200})
	g.Observe(Observation{URL: "https://anikage.cc/api/episodes", Method: "GET", Status: 200})

	n, _ := g.Node("/api/episodes")
	if n.HitCount != 2 {
		t.Errorf("HitCount = %d, want 2", n.HitCount)
	}
	if n.Status != 200 {
		t.Errorf("Status = %d, want 200", n.Status)
	}
}

func TestObserve_PopulatesBodyAndShape(t *testing.T) {
	g := New()
	g.Observe(Observation{
		URL: "https://anikage.cc/api/episodes", Method: "GET", Status: 200,
		RespBody: loadTestdata(t, "anikage_episodes.json"),
	})
	n, _ := g.Node("/api/episodes")
	if n.Shape != "json_list" {
		t.Errorf("Shape = %q, want json_list", n.Shape)
	}
	if len(n.IDFields) == 0 || n.IDFields[0] != "id" {
		t.Errorf("IDFields = %v, want [id]", n.IDFields)
	}
	if len(n.BodySample) == 0 {
		t.Error("BodySample should be set")
	}
}

func TestSiblings_Anikage(t *testing.T) {
	g := New()
	g.Observe(Observation{
		URL: "https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/1/downloads", Method: "GET", Status: 500,
		RespBody: loadTestdata(t, "anikage_error.json"),
	})
	g.Observe(Observation{
		URL: "https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/1/sources", Method: "GET", Status: 200,
		RespBody: loadTestdata(t, "anikage_sources.json"),
	})
	g.Observe(Observation{
		URL: "https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/1/servers", Method: "GET", Status: 200,
		RespBody: []byte(`[{"name":"kiss"},{"name":"miko"}]`),
	})

	siblings := g.Siblings("/api/media/anime/zMLNvt6MtV/episodes/1/downloads")
	// The siblings test asserts "shares prefix" so we should see sources and servers.
	// downloads has prefix /api/media/anime/{slug}/episodes/1/. siblings: anything
	// in that directory.
	gotPaths := []string{}
	for _, s := range siblings {
		gotPaths = append(gotPaths, s.Path)
	}
	sort.Strings(gotPaths)
	wantContains := []string{
		"/api/media/anime/zMLNvt6MtV/episodes/1/sources",
		"/api/media/anime/zMLNvt6MtV/episodes/1/servers",
	}
	for _, want := range wantContains {
		found := false
		for _, got := range gotPaths {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Siblings of downloads missing %q (got %v)", want, gotPaths)
		}
	}
}

func TestSiblings_NoMatchForTopLevel(t *testing.T) {
	g := New()
	g.Observe(Observation{URL: "https://x.com/api", Method: "GET", Status: 200})
	if got := g.Siblings("/api"); got != nil {
		t.Errorf("top-level paths should have no siblings, got %v", got)
	}
}

func TestChildren_Parents(t *testing.T) {
	g := New()
	g.Observe(Observation{URL: "https://x.com/page", Method: "GET", Status: 200})
	g.Observe(Observation{URL: "https://x.com/api/users", Method: "GET", Status: 200})

	children := g.Children("/page")
	if len(children) != 1 || children[0].Path != "/api/users" {
		t.Errorf("Children of /page = %v, want [/api/users]", children)
	}
	parents := g.Parents("/api/users")
	if len(parents) != 1 || parents[0].Path != "/page" {
		t.Errorf("Parents of /api/users = %v, want [/page]", parents)
	}
}

func TestIDFlows(t *testing.T) {
	g := New()
	// The list of episodes emits `id` values.
	g.Observe(Observation{
		URL: "https://anikage.cc/api/episodes", Method: "GET", Status: 200,
		RespBody: loadTestdata(t, "anikage_episodes.json"),
	})
	// The detail endpoint has a {id} placeholder.
	g.Observe(Observation{
		URL: "https://anikage.cc/api/episodes/{id}", Method: "GET", Status: 200,
	})
	flows := g.IDFlows()
	if len(flows) == 0 {
		t.Fatal("expected at least one ID flow")
	}
	// First flow should be from /api/episodes (source) to /api/episodes/{id} (target).
	found := false
	for _, f := range flows {
		if f.Source == "/api/episodes" && f.Target == "/api/episodes/{id}" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected flow from /api/episodes to /api/episodes/{id}, got %+v", flows)
	}
}

func TestString(t *testing.T) {
	g := New()
	g.Observe(Observation{URL: "https://x.com/a", Method: "GET", Status: 200})
	g.Observe(Observation{URL: "https://x.com/b", Method: "GET", Status: 404})
	out := g.String()
	if !strings.Contains(out, "/a") || !strings.Contains(out, "/b") {
		t.Errorf("String() should contain both nodes: %q", out)
	}
	if !strings.Contains(out, "200") || !strings.Contains(out, "404") {
		t.Errorf("String() should contain both status codes: %q", out)
	}
}

func TestBuild_Synchronous(t *testing.T) {
	obs := []Observation{
		{URL: "https://x.com/a", Method: "GET", Status: 200},
		{URL: "https://x.com/b", Method: "GET", Status: 200},
	}
	g := Build(obs)
	paths := g.Paths()
	if len(paths) != 2 {
		t.Errorf("Paths = %v, want 2 entries", paths)
	}
}

func TestPathHasIDPlaceholder(t *testing.T) {
	cases := []struct {
		path, value string
		want        bool
	}{
		{"/api/{id}", "abc123", false},    // length mismatch
		{"/api/{abc123}", "abc123", true}, // exact
		{"/api/{ABC123}", "abc123", true}, // case-insensitive
		{"/api/{xyz123}", "abc123", false}, // same length but different content
		{"/api/no-placeholder", "abc123", false},
	}
	for _, tc := range cases {
		t.Run(tc.path+"-"+tc.value, func(t *testing.T) {
			if got := pathHasIDPlaceholder(tc.path, tc.value); got != tc.want {
				t.Errorf("pathHasIDPlaceholder(%q, %q) = %v, want %v", tc.path, tc.value, got, tc.want)
			}
		})
	}
}
