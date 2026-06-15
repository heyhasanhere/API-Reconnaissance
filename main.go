// api-recon — a stateful knowledge-mining CLI for discovering
// and replaying streaming APIs.
//
// Usage:
//
//	api-recon <url>                  # discover the chain, prompt for picks, download
//	api-recon run <domain> [args]    # replay a saved recipe
//	api-recon ls                     # list saved recipes
//	api-recon show <domain>          # print a saved recipe
//	api-recon --help
//	api-recon --version
//
// The default command is fully automatic: it runs the chain
// engine against <url>, prints what it found (episodes,
// providers, headers, resolved stream URL), prompts the user
// for episode and provider selection, then downloads.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/heyhasanhere/API-Reconnaissance/pkg/chain"
	"github.com/heyhasanhere/API-Reconnaissance/pkg/classify"
	"github.com/heyhasanhere/API-Reconnaissance/pkg/download"
	"github.com/heyhasanhere/API-Reconnaissance/pkg/pick"
	"github.com/heyhasanhere/API-Reconnaissance/pkg/pickjson"
	probetype "github.com/heyhasanhere/API-Reconnaissance/pkg/probe"
	"github.com/heyhasanhere/API-Reconnaissance/pkg/recipe"
)

// version is set at build time via -ldflags. Default for `go run`.
const version = "0.2.0-dev"

type globalFlags struct {
	json     bool
	noREPL   bool
	storeDir string
	help     bool
	ver      bool
	noColor  bool
	dryRun   bool
	episodes string
	provider string
	output   string
	parallel int
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Stdin))
}

