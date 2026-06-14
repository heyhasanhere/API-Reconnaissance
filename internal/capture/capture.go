// Package capture runs the Playwright helper as a subprocess and
// parses its JSONL output into Observation events. The Go side
// feeds these into graph.Graph and creds.Store.
package capture

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"

	"github.com/falcon/api-recon/pkg/creds"
	"github.com/falcon/api-recon/pkg/graph"
)

// Event is one parsed JSONL record from the helper.
type Event struct {
	Dir     string            // "req" | "resp" | "error"
	TS      int64             // epoch ms
	Method  string            // for req
	URL     string            // for both
	Status  int               // for resp
	Headers map[string]string // for both
	Body    []byte            // decoded from base64
}

// Opts configures Start.
type Opts struct {
	HelperPath string // path to helper.mjs
	URL        string
	Filter     string // regex
	Click      string // CSS selector
	Wait       int    // ms
	NoAds      bool
	Save       string // directory
}

// Start spawns the helper and returns a channel of Events. The
// channel is closed when the helper exits. Errors are sent as
// Event{Dir:"error", URL:err.Error()}.
func Start(ctx context.Context, opts Opts) (<-chan Event, error) {
	if opts.HelperPath == "" {
		return nil, fmt.Errorf("capture: HelperPath is required")
	}
	if opts.URL == "" {
		return nil, fmt.Errorf("capture: URL is required")
	}

	args := []string{opts.HelperPath, "watch", opts.URL}
	if opts.Filter != "" {
		args = append(args, "--filter", opts.Filter)
	}
	if opts.Click != "" {
		args = append(args, "--click", opts.Click)
	}
	if opts.Wait > 0 {
		args = append(args, "--wait", fmt.Sprintf("%d", opts.Wait))
	}
	if opts.NoAds {
		args = append(args, "--no-ads")
	}
	if opts.Save != "" {
		args = append(args, "--save", opts.Save)
	}

	cmd := exec.CommandContext(ctx, "node", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("capture: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("capture: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("capture: start: %w", err)
	}

	out := make(chan Event, 64)

	// Drain stderr in a goroutine so the subprocess doesn't block.
	// We don't currently surface helper stderr anywhere; the Go
	// side gets structured data via the JSONL channel.
	go func() {
		_, _ = io.Copy(io.Discard, stderr)
	}()

	go func() {
		defer close(out)
		scanner := bufio.NewScanner(stdout)
		// Increase buffer for large headers/bodies.
		scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			ev, err := parseEvent(line)
			if err != nil {
				out <- Event{Dir: "error", URL: "parse: " + err.Error()}
				continue
			}
			out <- ev
		}
		_ = cmd.Wait()
	}()

	return out, nil
}

// parseEvent unmarshals one JSONL record.
func parseEvent(line []byte) (Event, error) {
	var raw struct {
		V       int               `json:"v"`
		Dir     string            `json:"dir"`
		TS      int64             `json:"ts"`
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
		Message string            `json:"message"`
	}
	if err := json.Unmarshal(line, &raw); err != nil {
		return Event{}, err
	}
	ev := Event{
		Dir:     raw.Dir,
		TS:      raw.TS,
		Method:  raw.Method,
		URL:     raw.URL,
		Status:  raw.Status,
		Headers: raw.Headers,
	}
	if raw.Body != "" {
		body, err := base64.StdEncoding.DecodeString(raw.Body)
		if err == nil {
			ev.Body = body
		}
	}
	if raw.Dir == "error" {
		ev.URL = raw.Message
	}
	return ev, nil
}

// ToHeaders converts our map[string]string to http.Header. Headers
// with the same name (rare) keep only the first.
func ToHeaders(m map[string]string) http.Header {
	h := http.Header{}
	for k, v := range m {
		if h.Get(k) == "" {
			h.Set(k, v)
		}
	}
	return h
}

// ToCredsAndGraph runs an event stream through creds.Store and
// graph.Graph, returning the events consumed as Observation list.
//
// It's a convenience for the harvest driver: feed in the channel
// from Start, get back the observations and an updated creds/graph.
func ToCredsAndGraph(events <-chan Event, cs *creds.Store, g *graph.Graph) []graph.Observation {
	var observations []graph.Observation
	// Pair requests with responses by URL+method.
	pendingReqs := map[string]Event{}

	for ev := range events {
		switch ev.Dir {
		case "req":
			cs.ObserveHeaders(ToHeaders(ev.Headers), nil, ev.URL)
			pendingReqs[ev.URL] = ev
		case "resp":
			cs.ObserveHeaders(nil, ToHeaders(ev.Headers), ev.URL)
			observations = append(observations, graph.Observation{
				URL:         ev.URL,
				Method:      ev.Method,
				Status:      ev.Status,
				RespBody:    ev.Body,
				RespHeaders: ToHeaders(ev.Headers),
			})
			delete(pendingReqs, ev.URL)
		}
	}

	// If the helper exited without sending responses for some
	// requests, log them as observations with status 0.
	for _, ev := range pendingReqs {
		observations = append(observations, graph.Observation{
			URL:    ev.URL,
			Method: ev.Method,
			Status: 0,
		})
	}

	// Feed observations into the graph.
	for _, obs := range observations {
		g.Observe(obs)
	}

	return observations
}

// ToString is a debug helper.
func (e Event) ToString() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]", e.Dir)
	if e.Method != "" {
		fmt.Fprintf(&b, " %s", e.Method)
	}
	if e.Status > 0 {
		fmt.Fprintf(&b, " %d", e.Status)
	}
	if e.URL != "" {
		fmt.Fprintf(&b, " %s", e.URL)
	}
	return b.String()
}

// isEmptyHeaderValue is a tiny helper used in tests.
func isEmptyHeaderValue(v string) bool { return v == "" }

// _ keeps the sync package available for future concurrency tweaks.
var _ = sync.Mutex{}
