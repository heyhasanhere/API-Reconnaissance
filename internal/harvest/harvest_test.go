package harvest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/falcon/api-recon/pkg/action"
	"github.com/falcon/api-recon/pkg/creds"
	"github.com/falcon/api-recon/pkg/graph"
	"github.com/falcon/api-recon/pkg/recipe"
	"github.com/falcon/api-recon/pkg/shape"
)

func newCtx(t *testing.T) (*action.Context, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`[{"id":"abc","title":"ep1"}]`))
	}))
	c := &action.Context{
		Stdout: &strings.Builder{},
		Stderr: &strings.Builder{},
		Creds:  creds.New(),
		Graph:  graph.New(),
		Shape:  shape.New(),
	}
	return c, server
}

func TestRun_RequiresURL(t *testing.T) {
	c, srv := newCtx(t)
	defer srv.Close()
	a := Action()
	_, err := a.Run(context.Background(), c, nil)
	if err == nil {
		t.Error("expected error for missing URL")
	}
}

func TestRun_ProbesURL(t *testing.T) {
	c, srv := newCtx(t)
	defer srv.Close()
	a := Action()
	res, err := a.Run(context.Background(), c, []string{srv.URL + "/api/episodes"})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil {
		t.Fatal("expected result")
	}
	if res.Summary == "" {
		t.Error("Summary should be set")
	}
	// Should have shape:json_list tag.
	found := false
	for _, tag := range res.Tags {
		if tag == "shape:json_list" {
			found = true
		}
	}
	if !found {
		t.Errorf("Tags should contain shape:json_list, got %v", res.Tags)
	}
	// Recipe should have an endpoint named after the path's last segment.
	if c.Recipe == nil {
		t.Fatal("Recipe should be created")
	}
	ep, ok := c.Recipe.Endpoints["episodes"]
	if !ok {
		t.Errorf("Recipe should have endpoint 'episodes', got %v", c.Recipe.Endpoints)
	}
	if ep.Shape != "json_list" {
		t.Errorf("Endpoint shape = %q, want json_list", ep.Shape)
	}
}

func TestRun_RejectsRelativeURL(t *testing.T) {
	c, srv := newCtx(t)
	defer srv.Close()
	a := Action()
	_, err := a.Run(context.Background(), c, []string{"/relative/path"})
	if err == nil {
		t.Error("expected error for relative URL")
	}
}

func TestEndpointNameFromPath(t *testing.T) {
	cases := map[string]string{
		"/":                              "root",
		"/api":                           "api",
		"/api/episodes":                  "episodes",
		"/api/media/anime/{slug}":        "{slug}",
		"/api/media/anime/{slug}/episodes": "episodes",
		"":                               "root",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := endpointNameFromPath(in); got != want {
				t.Errorf("endpointNameFromPath(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

func TestQueryParamNames(t *testing.T) {
	got := queryParamNames("provider=miko&lang=sub")
	if len(got) != 2 {
		t.Errorf("got %v, want 2 names", got)
	}
}

func TestTagsForShape(t *testing.T) {
	s := shape.Shape{Kind: shape.KindJSONList, CrossHost: "cdn.example.com"}
	tags := tagsForShape(s, "example.com")
	wantHas := []string{"shape:json_list", "auth:cross_host", "host:cdn.example.com", "graph:list_endpoint"}
	for _, w := range wantHas {
		found := false
		for _, t := range tags {
			if t == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("tags should contain %q, got %v", w, tags)
		}
	}
}

func TestRun_BuildsRecipeWithAuth(t *testing.T) {
	c, srv := newCtx(t)
	defer srv.Close()
	// Pre-populate creds with an Origin header (the anikage case).
	c.Creds.ObserveHeaders(http.Header{"Origin": []string{"https://example.com"}}, nil, "https://example.com")

	a := Action()
	_, err := a.Run(context.Background(), c, []string{srv.URL + "/api/x"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Recipe.Auth.RequiredHeaders["Origin"] != "https://example.com" {
		t.Errorf("Recipe auth should carry captured Origin, got %v", c.Recipe.Auth)
	}
}

func TestRun_ObservesThroughGraph(t *testing.T) {
	c, srv := newCtx(t)
	defer srv.Close()
	a := Action()
	_, err := a.Run(context.Background(), c, []string{srv.URL + "/api/episodes"})
	if err != nil {
		t.Fatal(err)
	}
	paths := c.Graph.Paths()
	if len(paths) == 0 {
		t.Error("Graph should have a path")
	}
}

func TestPickArg(t *testing.T) {
	if got := pickArg([]string{"--method", "POST", "url"}, "--method"); got != "POST" {
		t.Errorf("pickArg = %q, want POST", got)
	}
	if got := pickArg([]string{"url"}, "--method"); got != "" {
		t.Errorf("pickArg (missing) = %q", got)
	}
	if got := pickArg([]string{"--method"}, "--method"); got != "" {
		t.Errorf("pickArg (no value) = %q", got)
	}
}

// Compile-time check that action.Action has the fields we set.
var _ = recipe.New
