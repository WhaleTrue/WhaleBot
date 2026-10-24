// Package providers implements durable, principal-isolated mesh storage capabilities.
package providers

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/WhaleTrue/WhaleBot/pkg/mesh"
	_ "modernc.org/sqlite"
)

const maxBody = 12 << 20

var stores sync.Map

type store struct {
	db        *sql.DB
	node      *mesh.Node
	namespace string
	stateID   string
	gcm       cipher.AEAD
}
type Scope struct {
	Owner string `json:"owner,omitempty"`
}
type Page struct {
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
}
type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string   { return e.message }
func bad(message string) error      { return &apiError{400, message} }
func conflict(message string) error { return &apiError{409, message} }

var missing = &apiError{404, "record not found"}

func (s *store) owner(r *http.Request, scope Scope, family string) (string, error) {
	principal := s.node.Principal(r)
	if principal == "" {
		return "", &apiError{401, "authenticated principal required"}
	}
	if scope.Owner == "" {
		return "principal:" + principal, nil
	}
	if !validID(scope.Owner) {
		return "", bad("invalid owner")
	}
	if !s.node.Authorized(r, family+".delegate") {
		return "", &apiError{403, "delegated ownership requires explicit scope"}
	}
	if strings.HasPrefix(scope.Owner, "node:") {
		node := strings.TrimPrefix(scope.Owner, "node:")
		if node == "" {
			return "", bad("invalid node owner")
		}
		return "principal:" + node, nil
	}
	return "user:" + scope.Owner, nil
}
func validID(id string) bool {
	return id != "" && len(id) <= 256 && !strings.ContainsAny(id, "\x00\r\n")
}
func (p Page) bounds() (int, int, error) {
	if p.Limit < 0 || p.Limit > 200 || p.Offset < 0 || p.Offset > 100000 {
		return 0, 0, bad("invalid pagination")
	}
	if p.Limit == 0 {
		p.Limit = 50
	}
	return p.Limit, p.Offset, nil
}
func validateMetadata(m map[string]string) error {
	if len(m) > 64 {
		return bad("too many metadata entries")
	}
	for k, v := range m {
		if !validID(k) || len(v) > 4096 {
			return bad("invalid metadata")
		}
	}
	return nil
}
func decode(r *http.Request, value any) error {
	reader := bufio.NewReader(r.Body)
	for {
		first, err := reader.ReadByte()
		if err != nil {
			return bad("JSON object required")
		}
		if first == ' ' || first == '\t' || first == '\r' || first == '\n' {
			continue
		}
		if first != '{' {
			return bad("JSON object required")
		}
		if err = reader.UnreadByte(); err != nil {
			return err
		}
		break
	}
	d := json.NewDecoder(reader)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return bad("invalid JSON: " + err.Error())
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return bad("exactly one JSON value required")
	}
	return nil
}
func bind[T any](s *store, method, path, scope string, fn func(*http.Request, T) (any, error)) mesh.Operation {
	s.node.Handle(method, path, []string{scope}, func(w http.ResponseWriter, r *http.Request) {
		if !s.node.Authorized(r, scope) {
			respondError(w, &apiError{403, "scope denied"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		var input T
		if method != "GET" {
			if err := decode(r, &input); err != nil {
				respondError(w, err)
				return
			}
		}
		output, err := fn(r, input)
		if err != nil {
			respondError(w, err)
			return
		}
		mesh.WriteJSON(w, http.StatusOK, output)
	})
	read := strings.HasSuffix(scope, ".read") || strings.HasSuffix(scope, ".search") || scope == "secrets.resolve"
	return mesh.Operation{Name: strings.TrimPrefix(path, "/"), Method: method, Path: path, Scopes: []string{scope}, Semantics: mesh.Semantics{ReadOnly: read, Idempotent: read || method == "PUT", Mutating: !read}}
}
func respondError(w http.ResponseWriter, err error) {
	status := 500
	message := "storage operation failed"
	var ae *apiError
	if errors.As(err, &ae) {
		status = ae.status
		message = ae.message
	}
	mesh.WriteError(w, status, strings.ReplaceAll(strings.ToLower(http.StatusText(status)), " ", "_"), message)
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *store) read(ctx context.Context, q queryer, owner, kind, id string, value any) error {
	var data []byte
	err := q.QueryRowContext(ctx, `SELECT data FROM records WHERE namespace=? AND owner=? AND kind=? AND id=?`, s.namespace, owner, kind, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return missing
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
func (s *store) put(ctx context.Context, tx *sql.Tx, owner, kind, id string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	text := ""
	switch v := value.(type) {
	case Note:
		text = v.Key + " " + v.Value
	case Skill:
		if kind == "skill" {
			var builder strings.Builder
			builder.WriteString(v.ID + " " + v.Title + " " + v.Description + " " + strings.Join(v.Tags, " "))
			for _, file := range v.Files {
				builder.WriteByte(' ')
				builder.WriteString(file.Path)
				builder.WriteByte(' ')
				builder.Write(file.Content)
			}
			text = builder.String()
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO records(namespace,owner,kind,id,data,updated,search) VALUES(?,?,?,?,?,?,?) ON CONFLICT(namespace,owner,kind,id) DO UPDATE SET data=excluded.data,updated=excluded.updated,search=excluded.search`, s.namespace, owner, kind, id, data, time.Now().UnixNano(), strings.ToLower(text))
	return err
}
func (s *store) remove(ctx context.Context, owner, kind, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM records WHERE namespace=? AND owner=? AND kind=? AND id=?`, s.namespace, owner, kind, id)
	return err
}
func (s *store) list(ctx context.Context, owner, kind string, limit, offset int) ([]json.RawMessage, error) {
	projection := "data"
	if kind == "session" {
		projection = "json_remove(data,'$.messages')"
	} else if kind == "skill" {
		projection = "json_remove(data,'$.files')"
	} else if kind == "secret" {
		projection = "json_remove(data,'$.ciphertext','$.nonce')"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+projection+` FROM records WHERE namespace=? AND owner=? AND kind=? ORDER BY updated DESC,id LIMIT ? OFFSET ?`, s.namespace, owner, kind, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecords(rows)
}
func scanRecords(rows *sql.Rows) ([]json.RawMessage, error) {
	items := []json.RawMessage{}
	size := 0
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		size += len(data)
		if size > maxBody-65536 {
			return nil, &apiError{413, "result exceeds response limit; reduce limit"}
		}
		items = append(items, json.RawMessage(data))
	}
	return items, rows.Err()
}
func (s *store) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Register installs only selected capabilities. Existing data never migrates between providers.
func Register(node *mesh.Node, dataDir string) ([]mesh.Capability, error) {
	if node == nil {
		return nil, fmt.Errorf("node required")
	}
	if _, ok := stores.Load(node); ok {
		return nil, fmt.Errorf("providers already registered")
	}
	selected := map[string]bool{}
	raw := strings.TrimSpace(os.Getenv("PROVIDER_CAPABILITIES"))
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id != "" {
			selected[id] = true
		}
	}
	known := map[string]bool{"session.store": true, "memory.store": true, "memory.search": true, "secrets.resolve": true, "skills.search": true, "workspace.files": true, "events.write": true, "stats.ingest": true}
	for id := range selected {
		if !known[id] {
			return nil, fmt.Errorf("unknown provider capability %q", id)
		}
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	// ponytail: one connection serializes local transactions; add a bounded read pool with immediate writes if measured contention warrants it.
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "providers.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	failed := true
	defer func() {
		if failed {
			_ = db.Close()
		}
	}()
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=10000;
 CREATE TABLE IF NOT EXISTS provider_meta(k TEXT PRIMARY KEY,value TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS records(namespace TEXT NOT NULL,owner TEXT NOT NULL,kind TEXT NOT NULL,id TEXT NOT NULL,data BLOB NOT NULL,updated INTEGER NOT NULL,search TEXT NOT NULL DEFAULT '',PRIMARY KEY(namespace,owner,kind,id));
 CREATE INDEX IF NOT EXISTS records_recent ON records(namespace,owner,kind,updated);
 CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY AUTOINCREMENT,namespace TEXT NOT NULL,owner TEXT NOT NULL,ts INTEGER NOT NULL,kind TEXT NOT NULL,session_id TEXT NOT NULL,data BLOB NOT NULL,prompt INTEGER NOT NULL,completion INTEGER NOT NULL,total INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS events_owner ON events(namespace,owner,id);
 CREATE INDEX IF NOT EXISTS events_time ON events(namespace,owner,ts);`); err != nil {
		return nil, err
	}
	s := &store{db: db, node: node, namespace: node.Manifest().Identity.Namespace}
	var stateBytes [16]byte
	if _, err = rand.Read(stateBytes[:]); err != nil {
		return nil, err
	}
	if _, err = db.Exec(`INSERT INTO provider_meta(k,value) VALUES('state_id',?) ON CONFLICT(k) DO NOTHING`, hex.EncodeToString(stateBytes[:])); err != nil {
		return nil, err
	}
	if err = db.QueryRow(`SELECT value FROM provider_meta WHERE k='state_id'`).Scan(&s.stateID); err != nil {
		return nil, err
	}
	if raw == "" || selected["secrets.resolve"] {
		key, err := loadKey(db, dataDir)
		if err != nil {
			return nil, err
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		s.gcm, err = cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
	}
	caps := []mesh.Capability{}
	add := func(id string, register func() []mesh.Operation, scopes []string) {
		if raw != "" && !selected[id] {
			return
		}
		ops := register()
		capability := mesh.Capability{Schema: "whalemesh.capability/v1", ID: id, Interface: id, Version: "1.0.0", Endpoint: ops[0].Path, Ready: true, Operations: ops, Scopes: scopes, Attributes: map[string]string{"storage_boundary": node.Manifest().Identity.Principal, "stateful": "true", "state_id": s.stateID, "persistence": "sqlite", "ownership": "namespace/principal-or-authorized-user", "sticky": "required"}}
		if id == "secrets.resolve" {
			capability.TransportSecurity = "tls"
		}
		describe(&capability)
		caps = append(caps, capability)
	}
	add("session.store", s.sessions, []string{"session.read", "session.write"})
	add("memory.store", s.memoryStore, []string{"memory.read", "memory.write"})
	add("memory.search", s.memorySearch, []string{"memory.search"})
	add("secrets.resolve", s.secrets, []string{"secrets.resolve", "secrets.read", "secrets.write"})
	add("skills.search", s.skills, []string{"skills.search", "skills.read", "skills.write"})
	add("workspace.files", s.workspace, []string{"workspace.read", "workspace.write"})
	add("events.write", s.eventRoutes, []string{"events.write", "events.read"})
	add("stats.ingest", s.statRoutes, []string{"stats.ingest", "stats.read"})
	stores.Store(node, s)
	failed = false
	return caps, nil
}

// Close releases provider resources after the node has stopped serving requests.
func Close(node *mesh.Node) error {
	value, ok := stores.LoadAndDelete(node)
	if !ok {
		return nil
	}
	return value.(*store).db.Close()
}
func loadKey(db *sql.DB, dir string) ([]byte, error) {
	path := filepath.Join(dir, "secrets.key")
	data, err := os.ReadFile(path)
	if err == nil {
		key, e := hex.DecodeString(strings.TrimSpace(string(data)))
		if e != nil || len(key) != 32 {
			return nil, fmt.Errorf("invalid secrets.key; restore original key")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM records WHERE kind='secret'`).Scan(&count); err != nil {
		return nil, err
	}
	if count != 0 {
		return nil, fmt.Errorf("secrets.key missing for existing ciphertext; restore original key")
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	_, writeErr := f.WriteString(hex.EncodeToString(key))
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return nil, writeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return key, nil
}
