package providers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/WhaleMesh/WhaleMesh/pkg/mesh"
)

const maxWorkspaceBytes = 64 << 20
const maxWorkspaceFiles = 4096

type WorkspaceRequest struct {
	Scope
	Page
	Workspace string `json:"workspace,omitempty"`
	Path      string `json:"path,omitempty"`
	Artifact  bool   `json:"artifact,omitempty"`
}
type WorkspaceInput struct {
	Scope
	Workspace string `json:"workspace"`
	Path      string `json:"path"`
	Content   []byte `json:"content"`
	Artifact  bool   `json:"artifact,omitempty"`
}
type WorkspaceFile struct {
	Workspace string    `json:"workspace"`
	Path      string    `json:"path"`
	Content   []byte    `json:"content,omitempty"`
	Artifact  bool      `json:"artifact"`
	Size      int       `json:"size"`
	UpdatedAt time.Time `json:"updated_at"`
}
type Workspace struct {
	Workspace string    `json:"workspace"`
	CreatedAt time.Time `json:"created_at"`
}

func fileKey(workspace, path string, artifact bool) string {
	data, _ := json.Marshal([]any{workspace, path, artifact})
	return string(data)
}
func (s *store) putWorkspace(r *http.Request, input WorkspaceInput) (any, error) {
	owner, err := s.owner(r, input.Scope, "workspace")
	if err != nil {
		return nil, err
	}
	if !validID(input.Workspace) || !safePath(input.Path) || len(input.Content) > 8<<20 {
		return nil, bad("invalid workspace file or file exceeds 8 MiB")
	}
	file := WorkspaceFile{Workspace: input.Workspace, Path: input.Path, Content: input.Content, Artifact: input.Artifact, Size: len(input.Content), UpdatedAt: time.Now().UTC()}
	key := fileKey(input.Workspace, input.Path, input.Artifact)
	err = s.transaction(r.Context(), func(tx *sql.Tx) error {
		var count, size int
		err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*),COALESCE(SUM(json_extract(data,'$.size')),0) FROM records WHERE namespace=? AND owner=? AND kind='workspace-file' AND json_extract(data,'$.workspace')=? AND id<>?`, s.namespace, owner, input.Workspace, key).Scan(&count, &size)
		if err != nil {
			return err
		}
		if count >= maxWorkspaceFiles || size+file.Size > maxWorkspaceBytes {
			return &apiError{413, "workspace quota exceeded (4096 files, 64 MiB)"}
		}
		if input.Artifact {
			var present int
			if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM records WHERE namespace=? AND owner=? AND kind='workspace-file' AND id=?`, s.namespace, owner, key).Scan(&present); err != nil {
				return err
			}
			if present != 0 {
				return conflict("artifacts are immutable; use a new path")
			}
		}
		if err = s.put(r.Context(), tx, owner, "workspace-file", key, file); err != nil {
			return err
		}
		_, err = tx.ExecContext(r.Context(), `INSERT INTO records(namespace,owner,kind,id,data,updated,search) VALUES(?,?,'workspace',?,?,?,'') ON CONFLICT(namespace,owner,kind,id) DO NOTHING`, s.namespace, owner, input.Workspace, mustJSON(Workspace{Workspace: input.Workspace, CreatedAt: file.UpdatedAt}), time.Now().UnixNano())
		return err
	})
	file.Content = nil
	return file, err
}
func mustJSON(value any) []byte { data, _ := json.Marshal(value); return data }
func (s *store) workspace() []mesh.Operation {
	return []mesh.Operation{
		bind(s, "PUT", "/workspace/put", "workspace.write", s.putWorkspace),
		bind(s, "POST", "/workspace/create", "workspace.write", func(r *http.Request, input WorkspaceRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "workspace")
			if err != nil {
				return nil, err
			}
			if !validID(input.Workspace) {
				return nil, bad("workspace required")
			}
			workspace := Workspace{Workspace: input.Workspace, CreatedAt: time.Now().UTC()}
			err = s.transaction(r.Context(), func(tx *sql.Tx) error {
				var count int
				if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM records WHERE namespace=? AND owner=? AND kind='workspace' AND id=?`, s.namespace, owner, input.Workspace).Scan(&count); err != nil {
					return err
				}
				if count != 0 {
					return conflict("workspace already exists")
				}
				return s.put(r.Context(), tx, owner, "workspace", input.Workspace, workspace)
			})
			return workspace, err
		}),
		bind(s, "POST", "/workspace/get", "workspace.read", func(r *http.Request, input WorkspaceRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "workspace")
			if err != nil {
				return nil, err
			}
			if !validID(input.Workspace) || !safePath(input.Path) {
				return nil, bad("workspace and safe path required")
			}
			var file WorkspaceFile
			err = s.read(r.Context(), s.db, owner, "workspace-file", fileKey(input.Workspace, input.Path, input.Artifact), &file)
			if file.Content == nil {
				file.Content = []byte{}
			}
			return file, err
		}),
		bind(s, "POST", "/workspace/list", "workspace.read", func(r *http.Request, input WorkspaceRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "workspace")
			if err != nil {
				return nil, err
			}
			limit, offset, err := input.Page.bounds()
			if err != nil {
				return nil, err
			}
			if input.Workspace == "" {
				items, err := s.list(r.Context(), owner, "workspace", limit, offset)
				return map[string]any{"items": items}, err
			}
			if !validID(input.Workspace) || (input.Path != "" && !safePath(input.Path)) {
				return nil, bad("invalid workspace or path")
			}
			rows, err := s.db.QueryContext(r.Context(), `SELECT json_remove(data,'$.content') FROM records WHERE namespace=? AND owner=? AND kind='workspace-file' AND json_extract(data,'$.workspace')=? AND json_extract(data,'$.artifact')=? AND (?='' OR substr(json_extract(data,'$.path'),1,length(?))=?) ORDER BY json_extract(data,'$.path') LIMIT ? OFFSET ?`, s.namespace, owner, input.Workspace, input.Artifact, input.Path, input.Path, input.Path, limit, offset)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			items := []WorkspaceFile{}
			for rows.Next() {
				var data []byte
				var file WorkspaceFile
				if err = rows.Scan(&data); err != nil {
					return nil, err
				}
				if err = json.Unmarshal(data, &file); err != nil {
					return nil, err
				}
				items = append(items, file)
			}
			return map[string]any{"items": items}, rows.Err()
		}),
		bind(s, "POST", "/workspace/delete", "workspace.write", func(r *http.Request, input WorkspaceRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "workspace")
			if err != nil {
				return nil, err
			}
			if !validID(input.Workspace) || (input.Path != "" && !safePath(input.Path)) {
				return nil, bad("invalid workspace or path")
			}
			if input.Path != "" {
				err = s.remove(r.Context(), owner, "workspace-file", fileKey(input.Workspace, input.Path, input.Artifact))
			} else {
				err = s.transaction(r.Context(), func(tx *sql.Tx) error {
					_, err := tx.ExecContext(r.Context(), `DELETE FROM records WHERE namespace=? AND owner=? AND ((kind='workspace' AND id=?) OR (kind='workspace-file' AND json_extract(data,'$.workspace')=?))`, s.namespace, owner, input.Workspace, input.Workspace)
					return err
				})
			}
			return map[string]bool{"deleted": err == nil}, err
		}),
	}
}
