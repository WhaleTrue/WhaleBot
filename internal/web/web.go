// Package web exposes a user-authenticated browser surface over capability resolution.
package web

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/WhaleMesh/WhaleMesh/internal/executor"
	"github.com/WhaleMesh/WhaleMesh/pkg/mesh"
	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID           string   `json:"id"`
	Username     string   `json:"username"`
	PasswordHash string   `json:"password_hash,omitempty"`
	Scopes       []string `json:"scopes"`
}
type loginSession struct {
	UserID, CSRF string
	Expires      time.Time
}
type failure struct {
	Count int
	Until time.Time
}
type server struct {
	node        *mesh.Node
	dir, static string
	mu          sync.Mutex
	users       []User
	sessions    map[string]loginSession
	failures    map[string]failure
}
type invocation struct {
	Capability string          `json:"capability"`
	Operation  string          `json:"operation"`
	Node       string          `json:"node,omitempty"`
	Path       string          `json:"path,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Stream     bool            `json:"stream,omitempty"`
}

// Register installs public browser routes. Every API except login checks browser identity.
func Register(node *mesh.Node, dataDir string) ([]mesh.Capability, error) {
	dir := filepath.Join(dataDir, "web")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s := &server{node: node, dir: dir, static: os.Getenv("WEB_STATIC_DIR"), sessions: map[string]loginSession{}, failures: map[string]failure{}}
	if s.static == "" {
		s.static = "/app/web"
	}
	b, err := os.ReadFile(filepath.Join(dir, "users.json"))
	if err == nil {
		if err = json.Unmarshal(b, &s.users); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else {
		username := os.Getenv("WEB_ADMIN_USER")
		if username == "" {
			username = "admin"
		}
		password := os.Getenv("WEB_ADMIN_PASSWORD")
		passwordFile := os.Getenv("WEB_ADMIN_PASSWORD_FILE")
		if passwordFile == "" {
			passwordFile = "/identity/admin-password"
		}
		if password == "" {
			raw, e := os.ReadFile(passwordFile)
			if e == nil {
				password = strings.TrimSpace(string(raw))
			} else if !errors.Is(e, os.ErrNotExist) {
				return nil, e
			}
		}
		if password != "" {
			if len(password) < 12 || len(password) > 72 {
				return nil, errors.New("web administrator password must contain 12..72 bytes")
			}
			hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if e != nil {
				return nil, e
			}
			id, e := randomID()
			if e != nil {
				return nil, e
			}
			s.users = []User{{ID: id, Username: username, PasswordHash: string(hash), Scopes: []string{"*"}}}
			if e = s.persistUsers(); e != nil {
				return nil, e
			}
		}
	}
	node.HandlePublic("POST", "/api/auth/login", s.login)
	node.HandlePublic("GET", "/api/auth/me", s.protect(s.me))
	node.HandlePublic("POST", "/api/auth/logout", s.protect(s.logout))
	node.HandlePublic("PUT", "/api/auth/password", s.protect(s.password))
	node.HandlePublic("GET", "/api/users", s.protect(s.userList))
	node.HandlePublic("PUT", "/api/users", s.protect(s.userPut))
	node.HandlePublic("GET", "/api/topology", s.protect(s.topology))
	node.HandlePublic("PUT", "/api/policy", s.protect(s.policy))
	node.HandlePublic("POST", "/api/call", s.protect(s.call))
	node.HandlePublic("POST", "/api/container/approval", s.protect(s.containerApproval))
	node.HandlePublic("GET", "/", s.staticFile)
	return []mesh.Capability{{Schema: mesh.CapabilitySchema, ID: "web.browser", Interface: "web.browser", Version: "1.0.0", Endpoint: "/", InputSchema: mesh.JSONSchemaFor(nil), OutputSchema: json.RawMessage(`{"type":"string","contentMediaType":"text/html","description":"Public browser HTML document; browser APIs use their own HTTP routes."}`), Ready: len(s.users) > 0, Attributes: map[string]string{"configured": fmt.Sprint(len(s.users) > 0)}, Semantics: mesh.Semantics{ReadOnly: true, Idempotent: true}, Operations: []mesh.Operation{{Name: "open", Method: "GET", Path: "/", Semantics: mesh.Semantics{ReadOnly: true, Idempotent: true}}}}}, nil
}
func randomID() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}
func (s *server) persistUsers() error {
	b, e := json.MarshalIndent(s.users, "", "  ")
	if e != nil {
		return e
	}
	p := filepath.Join(s.dir, "users.json")
	if e = os.WriteFile(p+".tmp", b, 0600); e != nil {
		return e
	}
	return os.Rename(p+".tmp", p)
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		fail(w, 400, "invalid JSON: "+e.Error())
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		fail(w, 400, "one JSON value required")
		return false
	}
	return true
}
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	u, e := url.Parse(origin)
	return e == nil && u.Host == r.Host && (u.Scheme == "http" || u.Scheme == "https")
}
func secure(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
}
func (s *server) login(w http.ResponseWriter, r *http.Request) {
	secure(w)
	if !sameOrigin(r) {
		fail(w, 403, "cross-origin request rejected")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.users) == 0 {
		fail(w, 503, "web login unconfigured; set bootstrap administrator credentials")
		return
	}
	key := r.RemoteAddr
	if host, _, err := net.SplitHostPort(key); err == nil {
		key = host
	}
	f := s.failures[key]
	if f.Count >= 10 && time.Now().Before(f.Until) {
		fail(w, 429, "too many login attempts")
		return
	}
	var found *User
	for i := range s.users {
		if s.users[i].Username == in.Username {
			found = &s.users[i]
			break
		}
	}
	hash := "$2a$10$7EqJtq98hPqEX7fNZaFWoO5fYHnP6Q9rPo74yyh.1K7Pj/4F7yrUi"
	if found != nil {
		hash = found.PasswordHash
	}
	e := bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password))
	if found == nil || e != nil {
		if time.Now().After(f.Until) {
			f.Count = 0
		}
		f.Count++
		f.Until = time.Now().Add(10 * time.Minute)
		if len(s.failures) > 4096 {
			clear(s.failures)
		}
		s.failures[key] = f
		fail(w, 401, "invalid credentials")
		return
	}
	delete(s.failures, key)
	token, e := randomID()
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	csrf, e := randomID()
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	for k, v := range s.sessions {
		if time.Now().After(v.Expires) {
			delete(s.sessions, k)
		}
	}
	s.sessions[token] = loginSession{found.ID, csrf, time.Now().Add(12 * time.Hour)}
	http.SetCookie(w, &http.Cookie{Name: "whalemesh_session", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil || os.Getenv("WEB_SECURE_COOKIE") == "true", SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	respond(w, 200, map[string]any{"user": publicUser(*found), "csrf": csrf, "owner": ownerKey(*found), "mesh_principal": s.node.Manifest().Identity.Principal})
}
func publicUser(u User) User { u.PasswordHash = ""; return u }
func hasScope(u User, scope string) bool {
	for _, v := range u.Scopes {
		if v == "*" || v == scope {
			return true
		}
		if strings.HasSuffix(v, ".*") && strings.HasPrefix(scope, strings.TrimSuffix(v, "*")) {
			return true
		}
	}
	return false
}
func (s *server) identity(r *http.Request) (User, loginSession, bool) {
	c, e := r.Cookie("whalemesh_session")
	if e != nil {
		return User{}, loginSession{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[c.Value]
	if !ok || time.Now().After(session.Expires) {
		delete(s.sessions, c.Value)
		return User{}, loginSession{}, false
	}
	for _, u := range s.users {
		if u.ID == session.UserID {
			return u, session, true
		}
	}
	return User{}, loginSession{}, false
}
func (s *server) protect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secure(w)
		_, session, ok := s.identity(r)
		if !ok {
			fail(w, 401, "login required")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if !sameOrigin(r) || subtle.ConstantTimeCompare([]byte(session.CSRF), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
				fail(w, 403, "CSRF verification failed")
				return
			}
		}
		next(w, r)
	}
}
func (s *server) me(w http.ResponseWriter, r *http.Request) {
	u, session, _ := s.identity(r)
	respond(w, 200, map[string]any{"user": publicUser(u), "csrf": session.CSRF, "owner": ownerKey(u), "mesh_principal": s.node.Manifest().Identity.Principal})
}
func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Cookie("whalemesh_session")
	s.mu.Lock()
	delete(s.sessions, c.Value)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "whalemesh_session", Path: "/", Value: "", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || os.Getenv("WEB_SECURE_COOKIE") == "true"})
	respond(w, 200, map[string]bool{"ok": true})
}
func (s *server) password(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Current  string `json:"current"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	u, _, _ := s.identity(r)
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Current)) != nil {
		fail(w, 403, "current password invalid")
		return
	}
	if len(in.Password) < 12 || len(in.Password) > 72 {
		fail(w, 400, "password must contain 12..72 bytes")
		return
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.users {
		if s.users[i].ID == u.ID {
			s.users[i].PasswordHash = string(hash)
		}
	}
	if e = s.persistUsers(); e != nil {
		fail(w, 500, e.Error())
		return
	}
	for k, v := range s.sessions {
		if v.UserID == u.ID {
			delete(s.sessions, k)
		}
	}
	respond(w, 200, map[string]bool{"ok": true})
}
func (s *server) userList(w http.ResponseWriter, r *http.Request) {
	u, _, _ := s.identity(r)
	if !hasScope(u, "web.users") {
		fail(w, 403, "web.users scope required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]User, 0, len(s.users))
	for _, v := range s.users {
		out = append(out, publicUser(v))
	}
	respond(w, 200, out)
}
func (s *server) userPut(w http.ResponseWriter, r *http.Request) {
	u, _, _ := s.identity(r)
	if !hasScope(u, "web.users") {
		fail(w, 403, "web.users scope required")
		return
	}
	var in struct {
		Username string   `json:"username"`
		Password string   `json:"password"`
		Scopes   []string `json:"scopes"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Username) == "" || len(in.Username) > 128 || len(in.Password) < 12 || len(in.Password) > 72 {
		fail(w, 400, "username and password (12..72 bytes) required")
		return
	}
	for _, scope := range in.Scopes {
		if !hasScope(u, scope) {
			fail(w, 403, "cannot grant unowned scope")
			return
		}
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	id, e := randomID()
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.users {
		if v.Username == in.Username {
			fail(w, 409, "username exists")
			return
		}
	}
	s.users = append(s.users, User{ID: id, Username: in.Username, PasswordHash: string(hash), Scopes: in.Scopes})
	if e = s.persistUsers(); e != nil {
		s.users = s.users[:len(s.users)-1]
		fail(w, 500, e.Error())
		return
	}
	respond(w, 201, publicUser(s.users[len(s.users)-1]))
}
func (s *server) topology(w http.ResponseWriter, r *http.Request) {
	u, _, _ := s.identity(r)
	if !hasScope(u, "mesh.inspect") {
		fail(w, 403, "mesh.inspect scope required")
		return
	}
	manifest := s.node.Manifest()
	peers := s.node.Peers()
	resolutions := map[string]any{}
	for _, req := range manifest.Requires {
		explanation, _ := s.node.Explain(req)
		resolutions[req.ID] = explanation
	}
	respond(w, 200, map[string]any{"manifest": manifest, "peers": peers, "bindings": s.node.Bindings(), "recorded_bindings": s.node.RecordedBindings(), "resolutions": resolutions, "policies": mesh.PolicyNames()})
}
func (s *server) policy(w http.ResponseWriter, r *http.Request) {
	u, _, _ := s.identity(r)
	if !hasScope(u, "mesh.policy") {
		fail(w, 403, "mesh.policy scope required")
		return
	}
	var in struct {
		Requirement string `json:"requirement"`
		Policy      string `json:"policy"`
		Pin         string `json:"pin"`
	}
	if !decode(w, r, &in) {
		return
	}
	if e := s.node.SetPolicy(in.Requirement, in.Policy, in.Pin); e != nil {
		fail(w, 400, e.Error())
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
func ownerKey(u User) string {
	sum := sha256.Sum256([]byte(u.ID))
	return "web-" + hex.EncodeToString(sum[:12])
}
func operationPath(pattern, path string) bool {
	if path == pattern {
		return true
	}
	a := strings.Split(pattern, "/")
	b := strings.Split(path, "/")
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.HasPrefix(a[i], "{") && strings.HasSuffix(a[i], "}") {
			if b[i] == "" || b[i] == "." || b[i] == ".." {
				return false
			}
			continue
		}
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func (s *server) call(w http.ResponseWriter, r *http.Request) {
	u, _, _ := s.identity(r)
	var in invocation
	if !decode(w, r, &in) {
		return
	}
	requirement := mesh.Requirement{Capability: in.Capability}
	for _, declared := range s.node.Manifest().Requires {
		if declared.Capability == in.Capability {
			requirement = declared
			break
		}
	}
	requirement.Many = false
	if in.Node != "" {
		requirement.Pin = in.Node
	}
	bindings, e := s.node.Resolve(requirement)
	if e != nil {
		fail(w, 503, e.Error())
		return
	}
	var binding *mesh.Binding
	for i := range bindings {
		if in.Node == "" || mesh.IdentityKey(bindings[i].Node.Identity) == in.Node {
			binding = &bindings[i]
			break
		}
	}
	if binding == nil {
		fail(w, 503, "no matching capability provider")
		return
	}
	var op *mesh.Operation
	for i := range binding.Capability.Operations {
		if binding.Capability.Operations[i].Name == in.Operation {
			op = &binding.Capability.Operations[i]
			break
		}
	}
	if op == nil {
		fail(w, 400, "operation not advertised")
		return
	}
	if len(op.Scopes) == 0 {
		fail(w, 403, "operation has no advertised authorization scopes")
		return
	}
	for _, scope := range op.Scopes {
		if !hasScope(u, scope) {
			fail(w, 403, "user lacks operation scope: "+scope)
			return
		}
	}
	path := in.Path
	if path == "" {
		path = op.Path
	}
	parsed, e := url.Parse(path)
	if e != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Fragment != "" || !operationPath(op.Path, parsed.Path) {
		fail(w, 400, "path does not match operation")
		return
	}
	var input map[string]any
	if len(in.Input) > 0 && string(in.Input) != "null" {
		if e = json.Unmarshal(in.Input, &input); e != nil {
			fail(w, 400, "operation input must be object")
			return
		}
	}
	if input == nil {
		input = map[string]any{}
	}
	if e = prepareInput(u, parsed.Path, input); e != nil {
		fail(w, 403, e.Error())
		return
	}
	if in.Stream {
		input["stream"] = true
	}
	var body any = input
	q := parsed.Query()
	q.Del("owner")
	q.Del("user_id")
	if op.Method == "GET" || op.Method == "DELETE" {
		body = nil
		if delegatedPath(parsed.Path) {
			q.Set("owner", ownerKey(u))
		}
	}
	if parsed.Path == "/executor/deployments" {
		q.Set("owner", ownerKey(u))
	}
	parsed.RawQuery = q.Encode()
	path = parsed.String()
	response, e := s.node.Stream(r.Context(), *binding, op.Method, path, body)
	if e != nil {
		var remote *mesh.RemoteError
		if errors.As(e, &remote) && remote.Status >= 400 && remote.Status <= 599 {
			fail(w, remote.Status, e.Error())
		} else {
			fail(w, 502, e.Error())
		}
		return
	}
	defer response.Body.Close()
	for _, name := range []string{"Content-Type", "Content-Disposition"} {
		if v := response.Header.Get(name); v != "" {
			w.Header().Set(name, v)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(response.StatusCode)
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			if _, e = w.Write(buffer[:n]); e != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if readErr != nil {
			return
		}
	}
}
func (s *server) staticFile(w http.ResponseWriter, r *http.Request) {
	secure(w)
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	clean := filepath.Clean("/" + r.URL.Path)
	file := filepath.Join(s.static, clean)
	info, e := os.Stat(file)
	if e != nil || info.IsDir() {
		file = filepath.Join(s.static, "index.html")
	}
	if _, e = os.Stat(file); e != nil {
		fail(w, 503, "frontend assets unavailable; build web/ and configure WEB_STATIC_DIR")
		return
	}
	http.ServeFile(w, r, file)
}
func delegatedPath(path string) bool {
	for _, prefix := range []string{"/session/", "/memory/", "/secrets/", "/skills/", "/workspace/", "/events", "/stats", "/benchmark/", "/agent/", "/executor/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
func (s *server) containerApproval(w http.ResponseWriter, r *http.Request) {
	user, _, _ := s.identity(r)
	if !hasScope(user, "executor.admin") {
		fail(w, 403, "executor.admin user scope required")
		return
	}
	var in struct {
		Image string `json:"image"`
		Node  string `json:"node"`
	}
	if !decode(w, r, &in) {
		return
	}
	parts := strings.Split(in.Image, "@sha256:")
	if len(parts) != 2 || parts[0] == "" || len(parts[1]) != 64 {
		fail(w, 400, "immutable sha256 image digest required")
		return
	}
	if _, e := hex.DecodeString(parts[1]); e != nil {
		fail(w, 400, "invalid sha256 digest")
		return
	}
	bindings, e := s.node.Resolve(mesh.Requirement{Capability: "container.manage", Pin: in.Node})
	if e != nil {
		fail(w, 503, e.Error())
		return
	}
	if len(bindings) == 0 {
		fail(w, 503, "executor unavailable")
		return
	}
	binding := &bindings[0]
	identityFile := os.Getenv("MESH_IDENTITY_FILE")
	if identityFile == "" {
		identityFile = "/identity/identity.json"
	}
	raw, e := os.ReadFile(identityFile)
	if e != nil {
		fail(w, 503, "approval signing identity unavailable")
		return
	}
	var identity struct {
		Principal  string `json:"principal"`
		PrivateKey string `json:"private_key"`
	}
	if e = json.Unmarshal(raw, &identity); e != nil {
		fail(w, 500, "invalid signing identity")
		return
	}
	key, e := base64.StdEncoding.DecodeString(identity.PrivateKey)
	if e != nil || len(key) != ed25519.PrivateKeySize || identity.Principal != s.node.Manifest().Identity.Principal {
		fail(w, 500, "invalid signing key")
		return
	}
	id, e := randomID()
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	approval := executor.Approval{ID: id, Issuer: identity.Principal, Subject: ownerKey(user), Namespace: s.node.Manifest().Identity.Namespace, Action: "image.use", Resource: in.Image, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	payload, e := executor.ApprovalPayload(approval)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	approval.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(key), payload))
	var output executor.Approval
	if e = s.node.Call(r.Context(), *binding, "POST", "/executor/approvals", approval, &output); e != nil {
		fail(w, 502, e.Error())
		return
	}
	respond(w, 201, output)
}

// Requirements declares optional browser dependencies; absent providers remain visible as unavailable.
func Requirements() []mesh.Requirement {
	return []mesh.Requirement{{ID: "web.agent", Capability: "agent.run", Interface: "agent.run", Version: "^1.0.0", Optional: true, Sticky: true}, {ID: "web.sessions", Capability: "session.store", Interface: "session.store", Version: "^1.0.0", Optional: true, Sticky: true}, {ID: "web.models", Capability: "llm.models.list", Interface: "llm.models.list", Version: "^1.0.0", Optional: true}}
}

// prepareInput is the user-to-Mesh identity boundary. Client-supplied identity is never forwarded.
func prepareInput(user User, path string, input map[string]any) error {
	if path == "/secrets/resolve" {
		return errors.New("raw secret resolution is restricted to runtime tools")
	}
	delete(input, "owner")
	delete(input, "user_id")
	if delegatedPath(path) && path != "/executor/deployments" {
		input["owner"] = ownerKey(user)
	}
	if path == "/agent/run" {
		id, _ := input["session_id"].(string)
		prefix := ownerKey(user) + ":"
		if id == "" {
			var e error
			id, e = randomID()
			if e != nil {
				return e
			}
		}
		if !strings.HasPrefix(id, prefix) {
			id = prefix + id
		}
		input["session_id"] = id
	}
	if strings.HasPrefix(path, "/session/") {
		if id, ok := input["session_id"].(string); ok && !strings.HasPrefix(id, ownerKey(user)+":") {
			return errors.New("session belongs to another user")
		}
	}
	return nil
}
