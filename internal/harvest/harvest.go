// Package harvest is the guided discovery driver. It exposes
// actions that the REPL uses to build a recipe: probe, drill-down,
// watch the page, and save the result.
//
// The harvest package is itself a set of action.Actions, not a
// separate driver process. The REPL calls these actions; each one
// does one thing and returns a Result.
package harvest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/falcon/api-recon/pkg/action"
	"github.com/falcon/api-recon/pkg/graph"
	"github.com/falcon/api-recon/pkg/recipe"
	"github.com/falcon/api-recon/pkg/shape"
)

// Action is the harvest action — the entry point for guided
// discovery. It takes a starting URL, auto-probes it, and emits a
// Result with the initial recipe entry plus tags the REPL uses to
// suggest next steps.
func Action() action.Action {
	return action.Action{
		Name:        "harvest",
		Aliases:     []string{"h"},
		Summary:     "Discover and capture a domain's API",
		Description: "harvest drives guided discovery for a domain. It probes the entry URL, classifies the response, and emits Result tags the REPL uses to suggest next steps. Run inside the REPL for the full flow.",
		Examples: []string{
			"api-recon https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes",
			"api-recon harvest https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes",
		},
		Category: "discover",
		Run:      run,
	}
}

// run is the Action's Run func. It expects the entry URL as the
// first positional arg, with optional --method and --headers flags
// already parsed by the REPL (which is responsible for argv).
//
// The flow:
//  1. Parse the URL.
//  2. Send a GET (or specified method) request.
//  3. Classify the response.
//  4. Build/extend a Recipe with the entry endpoint + observed shape.
//  5. Observe the exchange through creds and graph.
//  6. Emit a Result with shape tags and next-step hints.
func run(ctx context.Context, c *action.Context, args []string) (*action.Result, error) {
	if c == nil {
		return nil, fmt.Errorf("harvest: nil context")
	}
	if len(args) == 0 || args[0] == "" {
		return nil, fmt.Errorf("harvest: entry URL is required")
	}
	entryURL, err := url.Parse(args[0])
	if err != nil {
		return nil, fmt.Errorf("harvest: parse URL: %w", err)
	}
	if entryURL.Scheme == "" || entryURL.Host == "" {
		return nil, fmt.Errorf("harvest: URL must be absolute (got %q)", args[0])
	}

	// Build the in-flight recipe if we don't have one yet.
	if c.Recipe == nil {
		c.Recipe = recipe.New(entryURL.Host)
	} else if c.Recipe.Domain == "" {
		c.Recipe.Domain = entryURL.Host
	}
	if c.Graph == nil {
		c.Graph = graph.New()
	}
	if c.Creds == nil {
		// Should be initialized by the REPL, but be defensive.
		return nil, fmt.Errorf("harvest: credential store not initialized")
	}
	if c.Shape == nil {
		c.Shape = shape.New()
	}

	// Send the request. Use the captured creds as the base.
	method := pickArg(args, "--method")
	if method == "" {
		method = "GET"
	}
	req, err := http.NewRequestWithContext(ctx, method, entryURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("harvest: build request: %w", err)
	}
	// Default headers. We must set these explicitly — Cloudflare
	// and other CDNs serve bot-detection HTML pages when User-Agent
	// is empty, so the shape classifier sees a 404 HTML page rather
	// than the 404 JSON envelope the API actually returns. Inject()
	// preserves caller-set headers, so defaults are safe.
	req.Header.Set("User-Agent", "api-recon/0.1.0")
	req.Header.Set("Accept", "application/json, */*;q=0.5")
	// Apply captured creds. We do this even for the first probe so
	// the recipe is built against the same headers the user will
	// need on replay.
	headers := c.Creds.Inject(req.Header, entryURL)
	req.Header = headers

	hc := &http.Client{Timeout: 30 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("harvest: request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("harvest: read body: %w", err)
	}

	// Classify.
	s := c.Shape.Classify(resp, body, entryURL.String())

	// Observe.
	c.Creds.ObserveHeaders(req.Header, resp.Header, entryURL.String())
	c.Graph.Observe(graph.Observation{
		URL: entryURL.String(), Method: method, Status: resp.StatusCode,
		RespBody: body, RespHeaders: resp.Header, Timestamp: time.Now(),
	})

	// Update recipe.
	endpointName := endpointNameFromPath(entryURL.Path)
	ep := c.Recipe.Endpoints[endpointName]
	ep.URL = entryURL.String()
	ep.Method = method
	ep.Shape = string(s.Kind)
	if len(ep.Params) == 0 && entryURL.RawQuery != "" {
		ep.Params = queryParamNames(entryURL.RawQuery)
	}
	c.Recipe.Endpoints[endpointName] = ep
	c.Recipe.Auth = c.Creds.AsRecipeAuth()

	// Update last response on the context so the REPL can show it.
	c.LastResponse = resp
	c.LastBody = body

	// Build the result.
	res := &action.Result{
		Summary: fmt.Sprintf("probed %s → %d (%s, %d bytes)", entryURL.Redacted(), resp.StatusCode, s.Kind, len(body)),
		Tags:    tagsForShape(s, entryURL.Host),
		Data:    s,
	}
	if s.Reasoning != "" {
		res.Hints = append(res.Hints, s.Reasoning)
	}
	if s.CrossHost != "" {
		res.Hints = append(res.Hints, "cross-host URL detected: "+s.CrossHost+" — make sure auth is captured for that host too")
	}
	if s.Kind == shape.KindJSONList && len(s.IDFields) > 0 {
		res.Hints = append(res.Hints, fmt.Sprintf("list with id field %q — try drilling into one item", s.IDFields[0]))
	}
	if s.Kind == shape.KindError && len(s.MissingValues) > 0 {
		res.Hints = append(res.Hints, "error message contains values to try: "+strings.Join(s.MissingValues, ", "))
	}

	return res, nil
}

// tagsForShape returns the tags the REPL pattern-matches on. The
// shape package's Kind is the head; metadata is appended.
func tagsForShape(s shape.Shape, host string) []string {
	tags := []string{"shape:" + s.Kind.String()}
	if s.CrossHost != "" {
		tags = append(tags, "auth:cross_host")
		tags = append(tags, "host:"+s.CrossHost)
	}
	if s.Kind == shape.KindError {
		tags = append(tags, "shape:error")
	}
	if s.Kind == shape.KindHLSMaster || s.Kind == shape.KindHLSVariant {
		tags = append(tags, "download:ready")
	}
	if s.Kind == shape.KindJSONList {
		tags = append(tags, "graph:list_endpoint")
	}
	return tags
}

// endpointNameFromPath derives a short name from a path. We use
// the last non-empty segment, capitalized. /api/episodes →
// "episodes". /api/media/anime/{slug}/episodes → "episodes".
func endpointNameFromPath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) == 0 {
		return "root"
	}
	last := parts[len(parts)-1]
	if last == "" {
		return "root"
	}
	return last
}

// queryParamNames extracts the names (not values) from a query
// string. Used to populate Endpoint.Params.
func queryParamNames(rawQuery string) []string {
	parsed, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(parsed))
	for k := range parsed {
		out = append(out, k)
	}
	return out
}

// pickArg returns the value following the first occurrence of flag
// in args, or "" if absent. Used to read --method from the
// pre-parsed argv without depending on the flag package.
func pickArg(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
