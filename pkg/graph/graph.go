// Package graph builds and queries the endpoint graph: a
// directed graph where each captured HTTP exchange becomes a node,
// and parent/child relationships (which page called which API) and
// sibling relationships (paths sharing a prefix) are edges.
//
// The graph is the unlock for the anikage 500→workaround transition:
// when /episodes/1/downloads returns 500, Siblings("downloads")
// returns ["sources"], and the REPL suggests trying the sibling.
//
// Two construction modes:
//   - Build(observations) — synchronous, for tests and post-mortem.
//   - (g *Graph).Observe(obs) — live observer, safe for concurrent
//     use. Used by the capture pipeline.
package graph

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/falcon/api-recon/pkg/shape"
)

// Observation is a single captured HTTP exchange. The bodies may be
// nil (we don't always capture them) and the URL is required.
type Observation struct {
	URL         string
	Method      string
	Status      int
	ReqBody     []byte
	RespBody    []byte
	RespHeaders http.Header
	Timestamp   time.Time
}

// Node is a single endpoint in the graph. Path is the URL's path
// (not the full URL), Method is the HTTP method. BodySample is the
// first non-empty response body, capped at 4KB.
type Node struct {
	Path       string
	Method     string
	Status     int
	Shape      string // last observed shape kind
	IDFields   []string
	HitCount   int
	LastSeen   time.Time
	Children   []string
	Parents    []string
	BodySample json.RawMessage
}

// Graph is a directed graph of endpoint relationships. Construct
// with Build (synchronous) or New + Observe (live).
type Graph struct {
	mu    sync.RWMutex
	nodes map[string]*Node   // path → node
	order []string           // insertion order (for stable output)
	// obsOrder remembers observations in arrival order so we can
	// reconstruct parent→child edges.
	obs []Observation
}

// New returns an empty Graph ready for Observe calls.
func New() *Graph {
	return &Graph{
		nodes: map[string]*Node{},
	}
}

// Build constructs a Graph from a slice of observations, in order.
// Subsequent calls to Observe update the same graph.
//
// The Build vs New distinction: Build lets callers (tests, the
// JSONL post-mortem tool) construct a graph from already-collected
// data without needing a live stream.
func Build(obs []Observation) *Graph {
	g := New()
	for _, o := range obs {
		g.Observe(o)
	}
	return g
}

// Observe adds an observation to the graph. Safe for concurrent use.
func (g *Graph) Observe(o Observation) {
	if o.URL == "" {
		return
	}
	parsed, err := url.Parse(o.URL)
	if err != nil {
		return
	}
	path := parsed.Path
	if path == "" {
		path = "/"
	}

	// Update node.
	g.mu.Lock()
	defer g.mu.Unlock()

	g.obs = append(g.obs, o)

	node, exists := g.nodes[path]
	if !exists {
		node = &Node{Path: path, Method: o.Method}
		g.nodes[path] = node
		g.order = append(g.order, path)
	}
	node.HitCount++
	node.Status = o.Status
	if o.Timestamp.IsZero() {
		node.LastSeen = time.Now()
	} else {
		node.LastSeen = o.Timestamp
	}
	if len(o.RespBody) > 0 && len(node.BodySample) == 0 {
		// Cap body samples so a giant response doesn't bloat the graph.
		const cap = 4096
		if len(o.RespBody) > cap {
			node.BodySample = json.RawMessage(o.RespBody[:cap])
		} else {
			node.BodySample = json.RawMessage(o.RespBody)
		}
		// Try to extract ID fields by re-running the classifier.
		if !looksLikeJSON(o.RespBody) {
			node.Shape = "binary"
		} else {
			// Cheap shape guess — full classification happens in pkg/shape.
			node.Shape = quickShapeGuess(o.RespBody)
		}
		node.IDFields = quickIDFields(o.RespBody)
	}

	// Edge from this observation to the previous one: in the
	// typical capture flow, the browser loads a page (e.g.
	// /anime/watch/x) and then fires a series of API calls. We
	// model any two consecutive observations as parent → child.
	// This is a coarse heuristic — the real relationship is "page
	// A triggered request B" — but captures arrive in roughly that
	// order, and refining the rule would require looking at
	// response bodies, which is the shape classifier's job.
	//
	// We skip self-edges (same path observed twice in a row).
	if len(g.obs) >= 2 {
		prev := g.obs[len(g.obs)-2]
		prevURL, err1 := url.Parse(prev.URL)
		if err1 == nil && prevURL.Path != path {
			if prevNode, ok := g.nodes[prevURL.Path]; ok {
				if currNode, ok := g.nodes[path]; ok {
					addChild(&prevNode.Children, path)
					addChild(&currNode.Parents, prevURL.Path)
				}
			}
		}
	}
}

