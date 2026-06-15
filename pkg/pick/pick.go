// Package pick handles interactive user selection at the end of
// discovery. The chain engine finishes with a list of episodes
// and a list of providers; the user picks which to download.
//
// This is *not* a REPL. It's a single read of one line per
// question. The tool exits after the user has answered both
// questions and the downloads have been kicked off.
//
// In non-interactive mode (stdin is not a TTY), the defaults
// kick in: "all episodes, first working provider" for the
// anikage case, "single item, first item" for everything else.
package pick

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Item is one entry in a selection list. Default marks the
// non-TTY default.
type Item struct {
	Label       string
	Description string
	Default     bool
}

// MultiSelect asks the user to choose one or more items. Returns
// the selected indices (0-based, in the same order as items).
//
// In non-TTY mode, returns the default (all items, or just the
// default ones if any are marked).
func MultiSelect(ctx context.Context, in io.Reader, out io.Writer, title string, items []Item) ([]int, error) {
	return multiSelect(ctx, in, out, title, items, defaultIsTerminal(in))
}

// multiSelect is the testable form. forceTTY lets tests bypass
// the TTY check.
func multiSelect(ctx context.Context, in io.Reader, out io.Writer, title string, items []Item, isTTY bool) ([]int, error) {
	if len(items) == 0 {
		return nil, nil
	}

	// Non-TTY: use defaults.
	if !isTTY {
		return defaultIndicesMulti(items), nil
	}

	fmt.Fprintln(out, title)
	for i, it := range items {
		marker := "  "
		if it.Default {
			marker = " *"
		}
		desc := ""
		if it.Description != "" {
			desc = "  " + it.Description
		}
		fmt.Fprintf(out, "  %s%d. %s%s\n", marker, i+1, it.Label, desc)
	}
	fmt.Fprintln(out)
	fmt.Fprint(out, "Select (e.g. '1-3,5,7-9', 'a' for all, Enter for default): ")

	line, err := readLine(ctx, in)
	if err != nil {
		return nil, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultIndicesMulti(items), nil
	}
	if line == "a" || line == "all" {
		outIndices := make([]int, len(items))
		for i := range items {
			outIndices[i] = i
		}
		return outIndices, nil
	}
	return parseRange(line, len(items))
}

// SingleSelect asks the user to choose one item. Returns the
// selected index. In non-TTY mode, returns the first default
// item, or 0.
func SingleSelect(ctx context.Context, in io.Reader, out io.Writer, title string, items []Item) (int, error) {
	return singleSelect(ctx, in, out, title, items, defaultIsTerminal(in))
}

func singleSelect(ctx context.Context, in io.Reader, out io.Writer, title string, items []Item, isTTY bool) (int, error) {
	if len(items) == 0 {
		return 0, fmt.Errorf("pick: no items to choose from")
	}
	if !isTTY {
		for i, it := range items {
			if it.Default {
				return i, nil
			}
		}
		return 0, nil
	}

	fmt.Fprintln(out, title)
	for i, it := range items {
		marker := "  "
		if it.Default {
			marker = " *"
		}
		desc := ""
		if it.Description != "" {
			desc = "  " + it.Description
		}
		fmt.Fprintf(out, "  %s%d. %s%s\n", marker, i+1, it.Label, desc)
	}
	fmt.Fprintln(out)
	fmt.Fprint(out, "Select (number, Enter for default): ")

	line, err := readLine(ctx, in)
	if err != nil {
		return 0, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultIndexSingle(items), nil
	}
	indices, err := parseRange(line, len(items))
	if err != nil {
		return 0, err
	}
	if len(indices) == 0 {
		return defaultIndexSingle(items), nil
	}
	return indices[0], nil
}

// parseRange parses "1-3,5,7-9" into a sorted, deduplicated slice
// of 0-based indices. Out-of-range entries are silently dropped.
func parseRange(s string, n int) ([]int, error) {
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
				return nil, fmt.Errorf("pick: bad range %q", part)
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
				return nil, fmt.Errorf("pick: bad number %q", part)
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

func defaultIndicesMulti(items []Item) []int {
	// If any items are marked Default, return those; else all.
	hasDefault := false
	for _, it := range items {
		if it.Default {
			hasDefault = true
			break
		}
	}
	out := make([]int, 0, len(items))
	if hasDefault {
		for i, it := range items {
			if it.Default {
				out = append(out, i)
			}
		}
	} else {
		for i := range items {
			out = append(out, i)
		}
	}
	return out
}

func defaultIndexSingle(items []Item) int {
	for i, it := range items {
		if it.Default {
			return i
		}
	}
	return 0
}

// defaultIsTerminal reports whether f looks like a TTY. The check
// is intentionally simple: if f is os.Stdin and os.Stdin is a
// character device (the TTY), return true; otherwise false.
//
// We don't import golang.org/x/term to keep the runtime dep
// surface small. The check is correct for the common cases
// (interactive shell, piped input, redirected input).
func defaultIsTerminal(f io.Reader) bool {
	if f == nil {
		return false
	}
	if f == os.Stdin {
		info, err := os.Stdin.Stat()
		if err != nil {
			return false
		}
		return (info.Mode() & os.ModeCharDevice) != 0
	}
	return false
}

// readLine reads one line from in, respecting ctx.
func readLine(ctx context.Context, in io.Reader) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		br := bufio.NewReader(in)
		line, err := br.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		return r.line, r.err
	}
}
