package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhaleTrue/WhaleBot/internal/llm"
	"github.com/WhaleTrue/WhaleBot/internal/providers"
	"github.com/WhaleTrue/WhaleBot/pkg/mesh"
)

type liveFixture struct {
	t     *testing.T
	keys  map[string]ed25519.PrivateKey
	trust map[string]mesh.TrustGrant
	seeds []string
}

func liveNetwork(t *testing.T, names ...string) *liveFixture {
	t.Helper()
	t.Setenv("LLM_MOCK", "0")
	t.Setenv("PROVIDER_CAPABILITIES", "")
	f := &liveFixture{t: t, keys: map[string]ed25519.PrivateKey{}, trust: map[string]mesh.TrustGrant{}}
	for _, name := range names {
		public, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		f.keys[name] = key
		f.trust[name] = mesh.TrustGrant{PublicKey: base64.StdEncoding.EncodeToString(public), Namespace: "integration", Scopes: []string{"*"}, Provides: []string{"*"}}
	}
	return f
}
func (f *liveFixture) node(name string, requirements []mesh.Requirement, register func(*mesh.Node, string) ([]mesh.Capability, error)) *mesh.Node {
	f.t.Helper()
	directory := f.t.TempDir()
	node, err := mesh.New(mesh.Config{Manifest: mesh.Manifest{Identity: mesh.Identity{Namespace: "integration", Name: name, Principal: name}, Requires: requirements}, PrivateKey: f.keys[name], Trust: f.trust, Seeds: f.seeds, Listen: "127.0.0.1:0", DataDir: directory, AllowPlaintextSensitive: true})
	if err != nil {
		f.t.Fatal(err)
	}
	if register != nil {
		capabilities, err := register(node, directory)
		if err != nil {
			f.t.Fatal(err)
		}
		if err = node.UpdateCapabilities(capabilities); err != nil {
			f.t.Fatal(err)
		}
	}
	if err = node.Start(context.Background()); err != nil {
		f.t.Fatal(err)
	}
	if len(f.seeds) == 0 {
		f.seeds = node.Manifest().Transport.Advertise
	}
	f.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := node.Close(ctx); err != nil {
			f.t.Error(err)
		}
		if err := providers.Close(node); err != nil {
			f.t.Error(err)
		}
	})
	for _, seed := range f.seeds {
		if seed == node.Manifest().Transport.Advertise[0] {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_, err := node.Discover(ctx, seed)
		cancel()
		if err != nil {
			f.t.Fatalf("initial authenticated discovery: %v", err)
		}
	}
	return node
}
func liveBinding(t *testing.T, node *mesh.Node, capability string) mesh.Binding {
	t.Helper()
	limit := time.NewTimer(15 * time.Second)
	defer limit.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		bindings, err := node.Resolve(mesh.Requirement{Capability: capability})
		if err == nil && len(bindings) == 1 {
			return bindings[0]
		}
		select {
		case <-limit.C:
			t.Fatalf("%s did not become available: %v", capability, err)
		case <-ticker.C:
		}
	}
}
func configureModel(t *testing.T, base, id, cost string) func(*mesh.Node, string) ([]mesh.Capability, error) {
	return func(node *mesh.Node, dir string) ([]mesh.Capability, error) {
		config := llm.Config{ActiveModelID: id, Models: []llm.Profile{{ID: id, Name: id, Model: id, BaseURL: base, CostClass: cost, Capabilities: []string{"tools"}, TimeoutSeconds: 3}}}
		data, err := json.Marshal(config)
		if err != nil {
			return nil, err
		}
		if err = os.WriteFile(filepath.Join(dir, "llm-config.json"), data, 0600); err != nil {
			return nil, err
		}
		return llm.Register(node, dir)
	}
}
func TestReActUsesRealSessionRetrievalToolAndUsageCapabilities(t *testing.T) {
	var mu sync.Mutex
	var prompts []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		mu.Lock()
		prompts = append(prompts, input.Messages[0].Content)
		mu.Unlock()
		message := map[string]any{"role": "assistant"}
		last := input.Messages[len(input.Messages)-1]
		if last.Role == "tool" {
			message["content"] = "Observed " + last.Content
		} else {
			message["tool_calls"] = []any{map[string]any{"id": "read-proof", "type": "function", "function": map[string]string{"name": "read_local_file", "arguments": `{"path":"proof.txt"}`}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
	}))
	defer upstream.Close()
	f := liveNetwork(t, "store", "model", "agent", "tool", "caller")
	f.node("store", nil, providers.Register)
	f.node("model", nil, configureModel(t, upstream.URL, "local-free", "free"))
	var toolCalls atomic.Int64
	f.node("tool", nil, func(node *mesh.Node, dir string) ([]mesh.Capability, error) {
		if err := os.WriteFile(filepath.Join(dir, "proof.txt"), []byte("filesystem-proof"), 0600); err != nil {
			return nil, err
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { root.Close() })
		input := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)
		semantic := mesh.Semantics{ReadOnly: true, Idempotent: true}
		capability := mesh.Capability{Schema: mesh.CapabilitySchema, ID: "tool.execute", Interface: "test.file.read", Version: "1.0.0", Endpoint: "/read", Ready: true, Scopes: []string{"tool.read"}, InputSchema: input, OutputSchema: mesh.JSONSchemaFor(struct {
			Content string `json:"content"`
		}{}), Semantics: semantic, Operations: []mesh.Operation{{Name: "read", Method: "POST", Path: "/read", Scopes: []string{"tool.read"}, Semantics: semantic}}, Tool: &mesh.ToolExport{Name: "read_local_file", Description: "Read a confined file", InputSchema: input, Method: "POST", Path: "/read", Scopes: []string{"tool.read"}}}
		node.Handle("POST", "/read", []string{"tool.read"}, func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Path string `json:"path"`
			}
			if mesh.ReadJSON(w, r, &in) != nil {
				return
			}
			data, err := root.ReadFile(in.Path)
			if err != nil {
				mesh.WriteError(w, 400, "read_failed", err.Error())
				return
			}
			toolCalls.Add(1)
			mesh.WriteJSON(w, 200, map[string]string{"content": string(data)})
		})
		return []mesh.Capability{capability}, nil
	})
	runtimeNode := f.node("agent", Requirements(), Register)
	caller := f.node("caller", nil, nil)
	storage := liveBinding(t, caller, "memory.store")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	owner := "node:caller"
	if err := caller.Call(ctx, storage, "PUT", "/memory/put", providers.NoteInput{Scope: providers.Scope{Owner: owner}, Key: "hello", Value: "memory-proof"}, nil); err != nil {
		t.Fatal(err)
	}
	skills := liveBinding(t, caller, "skills.search")
	if err := caller.Call(ctx, skills, "PUT", "/skills/put", providers.SkillInput{Scope: providers.Scope{Owner: owner}, ID: "hello", Version: "1.0.0", Title: "hello", Files: []providers.File{{Path: "SKILL.md", Content: []byte("hello skill-proof")}}}, nil); err != nil {
		t.Fatal(err)
	}
	agent := liveBinding(t, caller, "agent.run")
	liveBinding(t, runtimeNode, "tool.execute")
	liveBinding(t, runtimeNode, "memory.search")
	liveBinding(t, runtimeNode, "skills.search")
	var output Response
	cancel()
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := caller.Call(ctx, agent, "POST", "/agent/run", Request{SessionID: "proof-session", Message: "hello retrieve proof"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.Reply, "filesystem-proof") || toolCalls.Load() != 1 {
		t.Fatalf("real tool observation missing: %+v calls=%d", output, toolCalls.Load())
	}
	if output.Usage.TotalTokens != 30 {
		t.Fatalf("usage not accumulated: %+v", output.Usage)
	}
	sessions := liveBinding(t, caller, "session.store")
	var session providers.Session
	if err := caller.Call(ctx, sessions, "POST", "/session/get", providers.SessionRequest{Scope: providers.Scope{Owner: owner}, SessionID: "proof-session"}, &session); err != nil {
		t.Fatal(err)
	}
	roles := []string{}
	for _, message := range session.Messages {
		roles = append(roles, message.Role)
	}
	if strings.Join(roles, ",") != "user,assistant,tool,assistant" || session.Metadata["state"] != "completed" {
		t.Fatalf("durable ReAct history incomplete: %+v", session)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 || !strings.Contains(prompts[0], "memory-proof") || !strings.Contains(prompts[0], "skill-proof") {
		t.Fatalf("retrieval not injected: %v", prompts)
	}
}
func TestModelSelectionNeverBroadensCostOrIgnoresExplicitModel(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"paid-result"}}]}`))
	}))
	defer upstream.Close()
	f := liveNetwork(t, "seed", "model-a", "model-b", "consumer")
	f.node("seed", nil, nil)
	f.node("model-a", nil, configureModel(t, upstream.URL, "paid-model", "paid"))
	f.node("model-b", nil, configureModel(t, upstream.URL, "paid-model", "paid"))
	consumer := f.node("consumer", nil, nil)
	liveBinding(t, consumer, "llm.invoke")
	until := time.Now().Add(3 * time.Second)
	for len(consumer.Peers()) < 3 && time.Now().Before(until) {
		time.Sleep(10 * time.Millisecond)
	}
	s := &runtime{node: consumer}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.modelFor(ctx, Request{}); err == nil {
		t.Fatal("unspecified request chose paid provider")
	}
	if _, err := s.modelFor(ctx, Request{Model: "missing"}); err == nil {
		t.Fatal("unavailable model silently changed")
	}
	if _, err := s.modelFor(ctx, Request{Model: "paid-model"}); err == nil {
		t.Fatal("ambiguous paid provider selected without preference")
	}
	binding, err := s.modelFor(ctx, Request{Model: "paid-model", Provider: "model-b"})
	if err != nil {
		t.Fatal(err)
	}
	if binding.Node.Identity.Principal != "model-b" {
		t.Fatal("explicit provider ignored")
	}
	if calls.Load() != 0 {
		t.Fatal("selection charged for inference")
	}
	output, err := s.invoke(ctx, binding, llm.Request{Messages: []llm.Message{{Role: "user", Content: "explicit request"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || output.Message.Content != "paid-result" {
		t.Fatal("explicit inference did not execute selected provider")
	}
}

func TestFrozenModelRevisionAndFreeOnlyGuardAtExecution(t *testing.T) {
	var firstCalls, changedCalls atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls.Add(1)
		var input struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Model != "original-upstream" {
			t.Errorf("wrong original model: %+v %v", input, err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"original"}}]}`))
	}))
	defer first.Close()
	changed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		changedCalls.Add(1)
		var input struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Model != "paid-upstream" {
			t.Errorf("wrong paid model: %+v %v", input, err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"paid"}}]}`))
	}))
	defer changed.Close()
	f := liveNetwork(t, "model", "consumer")
	f.node("model", nil, configureModel(t, first.URL, "original-upstream", "free"))
	consumer := f.node("consumer", nil, nil)
	liveBinding(t, consumer, "llm.invoke")
	catalogBinding := liveBinding(t, consumer, "llm.models.list")
	s := &runtime{node: consumer}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	selected, err := s.modelFor(ctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	message := llm.Request{Messages: []llm.Message{{Role: "user", Content: "hello"}}}
	out, err := s.invoke(ctx, selected, message, nil)
	if err != nil || out.Message.Content != "original" || !selected.FreeOnly || selected.Model != "original-upstream" {
		t.Fatalf("wrong initial selection: %+v %+v %v", selected, out, err)
	}
	next := llm.Config{Revision: selected.Revision, ActiveModelID: selected.Model, Models: []llm.Profile{{ID: selected.Model, Name: "Changed", BaseURL: changed.URL, Model: "paid-upstream", CostClass: "paid", Capabilities: []string{"streaming"}}}}
	var catalog llm.PublicConfig
	if err = consumer.Call(ctx, catalogBinding, "PUT", "/llm/config", next, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Revision == "" || catalog.Revision == selected.Revision {
		t.Fatal("replacement retained caller revision")
	}
	for _, stream := range []bool{false, true} {
		message.Stream = stream
		if _, err = s.invoke(ctx, selected, message, nil); err == nil {
			t.Fatalf("stale revision executed, stream=%v", stream)
		}
		guarded := selected
		guarded.Revision = catalog.Revision
		if _, err = s.invoke(ctx, guarded, message, nil); err == nil {
			t.Fatalf("free-only request executed paid profile, stream=%v", stream)
		}
	}
	if firstCalls.Load() != 1 || changedCalls.Load() != 0 {
		t.Fatal("rejected selection reached upstream")
	}
	paid, err := s.modelFor(ctx, Request{Provider: "model", Model: selected.Model, ModelRevision: catalog.Revision})
	if err != nil {
		t.Fatal(err)
	}
	message.Stream = false
	out, err = s.invoke(ctx, paid, message, nil)
	if err != nil || out.Message.Content != "paid" || changedCalls.Load() != 1 {
		t.Fatalf("explicit paid selection rejected: %+v %v", out, err)
	}
}

func TestRunStopsAtNextReActStepAfterConfigurationChange(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"inspect","type":"function","function":{"name":"inspect_state","arguments":"{}"}}]}}]}`))
	}))
	defer upstream.Close()
	f := liveNetwork(t, "store", "model", "agent", "caller", "tool")
	f.node("store", nil, providers.Register)
	f.node("model", nil, configureModel(t, upstream.URL, "free-model", "free"))
	consumer := f.node("caller", nil, nil)
	catalogBinding := liveBinding(t, consumer, "llm.models.list")
	var before llm.PublicConfig
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := consumer.Call(ctx, catalogBinding, "GET", "/llm/models", nil, &before); err != nil {
		t.Fatal(err)
	}
	f.node("tool", nil, func(node *mesh.Node, dir string) ([]mesh.Capability, error) {
		schema := json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
		semantics := mesh.Semantics{ReadOnly: true, Idempotent: true}
		node.Handle("POST", "/inspect", []string{"tool.read"}, func(w http.ResponseWriter, r *http.Request) {
			next := llm.Config{ActiveModelID: "paid-model", Models: []llm.Profile{{ID: "paid-model", Name: "Paid", BaseURL: upstream.URL, Model: "paid-upstream", CostClass: "paid", Capabilities: []string{"tools"}}}}
			if err := consumer.Call(r.Context(), catalogBinding, "PUT", "/llm/config", next, nil); err != nil {
				t.Error(err)
				http.Error(w, "configuration update failed", 500)
				return
			}
			mesh.WriteJSON(w, 200, map[string]string{"state": "inspected"})
		})
		return []mesh.Capability{{Schema: mesh.CapabilitySchema, ID: "tool.execute", Interface: "test.inspect", Version: "1.0.0", Endpoint: "/inspect", Ready: true, Scopes: []string{"tool.read"}, InputSchema: schema, OutputSchema: mesh.JSONSchemaFor(map[string]string{}), Semantics: semantics, Operations: []mesh.Operation{{Name: "inspect", Method: "POST", Path: "/inspect", Scopes: []string{"tool.read"}, Semantics: semantics}}, Tool: &mesh.ToolExport{Name: "inspect_state", Description: "Inspect current state", InputSchema: schema, Method: "POST", Path: "/inspect", Scopes: []string{"tool.read"}}}}, nil
	})
	runtimeNode := f.node("agent", Requirements(), Register)
	liveBinding(t, runtimeNode, "tool.execute")
	liveBinding(t, runtimeNode, "session.store")
	liveBinding(t, runtimeNode, "llm.invoke")
	agent := liveBinding(t, consumer, "agent.run")
	runError := consumer.Call(ctx, agent, "POST", "/agent/run", Request{SessionID: "frozen-run", Message: "inspect state"}, nil)
	if runError == nil {
		t.Fatal("run silently switched to new paid model")
	}
	if calls.Load() != 1 {
		t.Fatalf("subsequent ReAct step reached upstream: calls=%d error=%v", calls.Load(), runError)
	}
	sessions := liveBinding(t, consumer, "session.store")
	var session providers.Session
	if err := consumer.Call(ctx, sessions, "POST", "/session/get", providers.SessionRequest{Scope: providers.Scope{Owner: "node:caller"}, SessionID: "frozen-run"}, &session); err != nil {
		t.Fatal(err)
	}
	if len(session.Messages) != 3 || session.Messages[0].Role != "user" || session.Metadata["state"] != "failed" || session.Metadata["model"] != "free-model" || session.Metadata["model_revision"] != before.Revision || session.Metadata["provider"] != "model" {
		t.Fatalf("failed run lost turn or frozen identity: %+v", session)
	}
	if err := consumer.Call(ctx, agent, "POST", "/agent/run", Request{SessionID: "selection-failed", Message: "keep this turn"}, nil); err == nil {
		t.Fatal("unspecified run selected new paid profile")
	}
	if err := consumer.Call(ctx, sessions, "POST", "/session/get", providers.SessionRequest{Scope: providers.Scope{Owner: "node:caller"}, SessionID: "selection-failed"}, &session); err != nil {
		t.Fatal(err)
	}
	if len(session.Messages) != 1 || session.Messages[0].Content != "keep this turn" || session.Metadata["state"] != "failed" || calls.Load() != 1 {
		t.Fatalf("selection failure lost user turn or charged inference: %+v", session)
	}
}

