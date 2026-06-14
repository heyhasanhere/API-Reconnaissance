// api-recon — stateful CLI for discovering and replaying APIs.
//
// Default to REPL when no subcommand given; subcommand dispatch as
// escape hatch. See PLAN.md for the full design.
//
// Subcommands in v1:
//   harvest <url>  — guided discovery (REPL)
//   run <domain>   — replay a saved recipe
//   ls             — list known domains
//   show <domain>  — print a recipe
//   verify <domain>— re-probe endpoints, report drift
//   recipe edit <domain> — open in $EDITOR (or vi)
//
// Plus implicit REPL when no subcommand is given, or when the
// first arg looks like a URL.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/falcon/api-recon/internal/harvest"
	"github.com/falcon/api-recon/internal/repl"
	"github.com/falcon/api-recon/internal/store"
	"github.com/falcon/api-recon/pkg/action"
	"github.com/falcon/api-recon/pkg/creds"
	"github.com/falcon/api-recon/pkg/graph"
	"github.com/falcon/api-recon/pkg/recipe"
	"github.com/falcon/api-recon/pkg/shape"
)

const usage = `api-recon — stateful knowledge-mining CLI

Usage:
  api-recon [url]                        enter REPL (auto-probe if url given)
  api-recon harvest <url>                guided discovery for a domain
  api-recon run <domain> [args...]       replay a saved recipe
  api-recon ls                           list known domains
  api-recon show <domain>                print a recipe
  api-recon verify <domain>              re-probe endpoints, report drift
  api-recon recipe edit <domain>         open in $EDITOR (or vi)

Global flags:
  --json          scriptable output (subcommands only)
  --no-repl       force subcommand mode even when a URL is given
  --store <dir>   override the recipe store location
  --help, -h      show this message
  --version       print version

Run 'api-recon help' inside the REPL for action-level help.
`

// version is overridden at build time via -ldflags.
var version = "0.1.0"

func main() {
	if err := runMain(); err != nil {
		fmt.Fprintf(os.Stderr, "api-recon: %v\n", err)
		os.Exit(1)
	}
}

func runMain() error {
	args := os.Args[1:]

	// Parse global flags first.
	fs := flag.NewFlagSet("api-recon", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "scriptable output (subcommands only)")
	noRepl := fs.Bool("no-repl", false, "force subcommand mode even when a URL is given")
	storeDir := fs.String("store", "", "override the recipe store location")
	showHelp := fs.Bool("help", false, "show help")
	showVer := fs.Bool("version", false, "print version")
	fs.BoolVar(showHelp, "h", false, "show help (shorthand)")
	// Silence flag's default error output; we handle errors.
	fs.SetOutput(os.Stderr)

	// Allow flags anywhere.
	flagArgs, pos := splitArgs(args, fs)
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	args = pos

	if *showHelp {
		fmt.Print(usage)
		return nil
	}
	if *showVer {
		fmt.Println(version)
		return nil
	}

	// Determine the subcommand. The first arg is the subcommand OR
	// a URL OR empty (REPL).
	cmd, cmdArgs := firstCommand(args)

	// Decide between REPL and subcommand mode.
	if cmd == "" || isURL(cmd) {
		if *noRepl && cmd != "" {
			// User gave a URL but --no-repl; treat as harvest.
			cmd = "harvest"
		} else {
			return runREPL(context.Background(), cmd, *storeDir)
		}
	}

	return runSubcommand(context.Background(), cmd, cmdArgs, *jsonOut, *storeDir)
}

// splitArgs uses cli.Split to reorder argv.
func splitArgs(args []string, fs *flag.FlagSet) (flagArgs, pos []string) {
	// We can't import internal/cli from main because main is in the
	// root package; the cli package would have to be public. To
	// avoid that complication, we re-implement a minimal version
	// here. (The internal/cli package is for the harvest driver.)
	return localSplit(args, fs)
}

