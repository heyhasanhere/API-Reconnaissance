// Package action defines the Action type that powers api-recon's
// REPL, dispatch table, and --help output. A single Action struct
// is the source of truth for what a subcommand does, what flags it
// accepts, and how to run it. The REPL reads this struct to render
// the menu; the dispatcher reads it to parse argv; --help reads it
// to generate docs.
//
// Action is a struct with a Run func field, not an interface. This
// keeps the metadata as a literal (easy to read, easy to grep) and
// the runner as a closure (captures deps cleanly).
package action

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/falcon/api-recon/pkg/creds"
	"github.com/falcon/api-recon/pkg/graph"
	"github.com/falcon/api-recon/pkg/recipe"
	"github.com/falcon/api-recon/pkg/shape"
)

// Flag is a single CLI flag with annotations the REPL/help text
// uses. LookFor is "what to look for in the output" — surfaced in
// the help text as a hint to the user about why this flag exists.
type Flag struct {
	Name        string // "filter"
	Type        string // "string", "int", "bool", "duration"
	Default     string
	Description string
	LookFor     string
}

// Action is a single subcommand or REPL option.
type Action struct {
	Name        string   // "probe", "watch", "harvest", "run"
	Aliases     []string // ["p"] for probe
	Summary     string   // one line, shown in menu
	Description string   // paragraph(s) for --help
	Flags       []Flag
	Examples    []string
	Category    string // "discover" | "replay" | "meta"

	// Run executes the action. Returns a Result the REPL inspects
	// for Summary, Tags (driving heuristic suggestions), Data
	// (opaque payload), and Hints (short human suggestions).
	Run func(ctx context.Context, c *Context, args []string) (*Result, error)
}

// Result is what an Action returns. The REPL inspects Tags to
// suggest next steps, prints Summary, prints Hints, and may save
// the Data payload (e.g. an in-progress recipe).
type Result struct {
	Summary string
	Tags    []string // "shape:json_list", "auth:cross_host", etc.
	Data    any
	Hints   []string
}

// Context flows through every Action.Run. It carries the in-flight
// state: the recipe being built (during harvest) or loaded (during
// run), the live graph and credentials, the shape classifier, and
// the last HTTP exchange (so subsequent actions can build on it).
type Context struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader

	Logger Logger

	Recipe *recipe.Recipe
	Graph  *graph.Graph
	Creds  *creds.Store
	Shape  *shape.Classifier

	// LastResponse / LastBody — the most recent HTTP exchange. Set
	// by probe-like actions so the REPL can offer "show me that
	// response" and so subsequent actions can build on it.
	LastResponse *http.Response
	LastBody     []byte

	// Scratch is a free-form bag for action-to-action handoff. Use
	// sparingly — most cross-action data should live in Recipe/Graph.
	Scratch map[string]any
}

// Logger is a minimal structured-log sink. The default is
// log.Default() bound to Stderr; the REPL may swap it for a
// silent logger in --json mode.
type Logger interface {
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
	Debug(msg string, kv ...any)
}

// Help renders a multi-line help string for an Action. Used by
// --help on the dispatcher and by the REPL's "help" menu option.
func Help(a Action) string {
	var b strings.Builder
	b.WriteString(a.Name)
	if len(a.Aliases) > 0 {
		b.WriteString(" (aliases: ")
		b.WriteString(strings.Join(a.Aliases, ", "))
		b.WriteString(")")
	}
	b.WriteString("\n")
	if a.Summary != "" {
		b.WriteString("  ")
		b.WriteString(a.Summary)
		b.WriteString("\n")
	}
	if a.Description != "" {
		b.WriteString("\n")
		b.WriteString(a.Description)
		b.WriteString("\n")
	}
	if len(a.Flags) > 0 {
		b.WriteString("\nFlags:\n")
		for _, f := range a.Flags {
			b.WriteString("  -")
			b.WriteString(f.Name)
			b.WriteString(" (")
			b.WriteString(f.Type)
			if f.Default != "" {
				b.WriteString(", default ")
				b.WriteString(f.Default)
			}
			b.WriteString(")\n")
			if f.Description != "" {
				b.WriteString("      ")
				b.WriteString(f.Description)
				b.WriteString("\n")
			}
			if f.LookFor != "" {
				b.WriteString("      Look for: ")
				b.WriteString(f.LookFor)
				b.WriteString("\n")
			}
		}
	}
	if len(a.Examples) > 0 {
		b.WriteString("\nExamples:\n")
		for _, ex := range a.Examples {
			b.WriteString("  ")
			b.WriteString(ex)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// OneLineHelp returns a compact "name — summary" string, used in
// the REPL menu header.
func OneLineHelp(a Action) string {
	if a.Summary == "" {
		return a.Name
	}
	return a.Name + " — " + a.Summary
}
