package repl

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/heyhasanhere/API-Reconnaissance/pkg/action"
	"github.com/heyhasanhere/API-Reconnaissance/pkg/shape"
)

// REPL is the interactive prompt loop. Construct with New, run
// with Run. The REPL owns no state beyond the action list and the
// in-flight context; all recipe/graph/creds state lives on the
// context.
type REPL struct {
	actions []action.Action
	ctx     *action.Context
	in      *bufio.Reader
	out     io.Writer
	errOut  io.Writer
}

// New returns a REPL bound to the given context. The context
// carries recipe/graph/creds state and the I/O streams.
func New(ctx *action.Context, actions []action.Action) *REPL {
	if ctx == nil {
		ctx = &action.Context{}
	}
	if ctx.Stdout == nil {
		ctx.Stdout = os.Stdout
	}
	if ctx.Stderr == nil {
		ctx.Stderr = os.Stderr
	}
	if ctx.Stdin == nil {
		ctx.Stdin = os.Stdin
	}
	all := append([]action.Action{}, actions...)
	all = append(all, HelpAction())
	return &REPL{
		actions: all,
		ctx:     ctx,
		in:      bufio.NewReader(ctx.Stdin),
		out:     ctx.Stdout,
		errOut:  ctx.Stderr,
	}
}

// Run reads lines, dispatches, and prints results. Returns when
// the user types 'q', EOF, or context cancellation.
func (r *REPL) Run(ctx context.Context) error {
	if r.ctx.Recipe != nil {
		CheckPlaywrightSetup(r.out, r.errOut)
	}

	// Initial menu.
	r.printMenu(nil)

	for {
		fmt.Fprint(r.out, "\npick a number, name, or 'q': ")
		line, err := r.in.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				fmt.Fprintln(r.out, "\nbye.")
				return nil
			}
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if isQuit(line) {
			fmt.Fprintln(r.out, "bye.")
			return nil
		}

		// Split "action args..." so the user can type
		// "harvest https://..." rather than just "harvest".
		name, args := splitActionLine(line)

		// Resolve to an action.
		act, ok := r.lookupAction(name)
		if !ok {
			fmt.Fprintf(r.errOut, "unknown action: %q (try 'help')\n", name)
			r.printMenu(nil)
			continue
		}

		// Run.
		res, err := act.Run(ctx, r.ctx, args)
		if err != nil {
			fmt.Fprintf(r.errOut, "error: %v\n", err)
			r.printMenu(nil)
			continue
		}

		// Print the result.
		r.printResult(res)

		// Compute suggestions from result tags.
		var lastShape *shape.Shape
		if s, ok := res.Data.(shape.Shape); ok {
			lastShape = &s
		}
		suggestions := Suggest(res.Tags, lastShape)
		r.printMenu(suggestions)
	}
}

// printMenu writes the current menu + suggestions to stdout.
func (r *REPL) printMenu(suggestions []Suggestion) {
	menu := BuildMenu(r.actions, suggestions, recipeDomain(r.ctx))
	menu.Render(r.out, recipeDomain(r.ctx))
}

// printResult writes the result's summary and hints.
func (r *REPL) printResult(res *action.Result) {
	if res == nil {
		return
	}
	if res.Summary != "" {
		fmt.Fprintf(r.out, "\n  %s\n", res.Summary)
	}
	for _, hint := range res.Hints {
		fmt.Fprintf(r.out, "  hint: %s\n", hint)
	}
}

// lookupAction resolves a user input to an action.
func (r *REPL) lookupAction(input string) (action.Action, bool) {
	menu := BuildMenu(r.actions, nil, recipeDomain(r.ctx))
	return menu.Lookup(input)
}

// recipeDomain returns the current recipe's domain, or "" if none.
func recipeDomain(c *action.Context) string {
	if c == nil || c.Recipe == nil {
		return ""
	}
	return c.Recipe.Domain
}

// isQuit returns true if line is one of the recognized quit inputs.
func isQuit(line string) bool {
	switch strings.ToLower(line) {
	case "q", "quit", "exit", ":q":
		return true
	}
	return false
}

// splitActionLine splits a REPL input into an action name and the
// rest as args. Whitespace separates; quoting is not supported in v1
// (URLs don't usually need it). The name is lowercased to match
// menu.Lookup's case-insensitive name match.
func splitActionLine(line string) (name string, args []string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", nil
	}
	fields := strings.Fields(line)
	return fields[0], fields[1:]
}

// allActions returns the static list of meta actions registered
// with the REPL. (Action-specific ones are passed in via New.)
func allActions() []action.Action {
	return []action.Action{
		HelpAction(),
	}
}