// localSplit is a minimal copy of cli.Split that handles the flags
// we care about at the top level. The harvest driver uses the
// internal/cli version; main has its own because internal/ can't
// be imported from root.
func localSplit(args []string, fs *flag.FlagSet) (flagArgs, pos []string) {
	known := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		known[f.Name] = true
	})
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" || a == "--" {
			pos = append(pos, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		value := ""
		hasValue := false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			value = name[eq+1:]
			name = name[:eq]
			hasValue = true
		}
		if !known[name] {
			pos = append(pos, a)
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			pos = append(pos, a)
			continue
		}
		if hasValue {
			flagArgs = append(flagArgs, "-"+name+"="+value)
			continue
		}
		if isBoolFlag(f) {
			flagArgs = append(flagArgs, "-"+name)
			continue
		}
		if i+1 < len(args) {
			flagArgs = append(flagArgs, "-"+name+"="+args[i+1])
			i++
			continue
		}
		flagArgs = append(flagArgs, "-"+name)
	}
	return flagArgs, pos
}

// isBoolFlag returns true if the flag was registered via BoolVar /
// Bool. We use the value's String() form: bools render as "true" or
// "false", strings render as the actual value or "" for unset. A
// non-bool DefValue of "" (the Go default for strings) is *not* a
// bool — that's how we avoid the common bug of `--store /tmp/foo`
// being misread as a bool flag.
func isBoolFlag(f *flag.Flag) bool {
	if bv, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
		return bv.IsBoolFlag()
	}
	// Type-assert the underlying value. flag.Value's dynamic type
	// is one of *boolValue, *stringValue, *intValue, etc. — names
	// not exported, so we use a structural check: bools print as
	// exactly "true" or "false".
	if f.Value != nil {
		s := f.Value.String()
		if s == "true" || s == "false" {
			return true
		}
	}
	return false
}

// firstCommand returns the first arg and the rest. If args is
// empty, returns ("", nil).
func firstCommand(args []string) (string, []string) {
	if len(args) == 0 {
		return "", nil
	}
	return args[0], args[1:]
}

// isURL returns true if s looks like an HTTP(S) URL.
func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// runREPL launches the interactive REPL. If entryURL is non-empty,
// the harvest action is registered and the user can use it from
// the menu.
func runREPL(ctx context.Context, entryURL, storeDirOverride string) error {
	cwd, _ := os.Getwd()
	st, err := newStore(storeDirOverride, cwd)
	if err != nil {
		return err
	}

	c := &action.Context{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Stdin:  os.Stdin,
		Creds:  creds.New(),
		Graph:  graph.New(),
		Shape:  shape.New(),
	}
	// If a URL was given, pre-populate the recipe's domain from
	// it so the prompt shows something useful. The harvest action
	// will overwrite this on first probe.
	if entryURL != "" {
		host := hostOf(entryURL)
		if host != "" {
			c.Recipe = recipe.New(host)
		}
	}

	actions := []action.Action{
		harvest.Action(),
	}
	// Add the run/show subcommands as actions too, so they're
	// reachable from the REPL.
	actions = append(actions, runAction(st), showAction(st), listAction(st), verifyAction(st), editAction(st), saveAction(st))

	r := repl.New(c, actions)
	if entryURL != "" {
		fmt.Fprintf(c.Stdout, "Entering REPL. Auto-probing %s ...\n", entryURL)
		// Run the harvest action once so the user lands in a
		// populated context (graph has the entry endpoint, creds
		// has any captured headers, last response is set).
		harvestAct := harvest.Action()
		if res, err := harvestAct.Run(ctx, c, []string{entryURL}); err != nil {
			fmt.Fprintf(c.Stderr, "auto-probe failed: %v\n", err)
		} else if res != nil {
			fmt.Fprintln(c.Stdout, res.Summary)
			for _, h := range res.Hints {
				fmt.Fprintf(c.Stdout, "  hint: %s\n", h)
			}
		}
	} else {
		fmt.Fprintln(c.Stdout, "Entering REPL. Type 'help' to list actions, 'q' to quit.")
	}
	return r.Run(ctx)
}

