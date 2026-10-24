package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/WhaleMesh/WhaleMesh/internal/llm"
	"github.com/WhaleMesh/WhaleMesh/internal/providers"
	"github.com/WhaleMesh/WhaleMesh/pkg/mesh"
)

var secretPattern = regexp.MustCompile(`\{\{secret:([a-zA-Z0-9_.-]{1,192})\}\}`)

func redact(text string, values map[string]string) string {
	ordered := make([]string, 0, len(values)*2)
	for _, v := range values {
		if v != "" {
			ordered = append(ordered, v)
			encoded, _ := json.Marshal(v)
			if len(encoded) > 2 {
				ordered = append(ordered, string(encoded[1:len(encoded)-1]))
			}
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, v := range ordered {
		text = strings.ReplaceAll(text, v, "[SECRET_REDACTED]")
	}
	return text
}
func (s *runtime) resolveSecrets(ctx context.Context, owner string, raw json.RawMessage, values map[string]string) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	var visit func(any) (any, error)
	visit = func(input any) (any, error) {
		switch v := input.(type) {
		case string:
			for _, match := range secretPattern.FindAllStringSubmatch(v, -1) {
				secret, exists := values[match[1]]
				if !exists {
					b, err := s.resolve("secrets.resolve")
					if err != nil {
						return nil, err
					}
					var out struct {
						Key   string `json:"key"`
						Value string `json:"value"`
					}
					if err = s.node.Call(ctx, b, "POST", "/secrets/resolve", map[string]string{"owner": owner, "key": match[1]}, &out); err != nil {
						return nil, errors.New("secret resolution failed")
					}
					if out.Value == "" {
						return nil, errors.New("resolved secret is empty")
					}
					secret = out.Value
					values[match[1]] = secret
				}
				v = strings.ReplaceAll(v, match[0], secret)
			}
			return v, nil
		case []any:
			for i, item := range v {
				out, err := visit(item)
				if err != nil {
					return nil, err
				}
				v[i] = out
			}
			return v, nil
		case map[string]any:
			for key, item := range v {
				out, err := visit(item)
				if err != nil {
					return nil, err
				}
				v[key] = out
			}
			return v, nil
		default:
			return input, nil
		}
	}
	out, err := visit(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}
func (s *runtime) history(ctx context.Context, in Request) (mesh.Binding, providers.Session, error) {
	binding, err := s.resolve("session.store")
	if err != nil {
		return binding, providers.Session{}, err
	}
	var session providers.Session
	err = s.node.Call(ctx, binding, "POST", "/session/context", map[string]any{"owner": in.Owner, "session_id": in.SessionID, "limit": 80}, &session)
	if err != nil {
		return binding, session, fmt.Errorf("session context: %w", err)
	}
	if session.Expired {
		return binding, session, errors.New("session expired; create a new session")
	}
	return binding, session, nil
}
func (s *runtime) append(ctx context.Context, binding mesh.Binding, in Request, revision int64, messages []providers.Message, metadata map[string]string) (providers.Session, error) {
	var session providers.Session
	input := map[string]any{"owner": in.Owner, "session_id": in.SessionID, "messages": messages, "expected_revision": revision, "metadata": metadata}
	if err := s.node.Call(ctx, binding, "POST", "/session/append", input, &session); err != nil {
		return session, fmt.Errorf("persist session: %w", err)
	}
	return session, nil
}
func (s *runtime) optionalContext(ctx context.Context, in Request) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var blocks []string
	if b, err := s.resolve("skills.search"); err == nil {
		var result struct {
			Items []struct {
				Title       string `json:"title"`
				Description string `json:"description"`
				Files       []struct {
					Path    string `json:"path"`
					Content []byte `json:"content"`
				} `json:"files"`
			} `json:"items"`
		}
		if err = s.node.Call(ctx, b, "POST", "/skills/search", map[string]any{"owner": in.Owner, "query": in.Message, "limit": 3}, &result); err == nil {
			for _, item := range result.Items {
				text := item.Title + "\n" + item.Description
				for _, file := range item.Files {
					if strings.HasSuffix(strings.ToLower(file.Path), "skill.md") {
						text += "\n" + string(file.Content)
					}
				}
				if len(text) > 16000 {
					text = text[:16000]
				}
				blocks = append(blocks, "<retrieved_skill>\n"+text+"\n</retrieved_skill>")
			}
		}
	}
	if b, err := s.resolve("memory.search"); err == nil {
		var result struct {
			Items []struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"items"`
		}
		if err = s.node.Call(ctx, b, "POST", "/memory/search", map[string]any{"owner": in.Owner, "query": in.Message, "limit": 5}, &result); err == nil {
			for _, item := range result.Items {
				text := item.Key + ": " + item.Value
				if len(text) > 8000 {
					text = text[:8000]
				}
				blocks = append(blocks, "<retrieved_memory>\n"+text+"\n</retrieved_memory>")
			}
		}
	}
	return strings.Join(blocks, "\n")
}
func fromStored(m providers.Message) llm.Message {
	out := llm.Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
	for _, c := range m.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, llm.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments})
	}
	return out
}
func stored(m llm.Message, values map[string]string) providers.Message {
	out := providers.Message{Role: m.Role, Content: redact(m.Content, values), ToolCallID: m.ToolCallID}
	for _, c := range m.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, providers.ToolCall{ID: c.ID, Name: c.Name, Arguments: redactJSON(c.Arguments, values)})
	}
	return out
}
func addUsage(total *llm.Usage, next llm.Usage) {
	total.PromptTokens += next.PromptTokens
	total.CompletionTokens += next.CompletionTokens
	total.TotalTokens += next.TotalTokens
}
func redactJSON(raw json.RawMessage, values map[string]string) json.RawMessage {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return json.RawMessage(`{"error":"invalid tool JSON"}`)
	}
	var visit func(any) any
	visit = func(v any) any {
		switch item := v.(type) {
		case string:
			return redact(item, values)
		case []any:
			for i := range item {
				item[i] = visit(item[i])
			}
			return item
		case map[string]any:
			for key, val := range item {
				item[key] = visit(val)
			}
			return item
		default:
			return v
		}
	}
	result, err := json.Marshal(visit(value))
	if err != nil {
		return json.RawMessage(`{"error":"cannot redact JSON"}`)
	}
	return result
}
