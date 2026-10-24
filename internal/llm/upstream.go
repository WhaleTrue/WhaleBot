package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type wireCall struct {
	Index    int    `json:"index,omitempty"`
	ID       string `json:"id"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type wireMessage struct {
	Role       string     `json:"role,omitempty"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []wireCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
type wireResponse struct {
	Choices []struct {
		Message      wireMessage `json:"message"`
		Delta        wireMessage `json:"delta"`
		FinishReason *string     `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func validateRequest(r Request) error {
	if r.Model == "" || r.ModelRevision == "" {
		return errors.New("model and model_revision are required")
	}
	if len(r.Messages) == 0 || len(r.Messages) > 1024 || len(r.Tools) > 128 || r.MaxTokens < 0 || r.MaxTokens > 131072 {
		return errors.New("invalid messages, tools or max_tokens limit")
	}
	for _, m := range r.Messages {
		switch m.Role {
		case "system", "user", "assistant", "tool":
		default:
			return errors.New("unsupported message role")
		}
		if m.Role == "tool" && m.ToolCallID == "" {
			return errors.New("tool message requires tool_call_id")
		}
		for _, c := range m.ToolCalls {
			if c.ID == "" || c.Name == "" || !json.Valid(c.Arguments) {
				return errors.New("invalid tool call")
			}
		}
	}
	seen := map[string]bool{}
	for _, t := range r.Tools {
		if t.Name == "" || len(t.Name) > 64 || seen[t.Name] || !json.Valid(t.InputSchema) {
			return errors.New("invalid or duplicate tool schema")
		}
		seen[t.Name] = true
	}
	return nil
}
func upstream(ctx context.Context, p Profile, r Request, emit func(Delta) error) (Response, error) {
	limit := p.TimeoutSeconds
	if limit == 0 {
		limit = 120
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(limit)*time.Second)
	defer cancel()
	messages := make([]wireMessage, 0, len(r.Messages))
	for _, m := range r.Messages {
		wm := wireMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			wc := wireCall{ID: tc.ID, Type: "function"}
			wc.Function.Name = tc.Name
			wc.Function.Arguments = string(tc.Arguments)
			wm.ToolCalls = append(wm.ToolCalls, wc)
		}
		messages = append(messages, wm)
	}
	tools := make([]any, 0, len(r.Tools))
	for _, t := range r.Tools {
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": t.InputSchema}})
	}
	payload := map[string]any{"model": p.Model, "messages": messages, "stream": r.Stream}
	if len(tools) > 0 {
		payload["tools"] = tools
	}
	if r.MaxTokens > 0 {
		payload["max_tokens"] = r.MaxTokens
	}
	if r.Stream {
		payload["stream_options"] = map[string]bool{"include_usage": true}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, completionURL(p.BaseURL), bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	// Inference may incur charges. Never replay a request, including HTTP 429.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("upstream transport failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}
	if r.Stream {
		return consumeStream(resp.Body, emit)
	}
	var wire wireResponse
	if err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&wire); err != nil {
		return Response{}, fmt.Errorf("upstream response: %w", err)
	}
	if wire.Error != nil {
		return Response{}, errors.New("upstream returned an API error")
	}
	if len(wire.Choices) == 0 {
		return Response{}, errors.New("upstream returned no choices")
	}
	return fromWire(wire.Choices[0].Message, wire.Usage)
}
func fromWire(w wireMessage, u Usage) (Response, error) {
	out := Response{Message: Message{Role: "assistant", Content: w.Content}, Usage: u}
	for _, c := range w.ToolCalls {
		if c.ID == "" || c.Function.Name == "" || !json.Valid([]byte(c.Function.Arguments)) {
			return Response{}, errors.New("upstream returned invalid tool arguments")
		}
		out.Message.ToolCalls = append(out.Message.ToolCalls, ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: json.RawMessage(c.Function.Arguments)})
	}
	if out.Message.Content == "" && len(out.Message.ToolCalls) == 0 {
		return Response{}, errors.New("upstream returned an empty assistant response")
	}
	return out, nil
}
func consumeStream(body io.Reader, emit func(Delta) error) (Response, error) {
	scanner := bufio.NewScanner(io.LimitReader(body, 16<<20))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	type streamedCall struct {
		call      wireCall
		arguments strings.Builder
	}
	var message wireMessage
	var content strings.Builder
	var usage Usage
	calls := map[int]*streamedCall{}
	finished := false
	done := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			done = true
			break
		}
		var chunk wireResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return Response{}, errors.New("invalid upstream SSE data")
		}
		if chunk.Error != nil {
			return Response{}, errors.New("upstream stream failed")
		}
		if chunk.Usage.TotalTokens != 0 {
			usage = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.FinishReason != nil {
			finished = true
		}
		d := Delta{Content: choice.Delta.Content}
		content.WriteString(d.Content)
		for _, c := range choice.Delta.ToolCalls {
			if c.Index < 0 || c.Index >= 128 {
				return Response{}, errors.New("invalid tool delta index")
			}
			target := calls[c.Index]
			if target == nil {
				target = &streamedCall{}
				calls[c.Index] = target
			}
			target.call.ID += c.ID
			target.call.Function.Name += c.Function.Name
			target.arguments.WriteString(c.Function.Arguments)
			if target.arguments.Len() > 1<<20 {
				return Response{}, errors.New("tool arguments exceed 1 MiB")
			}
			d.ToolCalls = append(d.ToolCalls, ToolDelta{Index: c.Index, ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments})
		}
		if emit != nil && (d.Content != "" || len(d.ToolCalls) > 0) {
			if err := emit(d); err != nil {
				return Response{}, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Response{}, err
	}
	if !done || !finished {
		return Response{}, errors.New("upstream stream ended before completion")
	}
	message.Content = content.String()
	for i := range len(calls) {
		c := calls[i]
		if c == nil {
			return Response{}, errors.New("non-contiguous tool delta indexes")
		}
		c.call.Function.Arguments = c.arguments.String()
		message.ToolCalls = append(message.ToolCalls, c.call)
	}
	return fromWire(message, usage)
}
