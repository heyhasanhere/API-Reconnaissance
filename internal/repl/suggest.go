// Package repl implements the interactive REPL: the numbered menu,
// the prompt, the suggestion engine, the playbooks, and the loop.
//
// The REPL is the front door for first-time discovery of a domain.
// After the user has saved a recipe, they don't touch the REPL —
// they use `api-recon run <domain>`.
package repl

import (
	"fmt"
	"sort"
	"strings"

	"github.com/falcon/api-recon/pkg/shape"
)

// Suggestion is one menu option the REPL shows to the user.
type Suggestion struct {
	Text     string
	Stars    int    // 1-3, drives menu ordering (more stars = higher)
	ActionID string // which action to run when chosen
	Args     []string
	Reason   string // short rationale shown after the suggestion
}

// Suggest computes heuristic suggestions from a Result's Tags. It
// also runs the playbooks (see playbooks.go) and dedupes by Text.
func Suggest(tags []string, lastShape *shape.Shape) []Suggestion {
	seen := map[string]bool{}
	var out []Suggestion

	add := func(s Suggestion) {
		if seen[s.Text] {
			return
		}
		seen[s.Text] = true
		out = append(out, s)
	}

	for _, sg := range heuristicSuggestions(tags, lastShape) {
		add(sg)
	}
	for _, sg := range playbookSuggestions(tags, lastShape) {
		add(sg)
	}

	// Stable order: by Stars desc, then Text asc.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Stars != out[j].Stars {
			return out[i].Stars > out[j].Stars
		}
		return out[i].Text < out[j].Text
	})
	return out
}

// heuristicSuggestions maps tags to suggestions. The rules are
// defined here (a single table) and playbooks can add more.
func heuristicSuggestions(tags []string, lastShape *shape.Shape) []Suggestion {
	has := func(want string) bool {
		for _, t := range tags {
			if t == want {
				return true
			}
		}
		return false
	}

	var out []Suggestion

	if has("shape:json_list") {
		out = append(out, Suggestion{
			Text:     "Drill into one item",
			Stars:    2,
			ActionID: "drill",
			Reason:   "list endpoint — pick an id to fetch the detail",
		})
	}

	if has("shape:error") {
		out = append(out, Suggestion{
			Text:     "Read the error and try sibling endpoints",
			Stars:    3,
			ActionID: "siblings",
			Reason:   "error response — likely a value or path mismatch",
		})
		out = append(out, Suggestion{
			Text:     "Fuzz from the error message",
			Stars:    3,
			ActionID: "fuzz",
			Reason:   "extract values from the error and try them",
		})
	}

	if has("shape:hls_master") || has("shape:hls_variant") {
		out = append(out, Suggestion{
			Text:     "Generate a download command",
			Stars:    3,
			ActionID: "download",
			Reason:   "HLS stream detected — pick a downloader",
		})
	}

	if has("auth:cross_host") {
		out = append(out, Suggestion{
			Text:     "Probe the cross-host URL",
			Stars:    2,
			ActionID: "probe",
			Reason:   "verify the captured auth works for the CDN host",
		})
	}

	if has("download:ready") {
		out = append(out, Suggestion{
			Text:     "Save the recipe and exit",
			Stars:    2,
			ActionID: "save",
			Reason:   "recipe is complete enough to save and replay later",
		})
	}

	return out
}

// playbookSuggestions runs all the playbooks and returns their
// suggestions. Playbooks are defined in playbooks.go.
func playbookSuggestions(tags []string, lastShape *shape.Shape) []Suggestion {
	var out []Suggestion
	for _, pb := range playbooks() {
		out = append(out, pb(tags, lastShape)...)
	}
	return out
}

// Playbook is a function that returns suggestions for a given tag
// set. Each playbook is a self-contained "if X then suggest Y."
type Playbook func(tags []string, lastShape *shape.Shape) []Suggestion

// playbooks returns the built-in playbooks. Order doesn't matter;
// suggestions are deduped and re-sorted by Stars.
func playbooks() []Playbook {
	return []Playbook{
		playbookVideoStreaming,
		playbookAuthLogin,
		playbookPagedData,
		playbookBrokenAPI,
	}
}

func playbookVideoStreaming(tags []string, _ *shape.Shape) []Suggestion {
	for _, t := range tags {
		if t == "shape:hls_master" || t == "shape:hls_variant" || t == "shape:dash" {
			return []Suggestion{{
				Text:     "Try different provider values from sibling endpoints",
				Stars:    2,
				ActionID: "siblings",
				Reason:   "video endpoints often have multiple providers",
			}}
		}
	}
	return nil
}

func playbookAuthLogin(tags []string, _ *shape.Shape) []Suggestion {
	for _, t := range tags {
		// 401/403-style observations show up as auth:missing in
		// tags from the capture pipeline (we don't have a real
		// "auth" pipeline yet, but the tag is reserved).
		if t == "auth:missing" {
			return []Suggestion{{
				Text:     "Find a login endpoint in captured traffic",
				Stars:    2,
				ActionID: "search",
				Reason:   "401/403 — look for /login or /auth in earlier requests",
			}}
		}
	}
	return nil
}

func playbookPagedData(tags []string, lastShape *shape.Shape) []Suggestion {
	has := func(want string) bool {
		for _, t := range tags {
			if t == want {
				return true
			}
		}
		return false
	}
	if has("shape:json_list") && lastShape != nil && lastShape.PaginationParam != "" {
		return []Suggestion{{
			Text:     fmt.Sprintf("Try %s=2 (next page)", lastShape.PaginationParam),
			Stars:    2,
			ActionID: "probe",
			Reason:   "pagination detected — verify it works",
		}}
	}
	return nil
}

func playbookBrokenAPI(tags []string, _ *shape.Shape) []Suggestion {
	for _, t := range tags {
		if t == "shape:error" {
			return []Suggestion{{
				Text:     "Try boundary values (-1, 0, 99999)",
				Stars:    1,
				ActionID: "fuzz",
				Reason:   "5xx — boundary fuzzing might find a working value",
			}}
		}
	}
	return nil
}

// String formats a suggestion for the menu. With star prefix if Stars > 0.
func (s Suggestion) String(starred bool) string {
	if starred && s.Stars > 0 {
		return "★ " + s.Text
	}
	return s.Text
}

// joinReasons returns the suggestions' Reasons joined with newlines,
// for the "Next suggestions:" block under the menu.
func joinReasons(suggestions []Suggestion, max int) string {
	if len(suggestions) == 0 {
		return ""
	}
	if max > 0 && len(suggestions) > max {
		suggestions = suggestions[:max]
	}
	var parts []string
	for _, s := range suggestions {
		if s.Reason != "" {
			parts = append(parts, "  "+s.Reason)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "\nNext suggestions:\n" + strings.Join(parts, "\n")
}
