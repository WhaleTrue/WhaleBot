package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/WhaleTrue/WhaleBot/internal/llm"
	"github.com/WhaleTrue/WhaleBot/pkg/mesh"
)

type exportedTool struct {
	binding mesh.Binding
	export  mesh.ToolExport
}
type approval struct {
	ID        string    `json:"id"`
	Owner     string    `json:"owner"`
	SessionID string    `json:"session_id"`
	Digest    string    `json:"digest"`
	Issuer    string    `json:"issuer"`
	ExpiresAt time.Time `json:"expires_at"`
}
type approvalRequest struct {
	Owner     string          `json:"owner,omitempty"`
	SessionID string          `json:"session_id"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *runtime) resolve(id string) (mesh.Binding, error) {
	manifest := s.node.Manifest()
	sum := sha256.Sum256([]byte(id))
	requirement := mesh.Requirement{ID: "agent.optional." + hex.EncodeToString(sum[:]), Capability: id, Namespace: manifest.Identity.Namespace, Policy: "first-healthy"}
	for _, declared := range manifest.Requires {
		if declared.Capability == id {
			requirement = declared
			break
		}
	}
	bindings, err := s.node.Resolve(requirement)
	if err != nil {
		return mesh.Binding{}, fmt.Errorf("required capability %s: %w", id, err)
	}
	if len(bindings) == 0 {
		return mesh.Binding{}, fmt.Errorf("required capability %s unavailable", id)
	}
	return bindings[0], nil
}
func (s *runtime) discoverTools() ([]llm.Tool, map[string]exportedTool) {
	ids := map[string]bool{}
	for _, p := range s.node.Peers() {
		for _, c := range p.Manifest.Provides {
			if c.Tool != nil {
				ids[c.ID] = true
			}
		}
	}
	for _, c := range s.node.Manifest().Provides {
		if c.Tool != nil {
			ids[c.ID] = true
		}
	}
	ordered := make([]string, 0, len(ids))
	for name := range ids {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	tools := []llm.Tool{}
	routes := map[string]exportedTool{}
	duplicates := map[string]bool{}
	for _, capID := range ordered {
		b, err := s.resolve(capID)
		if err != nil || b.Capability.Tool == nil {
			continue
		}
		t := *b.Capability.Tool
		if t.Name == "" || len(t.Name) > 64 || !json.Valid(t.InputSchema) || !strings.HasPrefix(t.Path, "/") || strings.ContainsAny(t.Path, "?#") {
			continue
		}
		if t.Method != "POST" && t.Method != "GET" && t.Method != "PUT" && t.Method != "DELETE" {
			continue
		}
		if _, exists := routes[t.Name]; exists {
			duplicates[t.Name] = true
			continue
		}
		routes[t.Name] = exportedTool{binding: b, export: t}
	}
	for name := range duplicates {
		delete(routes, name)
	}
	keys := make([]string, 0, len(routes))
	for key := range routes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, name := range keys {
		t := routes[name].export
		tools = append(tools, llm.Tool{Name: name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return tools, routes
}
func canonical(raw json.RawMessage) ([]byte, error) {
	if len(raw) > 64<<10 {
		return nil, errors.New("tool arguments exceed 64 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value map[string]any
	if err := d.Decode(&value); err != nil || value == nil {
		return nil, errors.New("tool arguments must be an object")
	}
	return json.Marshal(value)
}
func approvalDigest(t exportedTool, raw json.RawMessage) (string, error) {
	b, err := canonical(raw)
	if err != nil {
		return "", err
	}
	encoded, _ := json.Marshal([]any{t.binding.Node.Identity.Principal, t.binding.Capability.ID, t.export.Method, t.export.Path, string(t.export.InputSchema), string(b)})
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}
func (s *runtime) handleApproval(w http.ResponseWriter, r *http.Request) {
	var in approvalRequest
	if err := strict(w, r, &in); err != nil {
		fail(w, 400, "invalid_json", err)
		return
	}
	owner, err := s.owner(r, in.Owner)
	if err != nil {
		fail(w, 403, "forbidden", err)
		return
	}
	if !validID.MatchString(in.SessionID) {
		fail(w, 400, "invalid_session", errors.New("session_id required"))
		return
	}
	_, routes := s.discoverTools()
	tool, ok := routes[in.Tool]
	if !ok {
		fail(w, 404, "tool_unavailable", errors.New("exported tool not available"))
		return
	}
	validated, err := injectOwner(in.Arguments, tool.export.InputSchema, owner)
	if err != nil {
		fail(w, 400, "invalid_arguments", err)
		return
	}
	if err = validateArguments(tool.export.InputSchema, validated); err != nil {
		fail(w, 400, "invalid_arguments", err)
		return
	}
	digest, err := approvalDigest(tool, in.Arguments)
	if err != nil {
		fail(w, 400, "invalid_arguments", err)
		return
	}
	a := approval{ID: id(), Owner: owner, SessionID: in.SessionID, Digest: digest, Issuer: s.node.Principal(r), ExpiresAt: time.Now().Add(10 * time.Minute)}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := []approval{}
	for _, old := range s.approvals {
		if time.Now().Before(old.ExpiresAt) {
			next = append(next, old)
		}
	}
	next = append(next, a)
	if len(next) > 10000 {
		fail(w, 409, "approval_limit", errors.New("too many pending approvals"))
		return
	}
	if err = save(filepath.Join(s.dir, "approvals.json"), next); err != nil {
		fail(w, 500, "persistence_failed", err)
		return
	}
	s.approvals = next
	jsonReply(w, map[string]any{"id": a.ID, "expires_at": a.ExpiresAt})
}
func needsApproval(t exportedTool) bool {
	sem := t.binding.Capability.Semantics
	return t.export.Confirmation || sem.Mutating || sem.SideEffecting || !sem.ReadOnly
}
func (s *runtime) consumeApproval(owner, session string, t exportedTool, args json.RawMessage) (bool, error) {
	if !needsApproval(t) {
		return true, nil
	}
	digest, err := approvalDigest(t, args)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, a := range s.approvals {
		if a.Owner == owner && a.SessionID == session && a.Digest == digest && time.Now().Before(a.ExpiresAt) {
			next := append([]approval{}, s.approvals[:i]...)
			next = append(next, s.approvals[i+1:]...)
			if err = save(filepath.Join(s.dir, "approvals.json"), next); err != nil {
				return false, err
			}
			s.approvals = next
			return true, nil
		}
	}
	return false, nil
}
func injectOwner(raw json.RawMessage, schema json.RawMessage, owner string) (json.RawMessage, error) {
	var definition struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &definition); err != nil {
		return nil, err
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	if _, ok := definition.Properties["owner"]; ok {
		b, _ := json.Marshal(owner)
		args["owner"] = b
	} else {
		delete(args, "owner")
	}
	return json.Marshal(args)
}
func (s *runtime) execute(ctx context.Context, in Request, t exportedTool, call llm.ToolCall, secrets map[string]string) (json.RawMessage, error) {
	if _, err := canonical(call.Arguments); err != nil {
		return nil, err
	}
	validated, err := injectOwner(call.Arguments, t.export.InputSchema, in.Owner)
	if err != nil {
		return nil, err
	}
	if err = validateArguments(t.export.InputSchema, validated); err != nil {
		return nil, err
	}
	allowed, err := s.consumeApproval(in.Owner, in.SessionID, t, call.Arguments)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, errors.New("approval_required: trusted user must approve exact tool arguments")
	}
	raw, err := s.resolveSecrets(ctx, in.Owner, call.Arguments, secrets)
	if err != nil {
		return nil, err
	}
	raw, err = injectOwner(raw, t.export.InputSchema, in.Owner)
	if err != nil {
		return nil, err
	}
	var out json.RawMessage
	if err = s.node.Call(ctx, t.binding, t.export.Method, t.export.Path, raw, &out); err != nil {
		return nil, errors.New(redact(err.Error(), secrets))
	}
	if len(out) > 256<<10 {
		return nil, errors.New("tool result exceeds 256 KiB")
	}
	return redactJSON(out, secrets), nil
}

// Requirements belongs in the configured manifest before the node starts.
func Requirements() []mesh.Requirement {
	return []mesh.Requirement{
		{ID: "agent.sessions", Capability: "session.store", Interface: "session.store", Version: "^1.0.0", Sticky: true},
		{ID: "agent.model", Capability: "llm.invoke", Interface: "llm.invoke", Version: "^1.0.0"},
		{ID: "agent.memory", Capability: "memory.search", Interface: "memory.search", Version: "^1.0.0", Optional: true},
		{ID: "agent.skills", Capability: "skills.search", Interface: "skills.search", Version: "^1.0.0", Optional: true},
		{ID: "agent.secrets", Capability: "secrets.resolve", Interface: "secrets.resolve", Version: "^1.0.0", Optional: true},
		{ID: "agent.events", Capability: "events.write", Interface: "events.write", Version: "^1.0.0", Optional: true},
		{ID: "agent.stats", Capability: "stats.ingest", Interface: "stats.ingest", Version: "^1.0.0", Optional: true},
	}
}
