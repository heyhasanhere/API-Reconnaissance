package fuzz

import (
	"net/url"
	"strings"
	"testing"

	"github.com/falcon/api-recon/pkg/graph"
)

func TestFromErrorMessage_Anikage(t *testing.T) {
	last := &graph.Observation{
		URL:      "https://anikage.cc/api/media/anime/z/episodes/1/downloads?provider=pahe",
		Method:   "GET",
		Status:   500,
		RespBody: []byte(`{"success":false,"error":{"message":"No episodes found for provider pahe"}}`),
	}
	cands := FromErrorMessage{}.Candidates(nil, last)
	if len(cands) == 0 {
		t.Fatal("expected at least one candidate")
	}
	c := cands[0]
	if !strings.Contains(c.URL, "provider=pahe") {
		t.Errorf("URL should contain provider=pahe, got %q", c.URL)
	}
	if c.Source != "error_msg" {
		t.Errorf("Source = %q, want error_msg", c.Source)
	}
	if c.Stars != 3 {
		t.Errorf("Stars = %d, want 3", c.Stars)
	}
}

func TestFromErrorMessage_NoMatch(t *testing.T) {
	last := &graph.Observation{
		URL:      "https://x/y",
		RespBody: []byte(`{"data":[]}`),
	}
	strat := FromErrorMessage{}
	got := strat.Candidates(nil, last)
	if got != nil {
		t.Errorf("expected no candidates, got %v", got)
	}
}

func TestFromErrorMessage_NilLast(t *testing.T) {
	strat := FromErrorMessage{}
	got := strat.Candidates(nil, nil)
	if got != nil {
		t.Errorf("expected no candidates for nil last, got %v", got)
	}
}

func TestFromSiblings(t *testing.T) {
	g := graph.New()
	g.Observe(graph.Observation{URL: "https://x.com/episodes/1/downloads", Method: "GET", Status: 500})
	g.Observe(graph.Observation{URL: "https://x.com/episodes/1/sources", Method: "GET", Status: 200})
	g.Observe(graph.Observation{URL: "https://x.com/episodes/1/servers", Method: "GET", Status: 200})

	last := &graph.Observation{
		URL:    "https://x.com/episodes/1/downloads",
		Status: 500,
	}
	cands := FromSiblings{}.Candidates(g, last)
	if len(cands) < 2 {
		t.Errorf("expected at least 2 sibling candidates, got %d", len(cands))
	}
	for _, c := range cands {
		if c.Source != "sibling" {
			t.Errorf("Source = %q, want sibling", c.Source)
		}
	}
}

func TestFromSiblings_SkipsFailing(t *testing.T) {
	g := graph.New()
	g.Observe(graph.Observation{URL: "https://x.com/episodes/1/downloads", Status: 500})
	g.Observe(graph.Observation{URL: "https://x.com/episodes/1/sources", Status: 500})

	last := &graph.Observation{URL: "https://x.com/episodes/1/downloads", Status: 500}
	cands := FromSiblings{}.Candidates(g, last)
	if len(cands) != 0 {
		t.Errorf("siblings that are also failing should be skipped, got %d", len(cands))
	}
}

func TestFromBoundary(t *testing.T) {
	last := &graph.Observation{
		URL:    "https://x.com/episodes/1/sources",
		Method: "GET",
	}
	cands := FromBoundary{}.Candidates(nil, last)
	if len(cands) == 0 {
		t.Fatal("expected boundary candidates")
	}
	// We should not include 1 (the original) in the results.
	for _, c := range cands {
		if strings.HasSuffix(c.URL, "/episodes/1/sources") {
			t.Errorf("boundary should not echo the original value: %q", c.URL)
		}
	}
}

func TestFromBoundary_NoNumericSegment(t *testing.T) {
	last := &graph.Observation{URL: "https://x.com/foo/bar"}
	strat := FromBoundary{}
	got := strat.Candidates(nil, last)
	if got != nil {
		t.Errorf("expected no candidates for path with no numeric segment, got %v", got)
	}
}

func TestFromUI(t *testing.T) {
	last := &graph.Observation{URL: "https://x.com/sources"}
	strat := FromUI{Values: []string{"kiss", "miko", "verse"}, Param: "provider"}
	cands := strat.Candidates(nil, last)
	if len(cands) != 3 {
		t.Errorf("expected 3 UI candidates, got %d", len(cands))
	}
	for _, c := range cands {
		if c.Source != "ui" {
			t.Errorf("Source = %q, want ui", c.Source)
		}
		if !strings.Contains(c.URL, "provider=") {
			t.Errorf("URL should contain provider=, got %q", c.URL)
		}
	}
}

func TestFromUI_SkipsExisting(t *testing.T) {
	last := &graph.Observation{URL: "https://x.com/sources?provider=miko"}
	strat := FromUI{Values: []string{"miko", "kiss"}, Param: "provider"}
	cands := strat.Candidates(nil, last)
	if len(cands) != 1 {
		t.Errorf("expected 1 candidate (miko should be skipped), got %d", len(cands))
	}
}

func TestGuessParamName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "provider"},
		{"foo=bar", "foo"},
		{"provider=x&lang=y", "provider"},
		{"lang=y&provider=x", "provider"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			q, _ := url.ParseQuery(tc.in)
			if got := guessParamName(q); got != tc.want {
				t.Errorf("guessParamName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestStrategies(t *testing.T) {
	s := Strategies()
	if len(s) == 0 {
		t.Fatal("Strategies() returned empty")
	}
	names := []string{}
	for _, strat := range s {
		names = append(names, strat.Name())
	}
	want := []string{"error_msg", "sibling", "boundary"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("Strategies names = %v, want %v", names, want)
	}
}
