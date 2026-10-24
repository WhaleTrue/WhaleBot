package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/WhaleTrue/WhaleBot/internal/llm"
	"github.com/WhaleTrue/WhaleBot/pkg/mesh"
)

type Request struct {
	SessionID     string `json:"session_id,omitempty"`
	Message       string `json:"message"`
	Model         string `json:"model,omitempty"`
	Provider      string `json:"provider,omitempty"`
	ModelRevision string `json:"model_revision,omitempty"`
	Stream        bool   `json:"stream,omitempty"`
	Owner         string `json:"owner,omitempty"`
}
type Attachment struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	MediaType string `json:"media_type,omitempty"`
}
type Response struct {
	SessionID     string       `json:"session_id"`
	Model         string       `json:"model"`
	Provider      string       `json:"provider"`
	ModelRevision string       `json:"model_revision"`
	UpstreamModel string       `json:"upstream_model"`
	Reply         string       `json:"reply"`
	Usage         llm.Usage    `json:"usage"`
	Attachments   []Attachment `json:"attachments"`
}
type Trace struct {
	TraceID    string          `json:"trace_id"`
	SessionID  string          `json:"session_id"`
	Step       int             `json:"step"`
	Event      string          `json:"event"`
	Tool       string          `json:"tool,omitempty"`
	Usage      *llm.Usage      `json:"usage,omitempty"`
	DurationMS int64           `json:"duration_ms,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
}
type runtime struct {
	node          *mesh.Node
	dir           string
	mu            sync.Mutex
	active        map[string]bool
	approvals     []approval
	benchmarks    []Benchmark
	benchmarkJobs chan benchmarkJob
}
type emitter func(string, any) error

var validID = regexp.MustCompile(`^[a-zA-Z0-9_.:@/-]{1,192}$`)

func id() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func strict(w http.ResponseWriter, r *http.Request, out any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}
func jsonReply(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, code string, err error) {
	mesh.WriteError(w, status, code, err.Error())
}
func sse(w http.ResponseWriter, name string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b); err != nil {
		return err
	}
	return http.NewResponseController(w).Flush()
}
func (s *runtime) owner(r *http.Request, owner string) (string, error) {
	if owner != "" {
		if !s.node.Authorized(r, "agent.delegate") {
			return "", errors.New("owner requires agent.delegate")
		}
		if !validID.MatchString(owner) {
			return "", errors.New("invalid owner")
		}
		return owner, nil
	}
	return "node:" + s.node.Principal(r), nil
}
func cap(id, path, method string, sem mesh.Semantics) mesh.Capability {
	return mesh.Capability{Schema: "whalemesh.capability/v1", ID: id, Interface: id, Version: "1.0.0", Endpoint: path, Ready: true, Semantics: sem, Operations: []mesh.Operation{{Name: id, Method: method, Path: path, Semantics: sem, Scopes: []string{id}}}}
}
func Register(node *mesh.Node, dataDir string) ([]mesh.Capability, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	var stateID string
	if err := load(filepath.Join(dataDir, "state-id.json"), &stateID); err != nil {
		return nil, err
	}
	if stateID == "" {
		stateID = id()
		if err := save(filepath.Join(dataDir, "state-id.json"), stateID); err != nil {
			return nil, err
		}
	}
	if !validID.MatchString(stateID) {
		return nil, errors.New("invalid persisted agent state ID")
	}
	s := &runtime{node: node, dir: dataDir, active: map[string]bool{}, benchmarkJobs: make(chan benchmarkJob, 1)}
	if err := load(filepath.Join(dataDir, "approvals.json"), &s.approvals); err != nil {
		return nil, err
	}
	var records []persistedBenchmark
	if err := load(filepath.Join(dataDir, "benchmarks.json"), &records); err != nil {
		return nil, err
	}
	for _, record := range records {
		run := record.Benchmark
		run.Owner = record.Owner
		if run.Status == "running" {
			run.Status = "failed"
			run.Error = "node restarted during benchmark"
			run.FinishedAt = time.Now().UTC()
		}
		s.benchmarks = append(s.benchmarks, run)
	}
	if err := s.persistBenchmarks(s.benchmarks); err != nil {
		return nil, err
	}
	if err := node.AddWorker(s.benchmarkWorker); err != nil {
		return nil, err
	}
	node.Handle("POST", "/agent/run", []string{"agent.run"}, s.handleRun)
	node.Handle("POST", "/agent/approvals", []string{"agent.approve"}, s.handleApproval)
	node.Handle("POST", "/benchmark/run", []string{"benchmark.run"}, s.handleBenchmark)
	node.Handle("GET", "/benchmark/runs", []string{"benchmark.read"}, s.listBenchmarks)
	node.Handle("DELETE", "/benchmark/runs", []string{"benchmark.delete"}, s.deleteBenchmarks)
	node.Handle("DELETE", "/benchmark/runs/{id}", []string{"benchmark.delete"}, s.deleteBenchmarks)
	run := cap("agent.run", "/agent/run", "POST", mesh.Semantics{SideEffecting: true, Streaming: true})
	run.Scopes = []string{"agent.run"}
	run.InputSchema = mesh.JSONSchemaFor(Request{})
	run.OutputSchema = mesh.JSONSchemaFor(Response{})
	approve := cap("agent.approve", "/agent/approvals", "POST", mesh.Semantics{Mutating: true})
	approve.Scopes = []string{"agent.approve"}
	approve.InputSchema = mesh.JSONSchemaFor(approvalRequest{})
	approve.OutputSchema = mesh.JSONSchemaFor(struct {
		ID        string    `json:"id"`
		ExpiresAt time.Time `json:"expires_at"`
	}{})
	bench := cap("benchmark.run", "/benchmark/run", "POST", mesh.Semantics{SideEffecting: true})
	bench.Scopes = []string{"benchmark.run"}
	bench.InputSchema = mesh.JSONSchemaFor(benchmarkRequest{})
	bench.OutputSchema = mesh.JSONSchemaFor(Benchmark{})
	results := cap("benchmark.results", "/benchmark/runs", "GET", mesh.Semantics{ReadOnly: true, Idempotent: true})
	results.Operations[0].Scopes = []string{"benchmark.read"}
	results.Scopes = []string{"benchmark.read", "benchmark.delete"}
	results.InputSchema = mesh.JSONSchemaFor(struct{}{})
	results.OutputSchema = mesh.JSONSchemaFor(struct {
		Runs []Benchmark `json:"runs"`
	}{})
	results.Operations = append(results.Operations, mesh.Operation{Name: "delete", Method: "DELETE", Path: "/benchmark/runs", Scopes: []string{"benchmark.delete"}, Semantics: mesh.Semantics{Mutating: true}}, mesh.Operation{Name: "delete.one", Method: "DELETE", Path: "/benchmark/runs/{id}", Scopes: []string{"benchmark.delete"}, Semantics: mesh.Semantics{Mutating: true}})
	capabilities := []mesh.Capability{run, approve, bench, results}
	for i := range capabilities {
		capabilities[i].Attributes = map[string]string{"state_id": stateID, "stateful": "true", "persistence": "local-volume"}
	}
	return capabilities, nil
}
func (s *runtime) handleRun(w http.ResponseWriter, r *http.Request) {
	var input Request
	if err := strict(w, r, &input); err != nil {
		fail(w, 400, "invalid_json", err)
		return
	}
	owner, err := s.owner(r, input.Owner)
	if err != nil {
		fail(w, 403, "forbidden", err)
		return
	}
	input.Owner = owner
	if input.SessionID == "" {
		input.SessionID = id()
	}
	if !validID.MatchString(input.SessionID) || input.Message == "" || len(input.Message) > 128<<10 {
		fail(w, 400, "invalid_request", errors.New("valid session_id and nonempty message up to 128 KiB required"))
		return
	}
	key := owner + "\x00" + input.SessionID
	s.mu.Lock()
	busy := s.active[key]
	if !busy {
		s.active[key] = true
	}
	s.mu.Unlock()
	if busy {
		fail(w, 409, "session_busy", errors.New("session already running"))
		return
	}
	defer func() { s.mu.Lock(); delete(s.active, key); s.mu.Unlock() }()
	var emit emitter
	if input.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		emit = func(name string, value any) error { return sse(w, name, value) }
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	out, err := s.run(ctx, input, emit)
	if err != nil {
		if emit != nil {
			_ = emit("error", map[string]string{"code": "run_failed", "message": err.Error()})
		} else {
			fail(w, 502, "run_failed", err)
		}
		return
	}
	if emit != nil {
		_ = emit("done", out)
	} else {
		jsonReply(w, out)
	}
}
func load(path string, out any) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(b) > 8<<20 {
		return errors.New("state file exceeds 8 MiB")
	}
	return json.Unmarshal(b, out)
}
func save(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
