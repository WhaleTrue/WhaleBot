// Package telegram implements an outbound-only, capability-resolved Telegram adapter.
package telegram

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/WhaleTrue/WhaleBot/pkg/mesh"
)

type conversation struct {
	SessionID string    `json:"session_id"`
	Updated   time.Time `json:"updated"`
	Ended     bool      `json:"ended"`
}
type config struct {
	Token       string                  `json:"token"`
	Allowed     []int64                 `json:"allowed_user_ids"`
	Offset      int64                   `json:"offset"`
	Chats       map[string]conversation `json:"chats"`
	IdleSeconds int                     `json:"idle_seconds"`
}
type configInput struct {
	Token       *string `json:"token,omitempty"`
	Allowed     []int64 `json:"allowed_user_ids,omitempty"`
	IdleSeconds int     `json:"idle_seconds,omitempty"`
}
type publicConfig struct {
	HasToken    bool    `json:"has_token"`
	Allowed     []int64 `json:"allowed_user_ids"`
	IdleSeconds int     `json:"idle_seconds"`
	Configured  bool    `json:"configured"`
}
type publicStatus struct {
	State         string `json:"state"`
	LastError     string `json:"last_error"`
	Configured    bool   `json:"configured"`
	Conversations int    `json:"conversations"`
}
type adapter struct {
	node             *mesh.Node
	file             string
	mu               sync.Mutex
	config           config
	changed          chan struct{}
	client           *http.Client
	state, lastError string
	started          bool
}

var registry struct {
	sync.Mutex
	adapters []*adapter
}