func TestBenchmarkStopsWithoutReselectionAfterRevisionChange(t *testing.T) {
	var calls atomic.Int64
	var replace func(context.Context) error
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := replace(r.Context()); err != nil {
			t.Error(err)
			http.Error(w, "replacement failed", 500)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"plan_required\":false,\"approval_required\":false}"}}]}`))
	}))
	defer upstream.Close()
	f := liveNetwork(t, "model", "consumer")
	f.node("model", nil, configureModel(t, upstream.URL, "free-model", "free"))
	consumer := f.node("consumer", nil, nil)
	liveBinding(t, consumer, "llm.invoke")
	catalogBinding := liveBinding(t, consumer, "llm.models.list")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var before llm.PublicConfig
	if err := consumer.Call(ctx, catalogBinding, "GET", "/llm/models", nil, &before); err != nil {
		t.Fatal(err)
	}
	replace = func(ctx context.Context) error {
		next := llm.Config{ActiveModelID: "paid-model", Models: []llm.Profile{{ID: "paid-model", Name: "Paid", BaseURL: upstream.URL, Model: "paid-upstream", CostClass: "paid"}}}
		return consumer.Call(ctx, catalogBinding, "PUT", "/llm/config", next, nil)
	}
	s := &runtime{node: consumer}
	run := Benchmark{Scores: map[string]float64{}}
	s.executeBenchmark(ctx, benchmarkRequest{}, &run)
	if calls.Load() != 1 || run.Error == "" || len(run.Cases) != 2 || run.Model != "free-model" || run.Provider != "model" || run.ModelRevision != before.Revision || run.UpstreamModel != "free-model" {
		t.Fatalf("benchmark changed identity or replayed inference: calls=%d run=%+v", calls.Load(), run)
	}
}

func TestMockIsExplicitFreeAndRejectsTools(t *testing.T) {
	f := liveNetwork(t, "model", "consumer")
	t.Setenv("LLM_MOCK", "1")
	f.node("model", nil, llm.Register)
	consumer := f.node("consumer", nil, nil)
	liveBinding(t, consumer, "llm.invoke")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := &runtime{node: consumer}
	model, err := s.modelFor(ctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if model.Model != "mock" || !model.FreeOnly || !strings.HasPrefix(model.Revision, "mock:") || model.Capability.Attributes["supports_tools"] != "false" {
		t.Fatalf("mock misrepresented: %+v", model)
	}
	for _, stream := range []bool{false, true} {
		out, err := s.invoke(ctx, model, llm.Request{Messages: []llm.Message{{Role: "user", Content: "hello"}}, Stream: stream}, nil)
		if err != nil || out.Message.Content != "[MOCK: LLM_MOCK=1] hello" {
			t.Fatalf("mock result not labeled: %+v %v", out, err)
		}
	}
	if _, err := s.invoke(ctx, model, llm.Request{Messages: []llm.Message{{Role: "user", Content: "call tools"}}, Tools: []llm.Tool{{Name: "fake", InputSchema: json.RawMessage(`{"type":"object"}`)}}}, nil); err == nil {
		t.Fatal("mock accepted tools it cannot execute")
	}
}

func TestStreamingUpstream429IsVisibleWithoutReplay(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer upstream.Close()
	f := liveNetwork(t, "model", "consumer")
	f.node("model", nil, configureModel(t, upstream.URL, "free-model", "free"))
	consumer := f.node("consumer", nil, nil)
	liveBinding(t, consumer, "llm.invoke")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := &runtime{node: consumer}
	model, err := s.modelFor(ctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	next := llm.Config{ActiveModelID: "free-model", Models: []llm.Profile{{ID: "free-model", Name: "Free", Model: "free-model", BaseURL: upstream.URL, CostClass: "free", Capabilities: []string{"streaming"}}}}
	catalogBinding := liveBinding(t, consumer, "llm.models.list")
	if err = consumer.Call(ctx, catalogBinding, "PUT", "/llm/config", next, nil); err != nil {
		t.Fatal(err)
	}
	model, err = s.modelFor(ctx, Request{Model: "free-model", Provider: "model"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.invoke(ctx, model, llm.Request{Messages: []llm.Message{{Role: "user", Content: "once"}}, Stream: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") || calls.Load() != 1 {
		t.Fatalf("stream hid rejection or replayed inference: calls=%d error=%v", calls.Load(), err)
	}
}
