// Package fuzz implements informed fuzzing: instead of randomly
// trying values, the strategies in this package look at the response
// shape, the endpoint graph, and the error messages to suggest
// high-value candidates.
//
// The REPL asks "what should I try next?" and fuzz returns a list
// of Candidates. The user picks one (or has the REPL run the
// top-ranked one) and the result feeds back into the graph and
// shape classifier for the next round.
package fuzz

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/falcon/api-recon/pkg/graph"
)

// Candidate is a single fuzzing attempt. URL is fully resolved.
// Headers are optional overrides applied on top of the captured
// credentials.
type Candidate struct {
	URL     string
	Method  string
	Headers map[string]string
	Reason  string // human-readable explanation
	Source  string // "sibling" | "error_msg" | "ui" | "boundary"
	Stars   int    // 1-3, drives menu ordering
}

// Strategy produces candidates given the current graph and the last
// failed observation. The Candidate list is in priority order.
type Strategy interface {
	Candidates(g *graph.Graph, last *graph.Observation) []Candidate
	Name() string
}

// Strategies returns the default set, in priority order.
func Strategies() []Strategy {
	return []Strategy{
		FromErrorMessage{},
		FromSiblings{},
		FromBoundary{},
	}
}

// missingValueRE extracts the value from "provider {X} not found",
// "missing {X}", "no {X} available", etc. Case-insensitive.
var missingValueRE = regexp.MustCompile(`(?i)(?:provider|missing|requires?|needs?)\s+["']?([A-Za-z0-9_.-]+)["']?`)

// FromErrorMessage extracts candidate values from the error message
// of the last observation. This is the highest-value strategy —
// the anikage case was cracked this way ("provider pahe not found"
// → try provider=miko).
type FromErrorMessage struct{}

func (FromErrorMessage) Name() string { return "error_msg" }

func (FromErrorMessage) Candidates(g *graph.Graph, last *graph.Observation) []Candidate {
	if last == nil || last.RespBody == nil {
		return nil
	}
	body := string(last.RespBody)
	matches := missingValueRE.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	// Dedupe.
	seen := map[string]bool{}
	var out []Candidate
	for _, m := range matches {
		val := m[1]
		if seen[val] {
			continue
		}
		seen[val] = true
		cand := candidateFromURL(last.URL, val)
		if cand == nil {
			continue
		}
		cand.Source = "error_msg"
		cand.Reason = "extracted from error message: " + val
		cand.Stars = 3
		out = append(out, *cand)
	}
	return out
}

// FromSiblings tries sibling paths of the last failed endpoint with
// the same query params. The anikage unlock: /episodes/1/downloads
// failed → try /episodes/1/sources.
type FromSiblings struct{}

func (FromSiblings) Name() string { return "sibling" }

func (FromSiblings) Candidates(g *graph.Graph, last *graph.Observation) []Candidate {
	if g == nil || last == nil {
		return nil
	}
	parsed, err := url.Parse(last.URL)
	if err != nil {
		return nil
	}
	siblings := g.Siblings(parsed.Path)
	var out []Candidate
	for _, s := range siblings {
		// Reconstruct a URL with the same scheme/host/query.
		newURL := *parsed
		newURL.Path = s.Path
		// Skip the failed status if we know it.
		if s.Status == 0 || s.Status >= 400 {
			continue
		}
		out = append(out, Candidate{
			URL:    newURL.String(),
			Method: s.Method,
			Source: "sibling",
			Reason: "sibling of " + parsed.Path + " (status " + statusStr(s.Status) + ")",
			Stars:  2,
		})
	}
	return out
}

// FromBoundary tries boundary values against the last failed URL:
// -1, 0, 99999 for numeric segments. The classic "the API says 500
// for episode 1 — does it work for episode 0 or 99999?"
type FromBoundary struct{}

func (FromBoundary) Name() string { return "boundary" }