func run(args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	// Top-level flags. We use a manual parse so subcommand-style
	// args don't trip on unknown flags before we know which
	// subcommand is in play.
	fs := flag.NewFlagSet("api-recon", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var gf globalFlags
	fs.BoolVar(&gf.json, "json", false, "machine-readable output")
	fs.BoolVar(&gf.noREPL, "no-repl", false, "force subcommand mode even if a URL is given")
	fs.StringVar(&gf.storeDir, "store", "", "override recipe store directory")
	fs.BoolVar(&gf.help, "help", false, "show help")
	fs.BoolVar(&gf.help, "h", false, "show help")
	fs.BoolVar(&gf.ver, "version", false, "print version")
	fs.BoolVar(&gf.ver, "v", false, "print version")
	fs.BoolVar(&gf.noColor, "no-color", false, "disable color output")
	fs.BoolVar(&gf.dryRun, "dry-run", false, "discover and resolve the stream, but don't download")
	fs.StringVar(&gf.episodes, "episodes", "", "episode range (e.g. '1,3-5'); skips the prompt")
	fs.StringVar(&gf.provider, "provider", "", "provider name; skips the prompt")
	fs.StringVar(&gf.output, "output", "", "output directory for downloaded files")
	fs.IntVar(&gf.parallel, "concurrency", 0, "concurrent fragments for HLS/DASH (default 16)")

	// Split flags from positionals using the flag.FlagSet (this
	// is the same idea as internal/cli.Split but folded into
	// main because main can't import internal/).
	flagArgs, pos := splitArgsLocal(args, fs)
	if err := fs.Parse(flagArgs); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if gf.ver {
		fmt.Fprintf(stdout, "api-recon %s\n", version)
		return 0
	}
	if gf.help || (len(pos) == 0 && !gf.json) {
		printUsage(stdout, fs)
		return 0
	}

	// Subcommand dispatch.
	if len(pos) > 0 {
		switch pos[0] {
		case "run":
			return runReplay(ctx(), pos[1:], gf, stdout, stderr, stdin)
		case "ls":
			return listRecipes(gf, stdout, stderr)
		case "show":
			return showRecipe(pos[1:], gf, stdout, stderr)
		case "help":
			printUsage(stdout, fs)
			return 0
		}
	}

	// Default: treat first positional as a URL.
	if looksLikeURL(pos[0]) {
		return runDiscover(ctx(), pos[0], gf, stdout, stderr, stdin)
	}

	// Unknown.
	fmt.Fprintf(stderr, "api-recon: unknown command %q (try `api-recon --help`)\n", pos[0])
	return 2
}

// --- subcommands ---

func runDiscover(ctx context.Context, entryURL string, gf globalFlags, stdout, stderr io.Writer, stdin io.Reader) int {
	fmt.Fprintf(stdout, "Resolving %s\n", entryURL)

	// Phase 1: chain.
	d, err := chain.Run(ctx, entryURL)
	if err != nil {
		fmt.Fprintf(stderr, "discovery failed: %v\n", err)
		fmt.Fprintln(stderr, "last steps:")
		for _, s := range d.Steps {
			fmt.Fprintf(stderr, "  %s %s -> %s (%s)\n", s.Method, s.URL, s.Shape.Kind, s.Note)
		}
		return 1
	}

	// Phase 2: print summary.
	if gf.json {
		printJSONSummary(stdout, d)
	} else {
		printHumanSummary(stdout, d)
	}

	if gf.dryRun {
		fmt.Fprintln(stdout, "(dry-run: skipping download)")
		return 0
	}

	// Phase 3: pick.
	episodeItems := buildEpisodeItems(d.Episodes)
	selectedEp, err := pickEpisodes(ctx, episodeItems, gf, stdin, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "episode selection: %v\n", err)
		return 1
	}
	if len(selectedEp) == 0 {
		fmt.Fprintln(stdout, "no episodes selected, exiting")
		return 0
	}

	providerItems := buildProviderItems(d.Providers)
	selectedProv, err := pickProvider(ctx, providerItems, d.Providers, gf, stdin, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "provider selection: %v\n", err)
		return 1
	}
	if selectedProv < 0 {
		fmt.Fprintln(stdout, "no provider selected, exiting")
		return 0
	}

	// Phase 4: download.
	outputDir := gf.output
	if outputDir == "" {
		outputDir, _ = os.Getwd()
	}
	prov := d.Providers[selectedProv]

	for _, epIdx := range selectedEp {
		ep := d.Episodes[epIdx]
		title := strings.TrimSpace(ep.Title)
		if title == "" {
			title = fmt.Sprintf("Episode %d", ep.Number)
		}
		safe := sanitizeFilename(title)
		outPath := filepath.Join(outputDir, fmt.Sprintf("%s - %02d.mkv", safe, ep.Number))

		fmt.Fprintf(stdout, "\nDownloading: %s (provider=%s)\n", outPath, prov.Name)
		fmt.Fprintf(stdout, "  stream: %s\n", d.Final.StreamURL)

		headers := d.Final.RequiredHdrs
		if headers == nil {
			headers = map[string]string{}
		}
		req := download.Request{
			StreamURL:  d.Final.StreamURL,
			OutputPath: outPath,
			Headers:    headers,
			Kind:       string(d.Final.StreamKind),
			Concurrent: gf.parallel,
			Stdout:     stdout,
			Stderr:     stderr,
		}
		res, err := download.Run(ctx, req)
		if err != nil {
			fmt.Fprintf(stderr, "download failed: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "  %s in %s (%s)\n",
			humanBytes(res.Bytes), res.Took.Round(time.Second), res.Tool)
	}

	// Phase 5: save recipe (in background — synchronous here
	// because the user is about to exit and we want them to see
	// the confirmation).
	if err := saveRecipe(d, gf, stdout); err != nil {
		fmt.Fprintf(stderr, "warning: failed to save recipe: %v\n", err)
	}

	return 0
}