// runSubcommand dispatches to one of the subcommand handlers.
func runSubcommand(ctx context.Context, cmd string, args []string, jsonOut bool, storeDirOverride string) error {
	cwd, _ := os.Getwd()
	st, err := newStore(storeDirOverride, cwd)
	if err != nil {
		return err
	}
	switch cmd {
	case "harvest":
		if len(args) == 0 {
			return fmt.Errorf("harvest: URL is required")
		}
		return runREPL(ctx, args[0], storeDirOverride)
	case "run":
		return runReplay(ctx, st, args, jsonOut)
	case "ls":
		return listDomains(st, jsonOut)
	case "show":
		if len(args) == 0 {
			return fmt.Errorf("show: domain is required")
		}
		return showRecipe(st, args[0], jsonOut)
	case "verify":
		if len(args) == 0 {
			return fmt.Errorf("verify: domain is required")
		}
		return verifyRecipe(ctx, st, args[0])
	case "recipe":
		if len(args) < 2 || args[0] != "edit" {
			return fmt.Errorf("recipe: 'recipe edit <domain>' is the only subcommand")
		}
		return editRecipe(st, args[1])
	case "help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %q\n\n", cmd)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
		return nil
	}
}

func newStore(override, projectDir string) (*store.Store, error) {
	if override != "" {
		return store.NewWithRoot(override, projectDir)
	}
	return store.New(projectDir)
}

