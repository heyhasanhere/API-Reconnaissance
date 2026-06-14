package action

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestHelp_RendersNameAndSummary(t *testing.T) {
	a := Action{
		Name:        "probe",
		Summary:     "Send a single HTTP request",
		Description: "Probe sends an HTTP request and shows the response.",
		Examples:    []string{"api-recon probe https://example.com"},
	}
	got := Help(a)
	if !strings.Contains(got, "probe") {
		t.Error("Help should contain name")
	}
	if !strings.Contains(got, "Send a single HTTP request") {
		t.Error("Help should contain summary")
	}
	if !strings.Contains(got, "Probe sends an HTTP request") {
		t.Error("Help should contain description")
	}
	if !strings.Contains(got, "api-recon probe https://example.com") {
		t.Error("Help should contain examples")
	}
}

func TestHelp_RendersFlags(t *testing.T) {
	a := Action{
		Name: "probe",
		Flags: []Flag{
			{Name: "method", Type: "string", Default: "GET", Description: "HTTP method", LookFor: "the response status code"},
			{Name: "timeout", Type: "duration", Default: "30s", Description: "request timeout"},
		},
	}
	got := Help(a)
	if !strings.Contains(got, "-method") {
		t.Error("Help should contain flag name")
	}
	if !strings.Contains(got, "string") {
		t.Error("Help should contain flag type")
	}
	if !strings.Contains(got, "default GET") {
		t.Error("Help should contain flag default")
	}
	if !strings.Contains(got, "Look for: the response status code") {
		t.Error("Help should contain LookFor annotation")
	}
	if !strings.Contains(got, "request timeout") {
		t.Error("Help should contain flag description")
	}
}

func TestHelp_RendersAliases(t *testing.T) {
	a := Action{
		Name:    "probe",
		Aliases: []string{"p", "P"},
		Summary: "Probe",
	}
	got := Help(a)
	if !strings.Contains(got, "aliases: p, P") {
		t.Errorf("Help should list aliases, got %q", got)
	}
}

func TestOneLineHelp(t *testing.T) {
	a := Action{Name: "probe", Summary: "Send a request"}
	got := OneLineHelp(a)
	want := "probe — Send a request"
	if got != want {
		t.Errorf("OneLineHelp = %q, want %q", got, want)
	}
	// Action with no summary should just return the name.
	a2 := Action{Name: "run"}
	if got := OneLineHelp(a2); got != "run" {
		t.Errorf("OneLineHelp(no summary) = %q, want run", got)
	}
}

func TestAction_Run(t *testing.T) {
	called := false
	a := Action{
		Name: "test",
		Run: func(ctx context.Context, c *Context, args []string) (*Result, error) {
			called = true
			return &Result{Summary: "ok", Tags: []string{"test:ok"}}, nil
		},
	}
	res, err := a.Run(context.Background(), &Context{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("Run was not called")
	}
	if res.Summary != "ok" {
		t.Errorf("Summary = %q, want ok", res.Summary)
	}
	if len(res.Tags) != 1 || res.Tags[0] != "test:ok" {
		t.Errorf("Tags = %v", res.Tags)
	}
}

func TestAction_RunError(t *testing.T) {
	want := errors.New("boom")
	a := Action{
		Name: "test",
		Run: func(ctx context.Context, c *Context, args []string) (*Result, error) {
			return nil, want
		},
	}
	_, err := a.Run(context.Background(), &Context{}, nil)
	if err != want {
		t.Errorf("err = %v, want %v", err, want)
	}
}