func runReplay(ctx context.Context, args []string, gf globalFlags, stdout, stderr io.Writer, stdin io.Reader) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "api-recon run: domain required")
		return 2
	}
	domain := args[0]
	rest := args[1:]

	store, err := openStore(gf)
	if err != nil {
		fmt.Fprintf(stderr, "store: %v\n", err)
		return 1
	}
	r, err := store.Load(domain)
	if err != nil {
		fmt.Fprintf(stderr, "load recipe: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "loaded recipe for %s (%d steps)\n", domain, len(r.Chain))

	// Replay is a simplified chain: the recipe has the chain
	// already, we just fill placeholders and probe. For v0.2.0,
	// the simplest replay is: assume the recipe is the
	// authoritative source for the chain, and just re-fetch
	// /sources, resolve the stream, download. Discovery code
	// path is not re-run.

	// We need at least the /sources step.
	var sourcesStep *recipe.Step
	for i := range r.Chain {
		if r.Chain[i].Name == "sources" {
			sourcesStep = &r.Chain[i]
			break
		}
	}
	if sourcesStep == nil {
		fmt.Fprintln(stderr, "recipe has no 'sources' step — replay not possible")
		return 1
	}

	// Fill placeholders from positional args.
	values := map[string]string{}
	for i, p := range sourcesStep.Placeholders {
		if i < len(rest) {
			values[p] = rest[i]
		}
	}
	for _, p := range []string{"provider", "lang"} {
		if values[p] == "" {
			values[p] = "miko"
		}
	}
	// Always require n.
	if values["n"] == "" {
		values["n"] = "1"
	}
	filled, err := recipe.FillTemplate(sourcesStep.URLTemplate, values)
	if err != nil {
		fmt.Fprintf(stderr, "fill: %v\n", err)
		return 1
	}
	if !strings.HasPrefix(filled, "http") {
		filled = deriveBase(r, entryURLFromDomain(domain)) + filled
	}

	fmt.Fprintf(stdout, "fetching %s ...\n", filled)
	resp, err := probetype.Do(context.Background(), probetype.Request{
		URL: filled, Headers: r.Headers,
	})
	if err != nil {
		fmt.Fprintf(stderr, "probe: %v\n", err)
		return 1
	}
	shape := classify.Classify(resp, filled)
	if shape.Kind != classify.KindJSON {
		fmt.Fprintf(stderr, "sources endpoint returned %s: %s\n", shape.Kind, shape.Reasoning)
		return 1
	}
	streamKey := shape.StreamKey
	if streamKey == "" {
		fmt.Fprintln(stderr, "no stream key in sources response")
		return 1
	}

	// Resolve the stream URL using the recipe's stream template.
	streamURL := streamKey
	if r.StreamTemplate != "" && !strings.HasPrefix(streamKey, "http") {
		streamURL, err = recipe.FillTemplate(r.StreamTemplate, map[string]string{"key": streamKey})
		if err != nil {
			fmt.Fprintf(stderr, "stream template fill: %v\n", err)
			return 1
		}
	}

	// Apply recorded headers.
	headers := r.Headers
	if headers == nil {
		headers = map[string]string{}
	}

	// Classify the playlist.
	plist, err := probetype.Do(context.Background(), probetype.Request{
		URL: streamURL, Headers: headers,
	})
	if err != nil {
		fmt.Fprintf(stderr, "playlist probe: %v\n", err)
		return 1
	}
	pshape := classify.Classify(plist, streamURL)

	// Download.
	outDir := gf.output
	if outDir == "" {
		outDir, _ = os.Getwd()
	}
	outPath := filepath.Join(outDir, fmt.Sprintf("%s - %s.mkv", domain, values["n"]))
	if gf.dryRun {
		fmt.Fprintf(stdout, "(dry-run) would download %s -> %s\n", streamURL, outPath)
		return 0
	}
	fmt.Fprintf(stdout, "downloading %s -> %s\n", streamURL, outPath)
	res, err := download.Run(ctx, download.Request{
		StreamURL:  streamURL,
		OutputPath: outPath,
		Headers:    headers,
		Kind:       string(pshape.Kind),
		Concurrent: gf.parallel,
		Stdout:     stdout,
		Stderr:     stderr,
	})
	if err != nil {
		fmt.Fprintf(stderr, "download failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "  %s in %s\n", humanBytes(res.Bytes), res.Took.Round(time.Second))
	return 0
}

func listRecipes(gf globalFlags, stdout, stderr io.Writer) int {
	store, err := openStore(gf)
	if err != nil {
		fmt.Fprintf(stderr, "store: %v\n", err)
		return 1
	}
	domains, err := store.List()
	if err != nil {
		fmt.Fprintf(stderr, "list: %v\n", err)
		return 1
	}
	if gf.json {
		out := map[string]any{"domains": domains}
		_ = json.NewEncoder(stdout).Encode(out)
		return 0
	}
	if len(domains) == 0 {
		fmt.Fprintln(stdout, "(no recipes yet)")
		return 0
	}
	for _, d := range domains {
		fmt.Fprintln(stdout, d)
	}
	return 0
}

func showRecipe(args []string, gf globalFlags, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "api-recon show: domain required")
		return 2
	}
	store, err := openStore(gf)
	if err != nil {
		fmt.Fprintf(stderr, "store: %v\n", err)
		return 1
	}
	r, err := store.Load(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "load: %v\n", err)
		return 1
	}
	if gf.json {
		_ = json.NewEncoder(stdout).Encode(r)
		return 0
	}
	fmt.Fprintf(stdout, "# %s\n", r.Domain)
	fmt.Fprintf(stdout, "discovered: %s\n", r.Discovered.Format("2006-01-02"))
	fmt.Fprintf(stdout, "chain: %d steps\n", len(r.Chain))
	for _, s := range r.Chain {
		fmt.Fprintf(stdout, "  %s: %s %s\n", s.Name, s.Method, s.URLTemplate)
	}
	if len(r.Providers) > 0 {
		fmt.Fprintf(stdout, "providers: %s\n", joinStrings(r.Providers))
	}
	if r.StreamTemplate != "" {
		fmt.Fprintf(stdout, "stream: %s\n", r.StreamTemplate)
	}
	return 0
}

