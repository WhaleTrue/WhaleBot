package providers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/WhaleTrue/WhaleBot/pkg/mesh"
	"net/http"
	"time"
)

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (u Usage) valid() bool {
	return u.PromptTokens >= 0 && u.CompletionTokens >= 0 && u.TotalTokens >= 0 && u.PromptTokens <= 1000000000 && u.CompletionTokens <= 1000000000 && u.TotalTokens <= 1000000000
}
func (u *Usage) add(v Usage) {
	u.PromptTokens += v.PromptTokens
	u.CompletionTokens += v.CompletionTokens
	u.TotalTokens += v.TotalTokens
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Timestamp  time.Time  `json:"timestamp,omitempty"`
	Usage      Usage      `json:"usage,omitempty"`
}
type Session struct {
	SessionID string            `json:"session_id"`
	Messages  []Message         `json:"messages"`
	Metadata  map[string]string `json:"metadata"`
	Usage     Usage             `json:"usage"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	ExpiresAt time.Time         `json:"expires_at"`
	Expired   bool              `json:"expired"`
	Revision  int64             `json:"revision"`
}
type SessionRequest struct {
	Scope
	SessionID string `json:"session_id"`
	Limit     int    `json:"limit,omitempty"`
}
type AppendRequest struct {
	Scope
	SessionID        string            `json:"session_id"`
	Messages         []Message         `json:"messages"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	ExpectedRevision *int64            `json:"expected_revision,omitempty"`
	IdleSeconds      int               `json:"idle_seconds,omitempty"`
}
type ListRequest struct {
	Scope
	Page
}

