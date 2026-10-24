package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestStreamingToolFragmentsAndUsage(t *testing.T) {
	stream := `data: {"choices":[{"delta":{"content":"working ","tool_calls":[{"index":0,"id":"call_1","function":{"name":"container_","arguments":"{\"id\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"exec","arguments":"\"one\"}"}}]},"finish_reason":"tool_calls"}]}

data: {"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}

data: [DONE]

`
	var deltas []Delta
	out, err := consumeStream(strings.NewReader(stream), func(d Delta) error { deltas = append(deltas, d); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 2 || out.Usage.TotalTokens != 10 || len(out.Message.ToolCalls) != 1 || out.Message.ToolCalls[0].Name != "container_exec" || string(out.Message.ToolCalls[0].Arguments) != `{"id":"one"}` {
		t.Fatalf("unexpected assembled result: %+v", out)
	}
	if _, err = consumeStream(strings.NewReader(`data: {"choices":[{"delta":{"content":"partial"}}]}`), nil); err == nil {
		t.Fatal("truncated stream accepted")
	}
}
func TestConfigReplacementIsAtomicAndSelectionExplicit(t *testing.T) {
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	initial := Config{ActiveModelID: "first", Models: []Profile{{ID: "first", Name: "First", BaseURL: "http://localhost:1234/v1", Model: "local", APIKey: "secret"}, {ID: "second", Name: "Second", BaseURL: "https://example.com", Model: "other"}}}
	if err = s.replace(initial); err != nil {
		t.Fatal(err)
	}
	invalid := Config{ActiveModelID: "missing", Models: []Profile{{ID: "second", Name: "Changed", BaseURL: "https://example.com", Model: "other"}}}
	if err = s.replace(invalid); err == nil {
		t.Fatal("invalid config accepted")
	}
	profile, err := s.profile(Request{Model: "first", ModelRevision: s.public().Revision}, false)
	if err != nil || profile.ID != "first" || profile.APIKey != "secret" {
		t.Fatalf("active config changed: %+v %v", profile, err)
	}
	if _, err = s.profile(Request{Model: "unknown", ModelRevision: s.public().Revision}, false); err == nil {
		t.Fatal("unknown model silently switched")
	}
	public, _ := json.Marshal(s.public())
	if strings.Contains(string(public), "secret") {
		t.Fatal("public config exposes key")
	}
	if completionURL(profile.BaseURL) != "http://localhost:1234/v1/chat/completions" {
		t.Fatal("upstream URL rewritten")
	}
}
func TestRealUpstreamNoKeyNoFallback(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/chat/completions" {
			t.Error(r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "selected-model" {
			t.Error("wrong model")
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"real upstream"}}],"usage":{"total_tokens":2}}`)
	}))
	defer srv.Close()
	out, err := upstream(context.Background(), Profile{BaseURL: srv.URL, Model: "selected-model"}, Request{Messages: []Message{{Role: "user", Content: "hello"}}}, nil)
	if err != nil || out.Message.Content != "real upstream" || calls.Load() != 1 {
		t.Fatalf("real call failed: %+v %v", out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = upstream(ctx, Profile{BaseURL: srv.URL, Model: "selected-model"}, Request{Messages: []Message{{Role: "user", Content: "hello"}}}, nil); err == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestStrictJSON(t *testing.T) {
	for _, body := range []string{`{"models":[],"unexpected":true}`, `{} {}`} {
		var value Config
		if decode(strings.NewReader(body), &value) == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestRevisionPersistsAndReplacementIgnoresCallerRevision(t *testing.T) {
	dir := t.TempDir()
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	initial := s.public().Revision
	if initial == "" {
		t.Fatal("missing initial revision")
	}
	reopened, err := openStore(dir)
	if err != nil || reopened.public().Revision != initial {
		t.Fatalf("revision not persisted: %v", err)
	}
	if err := s.replace(Config{Revision: initial, Models: []Profile{}}); err != nil {
		t.Fatal(err)
	}
	replacement := s.public().Revision
	if replacement == "" || replacement == initial {
		t.Fatal("replacement trusted caller revision")
	}
	reopened, err = openStore(dir)
	if err != nil || reopened.public().Revision != replacement {
		t.Fatalf("replacement revision not persisted: %v", err)
	}
}

func TestInferenceNeverReplaysHTTP429(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	_, err := upstream(context.Background(), Profile{BaseURL: server.URL, Model: "paid"}, Request{Messages: []Message{{Role: "user", Content: "once"}}}, nil)
	if err == nil || calls.Load() != 1 {
		t.Fatalf("inference replayed: calls=%d error=%v", calls.Load(), err)
	}
}