// --- helpers ---

func ctx() context.Context {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	_ = stop
	return ctx
}

func openStore(gf globalFlags) (*recipe.Store, error) {
	if gf.storeDir != "" {
		return recipe.NewStoreAt(gf.storeDir)
	}
	return recipe.NewStore()
}

func printUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintln(w, "api-recon — discover and replay streaming APIs")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  api-recon <url>                  discover, prompt, download")
	fmt.Fprintln(w, "  api-recon run <domain> [args]    replay a saved recipe")
	fmt.Fprintln(w, "  api-recon ls                     list saved recipes")
	fmt.Fprintln(w, "  api-recon show <domain>          print a recipe")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	fs.VisitAll(func(f *flag.Flag) {
		def := ""
		if f.DefValue != "" && f.DefValue != "false" {
			def = fmt.Sprintf(" (default %s)", f.DefValue)
		}
		fmt.Fprintf(w, "  -%s\n    %s%s\n", f.Name, f.Usage, def)
	})
}

func printHumanSummary(w io.Writer, d *chain.Discovery) {
	fmt.Fprintf(w, "\nAPI base: %s\n", d.APIBase)
	if len(d.Episodes) > 0 {
		fmt.Fprintf(w, "\nFound %d episodes:\n", len(d.Episodes))
		// Show first 5 + last 1 for long lists.
		showN := d.Episodes
		if len(showN) > 6 {
			showN = append(showN[:5], d.Episodes[len(d.Episodes)-1])
			fmt.Fprintln(w, "  (showing first 5 and last)")
		}
		for i, e := range showN {
			title := e.Title
			if title == "" {
				title = "(no title)"
			}
			if i == 5 && len(d.Episodes) > 6 {
				fmt.Fprintf(w, "  ...\n")
			}
			fmt.Fprintf(w, "  %3d. %s\n", e.Number, title)
		}
	}
	if len(d.Providers) > 0 {
		fmt.Fprintf(w, "\nFound %d providers:\n", len(d.Providers))
		for _, p := range d.Providers {
			note := ""
			if p.IsHLS {
				note = " (HLS)"
			} else if p.IsEmbed {
				note = " (embeds)"
			}
			fmt.Fprintf(w, "  %s%s\n", p.Name, note)
		}
	}
	if d.Final != nil {
		fmt.Fprintf(w, "\nStream resolved:\n")
		fmt.Fprintf(w, "  URL:  %s\n", d.Final.StreamURL)
		fmt.Fprintf(w, "  Kind: %s\n", d.Final.StreamKind)
		if d.Final.Segments > 0 {
			fmt.Fprintf(w, "  Estimated: %d segments, %s\n", d.Final.Segments, humanBytes(d.Final.BytesEst))
		}
		if len(d.Final.RequiredHdrs) > 0 {
			fmt.Fprintf(w, "  Required headers:\n")
			keys := make([]string, 0, len(d.Final.RequiredHdrs))
			for k := range d.Final.RequiredHdrs {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if k == "User-Agent" || k == "Accept" {
					continue
				}
				fmt.Fprintf(w, "    %s: %s\n", k, d.Final.RequiredHdrs[k])
			}
		}
	}
	fmt.Fprintln(w)
	for _, log := range d.Logs {
		fmt.Fprintf(w, "  ↳ %s\n", log)
	}
}

