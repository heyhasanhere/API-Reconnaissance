package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDo_GET(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom", "yes")
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	resp, err := Do(context.Background(), Request{URL: srv.URL + "/foo"})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Status != 200 {
		t.Errorf("Status = %d, want 200", resp.Status)
	}
	if string(resp.Body) != `{"ok":true}` {
		t.Errorf("Body = %q, want %q", string(resp.Body), `{"ok":true}`)
	}
	if resp.Headers.Get("X-Custom") != "yes" {
		t.Errorf("missing X-Custom header")
	}
}

func TestDo_Headers(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Sent")
	}))
	defer srv.Close()

	_, err := Do(context.Background(), Request{
		URL:     srv.URL,
		Headers: map[string]string{"X-Sent": "value"},
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if got != "value" {
		t.Errorf("X-Sent = %q, want %q", got, "value")
	}
}

func TestDo_Truncation(t *testing.T) {
	// Server sends 2 MiB; probe should cap at 1 MiB and set Truncated.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		big := strings.Repeat("x", 2<<20)
		w.Write([]byte(big))
	}))
	defer srv.Close()

	resp, err := Do(context.Background(), Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !resp.BodyTruncated {
		t.Errorf("expected BodyTruncated = true")
	}
	if len(resp.Body) != MaxBodyBytes {
		t.Errorf("len(Body) = %d, want %d", len(resp.Body), MaxBodyBytes)
	}
}

func TestDo_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	resp, err := Do(context.Background(), Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Status != 404 {
		t.Errorf("Status = %d, want 404", resp.Status)
	}
}

func TestDo_EmptyURL(t *testing.T) {
	_, err := Do(context.Background(), Request{URL: ""})
	if err == nil {
		t.Error("expected error for empty URL")
	}
}

func TestDo_Redirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", 302)
			return
		}
		w.WriteHeader(200)
		w.Write([]byte("arrived"))
	}))
	defer srv.Close()

	resp, err := Do(context.Background(), Request{URL: srv.URL + "/old"})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if string(resp.Body) != "arrived" {
		t.Errorf("Body = %q, want %q", string(resp.Body), "arrived")
	}
	if !strings.HasSuffix(resp.FinalURL, "/new") {
		t.Errorf("FinalURL = %q, want suffix /new", resp.FinalURL)
	}
}
