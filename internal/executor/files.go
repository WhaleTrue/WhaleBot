package executor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxFileBytes = 8 << 20

// FileRequest paths refer only to /workspace. Owner is trusted only with container.delegate.
type FileRequest struct {
	ID            string `json:"id"`
	Operation     string `json:"operation"`
	Path          string `json:"path"`
	To            string `json:"to,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
	Owner         string `json:"owner,omitempty"`
}
type FileEntry struct {
	Name       string    `json:"name"`
	Directory  bool      `json:"directory"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
}
type FileResult struct {
	Path    string      `json:"path"`
	Content []byte      `json:"content_base64,omitempty"`
	Entries []FileEntry `json:"entries,omitempty"`
	Size    int64       `json:"size"`
	Error   string      `json:"error,omitempty"`
}
type Artifact struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	MediaType string    `json:"media_type"`
	Owner     string    `json:"owner"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func relativePath(value string) (string, error) {
	if strings.ContainsAny(value, "\\\x00") {
		return "", errors.New("invalid workspace path")
	}
	if value == "/workspace" {
		return ".", nil
	}
	value = strings.TrimPrefix(value, "/workspace/")
	if strings.HasPrefix(value, "/") {
		return "", errors.New("absolute path outside workspace")
	}
	clean := path.Clean(value)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path escapes workspace")
	}
	return clean, nil
}

// RunFileHelper is dispatched by cmd/node before node startup. It never invokes a shell.
func RunFileHelper(args []string) bool {
	if len(args) == 0 || args[0] != "--executor-files" {
		return false
	}
	result := FileResult{}
	var err error
	if len(args) >= 2 && args[1] == "initialize" {
		for _, directory := range args[2:] {
			if err = os.Chmod(directory, 0700); err != nil {
				break
			}
			if err = os.Chown(directory, 65532, 65532); err != nil {
				break
			}
		}
		if err == nil {
			for _, directory := range []string{"/run/whalemesh", "/run/secrets"} {
				var entries []os.DirEntry
				entries, err = os.ReadDir(directory)
				if err != nil {
					break
				}
				for _, entry := range entries {
					if err = os.RemoveAll(filepath.Join(directory, entry.Name())); err != nil {
						break
					}
				}
				if err != nil {
					break
				}
			}
		}
	} else if len(args) == 3 && args[1] == "request" && strings.HasPrefix(args[2], "/data/whalemesh-request-") && path.Dir(args[2]) == "/data" {
		var input FileRequest
		var data []byte
		data, err = os.ReadFile(args[2])
		_ = os.Remove(args[2])
		if err == nil && len(data) > 16<<20 {
			err = errors.New("file request exceeds limit")
		}
		if err == nil {
			err = json.Unmarshal(data, &input)
		}
		if err == nil {
			var root *os.Root
			root, err = os.OpenRoot("/workspace")
			if err == nil {
				result, err = fileOperation(root, input)
				root.Close()
			}
		}
	} else {
		err = errors.New("invalid file helper invocation")
	}
	if err != nil {
		result.Error = err.Error()
	}
	_ = json.NewEncoder(os.Stdout).Encode(result)
	if err != nil {
		os.Exit(1)
	}
	return true
}
func fileOperation(root *os.Root, input FileRequest) (FileResult, error) {
	name, err := relativePath(input.Path)
	if err != nil {
		return FileResult{}, err
	}
	result := FileResult{Path: name}
	switch input.Operation {
	case "list":
		f, err := root.Open(name)
		if err != nil {
			return result, err
		}
		defer f.Close()
		entries, err := f.ReadDir(-1)
		if err != nil {
			return result, err
		}
		if len(entries) > 10000 {
			return result, errors.New("directory entry limit exceeded")
		}
		result.Entries = []FileEntry{}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				return result, err
			}
			result.Entries = append(result.Entries, FileEntry{Name: entry.Name(), Directory: entry.IsDir(), Size: info.Size(), ModifiedAt: info.ModTime()})
		}
		sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].Name < result.Entries[j].Name })
	case "read":
		f, err := root.Open(name)
		if err != nil {
			return result, err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return result, err
		}
		if !info.Mode().IsRegular() {
			return result, errors.New("only regular files can be read")
		}
		if info.Size() > maxFileBytes {
			return result, errors.New("file exceeds limit")
		}
		result.Content, err = io.ReadAll(io.LimitReader(f, maxFileBytes+1))
		if err != nil {
			return result, err
		}
		if len(result.Content) > maxFileBytes {
			return result, errors.New("file exceeds limit")
		}
		result.Size = int64(len(result.Content))
	case "write":
		if name == "." {
			return result, errors.New("cannot overwrite workspace root")
		}
		data, err := base64.StdEncoding.DecodeString(input.ContentBase64)
		if err != nil {
			return result, err
		}
		if len(data) > maxFileBytes {
			return result, errors.New("file exceeds limit")
		}
		if err = root.MkdirAll(path.Dir(name), 0770); err != nil {
			return result, err
		}
		temporary := path.Join(path.Dir(name), ".whalemesh-write-"+randomID())
		f, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
		if err != nil {
			return result, err
		}
		defer root.Remove(temporary)
		_, err = f.Write(data)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return result, err
		}
		if err = root.Rename(temporary, name); err != nil {
			return result, err
		}
		result.Size = int64(len(data))
	case "mkdir":
		err = root.MkdirAll(name, 0770)
	case "move":
		to, toErr := relativePath(input.To)
		if toErr != nil {
			return result, toErr
		}
		if name == "." || to == "." {
			return result, errors.New("cannot move workspace root")
		}
		err = root.Rename(name, to)
	case "delete":
		if name == "." {
			return result, errors.New("cannot delete workspace root")
		}
		err = root.RemoveAll(name)
	case "export":
		result.Content, err = exportRoot(root, name)
		result.Size = int64(len(result.Content))
	default:
		err = errors.New("operation must be list, read, write, mkdir, move or delete")
	}
	return result, err
}
func exportRoot(root *os.Root, name string) ([]byte, error) {
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(compressed)
	var total int64
	count := 0
	var walk func(string, string) error
	walk = func(source, archiveName string) error {
		count++
		if count > 10000 {
			return errors.New("artifact entry limit exceeded")
		}
		info, err := root.Lstat(source)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("artifact symlinks prohibited")
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("artifact special files prohibited")
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = archiveName
		header.Uid = 0
		header.Gid = 0
		header.Uname = ""
		header.Gname = ""
		header.Mode &= 0777
		if err = writer.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			directory, err := root.Open(source)
			if err != nil {
				return err
			}
			entries, err := directory.ReadDir(-1)
			directory.Close()
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if err = walk(path.Join(source, entry.Name()), path.Join(archiveName, entry.Name())); err != nil {
					return err
				}
			}
			return nil
		}
		total += info.Size()
		if total > maxFileBytes {
			return errors.New("artifact content exceeds limit")
		}
		f, err := root.Open(source)
		if err != nil {
			return err
		}
		defer f.Close()
		actual, err := f.Stat()
		if err != nil || !actual.Mode().IsRegular() {
			return errors.New("artifact changed during export")
		}
		_, err = io.CopyN(writer, f, info.Size())
		return err
	}
	archiveName := path.Base(name)
	if name == "." {
		archiveName = "workspace"
	}
	if err := walk(name, archiveName); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if err := compressed.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
func (e *Executor) installHelper(ctx context.Context, id, destination string) error {
	executable := os.Getenv("EXECUTOR_HELPER_BINARY")
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return err
		}
	}
	f, err := os.Open(executable)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 128<<20 {
		return errors.New("helper binary exceeds limit")
	}
	reader, writer := io.Pipe()
	go func() {
		tw := tar.NewWriter(writer)
		err := tw.WriteHeader(&tar.Header{Name: "whalemesh-files", Mode: 0555, Size: info.Size(), Uid: 0, Gid: 0})
		if err == nil {
			_, err = io.CopyN(tw, f, info.Size())
		}
		if err == nil {
			err = tw.Close()
		}
		_ = writer.CloseWithError(err)
	}()
	defer reader.Close()
	response, err := e.engine.request(ctx, "PUT", objectPath("containers", id)+"/archive?path="+url.QueryEscape(destination)+"&noOverwriteDirNonDir=1", reader, "application/x-tar")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, response.Body)
	return err
}
func (e *Executor) fileCall(ctx context.Context, id string, input FileRequest) (FileResult, error) {
	if _, err := relativePath(input.Path); err != nil {
		return FileResult{}, err
	}
	if input.Operation == "move" {
		if _, err := relativePath(input.To); err != nil {
			return FileResult{}, err
		}
	}
	requestName := "whalemesh-request-" + randomID() + ".json"
	data, err := json.Marshal(input)
	if err != nil {
		return FileResult{}, err
	}
	if err = e.archivePut(ctx, id, "/data", map[string][]byte{requestName: data}, 0400); err != nil {
		return FileResult{}, err
	}
	stdout, stderr, code, err := e.runCommand(ctx, id, []string{"/run/whalemesh/whalemesh-files", "--executor-files", "request", "/data/" + requestName}, "/workspace", nil, false, 24<<20)
	if err != nil {
		return FileResult{}, err
	}
	if code != 0 {
		return FileResult{}, commandFailure(stdout, stderr, code)
	}
	var result FileResult
	if err = json.Unmarshal([]byte(stdout), &result); err != nil {
		return result, err
	}
	if result.Error != "" {
		return result, errors.New(result.Error)
	}
	return result, nil
}
func (e *Executor) filesHTTP(w http.ResponseWriter, r *http.Request) {
	var input FileRequest
	if err := decode(r, &input); err != nil {
		fail(w, err)
		return
	}
	if input.Operation == "export" {
		fail(w, errors.New("use artifact.export capability"))
		return
	}
	principal, err := e.actor(r, input.Owner)
	if err != nil {
		fail(w, err)
		return
	}
	c, err := e.inspect(r.Context(), input.ID, principal, e.admin(r))
	if err != nil {
		fail(w, err)
		return
	}
	if err = e.touch(c.ID); err != nil {
		fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	result, err := e.fileCall(ctx, c.ID, input)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, 200, result)
}
func (e *Executor) exportHTTP(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID    string `json:"id"`
		Path  string `json:"path"`
		Owner string `json:"owner,omitempty"`
	}
	if err := decode(r, &input); err != nil {
		fail(w, err)
		return
	}
	principal, err := e.actor(r, input.Owner)
	if err != nil {
		fail(w, err)
		return
	}
	c, err := e.inspect(r.Context(), input.ID, principal, e.admin(r))
	if err != nil {
		fail(w, err)
		return
	}
	if err = e.touch(c.ID); err != nil {
		fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	result, err := e.fileCall(ctx, c.ID, FileRequest{Operation: "export", Path: input.Path})
	if err != nil {
		fail(w, err)
		return
	}
	directory := filepath.Join(e.dir, "artifacts")
	if err = os.MkdirAll(directory, 0700); err != nil {
		fail(w, err)
		return
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		fail(w, err)
		return
	}
	if len(entries) >= 1024 {
		fail(w, errors.New("artifact retention capacity reached"))
		return
	}
	id := randomID()
	artifact := Artifact{ID: id, Name: path.Base(input.Path) + ".tar.gz", URL: requestURL(id, e.node.Manifest().Identity.Principal), MediaType: "application/gzip", Owner: principal, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(24 * time.Hour)}
	if artifact.Name == "..tar.gz" || artifact.Name == "/.tar.gz" {
		artifact.Name = "workspace.tar.gz"
	}
	data, _ := json.Marshal(artifact)
	if err = os.WriteFile(filepath.Join(directory, id+".tar.gz"), result.Content, 0600); err == nil {
		err = os.WriteFile(filepath.Join(directory, id+".json"), data, 0600)
	}
	if err != nil {
		_ = os.Remove(filepath.Join(directory, id+".tar.gz"))
		fail(w, err)
		return
	}
	respond(w, 201, artifact)
}
func (e *Executor) downloadHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !identifier(id) {
		fail(w, errors.New("invalid artifact ID"))
		return
	}
	principal, err := e.actor(r, r.URL.Query().Get("owner"))
	if err != nil {
		fail(w, err)
		return
	}
	directory := filepath.Join(e.dir, "artifacts")
	data, err := os.ReadFile(filepath.Join(directory, id+".json"))
	if err != nil {
		fail(w, err)
		return
	}
	var artifact Artifact
	if err = json.Unmarshal(data, &artifact); err != nil {
		fail(w, err)
		return
	}
	if artifact.Owner != principal && !e.admin(r) {
		fail(w, errors.New("artifact not owned"))
		return
	}
	if time.Now().After(artifact.ExpiresAt) {
		fail(w, errors.New("artifact expired"))
		return
	}
	f, err := os.Open(filepath.Join(directory, id+".tar.gz"))
	if err != nil {
		fail(w, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", artifact.MediaType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", artifact.ID+".tar.gz"))
	http.ServeContent(w, r, artifact.Name, artifact.CreatedAt, f)
}
func (e *Executor) cleanupArtifacts() {
	directory := filepath.Join(e.dir, "artifacts")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !identifier(id) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			continue
		}
		var artifact Artifact
		if json.Unmarshal(data, &artifact) == nil && time.Now().After(artifact.ExpiresAt) {
			_ = os.Remove(filepath.Join(directory, id+".tar.gz"))
			_ = os.Remove(filepath.Join(directory, id+".json"))
		}
	}
}