func printJSONSummary(w io.Writer, d *chain.Discovery) {
	out := map[string]any{
		"entry_url": d.EntryURL,
		"api_base":  d.APIBase,
		"episodes":  d.Episodes,
		"providers": d.Providers,
		"final":     d.Final,
		"steps":     d.Steps,
		"logs":      d.Logs,
	}
	_ = json.NewEncoder(w).Encode(out)
}

func buildEpisodeItems(eps []chain.Episode) []pick.Item {
	out := make([]pick.Item, len(eps))
	for i, e := range eps {
		title := e.Title
		if title == "" {
			title = fmt.Sprintf("Episode %d", e.Number)
		}
		out[i] = pick.Item{
			Label:       fmt.Sprintf("%d. %s", e.Number, title),
			Description: "",
		}
	}
	return out
}

func buildProviderItems(ps []chain.Provider) []pick.Item {
	out := make([]pick.Item, len(ps))
	for i, p := range ps {
		desc := p.Note
		if p.IsHLS {
			desc = "HLS — recommended"
		} else if p.IsEmbed {
			desc = "third-party embeds"
		}
		// Default to HLS / first working.
		out[i] = pick.Item{
			Label:       p.Name,
			Description: desc,
			Default:     p.IsHLS,
		}
	}
	// If none marked default, mark the first.
	if len(out) > 0 && !anyDefault(out) {
		out[0].Default = true
	}
	return out
}

func anyDefault(items []pick.Item) bool {
	for _, it := range items {
		if it.Default {
			return true
		}
	}
	return false
}

func pickEpisodes(ctx context.Context, items []pick.Item, gf globalFlags, stdin io.Reader, stdout io.Writer) ([]int, error) {
	if gf.episodes != "" {
		return parseRangeLocal(gf.episodes, len(items))
	}
	return pick.MultiSelect(ctx, stdin, stdout, "Select episode(s) to download:", items)
}

func pickProvider(ctx context.Context, items []pick.Item, providers []chain.Provider, gf globalFlags, stdin io.Reader, stdout io.Writer) (int, error) {
	if gf.provider != "" {
		for i, p := range providers {
			if p.Name == gf.provider {
				return i, nil
			}
		}
		return -1, fmt.Errorf("provider %q not found in discovered providers", gf.provider)
	}
	return pick.SingleSelect(ctx, stdin, stdout, "Select provider:", items)
}

