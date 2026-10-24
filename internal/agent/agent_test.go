package agent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/WhaleTrue/WhaleBot/pkg/mesh"
)

func TestTrustedApprovalBindingConsumptionAndPersistence(t *testing.T) {
	s := &runtime{dir: t.TempDir()}
	tool := exportedTool{binding: mesh.Binding{Node: mesh.Manifest{Identity: mesh.Identity{Principal: "executor"}}, Capability: mesh.Capability{ID: "container.exec", Semantics: mesh.Semantics{SideEffecting: true}}}, export: mesh.ToolExport{Name: "exec", Method: "POST", Path: "/executor/exec", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	args := json.RawMessage(`{"command":["echo","ok"],"id":"container-1"}`)
	if ok, err := s.consumeApproval("alice", "session", tool, args); err != nil || ok {
		t.Fatal("unapproved mutation accepted")
	}
	digest, err := approvalDigest(tool, args)
	if err != nil {
		t.Fatal(err)
	}
	s.approvals = []approval{{ID: "grant", Owner: "alice", SessionID: "session", Digest: digest, Issuer: "trusted-web", ExpiresAt: time.Now().Add(time.Minute)}}
	for _, attempt := range []struct {
		owner, session string
		args           json.RawMessage
	}{{"bob", "session", args}, {"alice", "other", args}, {"alice", "session", json.RawMessage(`{"command":["rm","-rf","/"],"id":"container-1"}`)}} {
		if ok, err := s.consumeApproval(attempt.owner, attempt.session, tool, attempt.args); err != nil || ok {
			t.Fatal("approval binding bypass")
		}
	}
	// Canonical argument ordering does not invalidate the approved operation.
	if ok, err := s.consumeApproval("alice", "session", tool, json.RawMessage(`{"id":"container-1","command":["echo","ok"]}`)); err != nil || !ok {
		t.Fatalf("approved tool rejected: %v", err)
	}
	if ok, _ := s.consumeApproval("alice", "session", tool, args); ok {
		t.Fatal("approval replay accepted")
	}
	var disk []approval
	if err = load(filepath.Join(s.dir, "approvals.json"), &disk); err != nil || len(disk) != 0 {
		t.Fatalf("consumption not durable: %v", err)
	}
	s.approvals = []approval{{Owner: "alice", SessionID: "session", Digest: digest, ExpiresAt: time.Now().Add(-time.Second)}}
	if ok, _ := s.consumeApproval("alice", "session", tool, args); ok {
		t.Fatal("expired approval accepted")
	}
	other := tool
	other.binding.Node.Identity.Principal = "different-executor"
	changed, _ := approvalDigest(other, args)
	if changed == digest {
		t.Fatal("approval not provider-bound")
	}
}
func TestSecretRedactionPreservesJSONAndOpaquePlaceholders(t *testing.T) {
	values := map[string]string{"token": "a\"b\\c", "short": ":"}
	raw := json.RawMessage(`{"result":"a\"b\\c","count":3}`)
	out := redactJSON(raw, values)
	if !json.Valid(out) || strings.Contains(string(out), `a\"b`) || !strings.Contains(string(out), "SECRET_REDACTED") {
		t.Fatalf("unsafe JSON redaction: %s", out)
	}
	s := &runtime{}
	unchanged, err := s.resolveSecrets(context.Background(), "alice", json.RawMessage(`{"value":"ordinary"}`), map[string]string{})
	if err != nil || string(unchanged) != `{"value":"ordinary"}` {
		t.Fatalf("unexpected secret resolution: %s %v", unchanged, err)
	}
}
func TestToolSchemaAndOwnerInjection(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"owner":{"type":"string"}},"required":["id"],"additionalProperties":false}`)
	args, err := injectOwner(json.RawMessage(`{"id":"one","owner":"attacker"}`), schema, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), "attacker") || !strings.Contains(string(args), "alice") {
		t.Fatal("model controls delegated owner")
	}
	if err = validateArguments(schema, args); err != nil {
		t.Fatal(err)
	}
	if validateArguments(schema, json.RawMessage(`{"id":"one","approved":true}`)) == nil {
		t.Fatal("unknown field bypassed exported schema")
	}
	if validateArguments(schema, json.RawMessage(`{"id":2}`)) == nil {
		t.Fatal("invalid argument type accepted")
	}
}
func TestRunInputRejectsCallerApprovalAndTrailingJSON(t *testing.T) {
	for _, body := range []string{`{"session_id":"s","message":"x","approved":true}`, `{"message":"x"} {}`} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/agent/run", strings.NewReader(body))
		var input Request
		if strict(w, r, &input) == nil {
			t.Fatalf("unsafe run input accepted: %s", body)
		}
	}
}
func TestBenchmarkScoresExcludeBonusAndE2E(t *testing.T) {
	run := Benchmark{Scores: map[string]float64{}, Cases: []BenchmarkCase{{Category: "plan_gate", Score: 1}, {Category: "tool_call", Score: 1}, {Category: "react", Score: 1}, {Category: "bonus", Score: 0}, {Category: "e2e", Score: 0}}}
	scoreBenchmark(&run)
	if run.Scores["total"] != 100 || run.Scores["bonus"] != 0 {
		t.Fatalf("incorrect scores: %+v", run.Scores)
	}
}
