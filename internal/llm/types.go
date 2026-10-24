package llm

import "encoding/json"

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}
type Request struct {
	Model         string    `json:"model"`
	ModelRevision string    `json:"model_revision"`
	FreeOnly      bool      `json:"free_only,omitempty"`
	Messages      []Message `json:"messages"`
	Tools         []Tool    `json:"tools,omitempty"`
	Stream        bool      `json:"stream,omitempty"`
	MaxTokens     int       `json:"max_tokens,omitempty"`
}
type Response struct {
	Message Message `json:"message"`
	Usage   Usage   `json:"usage"`
}
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Delta preserves indexed fragments until the final response assembles tool calls.
type Delta struct {
	Content   string      `json:"content,omitempty"`
	ToolCalls []ToolDelta `json:"tool_calls,omitempty"`
}
type ToolDelta struct {
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type Profile struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	BaseURL        string   `json:"base_url"`
	Model          string   `json:"model"`
	APIKey         string   `json:"api_key,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
	CostClass      string   `json:"cost_class,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
}
type Config struct {
	Revision      string    `json:"revision"`
	Models        []Profile `json:"models"`
	ActiveModelID string    `json:"active_model_id"`
}
type PublicProfile struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	BaseURL        string   `json:"base_url"`
	Model          string   `json:"model"`
	HasAPIKey      bool     `json:"has_api_key"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	CostClass      string   `json:"cost_class"`
	Capabilities   []string `json:"capabilities"`
}
type PublicConfig struct {
	Revision      string          `json:"revision"`
	Models        []PublicProfile `json:"models"`
	ActiveModelID string          `json:"active_model_id"`
}