func saveRecipe(d *chain.Discovery, gf globalFlags, stdout io.Writer) error {
	store, err := openStore(gf)
	if err != nil {
		return err
	}
	domain, err := domainFromURL(d.EntryURL)
	if err != nil {
		return err
	}

	chain := buildRecipeChain(d)
	headers := map[string]string{}
	if d.Final != nil {
		for k, v := range d.Final.RequiredHdrs {
			if k == "User-Agent" || k == "Accept" {
				continue
			}
			headers[k] = v
		}
	}
	streamTpl := ""
	if d.Final != nil {
		streamTpl = streamTemplateFor(d.Final.StreamURL)
	}
	provs := make([]recipe.Provider, len(d.Providers))
	for i, p := range d.Providers {
		provs[i] = recipe.Provider{Name: p.Name, IsHLS: p.IsHLS, IsEmbed: p.IsEmbed, Note: p.Note}
	}

	r := &recipe.Recipe{
		Domain:         domain,
		Chain:          chain,
		Providers:      provs,
		Headers:        headers,
		StreamTemplate: streamTpl,
	}
	if err := store.Save(r); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "\nRecipe saved to %s\n", store.Path(domain))
	return nil
}

// buildRecipeChain turns the Discovery's steps into a recipe
// chain. The recorded URLs become templates with {placeholder}
// syntax; replay fills them from positional args.
func buildRecipeChain(d *chain.Discovery) []recipe.Step {
	// For v0.2.0, the recipe is intentionally simple: just the
	// /sources step with its placeholders, and any other
	// steps whose URL was probed successfully. The chain engine
	// on replay uses the recorded steps to know which URL to
	// hit.
	var out []recipe.Step
	seen := map[string]bool{}
	for _, s := range d.Steps {
		if s.Outcome != "ok" {
			continue
		}
		u, err := url.Parse(s.URL)
		if err != nil {
			continue
		}
		name := chainNameFor(u.Path)
		if name == "" {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true

		tpl, ph := templateFromURL(s.URL)
		out = append(out, recipe.Step{
			Name:         name,
			URLTemplate:  tpl,
			Method:       s.Method,
			Placeholders: ph,
		})
	}
	return out
}

func chainNameFor(path string) string {
	// Map common anikage-style paths to short names.
	switch {
	case strings.HasSuffix(path, "/episodes"):
		return "episodes"
	case strings.HasSuffix(path, "/servers"):
		return "servers"
	case strings.HasSuffix(path, "/sources"):
		return "sources"
	case strings.HasSuffix(path, "/downloads"):
		return "downloads"
	}
	return ""
}

func templateFromURL(rawURL string) (string, []string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, nil
	}
	// Replace the episode's identifier (path segment) with {n}
	// or {slug} depending on the chain name.
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	var ph []string
	if len(parts) >= 2 {
		// The last segment is the identifier (number or UUID).
		// Replace with {n}.
		parts[len(parts)-1] = "{n}"
		ph = append(ph, "n")
		// If the segment two before the end looks like a slug
		// (long alphanumeric, no slashes), replace with {slug}.
		if len(parts) >= 3 {
			slug := parts[len(parts)-2]
			if isSlug(slug) {
				parts[len(parts)-2] = "{slug}"
				ph = append(ph, "slug")
			}
		}
		u.Path = "/" + strings.Join(parts, "/")
	}
	// For /sources, the {provider} param is required.
	if strings.Contains(u.RawQuery, "provider=") {
		q := u.Query()
		q.Set("provider", "{provider}")
		q.Set("lang", "{lang}")
		u.RawQuery = q.Encode()
		ph = append(ph, "provider", "lang")
	}
	return u.String(), ph
}

func isSlug(s string) bool {
	if len(s) < 4 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func streamTemplateFor(rawURL string) string {
	// E.g. https://prox.anikage.cc/m3u8/{key} (placeholder
	// substituted back).
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 2 && (parts[0] == "m3u8" || parts[0] == "stream") {
		parts[1] = "{key}"
		u.Path = "/" + strings.Join(parts, "/")
		return u.String()
	}
	return rawURL
}

func domainFromURL(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("no host in %q", rawURL)
	}
	// Strip www. prefix.
	if strings.HasPrefix(host, "www.") {
		host = host[4:]
	}
	return host, nil
}