// Siblings returns the paths that share a prefix with path,
// excluding path itself. The "try /sources instead of /downloads"
// lookup.
//
// We define "sibling" as: paths that share at least 2 path segments
// and differ in the last segment.
func (g *Graph) Siblings(path string) []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()

	target, ok := g.nodes[path]
	if !ok {
		return nil
	}

	want := splitSegments(target.Path)
	if len(want) < 2 {
		// No siblings for top-level paths.
		return nil
	}
	prefix := "/" + strings.Join(want[:len(want)-1], "/") + "/"

	var out []Node
	for _, p := range g.order {
		if p == path {
			continue
		}
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		// The differing last segment must be a single segment.
		rest := p[len(prefix):]
		if strings.Contains(rest, "/") {
			continue
		}
		if n, ok := g.nodes[p]; ok {
			out = append(out, *n)
		}
	}
	return out
}

// Children returns the paths that this path called.
func (g *Graph) Children(path string) []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()

	node, ok := g.nodes[path]
	if !ok {
		return nil
	}
	var out []Node
	for _, c := range node.Children {
		if n, ok := g.nodes[c]; ok {
			out = append(out, *n)
		}
	}
	return out
}

// Parents returns the paths that called this path.
func (g *Graph) Parents(path string) []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()

	node, ok := g.nodes[path]
	if !ok {
		return nil
	}
	var out []Node
	for _, p := range node.Parents {
		if n, ok := g.nodes[p]; ok {
			out = append(out, *n)
		}
	}
	return out
}

// Node returns the node for a path, if any.
func (g *Graph) Node(path string) (Node, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if n, ok := g.nodes[path]; ok {
		return *n, true
	}
	return Node{}, false
}

// Paths returns all observed paths in insertion order.
func (g *Graph) Paths() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]string, len(g.order))
	copy(out, g.order)
	return out
}

// IDFlow represents an ID flowing from one endpoint to another:
// source emitted an ID value that target accepts.
type IDFlow struct {
	IDField     string
	Source      string
	Target      string
	SampleValue string
}

// IDFlows computes ID flow relationships: for each node that emits
// IDs (e.g. /episodes emits `id` values), find nodes whose URL
// contains a {placeholder} and report the flow.
//
// The match is name-based: if a node's body has an `id` field and
// another node's path has an `{id}` placeholder, that's a flow. We
// also do a length-based fallback for the case where the
// placeholder name doesn't match the field name but the value
// happens to fit.
func (g *Graph) IDFlows() []IDFlow {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var flows []IDFlow
	for _, sourcePath := range g.order {
		source := g.nodes[sourcePath]
		if source.BodySample == nil {
			continue
		}
		for _, idField := range source.IDFields {
			sample, ok := extractIDValue(source.BodySample, idField)
			if !ok {
				continue
			}
			for _, targetPath := range g.order {
				if targetPath == sourcePath {
					continue
				}
				if !pathAcceptsID(targetPath, idField, sample) {
					continue
				}
				flows = append(flows, IDFlow{
					IDField:     idField,
					Source:      sourcePath,
					Target:      targetPath,
					SampleValue: sample,
				})
			}
		}
	}
	return flows
}

// pathAcceptsID returns true if path has a {placeholder} whose name
// matches idField (e.g. {id} and field "id") OR whose length matches
// the captured sample value.
func pathAcceptsID(path, idField, sample string) bool {
	placeholders := placeholderNames(path)
	for _, p := range placeholders {
		// Name match: {id} accepts field "id".
		if strings.EqualFold(p, idField) {
			return true
		}
		// Length match: {abcdef} of length 6 accepts a 6-char value.
		if len(p) == len(sample) {
			return true
		}
	}
	return false
}

