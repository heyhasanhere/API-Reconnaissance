package repl

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/falcon/api-recon/pkg/action"
)

// MenuEntry is one numbered item in the REPL menu.
type MenuEntry struct {
	Index  int
	Label  string
	Action action.Action
	Suggested bool
}

// Menu is a snapshot of the REPL's current menu. The REPL
// regenerates the menu every loop iteration so it reflects the
// current state (suggestions change after each action).
type Menu struct {
	Entries []MenuEntry
	// Suggestions are the "next steps" shown after the menu, not
	// selectable directly. They become menu items when the user
	// picks them via "?" or "suggest" or by typing the number
	// from a fresh menu.
	Suggestions []Suggestion
}

// Render writes the menu to w. Format:
//
//	api-recon [domain] >
//
//	  1. Probe a URL
//	  2. ★ Drill into one item     (suggested)
//	  3. Save recipe and exit
//	  q. Quit
//
//	pick a number, type a name, or "help":
func (m Menu) Render(w io.Writer, domain string) {
	fmt.Fprintf(w, "\napi-recon [%s] >\n", displayDomain(domain))

	// Sort: suggested first (by Stars), then by category/name.
	entries := append([]MenuEntry{}, m.Entries...)
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Suggested != entries[j].Suggested {
			return entries[i].Suggested
		}
		return entries[i].Index < entries[j].Index
	})

	// Re-number after sorting.
	for i := range entries {
		entries[i].Index = i + 1
	}

	for _, e := range entries {
		label := e.Label
		if e.Suggested {
			label = "★ " + label
		}
		fmt.Fprintf(w, "  %2d. %s\n", e.Index, label)
	}
	fmt.Fprintln(w, "   q. Quit")

	if reasons := joinReasons(m.Suggestions, 5); reasons != "" {
		fmt.Fprintln(w, reasons)
	}
}

// displayDomain returns domain or "no recipe" if empty.
func displayDomain(d string) string {
	if d == "" {
		return "no recipe"
	}
	return d
}

// BuildMenu constructs a Menu from the registered actions and
// current suggestions.
func BuildMenu(actions []action.Action, suggestions []Suggestion, recipeDomain string) Menu {
	var entries []MenuEntry
	// Map suggestion.ActionID to a flag we set on the menu.
	sugByID := map[string]bool{}
	for _, s := range suggestions {
		if s.ActionID != "" {
			sugByID[s.ActionID] = true
		}
	}

	// Sort actions by Category then Name for stable order.
	sorted := append([]action.Action{}, actions...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Category != sorted[j].Category {
			return sorted[i].Category < sorted[j].Category
		}
		return sorted[i].Name < sorted[j].Name
	})

	for i, a := range sorted {
		label := action.OneLineHelp(a)
		entries = append(entries, MenuEntry{
			Index:    i + 1,
			Label:    label,
			Action:   a,
			Suggested: sugByID[a.Name],
		})
	}

	return Menu{Entries: entries, Suggestions: suggestions}
}

// Lookup finds the action for a given user input. Accepts a number
// (1-based index), an action name, or an alias. Returns the action
// and true if found.
func (m Menu) Lookup(input string) (action.Action, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return action.Action{}, false
	}
	// Try numeric.
	var idx int
	if _, err := fmt.Sscanf(input, "%d", &idx); err == nil {
		if idx >= 1 && idx <= len(m.Entries) {
			return m.Entries[idx-1].Action, true
		}
	}
	// Try name or alias.
	lower := strings.ToLower(input)
	for _, e := range m.Entries {
		if strings.ToLower(e.Action.Name) == lower {
			return e.Action, true
		}
		for _, a := range e.Action.Aliases {
			if strings.ToLower(a) == lower {
				return e.Action, true
			}
		}
	}
	return action.Action{}, false
}
