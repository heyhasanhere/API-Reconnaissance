package capture

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/falcon/api-recon/pkg/creds"
	"github.com/falcon/api-recon/pkg/graph"
)

func TestParseEvent_Req(t *testing.T) {
	line := []byte(`{"v":1,"dir":"req","ts":123,"method":"GET","url":"https://x/y","headers":{"Authorization":"Bearer abc"},"body":""}`)
	ev, err := parseEvent(line)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Dir != "req" {
		t.Errorf("Dir = %q", ev.Dir)
	}
	if ev.Method != "GET" {
		t.Errorf("Method = %q", ev.Method)
	}
	if ev.URL != "https://x/y" {
		t.Errorf("URL = %q", ev.URL)
	}
	if ev.Headers["Authorization"] != "Bearer abc" {
		t.Errorf("Authorization = %q", ev.Headers["Authorization"])
	}
}

func TestParseEvent_RespWithBody(t *testing.T) {
	body := base64.StdEncoding.EncodeToString([]byte(`{"data":[]}`))
	line := []byte(`{"v":1,"dir":"resp","ts":1,"status":200,"url":"https://x/y","headers":{"Content-Type":"application/json"},"body":"` + body + `"}`)
	ev, err := parseEvent(line)
	if err != nil {
		t.Fatal(err)
	}
	if string(ev.Body) != `{"data":[]}` {
		t.Errorf("Body = %q", ev.Body)
	}
	if ev.Status != 200 {
		t.Errorf("Status = %d", ev.Status)
	}
}

func TestParseEvent_Error(t *testing.T) {
	line := []byte(`{"v":1,"dir":"error","ts":1,"message":"oops"}`)
	ev, err := parseEvent(line)
	if err != nil {
		t.Fatal(err)
	}
	if ev.URL != "oops" {
		t.Errorf("error message should be in URL field, got %q", ev.URL)
	}
}

func TestToHeaders(t *testing.T) {
	h := ToHeaders(map[string]string{
		"Authorization": "Bearer x",
		"Origin":        "https://x",
	})
	if h.Get("Authorization") != "Bearer x" {
		t.Errorf("Authorization = %q", h.Get("Authorization"))
	}
	if h.Get("Origin") != "https://x" {
		t.Errorf("Origin = %q", h.Get("Origin"))
	}
}

func TestToCredsAndGraph(t *testing.T) {
	events := make(chan Event, 4)
	events <- Event{Dir: "req", URL: "https://x/api", Method: "GET", Headers: map[string]string{"Authorization": "Bearer t"}}
	events <- Event{Dir: "resp", URL: "https://x/api", Status: 200, Body: []byte(`[{"id":1}]`)}
	close(events)

	cs := creds.New()
	g := graph.New()
	obs := ToCredsAndGraph(events, cs, g)

	if len(obs) != 1 {
		t.Errorf("expected 1 observation, got %d", len(obs))
	}
	if cs.Bearer != "t" {
		t.Errorf("Bearer = %q", cs.Bearer)
	}
	if g.Paths()[0] != "/api" {
		t.Errorf("graph paths = %v", g.Paths())
	}
}

func TestEventToString(t *testing.T) {
	ev := Event{Dir: "resp", Status: 200, URL: "https://x"}
	got := ev.ToString()
	if !strings.Contains(got, "[resp]") || !strings.Contains(got, "200") || !strings.Contains(got, "https://x") {
		t.Errorf("ToString = %q", got)
	}
}

// ensure http import isn't dropped.
var _ = http.MethodGet
