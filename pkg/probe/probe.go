// Package probe does a single HTTP round-trip and returns the
// response as a flat struct. It is the only place in the codebase
// that talks to net/http; every other package receives a
// *probe.Response.
//
// The package is deliberately small. Headers are a map (caller
// decides defaults). The body is capped at 1 MiB so a misbehaving
// server can't OOM the tool — a probe that hits the cap sets
// BodyTruncated = true.
package probe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// MaxBodyBytes is the cap on response body capture. 1 MiB is enough
// for any playlist, JSON envelope, or HTML page we care about.
// Anything larger (video segments, raw files) is the domain of the
// downloader, not the probe.
const MaxBodyBytes = 1 << 20

// Request is a single HTTP call.
type Request struct {
	URL     string
	Method  string            // defaults to GET when empty
	Headers map[string]string // applied to the request; nil = none
	Body    []byte            // request body (for POST/PUT)
}

// Response is the result of a single HTTP call.
type Response struct {
	Status        int
	StatusText    string
	Headers       http.Header
	Body          []byte
	BodyTruncated bool   // true if the server sent more than MaxBodyBytes
	FinalURL      string // after redirects
	LatencyMS     int64
}

// Do executes req and returns the response. It applies the headers
// from req.Headers on top of an empty set (no implicit defaults —
// callers that need a User-Agent set it themselves, or use
// auth.DefaultHeaders()). Redirects are followed.
func Do(ctx context.Context, req Request) (*Response, error) {
	if req.URL == "" {
		return nil, fmt.Errorf("probe: empty URL")
	}
	if _, err := url.Parse(req.URL); err != nil {
		return nil, fmt.Errorf("probe: parse URL: %w", err)
	}

	method := req.Method
	if method == "" {
		method = http.MethodGet
	}

	var body io.Reader
	if len(req.Body) > 0 {
		body = bytesReader(req.Body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, body)
	if err != nil {
		return nil, fmt.Errorf("probe: build request: %w", err)
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	hc := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("probe: too many redirects")
			}
			return nil
		},
	}

	start := time.Now()
	resp, err := hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("probe: do: %w", err)
	}
	defer resp.Body.Close()

	// Read with cap.
	buf := make([]byte, 0, 4096)
	truncated := false
	for {
		chunk := make([]byte, 4096)
		n, err := resp.Body.Read(chunk)
		if n > 0 {
			if len(buf)+n > MaxBodyBytes {
				room := MaxBodyBytes - len(buf)
				if room > 0 {
					buf = append(buf, chunk[:room]...)
				}
				truncated = true
				// Drain the rest so the connection can be reused
				// (or at least closed cleanly).
				io.Copy(io.Discard, resp.Body)
				break
			}
			buf = append(buf, chunk[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("probe: read body: %w", err)
		}
	}

	return &Response{
		Status:        resp.StatusCode,
		StatusText:    http.StatusText(resp.StatusCode),
		Headers:       resp.Header,
		Body:          buf,
		BodyTruncated: truncated,
		FinalURL:      resp.Request.URL.String(),
		LatencyMS:     time.Since(start).Milliseconds(),
	}, nil
}

// bytesReader is a tiny io.Reader over a byte slice. We don't use
// bytes.NewReader directly to keep the import surface minimal and
// to make it trivial to swap in a streaming body later.
func bytesReader(b []byte) io.Reader {
	return &sliceReader{b: b}
}

type sliceReader struct {
	b []byte
	i int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
