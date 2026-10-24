package providers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/WhaleTrue/WhaleBot/pkg/mesh"
)

type IngestEvent struct {
	Kind      string            `json:"kind"`
	SessionID string            `json:"session_id,omitempty"`
	Message   string            `json:"message,omitempty"`
	Level     string            `json:"level,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Usage     Usage             `json:"usage,omitempty"`
}
type Event struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	IngestEvent
}
type EventsRequest struct {
	Scope
	Events []IngestEvent `json:"events"`
}
type EventsQuery struct {
	Scope
	AfterID   int64  `json:"after_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Kind      string `json:"kind,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}
type StatsQuery struct {
	Scope
	Since time.Time `json:"since,omitempty"`
	Until time.Time `json:"until,omitempty"`
}
type Stats struct {
	Events    int64            `json:"events"`
	Messages  int64            `json:"messages"`
	ToolCalls int64            `json:"tool_calls"`
	Usage     Usage            `json:"usage"`
	Since     time.Time        `json:"since"`
	Until     time.Time        `json:"until"`
	ByKind    map[string]int64 `json:"by_kind"`
}

func (s *store) ingest(r *http.Request, input EventsRequest, family string) (any, error) {
	owner, err := s.owner(r, input.Scope, family)
	if err != nil {
		return nil, err
	}
	if len(input.Events) == 0 || len(input.Events) > 200 {
		return nil, bad("events batch must contain 1 to 200 entries")
	}
	for _, event := range input.Events {
		if !validID(event.Kind) || len(event.Kind) > 128 || len(event.Message) > 65536 || (event.SessionID != "" && !validID(event.SessionID)) || !event.Usage.valid() {
			return nil, bad("invalid event")
		}
		if event.Level != "" && event.Level != "debug" && event.Level != "info" && event.Level != "warn" && event.Level != "error" {
			return nil, bad("invalid log level")
		}
		if err = validateMetadata(event.Metadata); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	err = s.transaction(r.Context(), func(tx *sql.Tx) error {
		statement, err := tx.PrepareContext(r.Context(), `INSERT INTO events(namespace,owner,ts,kind,session_id,data,prompt,completion,total) VALUES(?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer statement.Close()
		for _, input := range input.Events {
			event := Event{Timestamp: now, IngestEvent: input}
			if event.Level == "" {
				event.Level = "info"
			}
			data, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if _, err = statement.ExecContext(r.Context(), s.namespace, owner, now.UnixNano(), event.Kind, event.SessionID, data, event.Usage.PromptTokens, event.Usage.CompletionTokens, event.Usage.TotalTokens); err != nil {
				return err
			}
		}
		return nil
	})
	return map[string]int{"written": len(input.Events)}, err
}
func (s *store) queryEvents(r *http.Request, input EventsQuery) (any, error) {
	owner, err := s.owner(r, input.Scope, "events")
	if err != nil {
		return nil, err
	}
	if input.AfterID < 0 || input.Limit < 0 || input.Limit > 200 || len(input.Kind) > 128 || len(input.SessionID) > 256 {
		return nil, bad("invalid event query")
	}
	limit := input.Limit
	if limit == 0 {
		limit = 100
	}
	order := "DESC"
	if input.AfterID > 0 {
		order = "ASC"
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,data FROM events WHERE namespace=? AND owner=? AND id>? AND (?='' OR kind=?) AND (?='' OR session_id=?) ORDER BY id `+order+` LIMIT ?`, s.namespace, owner, input.AfterID, input.Kind, input.Kind, input.SessionID, input.SessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Event{}
	size := 0
	for rows.Next() {
		var id int64
		var data []byte
		var event Event
		if err = rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		size += len(data)
		if size > maxBody-65536 {
			return nil, &apiError{413, "result exceeds response limit; reduce limit"}
		}
		if err = json.Unmarshal(data, &event); err != nil {
			return nil, err
		}
		event.ID = id
		items = append(items, event)
	}
	return map[string]any{"items": items}, rows.Err()
}
func (s *store) queryStats(r *http.Request, input StatsQuery) (any, error) {
	owner, err := s.owner(r, input.Scope, "stats")
	if err != nil {
		return nil, err
	}
	since := input.Since
	until := input.Until
	if since.IsZero() {
		since = time.Unix(0, 0).UTC()
	}
	if until.IsZero() {
		until = time.Now().UTC()
	}
	if since.Before(time.Unix(0, 0)) || !until.After(since) || until.After(time.Now().Add(time.Minute)) {
		return nil, bad("invalid statistics time range")
	}
	result := Stats{Since: since, Until: until, ByKind: map[string]int64{}}
	err = s.transaction(r.Context(), func(tx *sql.Tx) error {
		err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*),COALESCE(SUM(CASE WHEN kind='message' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN kind='tool_call' THEN 1 ELSE 0 END),0),COALESCE(SUM(prompt),0),COALESCE(SUM(completion),0),COALESCE(SUM(total),0) FROM events WHERE namespace=? AND owner=? AND ts>=? AND ts<?`, s.namespace, owner, since.UnixNano(), until.UnixNano()).Scan(&result.Events, &result.Messages, &result.ToolCalls, &result.Usage.PromptTokens, &result.Usage.CompletionTokens, &result.Usage.TotalTokens)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(r.Context(), `SELECT kind,COUNT(*) FROM events WHERE namespace=? AND owner=? AND ts>=? AND ts<? GROUP BY kind ORDER BY COUNT(*) DESC,kind LIMIT 100`, s.namespace, owner, since.UnixNano(), until.UnixNano())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var kind string
			var count int64
			if err = rows.Scan(&kind, &count); err != nil {
				return err
			}
			result.ByKind[kind] = count
		}
		return rows.Err()
	})
	return result, err
}
func (s *store) eventRoutes() []mesh.Operation {
	return []mesh.Operation{
		bind(s, "POST", "/events", "events.write", func(r *http.Request, input EventsRequest) (any, error) { return s.ingest(r, input, "events") }),
		bind(s, "POST", "/events/query", "events.read", s.queryEvents),
		bind(s, "GET", "/events", "events.read", func(r *http.Request, _ struct{}) (any, error) { return s.queryEvents(r, EventsQuery{}) }),
	}
}
func (s *store) statRoutes() []mesh.Operation {
	return []mesh.Operation{
		bind(s, "POST", "/stats", "stats.ingest", func(r *http.Request, input EventsRequest) (any, error) { return s.ingest(r, input, "stats") }),
		bind(s, "POST", "/stats/query", "stats.read", s.queryStats),
		bind(s, "GET", "/stats", "stats.read", func(r *http.Request, _ struct{}) (any, error) { return s.queryStats(r, StatsQuery{}) }),
	}
}
