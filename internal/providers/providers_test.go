package providers

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/WhaleTrue/WhaleBot/pkg/mesh"
)

type fixture struct {
	provider *mesh.Node
	clients  map[string]*mesh.Node
	keys     map[string]ed25519.PrivateKey
	trust    map[string]mesh.TrustGrant
	dir      string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("PROVIDER_CAPABILITIES", "")
	f := &fixture{clients: map[string]*mesh.Node{}, keys: map[string]ed25519.PrivateKey{}, trust: map[string]mesh.TrustGrant{}, dir: t.TempDir()}
	scopes := []string{"session.read", "session.write", "memory.read", "memory.write", "memory.search", "secrets.resolve", "secrets.read", "secrets.write", "skills.read", "skills.write", "skills.search", "workspace.read", "workspace.write", "events.read", "events.write", "stats.read", "stats.ingest"}
	for _, name := range []string{"provider", "alice", "bob", "delegate"} {
		public, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		f.keys[name] = key
		grant := mesh.TrustGrant{PublicKey: base64.StdEncoding.EncodeToString(public), Namespace: "test", Scopes: scopes}
		if name == "provider" || name == "delegate" {
			grant.Scopes = []string{"*"}
			grant.Provides = []string{"*"}
		}
		f.trust[name] = grant
	}
	f.provider = f.node(t, "provider")
	for _, name := range []string{"alice", "bob", "delegate"} {
		f.clients[name] = f.node(t, name)
	}
	capabilities, err := Register(f.provider, f.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range capabilities {
		if err = mesh.ValidateCapability(capability); err != nil {
			t.Fatalf("capability %s: %v", capability.ID, err)
		}
	}
	t.Cleanup(func() {
		_ = Close(f.provider)
		_ = f.provider.Close(context.Background())
		for _, node := range f.clients {
			_ = node.Close(context.Background())
		}
	})
	return f
}
func (f *fixture) node(t *testing.T, name string) *mesh.Node {
	t.Helper()
	node, err := mesh.New(mesh.Config{Manifest: mesh.Manifest{Identity: mesh.Identity{Namespace: "test", Name: name, Principal: name}}, PrivateKey: f.keys[name], Trust: f.trust})
	if err != nil {
		t.Fatal(err)
	}
	return node
}
func (f *fixture) request(client, method, path string, input any) (*httptest.ResponseRecorder, error) {
	var body []byte
	var err error
	if raw, ok := input.(json.RawMessage); ok {
		body = raw
	} else {
		body, err = json.Marshal(input)
		if err != nil {
			return nil, err
		}
	}
	request := httptest.NewRequest(method, "https://provider"+path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	identity := f.provider.Manifest().Identity
	request.Header.Set("X-Mesh-Target", mesh.IdentityKey(identity))
	if err = f.clients[client].SignRequest(request, body); err != nil {
		return nil, err
	}
	response := httptest.NewRecorder()
	f.provider.Handler().ServeHTTP(response, request)
	return response, nil
}
func (f *fixture) call(t *testing.T, client, method, path string, input any, status int, output any) {
	t.Helper()
	response, err := f.request(client, method, path, input)
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != status {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, response.Code, status, response.Body.String())
	}
	if output != nil {
		if err = json.Unmarshal(response.Body.Bytes(), output); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConcurrentSessionDurabilityAndOwnership(t *testing.T) {
	f := newFixture(t)
	const count = 24
	failures := make(chan error, count)
	var group sync.WaitGroup
	for i := range count {
		group.Add(1)
		go func() {
			defer group.Done()
			response, err := f.request("alice", "POST", "/session/append", AppendRequest{SessionID: "shared", Messages: []Message{{Role: "user", Content: fmt.Sprint(i), Usage: Usage{TotalTokens: 1}}}})
			if err != nil {
				failures <- err
			} else if response.Code != 200 {
				failures <- fmt.Errorf("append: %d %s", response.Code, response.Body.String())
			}
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	var session Session
	f.call(t, "alice", "POST", "/session/context", SessionRequest{SessionID: "shared", Limit: 2000}, 200, &session)
	if len(session.Messages) != count || session.Revision != count || session.Usage.TotalTokens != count {
		t.Fatalf("lost concurrent append: %+v", session)
	}
	f.call(t, "bob", "POST", "/session/context", SessionRequest{SessionID: "shared"}, 200, &session)
	if session.Revision != 0 || len(session.Messages) != 0 {
		t.Fatal("principal isolation failed")
	}
	f.call(t, "alice", "POST", "/session/context", SessionRequest{Scope: Scope{Owner: "victim"}, SessionID: "shared"}, 403, nil)
	wrong := int64(0)
	f.call(t, "alice", "POST", "/session/append", AppendRequest{SessionID: "shared", ExpectedRevision: &wrong}, 409, nil)
	if err := Close(f.provider); err != nil {
		t.Fatal(err)
	}
	_ = f.provider.Close(context.Background())
	f.provider = f.node(t, "provider")
	if _, err := Register(f.provider, f.dir); err != nil {
		t.Fatal(err)
	}
	f.call(t, "alice", "POST", "/session/context", SessionRequest{SessionID: "shared", Limit: 2000}, 200, &session)
	if session.Revision != count || len(session.Messages) != count {
		t.Fatal("session not durable")
	}
	f.call(t, "alice", "POST", "/session/expire", SessionRequest{SessionID: "shared"}, 200, nil)
	f.call(t, "alice", "POST", "/session/context", SessionRequest{SessionID: "shared"}, 200, &session)
	if !session.Expired || len(session.Messages) != 0 {
		t.Fatal("expired context exposed")
	}
	f.call(t, "alice", "POST", "/session/append", AppendRequest{SessionID: "shared"}, 409, nil)
	f.call(t, "alice", "POST", "/session/delete", SessionRequest{SessionID: "shared"}, 200, nil)
	f.call(t, "alice", "POST", "/session/context", SessionRequest{SessionID: "shared"}, 200, &session)
	if session.Revision != 0 {
		t.Fatal("session delete failed")
	}
}
func TestDelegatedNotesAndStrictJSON(t *testing.T) {
	f := newFixture(t)
	f.call(t, "delegate", "PUT", "/memory/put", NoteInput{Scope: Scope{Owner: "user-a"}, Key: "theme", Value: "紫色 umbrella"}, 200, nil)
	f.call(t, "delegate", "POST", "/memory/search", SearchRequest{Scope: Scope{Owner: "user-a"}, Query: "UMBRELLA irrelevant"}, 200, new(any))
	var result struct {
		Items []Note `json:"items"`
	}
	f.call(t, "delegate", "POST", "/memory/search", SearchRequest{Scope: Scope{Owner: "user-a"}, Query: "紫色"}, 200, &result)
	if len(result.Items) != 1 {
		t.Fatal("Unicode search failed")
	}
	f.call(t, "delegate", "POST", "/memory/search", SearchRequest{Scope: Scope{Owner: "user-b"}, Query: "umbrella"}, 200, &result)
	if len(result.Items) != 0 {
		t.Fatal("delegated owner isolation failed")
	}
	for _, raw := range []string{`{"key":"x","extra":true}`, `{"key":"x"} {}`, `{"key":1}`, `{`} {
		f.call(t, "alice", "POST", "/memory/get", json.RawMessage(raw), 400, nil)
	}
	f.call(t, "alice", "PUT", "/memory/put", NoteInput{Key: "large", Value: strings.Repeat("x", (256<<10)+1)}, 400, nil)
}
func TestSecretEncryptionAndKeyLoss(t *testing.T) {
	f := newFixture(t)
	f.call(t, "alice", "PUT", "/secrets/put", SecretInput{Key: "token", Value: "never-store-plaintext", Note: "test"}, 200, nil)
	var result Secret
	f.call(t, "alice", "POST", "/secrets/resolve", KeyRequest{Key: "token"}, 200, &result)
	if result.Value != "never-store-plaintext" {
		t.Fatal("secret round trip failed")
	}
	f.call(t, "bob", "POST", "/secrets/resolve", KeyRequest{Key: "token"}, 404, nil)
	value, _ := stores.Load(f.provider)
	s := value.(*store)
	var encrypted encryptedSecret
	if err := s.read(context.Background(), s.db, "principal:alice", "secret", "token", &encrypted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted.Ciphertext, []byte(result.Value)) {
		t.Fatal("plaintext ciphertext")
	}
	if _, err := s.open("principal:bob", encrypted); err == nil {
		t.Fatal("ciphertext scope not authenticated")
	}
	encrypted.Ciphertext[0] ^= 1
	if _, err := s.open("principal:alice", encrypted); err == nil {
		t.Fatal("ciphertext tamper accepted")
	}
	f.call(t, "alice", "POST", "/secrets/list", ListRequest{}, 200, &struct {
		Items []Secret `json:"items"`
	}{})
	if err := os.Remove(filepath.Join(f.dir, "secrets.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := loadKey(s.db, f.dir); err == nil {
		t.Fatal("key loss silently regenerated")
	}
}
func makeZIP(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
func TestSkillsVersionsZIPAndSecurePaths(t *testing.T) {
	f := newFixture(t)
	archive := makeZIP(t, map[string]string{"wrapped/SKILL.md": "# Searchable zebra skill", "wrapped/assets/help.txt": "help"})
	f.call(t, "alice", "POST", "/skills/import", SkillImport{ID: "zebra", Version: "1", Archive: archive}, 200, nil)
	f.call(t, "alice", "POST", "/skills/get", SkillRequest{ID: "zebra", Version: "1"}, 200, new(Skill))
	f.call(t, "alice", "PUT", "/skills/file", SkillFileRequest{ID: "zebra", NewVersion: "2", Path: "SKILL.md", Content: []byte("# updated")}, 200, nil)
	var original Skill
	f.call(t, "alice", "POST", "/skills/get", SkillRequest{ID: "zebra", Version: "1"}, 200, &original)
	if !bytes.Contains(original.Files[0].Content, []byte("zebra")) {
		t.Fatal("old version changed")
	}
	f.call(t, "alice", "POST", "/skills/import", SkillImport{ID: "zebra", Version: "1", Archive: archive}, 409, nil)
	for _, name := range []string{"../escape", "/absolute", "C:/drive", "a\\b", "a/../b"} {
		if _, err := zipFiles(makeZIP(t, map[string]string{"SKILL.md": "valid", name: "bad"})); err == nil {
			t.Fatalf("unsafe ZIP accepted: %s", name)
		}
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: "link"}
	header.SetMode(os.ModeSymlink | 0777)
	file, _ := writer.CreateHeader(header)
	_, _ = file.Write([]byte("target"))
	_ = writer.Close()
	if _, err := zipFiles(buffer.Bytes()); err == nil {
		t.Fatal("ZIP symlink accepted")
	}
	f.call(t, "alice", "POST", "/skills/delete", SkillRequest{ID: "zebra"}, 200, nil)
	f.call(t, "alice", "POST", "/skills/get", SkillRequest{ID: "zebra", Version: "1"}, 404, nil)
}
func TestWorkspaceArtifactsAndEvents(t *testing.T) {
	f := newFixture(t)
	input := WorkspaceInput{Workspace: "work", Path: "docs/report.txt", Content: []byte("hello"), Artifact: true}
	f.call(t, "alice", "PUT", "/workspace/put", input, 200, nil)
	f.call(t, "alice", "PUT", "/workspace/put", input, 409, nil)
	var file WorkspaceFile
	f.call(t, "alice", "POST", "/workspace/get", WorkspaceRequest{Workspace: "work", Path: input.Path, Artifact: true}, 200, &file)
	if string(file.Content) != "hello" {
		t.Fatal("artifact read failed")
	}
	f.call(t, "bob", "POST", "/workspace/get", WorkspaceRequest{Workspace: "work", Path: input.Path, Artifact: true}, 404, nil)
	input.Path = "../escape"
	f.call(t, "alice", "PUT", "/workspace/put", input, 400, nil)
	f.call(t, "alice", "POST", "/events", EventsRequest{Events: []IngestEvent{{Kind: "message", Usage: Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}}, {Kind: "tool_call", Message: "done"}}}, 200, nil)
	var stats Stats
	f.call(t, "alice", "POST", "/stats/query", StatsQuery{}, 200, &stats)
	if stats.Events != 2 || stats.Messages != 1 || stats.ToolCalls != 1 || stats.Usage.TotalTokens != 5 {
		t.Fatalf("incorrect stats: %+v", stats)
	}
	f.call(t, "bob", "POST", "/stats/query", StatsQuery{}, 200, &stats)
	if stats.Events != 0 {
		t.Fatal("stats isolation failed")
	}
	f.call(t, "alice", "POST", "/workspace/delete", WorkspaceRequest{Workspace: "work"}, 200, nil)
	f.call(t, "alice", "POST", "/workspace/get", WorkspaceRequest{Workspace: "work", Path: "docs/report.txt", Artifact: true}, 404, nil)
}
