package providers

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/WhaleTrue/WhaleBot/pkg/mesh"
)

const maxSkillFile = 512 << 10
const maxSkillPackage = 2 << 20

type File struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
}
type Skill struct {
	ID          string    `json:"id"`
	Version     string    `json:"version"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Tags        []string  `json:"tags"`
	Files       []File    `json:"files"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type SkillInput struct {
	Scope
	ID          string   `json:"id"`
	Version     string   `json:"version"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Files       []File   `json:"files"`
}
type SkillRequest struct {
	Scope
	Page
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
}
type SkillImport struct {
	Scope
	ID          string   `json:"id"`
	Version     string   `json:"version"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Archive     []byte   `json:"archive"`
}
type SkillFileRequest struct {
	Scope
	ID         string `json:"id"`
	Version    string `json:"version,omitempty"`
	NewVersion string `json:"new_version,omitempty"`
	Path       string `json:"path"`
	Content    []byte `json:"content,omitempty"`
}

func safePath(name string) bool {
	if name == "" || len(name) > 512 || path.IsAbs(name) || strings.ContainsAny(name, "\\:\x00") || path.Clean(name) != name {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.TrimRight(part, " .") != part {
			return false
		}
	}
	return true
}
func versionKey(id, version string) string {
	data, _ := json.Marshal([]string{id, version})
	return string(data)
}
func validateSkill(input SkillInput) error {
	if !validID(input.ID) || !validID(input.Version) || len(input.Title) > 512 || len(input.Description) > 16384 || len(input.Tags) > 32 || len(input.Files) == 0 || len(input.Files) > 256 {
		return bad("invalid skill metadata or file count")
	}
	for _, tag := range input.Tags {
		if len(tag) > 128 {
			return bad("skill tag too long")
		}
	}
	seen := map[string]bool{}
	total := 0
	main := false
	for _, file := range input.Files {
		if !safePath(file.Path) || seen[strings.ToLower(file.Path)] {
			return bad("invalid or duplicate skill path")
		}
		seen[strings.ToLower(file.Path)] = true
		if len(file.Content) > maxSkillFile {
			return bad("skill file exceeds 512 KiB")
		}
		total += len(file.Content)
		if file.Path == "SKILL.md" {
			main = len(bytes.TrimSpace(file.Content)) > 0 && utf8.Valid(file.Content)
		}
	}
	if !main {
		return bad("nonempty UTF-8 SKILL.md required")
	}
	if total > maxSkillPackage {
		return bad("skill package exceeds 2 MiB")
	}
	return nil
}
func (s *store) getSkill(r *http.Request, owner, id, version string) (Skill, error) {
	var skill Skill
	if !validID(id) {
		return skill, bad("skill id required")
	}
	kind, key := "skill", id
	if version != "" {
		if !validID(version) {
			return skill, bad("invalid version")
		}
		kind, key = "skill-version", versionKey(id, version)
	}
	err := s.read(r.Context(), s.db, owner, kind, key, &skill)
	return skill, err
}
func (s *store) saveSkill(r *http.Request, input SkillInput) (any, error) {
	owner, err := s.owner(r, input.Scope, "skills")
	if err != nil {
		return nil, err
	}
	if err = validateSkill(input); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	skill := Skill{ID: input.ID, Version: input.Version, Title: input.Title, Description: input.Description, Tags: input.Tags, Files: input.Files, CreatedAt: now, UpdatedAt: now}
	if skill.Title == "" {
		skill.Title = skill.ID
	}
	if skill.Tags == nil {
		skill.Tags = []string{}
	}
	sort.Slice(skill.Files, func(i, j int) bool { return skill.Files[i].Path < skill.Files[j].Path })
	err = s.transaction(r.Context(), func(tx *sql.Tx) error {
		var prior Skill
		err := s.read(r.Context(), tx, owner, "skill-version", versionKey(skill.ID, skill.Version), &prior)
		if err == nil {
			return conflict("skill version already exists; publish a new version")
		}
		if !errors.Is(err, missing) {
			return err
		}
		err = s.read(r.Context(), tx, owner, "skill", skill.ID, &prior)
		if err == nil {
			skill.CreatedAt = prior.CreatedAt
		} else if !errors.Is(err, missing) {
			return err
		}
		if err = s.put(r.Context(), tx, owner, "skill-version", versionKey(skill.ID, skill.Version), skill); err != nil {
			return err
		}
		return s.put(r.Context(), tx, owner, "skill", skill.ID, skill)
	})
	return skill, err
}
func zipFiles(archive []byte) ([]File, error) {
	if len(archive) == 0 || len(archive) > 10<<20 {
		return nil, bad("ZIP exceeds 10 MiB or is empty")
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, bad("invalid ZIP")
	}
	if len(reader.File) > 512 {
		return nil, bad("too many ZIP entries")
	}
	files := []File{}
	seen := map[string]bool{}
	total := 0
	for _, entry := range reader.File {
		name := strings.TrimSuffix(entry.Name, "/")
		if !safePath(name) {
			return nil, bad("unsafe ZIP path")
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		if !entry.Mode().IsRegular() {
			return nil, bad("ZIP links and special files forbidden")
		}
		fold := strings.ToLower(name)
		if seen[fold] {
			return nil, bad("duplicate ZIP path")
		}
		seen[fold] = true
		if entry.UncompressedSize64 > maxSkillFile {
			return nil, bad("ZIP file exceeds 512 KiB")
		}
		rc, err := entry.Open()
		if err != nil {
			return nil, bad("cannot open ZIP entry")
		}
		data, readErr := io.ReadAll(io.LimitReader(rc, maxSkillFile+1))
		closeErr := rc.Close()
		if readErr != nil || closeErr != nil {
			return nil, bad("invalid ZIP entry")
		}
		if len(data) > maxSkillFile {
			return nil, bad("ZIP file exceeds 512 KiB")
		}
		total += len(data)
		if total > maxSkillPackage {
			return nil, bad("ZIP package exceeds 2 MiB")
		}
		files = append(files, File{Path: name, Content: data})
	}
	rootMain := false
	for _, file := range files {
		if file.Path == "SKILL.md" {
			rootMain = true
		}
	}
	if !rootMain && len(files) > 0 {
		prefix := strings.SplitN(files[0].Path, "/", 2)[0] + "/"
		wrapped := true
		for _, file := range files {
			if !strings.HasPrefix(file.Path, prefix) {
				wrapped = false
				break
			}
		}
		if wrapped {
			for i := range files {
				files[i].Path = strings.TrimPrefix(files[i].Path, prefix)
			}
		}
	}
	return files, nil
}
func (s *store) editSkillFile(r *http.Request, input SkillFileRequest, remove bool) (any, error) {
	owner, err := s.owner(r, input.Scope, "skills")
	if err != nil {
		return nil, err
	}
	if !safePath(input.Path) || !validID(input.NewVersion) {
		return nil, bad("safe path and new_version required")
	}
	skill, err := s.getSkill(r, owner, input.ID, input.Version)
	if err != nil {
		return nil, err
	}
	files := make([]File, 0, len(skill.Files)+1)
	found := false
	for _, file := range skill.Files {
		if file.Path == input.Path {
			found = true
			if !remove {
				files = append(files, File{Path: input.Path, Content: input.Content})
			}
		} else {
			files = append(files, file)
		}
	}
	if !found && !remove {
		files = append(files, File{Path: input.Path, Content: input.Content})
	}
	if remove && !found {
		return nil, missing
	}
	return s.saveSkill(r, SkillInput{Scope: input.Scope, ID: skill.ID, Version: input.NewVersion, Title: skill.Title, Description: skill.Description, Tags: skill.Tags, Files: files})
}
func (s *store) skills() []mesh.Operation {
	return []mesh.Operation{
		bind(s, "POST", "/skills/search", "skills.search", func(r *http.Request, input SearchRequest) (any, error) {
			return s.search(r, input.Scope, "skills", "skill", input)
		}),
		bind(s, "POST", "/skills/list", "skills.read", func(r *http.Request, input ListRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "skills")
			if err != nil {
				return nil, err
			}
			limit, offset, err := input.Page.bounds()
			if err != nil {
				return nil, err
			}
			items, err := s.list(r.Context(), owner, "skill", limit, offset)
			return map[string]any{"items": items}, err
		}),
		bind(s, "POST", "/skills/get", "skills.read", func(r *http.Request, input SkillRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "skills")
			if err != nil {
				return nil, err
			}
			return s.getSkill(r, owner, input.ID, input.Version)
		}),
		bind(s, "PUT", "/skills/put", "skills.write", s.saveSkill),
		bind(s, "POST", "/skills/import", "skills.write", func(r *http.Request, input SkillImport) (any, error) {
			files, err := zipFiles(input.Archive)
			if err != nil {
				return nil, err
			}
			return s.saveSkill(r, SkillInput{Scope: input.Scope, ID: input.ID, Version: input.Version, Title: input.Title, Description: input.Description, Tags: input.Tags, Files: files})
		}),
		bind(s, "POST", "/skills/versions", "skills.read", func(r *http.Request, input SkillRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "skills")
			if err != nil {
				return nil, err
			}
			if !validID(input.ID) {
				return nil, bad("id required")
			}
			limit, offset, err := input.Page.bounds()
			if err != nil {
				return nil, err
			}
			rows, err := s.db.QueryContext(r.Context(), `SELECT json_remove(data,'$.files') FROM records WHERE namespace=? AND owner=? AND kind='skill-version' AND json_extract(data,'$.id')=? ORDER BY updated DESC LIMIT ? OFFSET ?`, s.namespace, owner, input.ID, limit, offset)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			items := []Skill{}
			for rows.Next() {
				var data []byte
				var skill Skill
				if err = rows.Scan(&data); err != nil {
					return nil, err
				}
				if err = json.Unmarshal(data, &skill); err != nil {
					return nil, err
				}
				skill.Files = []File{}
				items = append(items, skill)
			}
			return map[string]any{"items": items}, rows.Err()
		}),
		bind(s, "POST", "/skills/delete", "skills.write", func(r *http.Request, input SkillRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "skills")
			if err != nil {
				return nil, err
			}
			if !validID(input.ID) || input.Version != "" {
				return nil, bad("id required; delete removes entire package history")
			}
			err = s.transaction(r.Context(), func(tx *sql.Tx) error {
				_, err := tx.ExecContext(r.Context(), `DELETE FROM records WHERE namespace=? AND owner=? AND ((kind='skill' AND id=?) OR (kind='skill-version' AND json_extract(data,'$.id')=?))`, s.namespace, owner, input.ID, input.ID)
				return err
			})
			return map[string]bool{"deleted": err == nil}, err
		}),
		bind(s, "POST", "/skills/file", "skills.read", func(r *http.Request, input SkillFileRequest) (any, error) {
			owner, err := s.owner(r, input.Scope, "skills")
			if err != nil {
				return nil, err
			}
			if !safePath(input.Path) {
				return nil, bad("invalid path")
			}
			skill, err := s.getSkill(r, owner, input.ID, input.Version)
			if err != nil {
				return nil, err
			}
			for _, file := range skill.Files {
				if file.Path == input.Path {
					return file, nil
				}
			}
			return nil, missing
		}),
		bind(s, "PUT", "/skills/file", "skills.write", func(r *http.Request, input SkillFileRequest) (any, error) { return s.editSkillFile(r, input, false) }),
		bind(s, "POST", "/skills/file/delete", "skills.write", func(r *http.Request, input SkillFileRequest) (any, error) { return s.editSkillFile(r, input, true) }),
	}
}