// hostOf extracts the host from a URL string. Returns "" for empty
// or unparseable input.
func hostOf(rawURL string) string {
	i := strings.Index(rawURL, "://")
	if i < 0 {
		return ""
	}
	rest := rawURL[i+3:]
	if j := strings.Index(rest, "/"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// ---- subcommand handlers ----

func listDomains(st *store.Store, jsonOut bool) error {
	domains, err := st.List()
	if err != nil {
		return err
	}
	if jsonOut {
		for _, d := range domains {
			fmt.Printf("%s\n", d)
		}
		return nil
	}
	if len(domains) == 0 {
		fmt.Println("(no recipes yet)")
		return nil
	}
	for _, d := range domains {
		fmt.Println(d)
	}
	return nil
}

func showRecipe(st *store.Store, domain string, jsonOut bool) error {
	r, err := st.Lookup(domain)
	if err != nil {
		return err
	}
	if jsonOut {
		// Raw JSON for scripting.
		data, _ := r.Marshal()
		fmt.Print(string(data))
		return nil
	}
	fmt.Printf("# %s\n", r.Domain)
	fmt.Printf("discovered: %s\n", r.Discovered.Format("2006-01-02"))
	fmt.Printf("endpoints: %d\n", len(r.Endpoints))
	for name, ep := range r.Endpoints {
		fmt.Printf("  %s: %s %s (%s)\n", name, ep.Method, ep.URL, ep.Shape)
	}
	return nil
}

func editRecipe(st *store.Store, domain string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	path := st.Path(domain)
	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func verifyRecipe(ctx context.Context, st *store.Store, domain string) error {
	r, err := st.Lookup(domain)
	if err != nil {
		return err
	}
	cs := creds.LoadFromRecipe(r.Auth)
	cls := shape.New()
	fmt.Printf("verifying %s (%d endpoints)\n", domain, len(r.Endpoints))
	ok, drift := 0, 0
	for name, ep := range r.Endpoints {
		filled, err := ep.Fill(partsFromRecipe(r))
		if err != nil {
			fmt.Printf("  %s: FILL ERROR %v\n", name, err)
			drift++
			continue
		}
		status, shapeKind, err := doProbe(ctx, ep.Method, filled, cs, cls)
		if err != nil {
			fmt.Printf("  %s: %v\n", name, err)
			drift++
			continue
		}
		// For non-error endpoints we expect 2xx/3xx; for
		// error-shape endpoints we expect 4xx/5xx. The shape
		// itself must match in either case.
		var statusOK bool
		if ep.Shape == "error" {
			statusOK = status >= 400
		} else {
			statusOK = status >= 200 && status < 400
		}
		shapeOK := shapeKind == ep.Shape
		sym := "✓"
		if !statusOK || !shapeOK {
			sym = "✗"
			drift++
		} else {
			ok++
		}
		fmt.Printf("  %s %s: %d %s (expected %s)\n", sym, name, status, shapeKind, ep.Shape)
	}
	fmt.Printf("\n%d/%d match recipe", ok, ok+drift)
	if drift == 0 {
		fmt.Println(" — no drift detected.")
	} else {
		fmt.Printf(" — %d endpoint(s) drifted.\n", drift)
	}
	return nil
}

func runReplay(ctx context.Context, st *store.Store, args []string, _ bool) error {
	if len(args) == 0 {
		return fmt.Errorf("run: domain is required")
	}
	domain := args[0]
	r, err := st.Lookup(domain)
	if err != nil {
		return err
	}
	// Find the "main" endpoint to drive parts from. Prefer "sources"
	// (anikage convention), else the first endpoint with placeholders,
	// else the first endpoint overall.
	var mainEP recipe.Endpoint
	if e, ok := r.Endpoints["sources"]; ok {
		mainEP = e
	} else {
		for _, e := range r.Endpoints {
			if len(e.Placeholders()) > 0 {
				mainEP = e
				break
			}
		}
		if mainEP.URL == "" {
			for _, e := range r.Endpoints {
				mainEP = e
				break
			}
		}
	}
	parts := partsFromArgs(args[1:], mainEP)
	cs := creds.LoadFromRecipe(r.Auth)
	cls := shape.New()
	g := graph.New()

	// Loop through endpoints, exercising each.
	for name, e := range r.Endpoints {
		filled, err := e.Fill(parts)
		if err != nil {
			fmt.Printf("  %s: skip (%v)\n", name, err)
			continue
		}
		fmt.Printf("  %s: %s %s\n", name, e.Method, filled)
		status, shapeKind, body, err := doProbeFull(ctx, e.Method, filled, cs, cls)
		if err != nil {
			fmt.Printf("    -> error: %v\n", err)
			continue
		}
		fmt.Printf("    -> %d %s\n", status, shapeKind)
		// Feed into graph so the REPL can see what we hit.
		g.Observe(graph.Observation{
			URL:      filled,
			Method:   e.Method,
			Status:   status,
			RespBody: body,
		})
	}
	return nil
}

func partsFromArgs(args []string, ep recipe.Endpoint) map[string]string {
	placeholders := ep.Placeholders()
	parts := map[string]string{}
	for i, p := range placeholders {
		if i+1 <= len(args) {
			parts[p] = args[i]
		}
	}
	return parts
}

func partsFromRecipe(r *recipe.Recipe) map[string]string {
	// For verify, we use empty parts — Fill will fail for endpoints
	// with placeholders, which is the correct behavior.
	return map[string]string{}
}

// listAction, runAction, etc. are REPL-accessible wrappers.
func listAction(st *store.Store) action.Action {
	return action.Action{
		Name:     "ls",
		Summary:  "List known domains",
		Category: "meta",
		Run: func(ctx context.Context, c *action.Context, _ []string) (*action.Result, error) {
			domains, err := st.List()
			if err != nil {
				return nil, err
			}
			for _, d := range domains {
				fmt.Fprintln(c.Stdout, d)
			}
			return &action.Result{Summary: fmt.Sprintf("%d domains", len(domains))}, nil
		},
	}
}

func showAction(st *store.Store) action.Action {
	return action.Action{
		Name:     "show",
		Summary:  "Show a recipe (in-memory if not yet saved)",
		Category: "meta",
		Run: func(ctx context.Context, c *action.Context, args []string) (*action.Result, error) {
			if len(args) == 0 {
				// No arg: show the in-memory recipe if we have one.
				if c.Recipe != nil && c.Recipe.Domain != "" {
					printRecipe(c.Stdout, c.Recipe, "(in-memory, not yet saved)")
					return &action.Result{Summary: "showed " + c.Recipe.Domain + " (in-memory)"}, nil
				}
				return nil, fmt.Errorf("show: domain is required (or no in-memory recipe yet)")
			}
			r, err := st.Lookup(args[0])
			if err != nil {
				// Fall back to the in-memory recipe if the domain matches.
				if c.Recipe != nil && c.Recipe.Domain == args[0] {
					printRecipe(c.Stdout, c.Recipe, "(in-memory, not yet saved)")
					return &action.Result{Summary: "showed " + args[0] + " (in-memory)"}, nil
				}
				return nil, err
			}
			printRecipe(c.Stdout, r, "(on disk)")
			return &action.Result{Summary: "showed " + args[0]}, nil
		},
	}
}

// printRecipe writes a human-readable recipe to w.
func printRecipe(w io.Writer, r *recipe.Recipe, note string) {
	fmt.Fprintf(w, "# %s %s\n", r.Domain, note)
	if !r.Discovered.IsZero() {
		fmt.Fprintf(w, "  discovered: %s\n", r.Discovered.Format("2006-01-02"))
	}
	fmt.Fprintf(w, "  endpoints: %d\n", len(r.Endpoints))
	for name, ep := range r.Endpoints {
		fmt.Fprintf(w, "  %s: %s %s (%s)\n", name, ep.Method, ep.URL, ep.Shape)
	}
}

// saveAction persists the in-memory recipe to the store. Used at
// the end of a discovery session to commit what was learned.
func saveAction(st *store.Store) action.Action {
	return action.Action{
		Name:     "save",
		Summary:  "Save the in-memory recipe to disk",
		Category: "meta",
		Run: func(ctx context.Context, c *action.Context, args []string) (*action.Result, error) {
			if c.Recipe == nil || c.Recipe.Domain == "" {
				return nil, fmt.Errorf("save: no recipe in memory (run harvest first)")
			}
			if c.Recipe.Discovered.IsZero() {
				c.Recipe.Discovered = time.Now()
			}
			if err := st.Save(c.Recipe); err != nil {
				return nil, fmt.Errorf("save: %w", err)
			}
			path := st.Path(c.Recipe.Domain)
			return &action.Result{
				Summary: fmt.Sprintf("saved recipe for %s to %s", c.Recipe.Domain, path),
				Tags:    []string{"recipe:saved"},
			}, nil
		},
	}
}

func runAction(st *store.Store) action.Action {
	return action.Action{
		Name:     "run",
		Summary:  "Replay a saved recipe",
		Category: "replay",
		Run: func(ctx context.Context, c *action.Context, args []string) (*action.Result, error) {
			if len(args) == 0 {
				return nil, fmt.Errorf("run: domain is required")
			}
			err := runReplay(ctx, st, args, false)
			return &action.Result{Summary: "ran " + args[0]}, err
		},
	}
}

func verifyAction(st *store.Store) action.Action {
	return action.Action{
		Name:     "verify",
		Summary:  "Re-probe a recipe's endpoints, report drift",
		Category: "replay",
		Run: func(ctx context.Context, c *action.Context, args []string) (*action.Result, error) {
			if len(args) == 0 {
				return nil, fmt.Errorf("verify: domain is required")
			}
			err := verifyRecipe(ctx, st, args[0])
			return &action.Result{Summary: "verified " + args[0]}, err
		},
	}
}

func editAction(st *store.Store) action.Action {
	return action.Action{
		Name:     "recipe edit",
		Summary:  "Open a recipe in $EDITOR (or vi)",
		Category: "meta",
		Run: func(ctx context.Context, c *action.Context, args []string) (*action.Result, error) {
			if len(args) == 0 {
				return nil, fmt.Errorf("recipe edit: domain is required")
			}
			err := editRecipe(st, args[0])
			return &action.Result{Summary: "edited " + args[0]}, err
		},
	}
}

// helpers for verify/run
//
// doProbe issues a single HTTP request and returns the status code
// and shape classification. Used by `verify` and `run`.
func doProbe(ctx context.Context, method, rawURL string, cs *creds.Store, cls *shape.Classifier) (int, string, error) {
	status, kind, _, err := doProbeFull(ctx, method, rawURL, cs, cls)
	return status, kind, err
}

// doProbeFull is the same but also returns the body. We return the
// body because the run path feeds it into graph.Observation.
func doProbeFull(ctx context.Context, method, rawURL string, cs *creds.Store, cls *shape.Classifier) (int, string, []byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, "", nil, fmt.Errorf("parse url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return 0, "", nil, fmt.Errorf("new request: %w", err)
	}
	// Default headers; creds.Inject preserves caller-set ones.
	// We must set these explicitly — Cloudflare and other CDNs serve
	// bot-detection HTML pages when User-Agent is empty.
	req.Header.Set("User-Agent", "api-recon/0.1.0")
	req.Header.Set("Accept", "application/json, */*;q=0.5")
	if cs != nil {
		cs.Inject(req.Header, u)
	}

	cli := &http.Client{Timeout: 30 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return 0, "", nil, fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return resp.StatusCode, "", nil, fmt.Errorf("read body: %w", err)
	}

	sh := cls.Classify(resp, body, rawURL)
	return resp.StatusCode, sh.Kind.String(), body, nil
}