// String returns a human-readable summary of the graph.
func (g *Graph) String() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var b strings.Builder
	for _, p := range g.order {
		n := g.nodes[p]
		fmtNode(&b, n)
	}
	return b.String()
}

// splitSegments splits a path on '/' and returns non-empty segments.
func splitSegments(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// addChild appends a child path to a parent's Children list,
// avoiding duplicates.
func addChild(list *[]string, child string) {
	for _, existing := range *list {
		if existing == child {
			return
		}
	}
	*list = append(*list, child)
}

// quickShapeGuess is a cheap shape detection used when the full
// classifier is overkill (e.g. in tests or post-mortem).
func quickShapeGuess(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) == 0 {
		return "empty"
	}
	if trimmed[0] == '[' {
		return string(shape.KindJSONList)
	}
	if trimmed[0] == '{' {
		return string(shape.KindJSON)
	}
	if strings.HasPrefix(trimmed, "<!DOCTYPE") || strings.HasPrefix(trimmed, "<html") {
		return string(shape.KindHTML)
	}
	return string(shape.KindUnknown)
}

// quickIDFields extracts a single ID field from a JSON body.
func quickIDFields(body []byte) []string {
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) == 0 || trimmed[0] != '[' && trimmed[0] != '{' {
		return nil
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	// For a list, inspect the first item.
	if list, ok := parsed.([]any); ok && len(list) > 0 {
		if m, ok := list[0].(map[string]any); ok {
			return findFirstIDField(m)
		}
		return nil
	}
	// For an object, just check top-level keys.
	if m, ok := parsed.(map[string]any); ok {
		return findFirstIDField(m)
	}
	return nil
}

func findFirstIDField(m map[string]any) []string {
	for _, name := range []string{"id", "uuid", "slug", "name", "key"} {
		if _, has := m[name]; has {
			return []string{name}
		}
	}
	for k := range m {
		if strings.HasSuffix(k, "_id") {
			return []string{k}
		}
	}
	return nil
}

// extractIDValue pulls a string value for a field from a JSON body.
// Returns ("", false) if the field is not a string.
func extractIDValue(body []byte, field string) (string, bool) {
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", false
	}
	// List of objects: take the first one.
	if list, ok := parsed.([]any); ok && len(list) > 0 {
		if m, ok := list[0].(map[string]any); ok {
			return getStringField(m, field)
		}
	}
	// Single object.
	if m, ok := parsed.(map[string]any); ok {
		return getStringField(m, field)
	}
	return "", false
}

func getStringField(m map[string]any, field string) (string, bool) {
	v, ok := m[field]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// pathHasIDPlaceholder returns true if path contains a {name}
// placeholder where name matches value (the heuristic for "this
// endpoint accepts the ID we saw earlier"). We do a fuzzy match:
// the placeholder is the same length as the value, and case-insensitively
// equal to it. This is intentionally forgiving because placeholder
// names (slug, n, id) and the captured ID values (zMLNvt6MtV, etc.)
// rarely match exactly.
func pathHasIDPlaceholder(path, value string) bool {
	if value == "" {
		return false
	}
	for _, placeholder := range placeholderNames(path) {
		if len(placeholder) == len(value) && strings.EqualFold(placeholder, value) {
			return true
		}
	}
	return false
}

func placeholderNames(path string) []string {
	var out []string
	for {
		i := strings.IndexByte(path, '{')
		if i < 0 {
			break
		}
		j := strings.IndexByte(path[i:], '}')
		if j < 0 {
			break
		}
		name := path[i+1 : i+j]
		out = append(out, name)
		path = path[i+j+1:]
	}
	return out
}

func looksLikeJSON(body []byte) bool {
	trimmed := strings.TrimSpace(string(body))
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}

func fmtNode(b *strings.Builder, n *Node) {
	b.WriteString(n.Path)
	if n.Status > 0 {
		b.WriteString(" [")
		b.WriteString(itoa(n.Status))
		b.WriteString("]")
	}
	if n.Shape != "" {
		b.WriteString(" (")
		b.WriteString(n.Shape)
		b.WriteString(")")
	}
	b.WriteString("\n")
	if len(n.Children) > 0 {
		b.WriteString("  → ")
		sorted := append([]string(nil), n.Children...)
		sort.Strings(sorted)
		b.WriteString(strings.Join(sorted, ", "))
		b.WriteString("\n")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
