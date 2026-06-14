package repl

import (
	"bytes"
	"strings"
	"testing"

	"github.com/falcon/api-recon/pkg/action"
)

func TestSuggest_NoTags(t *testing.T) {
	got := Suggest(nil, nil)
	if len(got) != 0 {
		t.Errorf("expected no suggestions for no tags, got %d", len(got))
	}
}

func TestSuggest_ShapeError(t *testing.T) {
	tags := []string{"shape:error", "shape:json_list"}
	got := Suggest(tags, nil)
	if len(got) == 0 {
		t.Fatal("expected suggestions for error+list tags")
	}
	// Highest-priority should be error-related.
	if !strings.Contains(got[0].Text, "error") && !strings.Contains(got[0].Text, "Fuzz") {
		t.Errorf("expected error/fuzz at top, got %q", got[0].Text)
	}
}

func TestSuggest_ShapeHLS(t *testing.T) {
	tags := []string{"shape:hls_master"}
	got := Suggest(tags, nil)
	found := false
	for _, s := range got {
		if strings.Contains(s.Text, "download") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected download suggestion for HLS, got %v", got)
	}
}

func TestSuggest_CrossHost(t *testing.T) {
	tags := []string{"auth:cross_host"}
	got := Suggest(tags, nil)
	found := false
	for _, s := range got {
		if strings.Contains(s.Text, "cross-host") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected cross-host probe suggestion, got %v", got)
	}
}

func TestSuggest_Dedup(t *testing.T) {
	// All playbooks return the same Text for the same input —
	// dedup should kick in.
	tags := []string{"shape:error"}
	got := Suggest(tags, nil)
	seen := map[string]int{}
	for _, s := range got {
		seen[s.Text]++
	}
	for text, count := range seen {
		if count > 1 {
			t.Errorf("dedup failed: %q appears %d times", text, count)
		}
	}
}

func TestSuggest_StarsOrdering(t *testing.T) {
	tags := []string{"shape:error", "shape:hls_master", "auth:cross_host"}
	got := Suggest(tags, nil)
	// First suggestion should have the highest stars value.
	for i := 1; i < len(got); i++ {
		if got[i].Stars > got[0].Stars {
			t.Errorf("ordering broken: %d stars > %d stars at index 0", got[i].Stars, got[0].Stars)
		}
	}
}

func TestBuildMenu(t *testing.T) {
	actions := []action.Action{
		{Name: "probe", Summary: "Send a request", Category: "discover"},
		{Name: "show", Summary: "Show recipe", Category: "meta"},
	}
	menu := BuildMenu(actions, nil, "anikage.cc")
	if len(menu.Entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(menu.Entries))
	}
}

func TestBuildMenu_SuggestionsMarkSuggested(t *testing.T) {
	actions := []action.Action{{Name: "probe", Summary: "Send a request"}}
	suggestions := []Suggestion{{Text: "Drill in", ActionID: "probe", Stars: 2}}
	menu := BuildMenu(actions, suggestions, "x.com")
	if !menu.Entries[0].Suggested {
		t.Error("entry with matching ActionID should be marked Suggested")
	}
}

func TestMenuLookup(t *testing.T) {
	actions := []action.Action{
		{Name: "probe", Aliases: []string{"p"}},
		{Name: "show", Aliases: []string{"s"}},
	}
	menu := BuildMenu(actions, nil, "")
	cases := []struct {
		input  string
		want   string
	}{
		{"1", "probe"},
		{"2", "show"},
		{"probe", "probe"},
		{"p", "probe"},
		{"P", "probe"},
		{"show", "show"},
		{"unknown", ""},
		{"", ""},
		{"99", ""},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			a, ok := menu.Lookup(tc.input)
			if tc.want == "" {
				if ok {
					t.Errorf("input %q should not match, got %q", tc.input, a.Name)
				}
				return
			}
			if !ok || a.Name != tc.want {
				t.Errorf("input %q: got (%q, %v), want %q", tc.input, a.Name, ok, tc.want)
			}
		})
	}
}

func TestMenuRender(t *testing.T) {
	actions := []action.Action{{Name: "probe", Summary: "Send a request"}}
	menu := BuildMenu(actions, nil, "anikage.cc")
	var buf bytes.Buffer
	menu.Render(&buf, "anikage.cc")
	out := buf.String()
	if !strings.Contains(out, "anikage.cc") {
		t.Errorf("output should contain domain, got %q", out)
	}
	if !strings.Contains(out, "probe") {
		t.Errorf("output should contain action name, got %q", out)
	}
	if !strings.Contains(out, "q. Quit") {
		t.Errorf("output should contain quit option, got %q", out)
	}
}

func TestDisplayDomain(t *testing.T) {
	if got := displayDomain(""); got != "no recipe" {
		t.Errorf("displayDomain('') = %q, want 'no recipe'", got)
	}
	if got := displayDomain("x.com"); got != "x.com" {
		t.Errorf("displayDomain('x.com') = %q", got)
	}
}

func TestIsQuit(t *testing.T) {
	for _, in := range []string{"q", "Q", "quit", "QUIT", "exit", ":q"} {
		if !isQuit(in) {
			t.Errorf("isQuit(%q) = false, want true", in)
		}
	}
	if isQuit("") || isQuit("x") {
		t.Error("isQuit should not match non-quit inputs")
	}
}

func TestHelpAction_ListAll(t *testing.T) {
	var buf bytes.Buffer
	act := HelpAction()
	res, err := act.Run(t.Context(), &action.Context{Stdout: &buf}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.Summary == "" {
		t.Error("expected summary")
	}
	if !strings.Contains(buf.String(), "Available actions") {
		t.Errorf("expected 'Available actions' header, got %q", buf.String())
	}
}

func TestHelpAction_OneAction(t *testing.T) {
	var buf bytes.Buffer
	act := HelpAction()
	res, err := act.Run(t.Context(), &action.Context{Stdout: &buf}, []string{"help"})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.Summary != "showed help for help" {
		t.Errorf("Summary = %v", res.Summary)
	}
	// Help text should contain the help action's own name and summary.
	if !strings.Contains(buf.String(), "help") {
		t.Errorf("output should contain action name, got %q", buf.String())
	}
}

func TestHelpAction_Unknown(t *testing.T) {
	var buf bytes.Buffer
	act := HelpAction()
	_, err := act.Run(t.Context(), &action.Context{Stdout: &buf}, []string{"nope"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no action named") {
		t.Errorf("expected 'no action named' message, got %q", buf.String())
	}
}