func entryURLFromDomain(domain string) string {
	return "https://" + domain
}

func deriveBase(r *recipe.Recipe, fallback string) string {
	// Look at the first chain step; the URL is on the same
	// origin as the stream template or fallback.
	if r.StreamTemplate != "" {
		u, err := url.Parse(r.StreamTemplate)
		if err == nil && u.Scheme != "" {
			return u.Scheme + "://" + u.Host
		}
	}
	return fallback
}

// probe is a tiny shim that runs the real probe with a header map.
// The real implementation is in pkg/probe; we just wrap it here
// for convenience in the replay path.
func probe(rawURL string, headers map[string]string) (*probetype.Response, error) {
	return probetype.Do(context.Background(), probetype.Request{
		URL: rawURL, Headers: headers,
	})
}

var _ = pickjson.ParseProviderList

// --- formatting helpers ---

func humanBytes(n int64) string {
	const (
		KiB = 1024
		MiB = 1024 * 1024
		GiB = 1024 * 1024 * 1024
	)
	switch {
	case n >= GiB:
		return fmt.Sprintf("%.1f GiB", float64(n)/GiB)
	case n >= MiB:
		return fmt.Sprintf("%.1f MiB", float64(n)/MiB)
	case n >= KiB:
		return fmt.Sprintf("%.1f KiB", float64(n)/KiB)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func sanitizeFilename(s string) string {
	// Replace anything that's not alnum, dash, underscore, or
	// dot with underscore. Trim leading/trailing whitespace.
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == ' ' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	return strings.TrimSpace(string(out))
}

func joinStrings(ps []recipe.Provider) string {
	ns := make([]string, len(ps))
	for i, p := range ps {
		ns[i] = p.Name
	}
	return strings.Join(ns, ", ")
}

func parseRangeLocal(s string, n int) ([]int, error) {
	// Minimal range parser for the --episodes flag.
	seen := map[int]bool{}
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.IndexByte(part, '-'); i >= 0 {
			a, errA := strconv.Atoi(strings.TrimSpace(part[:i]))
			b, errB := strconv.Atoi(strings.TrimSpace(part[i+1:]))
			if errA != nil || errB != nil || a > b || a < 1 || b > n {
				return nil, fmt.Errorf("bad range %q", part)
			}
			for k := a; k <= b; k++ {
				idx := k - 1
				if !seen[idx] {
					seen[idx] = true
					out = append(out, idx)
				}
			}
		} else {
			k, err := strconv.Atoi(part)
			if err != nil || k < 1 || k > n {
				return nil, fmt.Errorf("bad number %q", part)
			}
			idx := k - 1
			if !seen[idx] {
				seen[idx] = true
				out = append(out, idx)
			}
		}
	}
	return out, nil
}

func looksLikeURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// splitArgsLocal walks args, identifies which values are flag
// arguments vs positionals, and returns (flagArgs, positionals)
// in canonical order. It's the local equivalent of
// internal/cli.Split; we keep it in main because main can't
// import internal/.
func splitArgsLocal(args []string, fs *flag.FlagSet) (flagArgs, pos []string) {
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
		isBool := isBoolFlagLocal(f)

		if hasValue {
			flagArgs = append(flagArgs, "-"+name+"="+value)
			continue
		}
		if isBool {
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
	return
}

func isBoolFlagLocal(f *flag.Flag) bool {
	if bv, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
		return bv.IsBoolFlag()
	}
	if f.Value != nil {
		s := f.Value.String()
		return s == "true" || s == "false"
	}
	return false
}

// probeResponse is no longer used; kept commented for reference.
// type probeResponse = struct {
// 	Status  int
// 	Body    []byte
// 	Headers map[string][]string
// }
