package providers

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/WhaleMesh/WhaleMesh/pkg/mesh"
)

type Note struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}
type KeyRequest struct {
	Scope
	Key string `json:"key"`
}
type NoteInput struct {
	Scope
	Key   string `json:"key"`
	Value string `json:"value"`
}
type SearchRequest struct {
	Scope
	Page
	Query string `json:"query"`
}

func (s *store) search(r *http.Request, scope Scope, family, kind string, input SearchRequest) (any, error) {
	owner, err := s.owner(r, scope, family)
	if err != nil {
		return nil, err
	}
	limit, offset, err := input.Page.bounds()
	if err != nil {
		return nil, err
	}
	query := strings.ToLower(strings.TrimSpace(input.Query))
	if query == "" || len(query) > 512 {
		return nil, bad("search query must contain 1 to 512 bytes")
	}
	tokens := strings.FieldsFunc(query, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	if len(tokens) == 0 {
		return nil, bad("search query requires letters or numbers")
	}
	if len(tokens) > 16 {
		tokens = tokens[:16]
	}
	terms := make([]string, 0, len(tokens))
	arguments := []any{s.namespace, owner, kind}
	for _, token := range tokens {
		terms = append(terms, "(instr(search,?)>0)")
		arguments = append(arguments, token)
	}
	score := strings.Join(terms, "+")
	for _, token := range tokens {
		arguments = append(arguments, token)
	}
	arguments = append(arguments, limit, offset)
	// ponytail: ranked token substrings scan one owner's records; use FTS when measured volume warrants an index.
	rows, err := s.db.QueryContext(r.Context(), `SELECT data FROM records WHERE namespace=? AND owner=? AND kind=? AND (`+score+`)>0 ORDER BY (`+score+`) DESC,updated DESC,id LIMIT ? OFFSET ?`, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanRecords(rows)
	return map[string]any{"items": items}, err
}
func (s *store) memoryStore() []mesh.Operation {
	return []mesh.Operation{
		bind(s, "POST", "/memory/get", "memory.read", func(r *http.Request, input KeyRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "memory")
			if err != nil {
				return nil, err
			}
			if !validID(input.Key) {
				return nil, bad("key required")
			}
			var note Note
			err = s.read(r.Context(), s.db, owner, "note", input.Key, &note)
			return note, err
		}),
		bind(s, "PUT", "/memory/put", "memory.write", func(r *http.Request, input NoteInput) (any, error) {
			owner, err := s.owner(r, input.Scope, "memory")
			if err != nil {
				return nil, err
			}
			if !validID(input.Key) || len(input.Value) > 256<<10 {
				return nil, bad("invalid note or note exceeds 256 KiB")
			}
			note := Note{Key: input.Key, Value: input.Value, UpdatedAt: time.Now().UTC()}
			err = s.transaction(r.Context(), func(tx *sql.Tx) error { return s.put(r.Context(), tx, owner, "note", input.Key, note) })
			return note, err
		}),
		bind(s, "POST", "/memory/delete", "memory.write", func(r *http.Request, input KeyRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "memory")
			if err != nil {
				return nil, err
			}
			if !validID(input.Key) {
				return nil, bad("key required")
			}
			err = s.remove(r.Context(), owner, "note", input.Key)
			return map[string]bool{"deleted": err == nil}, err
		}),
		bind(s, "POST", "/memory/list", "memory.read", func(r *http.Request, input ListRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "memory")
			if err != nil {
				return nil, err
			}
			limit, offset, err := input.Page.bounds()
			if err != nil {
				return nil, err
			}
			items, err := s.list(r.Context(), owner, "note", limit, offset)
			return map[string]any{"items": items}, err
		}),
	}
}
func (s *store) memorySearch() []mesh.Operation {
	return []mesh.Operation{bind(s, "POST", "/memory/search", "memory.search", func(r *http.Request, input SearchRequest) (any, error) {
		return s.search(r, input.Scope, "memory", "note", input)
	})}
}

