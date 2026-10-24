package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/WhaleTrue/WhaleBot/pkg/mesh"
)

func TestGreeting(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		message    string
	}{
		{"greeting", `{"name":"WhaleMesh"}`, 200, "Hello, WhaleMesh!"},
		{"unicode", `{"name":"鲸鱼"}`, 200, "Hello, 鲸鱼!"},
		{"missing", `{}`, 400, ""},
		{"blank", `{"name":" "}`, 400, ""},
		{"unknown", `{"name":"x","admin":true}`, 400, ""},
		{"trailing", `{"name":"x"} {}`, 400, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/hello", strings.NewReader(test.body))
			response := httptest.NewRecorder()
			greet(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d, want %d: %s", response.Code, test.status, response.Body)
			}
			if test.message != "" {
				var output greetingResponse
				if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
					t.Fatal(err)
				}
				if output.Message != test.message {
					t.Fatalf("message=%q", output.Message)
				}
			}
		})
	}
}
func TestHelloContract(t *testing.T) {
	if err := mesh.ValidateJSONSchema(greetingInput); err != nil {
		t.Fatal(err)
	}
	if err := mesh.ValidateJSONSchema(greetingOutput); err != nil {
		t.Fatal(err)
	}
	if _, err := register(nil); err == nil {
		t.Fatal("nil SDK node accepted")
	}
}

func TestAuthenticatedSDKProvider(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	principal := "hello-test"
	node, err := mesh.New(mesh.Config{Manifest: mesh.Manifest{Identity: mesh.Identity{Namespace: "default", Name: "hello", Principal: principal}}, PrivateKey: private, Trust: map[string]mesh.TrustGrant{principal: {PublicKey: base64.StdEncoding.EncodeToString(public), Namespace: "default", Scopes: []string{"hello.invoke"}, Provides: []string{"hello.invoke"}}}})
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := register(node)
	if err != nil {
		t.Fatal(err)
	}
	if err = node.UpdateCapabilities(capabilities); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(node.Handler())
	defer server.Close()
	manifest := node.Manifest()
	manifest.Transport.Advertise = []string{server.URL}
	binding := mesh.Binding{Node: manifest, Capability: capabilities[0]}
	var output greetingResponse
	if err = node.Call(context.Background(), binding, "POST", "/hello", greetingRequest{Name: "SDK"}, &output); err != nil {
		t.Fatal(err)
	}
	if output.Message != "Hello, SDK!" {
		t.Fatalf("message=%q", output.Message)
	}
	unsigned := httptest.NewRecorder()
	node.Handler().ServeHTTP(unsigned, httptest.NewRequest("POST", "/hello", strings.NewReader(`{"name":"unsigned"}`)))
	if unsigned.Code != 401 {
		t.Fatalf("unsigned request status=%d", unsigned.Code)
	}
}