func emptySession(id string) Session {
	return Session{SessionID: id, Messages: []Message{}, Metadata: map[string]string{}}
}
func sessionExpired(session *Session, now time.Time) {
	if !session.ExpiresAt.IsZero() && !now.Before(session.ExpiresAt) {
		session.Expired = true
	}
}
func (s *store) context(r *http.Request, input SessionRequest) (any, error) {
	owner, err := s.owner(r, input.Scope, "session")
	if err != nil {
		return nil, err
	}
	if !validID(input.SessionID) || input.Limit < 0 || input.Limit > 2000 {
		return nil, bad("invalid session or context limit")
	}
	session := emptySession(input.SessionID)
	err = s.read(r.Context(), s.db, owner, "session", input.SessionID, &session)
	if err != nil && !errors.Is(err, missing) {
		return nil, err
	}
	sessionExpired(&session, time.Now())
	if session.Expired {
		session.Messages = []Message{}
	} else {
		limit := input.Limit
		if limit == 0 {
			limit = 40
		}
		if len(session.Messages) > limit {
			session.Messages = session.Messages[len(session.Messages)-limit:]
		}
	}
	return session, nil
}
func (s *store) appendSession(r *http.Request, input AppendRequest) (any, error) {
	owner, err := s.owner(r, input.Scope, "session")
	if err != nil {
		return nil, err
	}
	if !validID(input.SessionID) || len(input.Messages) > 200 || input.IdleSeconds < 0 || input.IdleSeconds > 31536000 {
		return nil, bad("invalid session append")
	}
	if err = validateMetadata(input.Metadata); err != nil {
		return nil, err
	}
	for _, m := range input.Messages {
		if m.Role != "system" && m.Role != "user" && m.Role != "assistant" && m.Role != "tool" {
			return nil, bad("invalid message role")
		}
		if len(m.Content) > 256<<10 || !m.Usage.valid() || len(m.ToolCalls) > 64 {
			return nil, bad("message limit exceeded")
		}
		for _, call := range m.ToolCalls {
			if !validID(call.ID) || !validID(call.Name) || !json.Valid(call.Arguments) || len(call.Arguments) > 65536 {
				return nil, bad("invalid tool call")
			}
		}
	}
	session := emptySession(input.SessionID)
	err = s.transaction(r.Context(), func(tx *sql.Tx) error {
		err := s.read(r.Context(), tx, owner, "session", input.SessionID, &session)
		if err != nil && !errors.Is(err, missing) {
			return err
		}
		now := time.Now().UTC()
		sessionExpired(&session, now)
		if session.Expired {
			return conflict("session expired; create a new session")
		}
		if input.ExpectedRevision != nil && *input.ExpectedRevision != session.Revision {
			return conflict("session revision mismatch")
		}
		if session.CreatedAt.IsZero() {
			session.CreatedAt = now
		}
		if session.Metadata == nil {
			session.Metadata = map[string]string{}
		}
		for k, v := range input.Metadata {
			if v == "" {
				delete(session.Metadata, k)
			} else {
				session.Metadata[k] = v
			}
		}
		if err = validateMetadata(session.Metadata); err != nil {
			return err
		}
		for i := range input.Messages {
			input.Messages[i].Timestamp = now
			session.Usage.add(input.Messages[i].Usage)
		}
		session.Messages = append(session.Messages, input.Messages...)
		if len(session.Messages) > 2000 {
			session.Messages = session.Messages[len(session.Messages)-2000:]
		}
		seconds := input.IdleSeconds
		if seconds == 0 {
			seconds = 604800
		}
		session.UpdatedAt = now
		session.ExpiresAt = now.Add(time.Duration(seconds) * time.Second)
		session.Revision++
		data, err := json.Marshal(session)
		if err != nil {
			return err
		}
		if len(data) > 4<<20 {
			return bad("session history exceeds 4 MiB; delete or start a new session")
		}
		return s.put(r.Context(), tx, owner, "session", input.SessionID, session)
	})
	return session, err
}
func (s *store) sessions() []mesh.Operation {
	return []mesh.Operation{
		bind(s, "POST", "/session/context", "session.read", s.context),
		bind(s, "POST", "/session/get", "session.read", func(r *http.Request, input SessionRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "session")
			if err != nil {
				return nil, err
			}
			if !validID(input.SessionID) {
				return nil, bad("session_id required")
			}
			var session Session
			err = s.read(r.Context(), s.db, owner, "session", input.SessionID, &session)
			sessionExpired(&session, time.Now())
			return session, err
		}),
		bind(s, "POST", "/session/append", "session.write", s.appendSession),
		bind(s, "POST", "/session/list", "session.read", func(r *http.Request, input ListRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "session")
			if err != nil {
				return nil, err
			}
			limit, offset, err := input.Page.bounds()
			if err != nil {
				return nil, err
			}
			raw, err := s.list(r.Context(), owner, "session", limit, offset)
			if err != nil {
				return nil, err
			}
			items := []Session{}
			for _, data := range raw {
				var session Session
				if err = json.Unmarshal(data, &session); err != nil {
					return nil, err
				}
				sessionExpired(&session, time.Now())
				session.Messages = []Message{}
				items = append(items, session)
			}
			return map[string]any{"items": items}, nil
		}),
		bind(s, "POST", "/session/delete", "session.write", func(r *http.Request, input SessionRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "session")
			if err != nil {
				return nil, err
			}
			if !validID(input.SessionID) {
				return nil, bad("session_id required")
			}
			err = s.remove(r.Context(), owner, "session", input.SessionID)
			return map[string]bool{"deleted": err == nil}, err
		}),
		bind(s, "POST", "/session/expire", "session.write", func(r *http.Request, input SessionRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "session")
			if err != nil {
				return nil, err
			}
			if !validID(input.SessionID) {
				return nil, bad("session_id required")
			}
			var session Session
			err = s.transaction(r.Context(), func(tx *sql.Tx) error {
				if err := s.read(r.Context(), tx, owner, "session", input.SessionID, &session); err != nil {
					return err
				}
				session.Expired = true
				session.ExpiresAt = time.Now().UTC()
				session.Revision++
				return s.put(r.Context(), tx, owner, "session", input.SessionID, session)
			})
			return session, err
		}),
	}
}
