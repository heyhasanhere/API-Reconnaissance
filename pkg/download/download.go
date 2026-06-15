// Package download is the download interpreter: given a recognized
// response shape and a captured auth context, it produces a Plan
// describing the right tool (yt-dlp, curl, aria2c) and argv to
// download the source.
//
// The dispatch is a map[shape.Kind]Strategy, not a switch — easy to
// extend, easy to test, easy to override.
//
// Execution is opt-in. By default, callers use Marshal to get the
// argv as a script for inspection. Plan.Run actually execs the
// process (the REPL offers this as a separate option after showing
// the generated command).
package download

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/heyhasanhere/API-Reconnaissance/pkg/recipe"
	"github.com/heyhasanhere/API-Reconnaissance/pkg/shape"
)

// Plan is the output of a Strategy: a list of argv to invoke, an
// optional environment, and a human-readable note explaining what
// the command does.
type Plan struct {
	Argv []string
	Env  []string
	Note string
}

// Marshal returns the Plan's argv as a single-line, shell-safe
// string. Used by the REPL to display the command before asking
// the user to run it.
func (p Plan) Marshal() string {
	if len(p.Argv) == 0 {
		return ""
	}
	var b strings.Builder
	for i, a := range p.Argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		// Quote args that contain spaces or shell metacharacters.
		if needsQuoting(a) {
			b.WriteByte('\'')
			b.WriteString(strings.ReplaceAll(a, "'", `'\\''`))
			b.WriteByte('\'')
		} else {
			b.WriteString(a)
		}
	}
	return b.String()
}

func needsQuoting(s string) bool {
	for _, r := range s {
		if r <= ' ' || r == '"' || r == '\'' || r == '\\' || r == '$' || r == '`' || r == '&' || r == '|' || r == ';' || r == '<' || r == '>' || r == '*' || r == '?' {
			return true
		}
	}
	return false
}

