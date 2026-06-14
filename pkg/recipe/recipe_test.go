package recipe

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	r := New("anikage.cc")
	if r.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", r.SchemaVersion, CurrentSchemaVersion)
	}
	if r.Domain != "anikage.cc" {
		t.Errorf("Domain = %q, want anikage.cc", r.Domain)
	}
	if r.Endpoints == nil {
		t.Error("Endpoints should be initialized to empty map")
	}
	if r.Discovered.IsZero() {
		t.Error("Discovered should be set")
	}
	if r.Updated.IsZero() {
		t.Error("Updated should be set")
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Recipe)
		wantErr string
	}{
		{"good", func(r *Recipe) {
			r.Endpoints["episodes"] = Endpoint{
				URL: "https://example.com/api/episodes", Method: "GET", Shape: "json_list",
			}
		}, ""},
		{"nil recipe", func(r *Recipe) { _ = r }, ""}, // we'll override
		{"missing domain", func(r *Recipe) { r.Domain = "" }, "domain is empty"},
		{"bad schema version", func(r *Recipe) { r.SchemaVersion = 99 }, "schema_version 99 is newer"},
		{"endpoint empty URL", func(r *Recipe) {
			r.Endpoints["x"] = Endpoint{URL: "", Method: "GET", Shape: "json"}
		}, "endpoint \"x\" has empty URL"},
		{"endpoint empty method", func(r *Recipe) {
			r.Endpoints["x"] = Endpoint{URL: "https://e.com", Method: "", Shape: "json"}
		}, "endpoint \"x\" has empty method"},
		{"endpoint bad URL", func(r *Recipe) {
			r.Endpoints["x"] = Endpoint{URL: "ht tp://bad", Method: "GET", Shape: "json"}
		}, "does not parse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r *Recipe
			if tc.name == "nil recipe" {
				err := error((func() error { r = nil; return r.Validate() })())
				if err == nil {
					t.Error("nil recipe should error")
				}
				return
			}
			r = New("example.com")
			tc.mutate(r)
			err := r.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestPlaceholders(t *testing.T) {
	cases := []struct {
		url  string
		want []string
	}{
		{"https://example.com/api/foo", nil},
		{"https://example.com/anime/{slug}", []string{"slug"}},
		{"https://example.com/anime/{slug}/episodes/{n}", []string{"slug", "n"}},
		{"https://example.com/anime/{slug}/episodes/{slug}", []string{"slug"}}, // dedup
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			e := &Endpoint{URL: tc.url, Method: "GET"}
			got := e.Placeholders()
			if len(got) != len(tc.want) {
				t.Fatalf("Placeholders() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("Placeholders()[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestFill(t *testing.T) {
	e := &Endpoint{
		URL:    "https://anikage.cc/api/media/anime/{slug}/episodes/{n}/sources",
		Method: "GET",
	}

	t.Run("ok", func(t *testing.T) {
		got, err := e.Fill(map[string]string{"slug": "zMLNvt6MtV", "n": "1"})
		if err != nil {
			t.Fatal(err)
		}
		want := "https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/1/sources"
		if got != want {
			t.Errorf("Fill = %q, want %q", got, want)
		}
	})

	t.Run("missing", func(t *testing.T) {
		_, err := e.Fill(map[string]string{"slug": "zMLNvt6MtV"})
		if err == nil {
			t.Fatal("expected error for missing placeholder")
		}
		if !strings.Contains(err.Error(), "{n}") {
			t.Errorf("error should mention {n}: %v", err)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		_, err := e.Fill(map[string]string{"slug": "z", "n": "1", "extra": "x"})
		if err == nil {
			t.Fatal("expected error for unknown placeholder")
		}
		if !strings.Contains(err.Error(), "{extra}") {
			t.Errorf("error should mention {extra}: %v", err)
		}
	})

	t.Run("value with special chars gets escaped", func(t *testing.T) {
		got, err := e.Fill(map[string]string{"slug": "a b/c", "n": "1"})
		if err != nil {
			t.Fatal(err)
		}
		// url.PathEscape encodes space as %20 and / as %2F.
		// The slug segment in the output should contain neither raw
		// space nor raw /.
		want := "https://anikage.cc/api/media/anime/a%20b%2Fc/episodes/1/sources"
		if got != want {
			t.Errorf("Fill = %q, want %q", got, want)
		}
	})
}

func TestRoundtrip(t *testing.T) {
	r := New("anikage.cc")
	r.Endpoints["episodes"] = Endpoint{
		URL:    "https://anikage.cc/api/media/anime/{slug}/episodes",
		Method: "GET",
		Params: []string{"page"},
		Shape:  "json_list",
	}
	r.Endpoints["sources"] = Endpoint{
		URL:    "https://anikage.cc/api/media/anime/{slug}/episodes/{n}/sources",
		Method: "GET",
		Params: []string{"provider", "lang"},
		Shape:  "stream_key",
	}
	r.Auth.RequiredHeaders = map[string]string{
		"Origin":  "https://anikage.cc",
		"Referer": "https://anikage.cc/",
	}
	r.CDN = &CDN{Host: "prox.anikage.cc", InheritsAuth: true}
	r.Download = &Download{Shape: "hls", Tool: "yt-dlp", Flags: []string{"--concurrent-fragments", "16"}}

	data, err := r.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	// Pretty-printed — should contain newlines and indentation.
	if !strings.Contains(string(data), "\n  ") {
		t.Errorf("expected pretty-printed JSON, got %q", data)
	}

	loaded, err := Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Domain != r.Domain {
		t.Errorf("Domain = %q, want %q", loaded.Domain, r.Domain)
	}
	if len(loaded.Endpoints) != 2 {
		t.Errorf("Endpoints count = %d, want 2", len(loaded.Endpoints))
	}
	if loaded.Endpoints["episodes"].URL != r.Endpoints["episodes"].URL {
		t.Errorf("episodes URL = %q, want %q", loaded.Endpoints["episodes"].URL, r.Endpoints["episodes"].URL)
	}
	if loaded.CDN == nil || loaded.CDN.Host != "prox.anikage.cc" {
		t.Errorf("CDN roundtrip lost: %+v", loaded.CDN)
	}
	if loaded.Download == nil || loaded.Download.Tool != "yt-dlp" {
		t.Errorf("Download roundtrip lost: %+v", loaded.Download)
	}
}

func TestUnmarshalStampsSchemaVersion(t *testing.T) {
	// Hand-written recipe without schema_version.
	data := []byte(`{"domain":"x.com","endpoints":{"e":{"url":"https://x.com/e","method":"GET","shape":"json"}}}`)
	r, err := Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if r.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d (should stamp)", r.SchemaVersion, CurrentSchemaVersion)
	}
}

func TestUnmarshalInitializesEndpoints(t *testing.T) {
	// Hand-written recipe without endpoints key.
	data := []byte(`{"schema_version":1,"domain":"x.com"}`)
	r, err := Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if r.Endpoints == nil {
		t.Error("Endpoints should be initialized")
	}
}

func TestMarshalIsJSON(t *testing.T) {
	r := New("example.com")
	r.Endpoints["e"] = Endpoint{URL: "https://e.com/e", Method: "GET", Shape: "json"}
	data, err := r.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var anyMap map[string]any
	if err := json.Unmarshal(data, &anyMap); err != nil {
		t.Fatalf("Marshal output is not valid JSON: %v\n%s", err, data)
	}
	if anyMap["domain"] != "example.com" {
		t.Errorf("domain in output = %v, want example.com", anyMap["domain"])
	}
}