type SecretInput struct {
	Scope
	Key   string `json:"key"`
	Value string `json:"value"`
	Note  string `json:"note,omitempty"`
}
type Secret struct {
	Key       string    `json:"key"`
	Value     string    `json:"value,omitempty"`
	Note      string    `json:"note"`
	UpdatedAt time.Time `json:"updated_at"`
}
type encryptedSecret struct {
	Key        string    `json:"key"`
	Ciphertext []byte    `json:"ciphertext"`
	Nonce      []byte    `json:"nonce"`
	Note       string    `json:"note"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (s *store) aad(owner, key string) []byte {
	data, _ := json.Marshal([]string{s.namespace, owner, key})
	return data
}
func (s *store) seal(owner, key, value, note string) (encryptedSecret, error) {
	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return encryptedSecret{}, err
	}
	return encryptedSecret{Key: key, Ciphertext: s.gcm.Seal(nil, nonce, []byte(value), s.aad(owner, key)), Nonce: nonce, Note: note, UpdatedAt: time.Now().UTC()}, nil
}
func (s *store) open(owner string, secret encryptedSecret) (string, error) {
	if len(secret.Nonce) != s.gcm.NonceSize() {
		return "", &apiError{500, "secret ciphertext invalid"}
	}
	data, err := s.gcm.Open(nil, secret.Nonce, secret.Ciphertext, s.aad(owner, secret.Key))
	if err != nil {
		return "", &apiError{500, "secret decryption failed; restore original key"}
	}
	return string(data), nil
}
func (s *store) secrets() []mesh.Operation {
	return []mesh.Operation{
		bind(s, "POST", "/secrets/resolve", "secrets.resolve", func(r *http.Request, input KeyRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "secrets")
			if err != nil {
				return nil, err
			}
			if !validID(input.Key) {
				return nil, bad("key required")
			}
			var encrypted encryptedSecret
			if err = s.read(r.Context(), s.db, owner, "secret", input.Key, &encrypted); err != nil {
				return nil, err
			}
			value, err := s.open(owner, encrypted)
			return Secret{Key: encrypted.Key, Value: value, Note: encrypted.Note, UpdatedAt: encrypted.UpdatedAt}, err
		}),
		bind(s, "PUT", "/secrets/put", "secrets.write", func(r *http.Request, input SecretInput) (any, error) {
			owner, err := s.owner(r, input.Scope, "secrets")
			if err != nil {
				return nil, err
			}
			if !validID(input.Key) || input.Value == "" || len(input.Value) > 65536 || len(input.Note) > 4096 {
				return nil, bad("invalid secret")
			}
			encrypted, err := s.seal(owner, input.Key, input.Value, input.Note)
			if err != nil {
				return nil, err
			}
			err = s.transaction(r.Context(), func(tx *sql.Tx) error { return s.put(r.Context(), tx, owner, "secret", input.Key, encrypted) })
			return Secret{Key: encrypted.Key, Note: encrypted.Note, UpdatedAt: encrypted.UpdatedAt}, err
		}),
		bind(s, "POST", "/secrets/list", "secrets.read", func(r *http.Request, input ListRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "secrets")
			if err != nil {
				return nil, err
			}
			limit, offset, err := input.Page.bounds()
			if err != nil {
				return nil, err
			}
			raw, err := s.list(r.Context(), owner, "secret", limit, offset)
			if err != nil {
				return nil, err
			}
			items := []Secret{}
			for _, data := range raw {
				var encrypted encryptedSecret
				if err = json.Unmarshal(data, &encrypted); err != nil {
					return nil, err
				}
				items = append(items, Secret{Key: encrypted.Key, Note: encrypted.Note, UpdatedAt: encrypted.UpdatedAt})
			}
			return map[string]any{"items": items}, nil
		}),
		bind(s, "POST", "/secrets/delete", "secrets.write", func(r *http.Request, input KeyRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "secrets")
			if err != nil {
				return nil, err
			}
			if !validID(input.Key) {
				return nil, bad("key required")
			}
			err = s.remove(r.Context(), owner, "secret", input.Key)
			return map[string]bool{"deleted": err == nil}, err
		}),
	}
}
