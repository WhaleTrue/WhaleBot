package web

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/WhaleMesh/WhaleMesh/pkg/mesh"
)

func TestTopologyDoesNotRecordPreviewBindings(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	requirement := mesh.Requirement{ID: "preview", Capability: "echo.read", Sticky: true, Optional: true}
	node, err := mesh.New(mesh.Config{
		Manifest: mesh.Manifest{
			Identity: mesh.Identity{Namespace: "default", Name: "observer", Principal: "observer"},
			Provides: []mesh.Capability{{ID: "echo.read", Interface: "echo.read", Version: "1.0.0", Endpoint: "/echo", Ready: true, InputSchema: mesh.JSONSchemaFor(nil), OutputSchema: mesh.JSONSchemaFor(map[string]string{}), Scopes: []string{"echo.read"}, Operations: []mesh.Operation{{Name: "read", Method: "GET", Path: "/echo", Scopes: []string{"echo.read"}}}}},
			Requires: []mesh.Requirement{requirement},
		},
		PrivateKey: private,
		Trust:      map[string]mesh.TrustGrant{"observer": {PublicKey: base64.StdEncoding.EncodeToString(public), Namespace: "default", Scopes: []string{"*"}, Provides: []string{"*"}}},
		DataDir:    dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &server{node: node, users: []User{{ID: "viewer", Scopes: []string{"mesh.inspect"}}}, sessions: map[string]loginSession{"token": {UserID: "viewer", Expires: time.Now().Add(time.Hour)}}}
	for range 2 {
		request := httptest.NewRequest("GET", "http://web.test/api/topology", nil)
		request.AddCookie(&http.Cookie{Name: "whalemesh_session", Value: "token"})
		response := httptest.NewRecorder()
		s.topology(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("topology status %d: %s", response.Code, response.Body.String())
		}
		var body struct {
			Resolutions map[string]mesh.Explanation `json:"resolutions"`
			Recorded    []mesh.RecordedBinding      `json:"recorded_bindings"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Resolutions[requirement.ID].Selected) != 1 || len(body.Recorded) != 0 || len(node.Bindings()) != 0 {
			t.Fatalf("topology confused preview with recorded bindings: %+v", body)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "resolver.json")); !os.IsNotExist(err) {
		t.Fatalf("topology persisted preview: %v", err)
	}
}