func (FromBoundary) Candidates(g *graph.Graph, last *graph.Observation) []Candidate {
	if last == nil || last.URL == "" {
		return nil
	}
	parsed, err := url.Parse(last.URL)
	if err != nil {
		return nil
	}
	segments := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if len(segments) == 0 {
		return nil
	}

	// Find the first numeric segment and fuzz it. We return all
	// boundary candidates for that segment in priority order.
	for i, seg := range segments {
		if _, err := strconv.Atoi(seg); err != nil {
			continue
		}
		var out []Candidate
		for _, val := range []string{"0", "-1", "99999"} {
			if val == seg {
				continue // skip the value we already tried
			}
			newSegs := append([]string{}, segments...)
			newSegs[i] = val
			newURL := *parsed
			newURL.Path = "/" + strings.Join(newSegs, "/")
			out = append(out, Candidate{
				URL:    newURL.String(),
				Method: last.Method,
				Source: "boundary",
				Reason: "boundary value " + val + " for segment " + seg,
				Stars:  1,
			})
		}
		return out
	}
	return nil
}

// FromUI returns candidates from a list of values the caller (the
// REPL) harvested from the page. The anikage case: the page had
// buttons labeled "kiss", "miko", "verse", "koto", "e-kiss", "e-aki"
// — these become provider= candidates.
type FromUI struct {
	Values []string
	Param  string // query param to set, e.g. "provider"
}

func (f FromUI) Name() string { return "ui" }

func (f FromUI) Candidates(g *graph.Graph, last *graph.Observation) []Candidate {
	if last == nil || len(f.Values) == 0 || f.Param == "" {
		return nil
	}
	parsed, err := url.Parse(last.URL)
	if err != nil {
		return nil
	}
	q := parsed.Query()
	var out []Candidate
	for _, v := range f.Values {
		// Skip the value already in the query.
		if q.Get(f.Param) == v {
			continue
		}
		newQ := url.Values{}
		for k, vs := range q {
			for _, val := range vs {
				newQ.Add(k, val)
			}
		}
		newQ.Set(f.Param, v)
		newURL := *parsed
		newURL.RawQuery = newQ.Encode()
		out = append(out, Candidate{
			URL:    newURL.String(),
			Method: last.Method,
			Source: "ui",
			Reason: "value " + v + " from page UI for " + f.Param,
			Stars:  2,
		})
	}
	return out
}

// candidateFromURL clones last.URL with the query param param set
// to value, returning a *Candidate. Returns nil if last.URL is
// unparseable.
func candidateFromURL(rawURL, value string) *Candidate {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	q := parsed.Query()
	// If the URL already has the param, we replace the value. If
	// it has no provider-like query, we still add the new value
	// under a guess: look for ?provider=, ?lang=, ?type=.
	paramName := guessParamName(q)
	q.Set(paramName, value)
	parsed.RawQuery = q.Encode()
	return &Candidate{
		URL:    parsed.String(),
		Method: "GET",
	}
}

// guessParamName returns the query param to set when fuzzing from
// an error message. Prefers "provider" if absent, otherwise picks
// the first existing param.
func guessParamName(q url.Values) string {
	if len(q) == 0 {
		return "provider"
	}
	for _, name := range []string{"provider", "lang", "type", "format", "id"} {
		if _, has := q[name]; has {
			return name
		}
	}
	// Otherwise return the first param.
	for k := range q {
		return k
	}
	return "provider"
}

// statusStr formats a status code as a string.
func statusStr(s int) string {
	if s == 0 {
		return "unknown"
	}
	return strconv.Itoa(s)
}

// Run picks the first non-empty candidate list, sends the request,
// and returns the result. The strategy list is tried in order; the
// first strategy to produce candidates wins. The HTTP client is the
// caller's — pass in one configured with reasonable timeouts and
// any auth you want applied (the REPL uses the captured creds).
func Run(ctx context.Context, hc *http.Client, g *graph.Graph, last *graph.Observation, extras ...Strategy) (*graph.Observation, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	strategies := append(Strategies(), extras...)
	for _, s := range strategies {
		cands := s.Candidates(g, last)
		if len(cands) == 0 {
			continue
		}
		// Try the top-ranked candidate.
		cand := cands[0]
		req, err := http.NewRequestWithContext(ctx, cand.Method, cand.URL, nil)
		if err != nil {
			return nil, err
		}
		for k, v := range cand.Headers {
			req.Header.Set(k, v)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fuzz: %s: %w", cand.URL, err)
		}
		defer resp.Body.Close()
		buf := make([]byte, 8192)
		n, _ := resp.Body.Read(buf)
		obs := &graph.Observation{
			URL:      cand.URL,
			Method:   cand.Method,
			Status:   resp.StatusCode,
			RespBody: buf[:n],
		}
		return obs, nil
	}
	return nil, nil // no candidates produced by any strategy
}
