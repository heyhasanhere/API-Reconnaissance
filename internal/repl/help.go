package repl

import (
	"context"
	"fmt"
	"io"

	"github.com/falcon/api-recon/pkg/action"
)

// HelpAction returns an Action that prints the help text for a
// given action (or all actions if no name is given).
func HelpAction() action.Action {
	return action.Action{
		Name:        "help",
		Aliases:     []string{"?"},
		Summary:     "Show help for an action",
		Description: "help [action] — show detailed help for the named action, or list all actions if none is given.",
		Examples: []string{
			"help",
			"help probe",
		},
		Category: "meta",
		Run: func(ctx context.Context, c *action.Context, args []string) (*action.Result, error) {
			if c == nil || c.Stdout == nil {
				return nil, fmt.Errorf("help: nil context or stdout")
			}
			if len(args) == 0 {
				fmt.Fprintln(c.Stdout, "Available actions:")
				for _, a := range allActions() {
					fmt.Fprintf(c.Stdout, "  %s\n", action.OneLineHelp(a))
				}
				fmt.Fprintln(c.Stdout, "\nType 'help <name>' for details on a specific action.")
				return &action.Result{Summary: "listed actions"}, nil
			}
			name := args[0]
			for _, a := range allActions() {
				if a.Name == name {
					fmt.Fprint(c.Stdout, action.Help(a))
					return &action.Result{Summary: "showed help for " + name}, nil
				}
			}
			fmt.Fprintf(c.Stdout, "no action named %q (try 'help' for the list)\n", name)
			return &action.Result{Summary: "unknown action: " + name, Tags: []string{"repl:error"}}, nil
		},
	}
}

// HelpText returns the auto-generated help string for the REPL.
func HelpText(io.Writer) string {
	return "api-recon [domain] > interactive prompt. Type a number, action name, 'help', or 'q'."
}