func Register(node *mesh.Node, dataDir string) ([]mesh.Capability, error) {
	dir := filepath.Join(dataDir, "telegram")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	a := &adapter{node: node, file: filepath.Join(dir, "config.json"), changed: make(chan struct{}, 1), client: &http.Client{Timeout: 65 * time.Second}, state: "unconfigured", config: config{Chats: map[string]conversation{}, IdleSeconds: 86400}}
	raw, e := os.ReadFile(a.file)
	if e == nil {
		if e = json.Unmarshal(raw, &a.config); e != nil {
			return nil, e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if a.config.Chats == nil {
		a.config.Chats = map[string]conversation{}
	}
	if a.config.IdleSeconds < 60 {
		a.config.IdleSeconds = 86400
	}
	if a.config.Token == "" {
		a.config.Token = os.Getenv("TELEGRAM_BOT_TOKEN")
	}
	if len(a.config.Allowed) == 0 {
		for _, v := range strings.Split(os.Getenv("TELEGRAM_ALLOWED_USER_IDS"), ",") {
			if strings.TrimSpace(v) == "" {
				continue
			}
			id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("TELEGRAM_ALLOWED_USER_IDS: %w", err)
			}
			a.config.Allowed = append(a.config.Allowed, id)
		}
	}
	if e = a.save(); e != nil {
		return nil, e
	}
	node.Handle("GET", "/telegram/config", []string{"adapter.config"}, a.getConfig)
	node.Handle("PUT", "/telegram/config", []string{"adapter.config"}, a.putConfig)
	node.Handle("GET", "/telegram/status", []string{"adapter.config"}, a.status)
	registry.Lock()
	registry.adapters = append(registry.adapters, a)
	registry.Unlock()
	return []mesh.Capability{{Schema: mesh.CapabilitySchema, ID: "adapter.telegram", Interface: "adapter.config", Version: "1.0.0", Endpoint: "/telegram/config", InputSchema: json.RawMessage(`{"description":"GET config/status has no JSON input; PUT config accepts optional token, allowed_user_ids and idle_seconds.","anyOf":[{"type":"null"},` + string(mesh.JSONSchemaFor(configInput{})) + `]}`), OutputSchema: json.RawMessage(`{"anyOf":[` + string(mesh.JSONSchemaFor(publicConfig{})) + `,` + string(mesh.JSONSchemaFor(publicStatus{})) + `]}`), Ready: true, Scopes: []string{"adapter.config"}, Semantics: mesh.Semantics{Mutating: true}, Attributes: map[string]string{"transport": "outbound-polling", "status_operation": "status"}, Operations: []mesh.Operation{
		{Name: "config.get", Method: "GET", Path: "/telegram/config", Scopes: []string{"adapter.config"}, Semantics: mesh.Semantics{ReadOnly: true}},
		{Name: "config.put", Method: "PUT", Path: "/telegram/config", Scopes: []string{"adapter.config"}, TransportSecurity: "tls", Semantics: mesh.Semantics{Mutating: true}},
		{Name: "status", Method: "GET", Path: "/telegram/status", Scopes: []string{"adapter.config"}, Semantics: mesh.Semantics{ReadOnly: true}},
	}}}, nil
}

// Start binds all registered adapters to the process lifecycle after Node.Start.
func Start(ctx context.Context) {
	registry.Lock()
	defer registry.Unlock()
	for _, a := range registry.adapters {
		a.mu.Lock()
		if !a.started {
			a.started = true
			go a.run(ctx)
		}
		a.mu.Unlock()
	}
}
func (a *adapter) save() error {
	b, e := json.MarshalIndent(a.config, "", "  ")
	if e != nil {
		return e
	}
	if e = os.WriteFile(a.file+".tmp", b, 0600); e != nil {
		return e
	}
	return os.Rename(a.file+".tmp", a.file)
}
func jsonReply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (a *adapter) getConfig(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	jsonReply(w, 200, publicConfig{HasToken: a.config.Token != "", Allowed: a.config.Allowed, IdleSeconds: a.config.IdleSeconds, Configured: a.config.Token != "" && len(a.config.Allowed) > 0})
}
func (a *adapter) putConfig(w http.ResponseWriter, r *http.Request) {
	var in configInput
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if e := d.Decode(&in); e != nil {
		jsonReply(w, 400, map[string]string{"error": e.Error()})
		return
	}
	if in.IdleSeconds != 0 && in.IdleSeconds < 60 {
		jsonReply(w, 400, map[string]string{"error": "idle_seconds must be at least 60"})
		return
	}
	a.mu.Lock()
	previous := a.config
	if in.Token != nil {
		a.config.Token = strings.TrimSpace(*in.Token)
		a.config.Offset = 0
	}
	a.config.Allowed = in.Allowed
	if in.IdleSeconds != 0 {
		a.config.IdleSeconds = in.IdleSeconds
	}
	e := a.save()
	if e != nil {
		a.config = previous
	}
	a.mu.Unlock()
	if e != nil {
		jsonReply(w, 500, map[string]string{"error": e.Error()})
		return
	}
	select {
	case a.changed <- struct{}{}:
	default:
	}
	a.getConfig(w, r)
}
func (a *adapter) status(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.state
	if a.config.Token == "" || len(a.config.Allowed) == 0 {
		state = "unconfigured"
	}
	jsonReply(w, 200, publicStatus{State: state, LastError: a.lastError, Configured: a.config.Token != "" && len(a.config.Allowed) > 0, Conversations: len(a.config.Chats)})
}
func (a *adapter) setState(state string, e error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state = state
	if e != nil {
		a.lastError = e.Error()
	} else {
		a.lastError = ""
	}
}
func (a *adapter) run(ctx context.Context) {
	for {
		a.mu.Lock()
		configured := a.config.Token != "" && len(a.config.Allowed) > 0
		a.mu.Unlock()
		if !configured {
			a.setState("unconfigured", nil)
			select {
			case <-ctx.Done():
				return
			case <-a.changed:
				continue
			}
		}
		pollCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); a.poll(pollCtx) }()
		select {
		case <-ctx.Done():
			cancel()
			<-done
			a.setState("stopped", nil)
			return
		case <-a.changed:
			cancel()
			<-done
		case <-done:
			cancel()
		}
	}
}
func (a *adapter) api(ctx context.Context, method string, input any, output any) error {
	raw, e := json.Marshal(input)
	if e != nil {
		return e
	}
	a.mu.Lock()
	token := a.config.Token
	a.mu.Unlock()
	request, e := http.NewRequestWithContext(ctx, "POST", "https://api.telegram.org/bot"+token+"/"+method, bytes.NewReader(raw))
	if e != nil {
		return errors.New("Telegram API request construction failed")
	}
	request.Header.Set("Content-Type", "application/json")
	response, e := a.client.Do(request)
	if e != nil {
		return errors.New("Telegram API transport failed")
	}
	defer response.Body.Close()
	var envelope struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if e = json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&envelope); e != nil {
		return e
	}
	if !envelope.OK {
		return fmt.Errorf("Telegram %s: %s", method, redactToken(envelope.Description, token))
	}
	if output != nil {
		return json.Unmarshal(envelope.Result, output)
	}
	return nil
}

