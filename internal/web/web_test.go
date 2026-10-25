package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUserBoundary(t *testing.T) {
	alice := User{ID: "alice", Scopes: []string{"session.read"}}
	bob := User{ID: "bob"}
	for _, path := range []string{"/agent/run", "/session/list", "/workspace/get", "/skills/search", "/events/query", "/stats/query", "/secrets/put", "/executor/files"} {
		input := map[string]any{"owner": ownerKey(bob), "user_id": "bob"}
		if err := prepareInput(alice, path, input); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if input["owner"] != ownerKey(alice) {
			t.Fatalf("%s forwards supplied identity: %#v", path, input)
		}
		if _, ok := input["user_id"]; ok {
			t.Fatal("user_id forwarded")
		}
	}
	input := map[string]any{"session_id": ownerKey(bob) + ":private"}
	if prepareInput(alice, "/session/get", input) == nil {
		t.Fatal("cross-user session accepted")
	}
	if prepareInput(User{ID: "admin", Scopes: []string{"*"}}, "/secrets/resolve", map[string]any{}) == nil {
		t.Fatal("browser administrator can resolve raw secret")
	}
	if hasScope(alice, "session.write") || !hasScope(alice, "session.read") {
		t.Fatal("scope grants not exact")
	}
	if ownerKey(alice) == ownerKey(bob) {
		t.Fatal("users share owner")
	}
}
func TestBenchmarkApprovalSessionBoundary(t *testing.T) {
	alice, bob := User{ID: "alice"}, User{ID: "bob"}
	for _, supplied := range []string{"benchmark-session", ownerKey(alice) + ":benchmark-session", ownerKey(bob) + ":benchmark-session"} {
		var session string
		for _, path := range []string{"/benchmark/e2e/prepare", "/agent/approvals", "/benchmark/run", "/agent/run"} {
			input := map[string]any{"session_id": supplied}
			if err := prepareInput(alice, path, input); err != nil {
				t.Fatal(err)
			}
			id := input["session_id"].(string)
			if session == "" {
				session = id
			}
			if id != session || input["owner"] != ownerKey(alice) {
				t.Fatalf("%s uses a different approval boundary: %#v", path, input)
			}
			if id == ownerKey(bob)+":benchmark-session" {
				t.Fatal("another user's session forwarded")
			}
		}
	}
	input := map[string]any{}
	if err := prepareInput(alice, "/benchmark/run", input); err != nil {
		t.Fatal(err)
	}
	if _, ok := input["session_id"]; ok {
		t.Fatal("non-E2E benchmark creates an unused session")
	}
}
func TestCookieCSRFBoundary(t *testing.T) {
	s := &server{users: []User{{ID: "alice"}}, sessions: map[string]loginSession{"token": {UserID: "alice", CSRF: "csrf", Expires: time.Now().Add(time.Hour)}}}
	handler := s.protect(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	for _, tc := range []struct {
		cookie, csrf, origin string
		want                 int
	}{{"", "", "", 401}, {"token", "", "", 403}, {"token", "csrf", "https://evil.example", 403}, {"token", "csrf", "http://web.test", 204}} {
		request := httptest.NewRequest("POST", "http://web.test/api/call", nil)
		if tc.cookie != "" {
			request.AddCookie(&http.Cookie{Name: "whalemesh_session", Value: tc.cookie})
		}
		request.Header.Set("X-CSRF-Token", tc.csrf)
		request.Header.Set("Origin", tc.origin)
		response := httptest.NewRecorder()
		handler(response, request)
		if response.Code != tc.want {
			t.Fatalf("got %d want %d", response.Code, tc.want)
		}
	}
	s.sessions["token"] = loginSession{UserID: "alice", CSRF: "csrf", Expires: time.Now().Add(-time.Second)}
	request := httptest.NewRequest("GET", "http://web.test/api/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: "whalemesh_session", Value: "token"})
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != 401 {
		t.Fatal("expired cookie accepted")
	}
}
func TestAdvertisedOperationPath(t *testing.T) {
	for _, path := range []string{"/runs/private/extra", "/runs/..", "/secrets/resolve", "https://evil.example/runs/id"} {
		if operationPath("/runs/{id}", path) {
			t.Fatalf("unadvertised path accepted: %s", path)
		}
	}
	if !operationPath("/runs/{id}", "/runs/run-1") {
		t.Fatal("valid operation template rejected")
	}
}
