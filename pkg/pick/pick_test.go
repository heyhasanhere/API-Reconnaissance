package pick

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestParseRange(t *testing.T) {
	cases := map[string][]int{
		"1":         {0},
		"1,3":       {0, 2},
		"1-3":       {0, 1, 2},
		"1-3,5,7-9": {0, 1, 2, 4, 6, 7, 8},
		"  2 , 4 ": {1, 3},
	}
	for in, want := range cases {
		got, err := parseRange(in, 10)
		if err != nil {
			t.Errorf("parseRange(%q): %v", in, err)
			continue
		}
		if !equalInts(got, want) {
			t.Errorf("parseRange(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseRange_Errors(t *testing.T) {
	bad := []string{"0", "11", "abc", "5-2", "1-x"}
	for _, s := range bad {
		if _, err := parseRange(s, 10); err == nil {
			t.Errorf("parseRange(%q) returned no error", s)
		}
	}
}

func TestMultiSelect_NonTTYDefaults(t *testing.T) {
	// Pipe input — not a TTY. Should use defaults.
	in := strings.NewReader("")
	out := &bytes.Buffer{}
	items := []Item{
		{Label: "a"},
		{Label: "b", Default: true},
		{Label: "c", Default: true},
	}
	got, err := MultiSelect(context.Background(), in, out, "Pick:", items)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// Default is "the items marked Default" = [b, c] = [1, 2].
	if !equalInts(got, []int{1, 2}) {
		t.Errorf("got %v, want [1 2]", got)
	}
}

func TestMultiSelect_NonTTYNoDefault(t *testing.T) {
	in := strings.NewReader("")
	out := &bytes.Buffer{}
	items := []Item{
		{Label: "a"},
		{Label: "b"},
		{Label: "c"},
	}
	got, _ := MultiSelect(context.Background(), in, out, "Pick:", items)
	// No defaults marked → all.
	if !equalInts(got, []int{0, 1, 2}) {
		t.Errorf("got %v, want [0 1 2]", got)
	}
}

func TestMultiSelect_TTYInput(t *testing.T) {
	in := strings.NewReader("1,3\n")
	out := &bytes.Buffer{}
	items := []Item{
		{Label: "a"}, {Label: "b"}, {Label: "c"},
	}
	got, err := multiSelect(context.Background(), in, out, "Pick:", items, true)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !equalInts(got, []int{0, 2}) {
		t.Errorf("got %v, want [0 2]", got)
	}
	if !strings.Contains(out.String(), "Pick:") {
		t.Error("title not printed")
	}
}

func TestMultiSelect_AllShorthand(t *testing.T) {
	in := strings.NewReader("a\n")
	out := &bytes.Buffer{}
	items := []Item{{Label: "a"}, {Label: "b"}, {Label: "c"}}
	got, _ := multiSelect(context.Background(), in, out, "Pick:", items, true)
	if !equalInts(got, []int{0, 1, 2}) {
		t.Errorf("got %v, want all", got)
	}
}

func TestMultiSelect_EmptyInputUsesDefaults(t *testing.T) {
	in := strings.NewReader("\n")
	out := &bytes.Buffer{}
	items := []Item{{Label: "a"}, {Label: "b", Default: true}}
	got, _ := multiSelect(context.Background(), in, out, "Pick:", items, true)
	if !equalInts(got, []int{1}) {
		t.Errorf("got %v, want [1]", got)
	}
}

func TestSingleSelect_NonTTY(t *testing.T) {
	in := strings.NewReader("")
	out := &bytes.Buffer{}
	items := []Item{{Label: "a"}, {Label: "b", Default: true}}
	got, _ := SingleSelect(context.Background(), in, out, "Pick:", items)
	if got != 1 {
		t.Errorf("got %d, want 1 (default)", got)
	}
}

func TestSingleSelect_TTY(t *testing.T) {
	in := strings.NewReader("2\n")
	out := &bytes.Buffer{}
	items := []Item{{Label: "a"}, {Label: "b"}}
	got, _ := singleSelect(context.Background(), in, out, "Pick:", items, true)
	if got != 1 {
		t.Errorf("got %d, want 1", got)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