func redactToken(message, token string) string {
	if token == "" {
		return message
	}
	for _, secret := range []string{token, url.PathEscape(token), url.QueryEscape(token)} {
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
	}
	return message
}

type update struct {
	ID      int64 `json:"update_id"`
	Message *struct {
		Text string `json:"text"`
		From *struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

func pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func (a *adapter) poll(ctx context.Context) {
	if e := a.api(ctx, "getMe", map[string]any{}, nil); e != nil {
		a.setState("error", e)
		if pause(ctx, 5*time.Second) {
			return
		}
		return
	}
	_ = a.api(ctx, "setMyCommands", map[string]any{"commands": []map[string]string{{"command": "new", "description": "Start a new session"}, {"command": "end", "description": "End current session"}, {"command": "status", "description": "Show session status"}, {"command": "help", "description": "Show commands"}}}, nil)
	for ctx.Err() == nil {
		a.mu.Lock()
		offset := a.config.Offset
		a.mu.Unlock()
		var updates []update
		e := a.api(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 45, "allowed_updates": []string{"message"}}, &updates)
		if e != nil {
			if ctx.Err() != nil {
				return
			}
			a.setState("error", e)
			if !pause(ctx, 5*time.Second) {
				return
			}
			continue
		}
		a.setState("polling", nil)
		// ponytail: process updates serially; per-chat queues are the upgrade when independent chats need parallel runs.
		for _, u := range updates {
			if ctx.Err() != nil {
				return
			}
			if u.Message != nil && u.Message.From != nil {
				a.mu.Lock()
				allowed := false
				for _, id := range a.config.Allowed {
					if id == u.Message.From.ID {
						allowed = true
						break
					}
				}
				a.mu.Unlock()
				if allowed {
					a.handle(ctx, u)
				}
			}
			a.mu.Lock()
			a.config.Offset = u.ID + 1
			e = a.save()
			a.mu.Unlock()
			if e != nil {
				a.setState("error", e)
				return
			}
		}
	}
}
func newSession() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}
func (a *adapter) handle(ctx context.Context, u update) {
	msg := u.Message
	chat := msg.Chat.ID
	owner := fmt.Sprintf("telegram-%d-%d", msg.From.ID, chat)
	key := owner
	a.mu.Lock()
	session := a.config.Chats[key]
	expired := !session.Updated.IsZero() && time.Since(session.Updated) > time.Duration(a.config.IdleSeconds)*time.Second
	command := strings.ToLower(strings.TrimSpace(msg.Text))
	if first := strings.Fields(command); len(first) > 0 {
		command = strings.Split(first[0], "@")[0]
	}
	switch command {
	case "/help", "/start", "/commands":
		a.mu.Unlock()
		a.sendNotice(ctx, chat, "/new — new session\n/end — end session\n/status — session status\n/help — commands")
		return
	case "/status":
		a.mu.Unlock()
		a.sendNotice(ctx, chat, fmt.Sprintf("Session: %s\nEnded: %t\nExpired: %t", session.SessionID, session.Ended, expired))
		return
	case "/end", "/stop":
		session.Ended = true
		session.Updated = time.Now()
		a.config.Chats[key] = session
		e := a.save()
		a.mu.Unlock()
		if e != nil {
			a.sendNotice(ctx, chat, "Session persistence failed: "+e.Error())
			return
		}
		a.sendNotice(ctx, chat, "Session ended. Next message starts a new session.")
		return
	}
	rotate := session.SessionID == "" || session.Ended || expired || command == "/new" || command == "/reset"
	if rotate {
		id, e := newSession()
		if e != nil {
			a.mu.Unlock()
			a.sendNotice(ctx, chat, e.Error())
			return
		}
		session = conversation{SessionID: owner + ":" + id, Updated: time.Now()}
	}
	session.Updated = time.Now()
	a.config.Chats[key] = session
	e := a.save()
	a.mu.Unlock()
	if e != nil {
		a.sendNotice(ctx, chat, "Session persistence failed: "+e.Error())
		return
	}
	if command == "/new" || command == "/reset" {
		a.sendNotice(ctx, chat, "New session: "+session.SessionID)
		return
	}
	if expired {
		a.sendNotice(ctx, chat, "Idle session expired. New session: "+session.SessionID)
	}
	if strings.TrimSpace(msg.Text) == "" {
		a.sendNotice(ctx, chat, "Send text. Incoming file ingestion is not enabled.")
		return
	}
	bindings, e := a.node.Resolve(mesh.Requirement{Capability: "agent.run"})
	if e != nil || len(bindings) == 0 {
		a.sendNotice(ctx, chat, "Agent unavailable: "+fmt.Sprint(e))
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	response, e := a.node.Stream(runCtx, bindings[0], "POST", "/agent/run", map[string]any{"session_id": session.SessionID, "owner": owner, "message": msg.Text, "stream": true})
	if e != nil {
		a.sendNotice(ctx, chat, "Agent error: "+e.Error())
		return
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		a.sendNotice(ctx, chat, fmt.Sprintf("Agent error %d: %s", response.StatusCode, body))
		return
	}
	var progress struct {
		ID int64 `json:"message_id"`
	}
	_ = a.api(runCtx, "sendMessage", map[string]any{"chat_id": chat, "text": "Working…"}, &progress)
	last := time.Time{}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	event := ""
	var data strings.Builder
	completed := false
	flush := func() {
		payload := strings.TrimSuffix(data.String(), "\n")
		data.Reset()
		if payload == "" {
			return
		}
		switch event {
		case "trace":
			if time.Since(last) < 2*time.Second {
				return
			}
			last = time.Now()
			var trace map[string]any
			if json.Unmarshal([]byte(payload), &trace) == nil {
				label := fmt.Sprint(trace["event"])
				if tool, ok := trace["tool"]; ok {
					label += " " + fmt.Sprint(tool)
				}
				if progress.ID != 0 {
					_ = a.api(runCtx, "editMessageText", map[string]any{"chat_id": chat, "message_id": progress.ID, "parse_mode": "HTML", "text": "<b>Working</b>\n" + html.EscapeString(label)}, nil)
				}
			}
		case "done":
			var done struct {
				Reply       string `json:"reply"`
				Attachments []struct {
					Name string `json:"name"`
					URL  string `json:"url"`
				} `json:"attachments"`
			}
			if e := json.Unmarshal([]byte(payload), &done); e != nil {
				a.sendNotice(runCtx, chat, "Invalid agent response: "+e.Error())
				return
			}
			completed = true
			a.sendNotice(runCtx, chat, done.Reply)
			for _, attachment := range done.Attachments {
				if e := a.artifact(runCtx, chat, owner, attachment.Name, attachment.URL); e != nil {
					a.sendNotice(runCtx, chat, "Artifact error: "+e.Error())
				}
			}
		case "error":
			completed = true
			var issue struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal([]byte(payload), &issue)
			a.sendNotice(runCtx, chat, "Agent error: "+issue.Message)
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			event = ""
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			data.WriteByte('\n')
		}
	}
	flush()
	if scanner.Err() != nil {
		a.sendNotice(ctx, chat, "Progress stream error: "+scanner.Err().Error())
	} else if !completed {
		a.sendNotice(ctx, chat, "Agent stream ended without a result")
	}
	if progress.ID != 0 {
		_ = a.api(ctx, "editMessageText", map[string]any{"chat_id": chat, "message_id": progress.ID, "text": "Run finished"}, nil)
	}
}
func (a *adapter) sendNotice(ctx context.Context, chat int64, text string) {
	if text == "" {
		text = "Agent returned an empty response"
	}
	for len(text) > 0 {
		runes := []rune(text)
		count := min(len(runes), 1800)
		chunk := string(runes[:count])
		text = string(runes[count:])
		e := a.api(ctx, "sendMessage", map[string]any{"chat_id": chat, "text": html.EscapeString(chunk), "parse_mode": "HTML", "link_preview_options": map[string]bool{"is_disabled": true}}, nil)
		if e != nil {
			a.setState("error", e)
			return
		}
	}
}
func (a *adapter) artifact(ctx context.Context, chat int64, owner, name, path string) error {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return errors.New("artifact URL is not an authenticated Mesh operation")
	}
	parsed, e := url.Parse(path)
	if e != nil || parsed.Host != "" || parsed.Fragment != "" {
		return errors.New("invalid artifact URL")
	}
	q := parsed.Query()
	q.Set("owner", owner)
	parsed.RawQuery = q.Encode()
	path = parsed.String()
	bindings, e := a.node.Resolve(mesh.Requirement{Capability: "artifact.export", Many: true})
	if e != nil {
		return e
	}
	var response *http.Response
	provider := parsed.Query().Get("provider")
	for _, binding := range bindings {
		if provider != "" && binding.Node.Identity.Principal != provider {
			continue
		}
		for _, op := range binding.Capability.Operations {
			if op.Method == "GET" && op.Path == parsed.Path {
				response, e = a.node.Stream(ctx, binding, "GET", path, nil)
				break
			}
		}
		if response != nil {
			break
		}
	}
	if response == nil {
		return errors.New("no advertised artifact download operation")
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return fmt.Errorf("artifact HTTP %d", response.StatusCode)
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, 45*1024*1024+1))
	if e != nil {
		return e
	}
	if len(raw) > 45*1024*1024 {
		return errors.New("artifact exceeds 45 MiB Telegram limit")
	}
	if name == "" {
		name = "artifact.bin"
	}
	return a.upload(ctx, chat, filepath.Base(name), raw)
}
func (a *adapter) upload(ctx context.Context, chat int64, name string, raw []byte) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if e := writer.WriteField("chat_id", strconv.FormatInt(chat, 10)); e != nil {
		return e
	}
	part, e := writer.CreateFormFile("document", name)
	if e != nil {
		return e
	}
	if _, e = part.Write(raw); e != nil {
		return e
	}
	if e = writer.Close(); e != nil {
		return e
	}
	a.mu.Lock()
	token := a.config.Token
	a.mu.Unlock()
	request, e := http.NewRequestWithContext(ctx, "POST", "https://api.telegram.org/bot"+token+"/sendDocument", &body)
	if e != nil {
		return errors.New("Telegram document request construction failed")
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, e := a.client.Do(request)
	if e != nil {
		return errors.New("Telegram document transport failed")
	}
	defer response.Body.Close()
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if e = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); e != nil {
		return e
	}
	if !result.OK {
		return errors.New("Telegram document: " + redactToken(result.Description, token))
	}
	return nil
}

// Requirements exposes ordinary adapter dependencies without assigning SDK roles.
func Requirements() []mesh.Requirement {
	return []mesh.Requirement{{ID: "telegram.agent", Capability: "agent.run", Optional: true, Sticky: true}, {ID: "telegram.artifacts", Capability: "artifact.export", Optional: true}}
}
