package pickjson

import (
	"reflect"
	"testing"
)

func TestParseProviderList(t *testing.T) {
	body := []byte(`[{"id":"megg","default":true},{"id":"kiss","default":false}]`)
	got := ParseProviderList(body)
	want := []string{"megg", "kiss"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseProviderList_BadJSON(t *testing.T) {
	got := ParseProviderList([]byte(`not json`))
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestWithQueryParam(t *testing.T) {
	got, err := WithQueryParam("https://x.com/sources?lang=sub", "provider", "miko")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://x.com/sources?lang=sub&provider=miko"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWithQueryParam_Replaces(t *testing.T) {
	got, _ := WithQueryParam("https://x.com/sources?provider=kiss", "provider", "miko")
	want := "https://x.com/sources?provider=miko"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWithQueryParams_Multiple(t *testing.T) {
	got, _ := WithQueryParams("https://x.com/sources", map[string]string{
		"provider": "miko",
		"lang":     "sub",
	})
	// We don't assert the exact order (Go's url.Values sorts).
	if got != "https://x.com/sources?lang=sub&provider=miko" {
		t.Errorf("got %q", got)
	}
}
