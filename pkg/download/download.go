// Package download shells out to yt-dlp (or aria2c / curl as
// fallback) to fetch a stream. The package builds the argv based
// on the stream's shape kind (HLS master / HLS variant / DASH /
// direct / segment list), injects captured headers via
// --add-header, and streams stdout to the caller's writer.
//
// The package is a thin wrapper around os/exec. The interesting
// work is in buildArgv — that's where the yt-dlp magic numbers
// (--concurrent-fragments 16 for HLS) come from.
package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Request is a single download.
type Request struct {
	StreamURL  string
	OutputPath string            // path with extension, or template
	Headers    map[string]string // injected as --add-header
	Kind       string            // shape kind: "hls_master", "hls_variant", "dash", "direct", "segment_list"
	Concurrent int               // HLS/DASH concurrent fragments; default 16
	Tool       string            // override the binary (default: auto-pick by kind)
	Stdout     io.Writer         // if nil, discard
	Stderr     io.Writer         // if nil, os.Stderr
	ExtraArgs  []string          // appended after the kind-specific args
}

// Result is what Run returns on success.
type Result struct {
	Tool   string
	Argv   []string
	Bytes  int64
	Took   time.Duration
	Stderr string
}

// Run executes the download. Returns an error if the subprocess
// exits non-zero, the binary is missing, or the context is
// cancelled.
func Run(ctx context.Context, req Request) (*Result, error) {
	if req.StreamURL == "" {
		return nil, errors.New("download: empty StreamURL")
	}
	if req.Concurrent == 0 {
		req.Concurrent = 16
	}

	tool, argv := buildArgv(req)

	if req.Tool != "" {
		tool = req.Tool
		argv = append([]string{tool}, argv[1:]...) // keep the rest
	}

	// Verify the tool is installed.
	if _, err := exec.LookPath(tool); err != nil {
		return nil, fmt.Errorf("download: %s not found in PATH (install with: %s)",
			tool, installHint(tool))
	}

	stdout := req.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderr := req.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	cmd := exec.CommandContext(ctx, tool, argv[1:]...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	start := time.Now()
	err := cmd.Run()
	took := time.Since(start)
	if err != nil {
		// Capture stderr for the result if the caller used a
		// custom writer.
		if w, ok := stderr.(*strings.Builder); ok {
			return &Result{Tool: tool, Argv: argv, Took: took, Stderr: w.String()}, err
		}
		return &Result{Tool: tool, Argv: argv, Took: took}, err
	}

	// Best-effort size: stat the output path.
	var bytes int64
	if req.OutputPath != "" {
		if info, err := os.Stat(req.OutputPath); err == nil {
			bytes = info.Size()
		}
	}
	return &Result{Tool: tool, Argv: argv, Bytes: bytes, Took: took}, nil
}

// buildArgv returns (tool, full-argv-including-tool). Callers can
// override the tool via Request.Tool.
func buildArgv(req Request) (string, []string) {
	switch req.Kind {
	case "hls_master", "hls_variant":
		return buildYTDLP(req, true)
	case "dash":
		return buildYTDLP(req, true)
	case "direct":
		return buildYTDLP(req, false)
	case "segment_list":
		return buildAria2(req)
	default:
		// Unknown kind — fall back to yt-dlp.
		return buildYTDLP(req, false)
	}
}

func buildYTDLP(req Request, concurrent bool) (string, []string) {
	argv := []string{"yt-dlp", "--no-warnings", "--no-progress"}
	if req.OutputPath != "" {
		argv = append(argv, "-o", req.OutputPath)
	}
	// Headers.
	for k, v := range req.Headers {
		// Skip default headers that yt-dlp sets itself
		// (User-Agent, Accept) to avoid the warning.
		if isYTDLPDefaultHeader(k) {
			continue
		}
		argv = append(argv, "--add-header", k+":"+v)
	}
	if concurrent {
		argv = append(argv, "--concurrent-fragments", fmt.Sprintf("%d", req.Concurrent))
	}
	argv = append(argv, req.ExtraArgs...)
	argv = append(argv, req.StreamURL)
	return "yt-dlp", argv
}

func buildAria2(req Request) (string, []string) {
	argv := []string{"aria2c", "--console-log-level=warn"}
	if req.OutputPath != "" {
		argv = append(argv, "-o", req.OutputPath)
	}
	for k, v := range req.Headers {
		if isYTDLPDefaultHeader(k) {
			continue
		}
		argv = append(argv, "--header", k+": "+v)
	}
	argv = append(argv, req.ExtraArgs...)
	argv = append(argv, req.StreamURL)
	return "aria2c", argv
}

// isYTDLPDefaultHeader returns true for headers yt-dlp sets
// itself, so we don't double-set them (and trigger a warning).
func isYTDLPDefaultHeader(name string) bool {
	switch strings.ToLower(name) {
	case "user-agent", "accept", "accept-language":
		return true
	}
	return false
}

func installHint(tool string) string {
	switch tool {
	case "yt-dlp":
		return "pip install yt-dlp  OR  brew install yt-dlp  OR  https://github.com/yt-dlp/yt-dlp#installation"
	case "aria2c":
		return "brew install aria2  OR  apt install aria2"
	}
	return "(check your package manager)"
}

// Argv returns the argv that Run would use, without actually
// running it. Useful for the dry-run mode and for tests.
func Argv(req Request) []string {
	if req.Concurrent == 0 {
		req.Concurrent = 16
	}
	_, argv := buildArgv(req)
	if req.Tool != "" {
		argv = append([]string{req.Tool}, argv[1:]...)
	}
	return argv
}