// Run executes the Plan in the foreground. The Plan's stdout/stderr
// is inherited from the parent process so the user sees the
// download progress. The function returns when the child exits or
// the context is cancelled.
func (p Plan) Run(ctx context.Context) error {
	if len(p.Argv) == 0 {
		return fmt.Errorf("download: empty plan")
	}
	if p.Argv[0] == "" {
		return fmt.Errorf("download: empty program name")
	}
	cmd := exec.CommandContext(ctx, p.Argv[0], p.Argv[1:]...)
	cmd.Env = append(cmd.Environ(), p.Env...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// Input is what strategies consume. Shape is the recognized kind;
// Auth is the captured credentials (for header injection); URL is
// the URL to download; Body is the response body (some strategies
// need to parse it for stream keys, segment lists, etc.).
type Input struct {
	URL        string
	Body       []byte
	Shape      shape.Shape
	Auth       recipe.Auth
	OutputPath string
}

// Strategy produces a Plan from an Input. Returning an error means
// the strategy can't handle this shape — the dispatcher will skip
// to the next strategy.
type Strategy func(ctx context.Context, in Input) (Plan, error)

// dispatchTable is the package-internal table. We populate it
// from init() to avoid the initialization cycle that would occur
// if jsonStreamKeyExtract (which references the table) were
// referenced from a package-level var initializer.
var dispatchTable map[shape.Kind]Strategy

func init() {
	dispatchTable = map[shape.Kind]Strategy{
		shape.KindHLSMaster:   hlsViaYTDLP,
		shape.KindHLSVariant:  hlsViaYTDLP,
		shape.KindDASH:        dashViaYTDLP,
		shape.KindDirect:      directViaCurl,
		shape.KindSegmentList: segmentListViaAria2,
		shape.KindHTML:        htmlExtractLinks,
		shape.KindJSON:        jsonStreamKeyExtract,
	}
}

// Dispatch returns the public table. Tests can mutate the returned
// map to override strategies.
func Dispatch() map[shape.Kind]Strategy {
	return dispatchTable
}

// PlanFor returns a Plan for the given Input by looking up the
// strategy in dispatchTable. Returns an error if no strategy is
// registered for the shape.
func PlanFor(ctx context.Context, in Input) (Plan, error) {
	if in.OutputPath == "" {
		in.OutputPath = "output"
	}
	strat, ok := dispatchTable[in.Shape.Kind]
	if !ok {
		return Plan{}, fmt.Errorf("download: no strategy for shape %q", in.Shape.Kind)
	}
	return strat(ctx, in)
}

// hlsViaYTDLP generates a yt-dlp argv for an HLS playlist. This is
// the anikage case directly: yt-dlp with concurrent fragments
// and the captured Origin/Referer headers.
func hlsViaYTDLP(ctx context.Context, in Input) (Plan, error) {
	argv := []string{"yt-dlp", "--no-warnings", "-o", in.OutputPath}
	for k, v := range in.Auth.RequiredHeaders {
		argv = append(argv, "--add-header", k+":"+v)
	}
	if in.Shape.Kind == shape.KindHLSMaster {
		argv = append(argv, "--allow-unplayable-formats")
	}
	argv = append(argv, "--concurrent-fragments", "16", in.URL)
	return Plan{
		Argv: argv,
		Note: "yt-dlp with 16 concurrent fragments" + describeAuth(in.Auth),
	}, nil
}

// dashViaYTDLP generates a yt-dlp argv for a DASH manifest.
func dashViaYTDLP(ctx context.Context, in Input) (Plan, error) {
	argv := []string{"yt-dlp", "--no-warnings", "-o", in.OutputPath}
	for k, v := range in.Auth.RequiredHeaders {
		argv = append(argv, "--add-header", k+":"+v)
	}
	argv = append(argv, in.URL)
	return Plan{
		Argv: argv,
		Note: "yt-dlp for DASH manifest" + describeAuth(in.Auth),
	}, nil
}

// directViaCurl generates a curl argv for a direct file URL (mp4,
// etc.) with parallel range requests.
func directViaCurl(ctx context.Context, in Input) (Plan, error) {
	argv := []string{"curl", "-L", "--fail", "-o", in.OutputPath}
	for k, v := range in.Auth.RequiredHeaders {
		argv = append(argv, "-H", k+": "+v)
	}
	if in.Auth.BearerToken != "" {
		argv = append(argv, "-H", "Authorization: Bearer "+in.Auth.BearerToken)
	}
	argv = append(argv, in.URL)
	return Plan{
		Argv: argv,
		Note: "curl with -L (follow redirects)" + describeAuth(in.Auth),
	}, nil
}

// segmentListViaAria2 generates an aria2c argv for a JSON segment
// list. We write the segment URLs to a temp file and pass it via
// --input-file.
func segmentListViaAria2(ctx context.Context, in Input) (Plan, error) {
	// Write segment URLs to a tmp file. We use the OutputPath's
	// directory or /tmp.
	tmpPath := in.OutputPath + ".segments.txt"
	if err := writeSegmentList(tmpPath, in.Body); err != nil {
		return Plan{}, fmt.Errorf("download: write segment list: %w", err)
	}
	argv := []string{"aria2c", "-x", "16", "-o", in.OutputPath, "--input-file=" + tmpPath}
	for k, v := range in.Auth.RequiredHeaders {
		argv = append(argv, "--header", k+": "+v)
	}
	return Plan{
		Argv: argv,
		Note: "aria2c with 16 connections per segment" + describeAuth(in.Auth),
	}, nil
}

// htmlExtractLinks parses an HTML page and returns the first
// downloadable <a href> or <a download> link. Used as a fallback
// when the page is the "real" download entry point.
func htmlExtractLinks(ctx context.Context, in Input) (Plan, error) {
	links := findHTMLLinks(string(in.Body))
	if len(links) == 0 {
		return Plan{}, fmt.Errorf("download: no <a href> links found in HTML")
	}
	// We just return the first; the REPL can present all of them
	// and let the user pick.
	link := links[0]
	argv := []string{"curl", "-L", "-o", in.OutputPath, link}
	return Plan{
		Argv: argv,
		Note: fmt.Sprintf("curl for HTML link (first of %d): %s", len(links), link),
	}, nil
}

// jsonStreamKeyExtract handles a JSON response that wraps a stream
// key (the anikage /sources case: {"sources": [{"url": "..."}]}).
// The "url" field is a base64-ish key into the CDN proxy, not an
// actual URL. We need to build the real URL from it and then
// hand off to the HLS handler.
func jsonStreamKeyExtract(ctx context.Context, in Input) (Plan, error) {
	// Find the first sources[].url value.
	key, host, err := extractStreamKey(in.Body, in.Shape.CrossHost)
	if err != nil {
		return Plan{}, fmt.Errorf("download: extract stream key: %w", err)
	}
	realURL := buildStreamURL(key, host, in.Auth)
	// Build a new Input with the resolved URL and re-dispatch.
	newIn := in
	newIn.URL = realURL
	newIn.Body = nil
	if isLikelyHLSKey(key) {
		newIn.Shape = shape.Shape{Kind: shape.KindHLSMaster, ContentType: "application/vnd.apple.mpegurl", Status: 200}
	} else {
		newIn.Shape = shape.Shape{Kind: shape.KindDirect, ContentType: "video/mp4", Status: 200}
	}
	strat, ok := dispatchTable[newIn.Shape.Kind]
	if !ok {
		return Plan{}, fmt.Errorf("download: no strategy for resolved shape")
	}
	return strat(ctx, newIn)
}

// describeAuth returns a short string describing which auth is
// applied. Used in Plan.Note.
func describeAuth(auth recipe.Auth) string {
	if auth.BearerToken == "" && len(auth.RequiredHeaders) == 0 && auth.SessionCookie == "" && auth.APIKey == "" {
		return ""
	}
	parts := []string{}
	if auth.BearerToken != "" {
		parts = append(parts, "bearer")
	}
	if auth.SessionCookie != "" {
		parts = append(parts, "cookie")
	}
	if auth.APIKey != "" {
		parts = append(parts, "api-key")
	}
	if len(auth.RequiredHeaders) > 0 {
		parts = append(parts, fmt.Sprintf("%d req headers", len(auth.RequiredHeaders)))
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
