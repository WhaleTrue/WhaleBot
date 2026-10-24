package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigurationPersistenceAndUnconfiguredStatus(t *testing.T) {
	a := &adapter{file: filepath.Join(t.TempDir(), "config.json"), changed: make(chan struct{}, 1), state: "polling", config: config{Chats: map[string]conversation{}, IdleSeconds: 86400}}
	response := httptest.NewRecorder()
	a.status(response, httptest.NewRequest("GET", "http://adapter/telegram/status", nil))
	var status map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["state"] != "unconfigured" {
		t.Fatalf("false ready status: %v", status)
	}
	request := httptest.NewRequest("PUT", "http://adapter/telegram/config", strings.NewReader(`{"token":"private-token","allowed_user_ids":[123],"idle_seconds":600}`))
	response = httptest.NewRecorder()
	a.putConfig(response, request)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if strings.Contains(response.Body.String(), "private-token") {
		t.Fatal("token exposed in config response")
	}
	raw, err := os.ReadFile(a.file)
	if err != nil {
		t.Fatal(err)
	}
	var stored config
	if err = json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Token != "private-token" || len(stored.Allowed) != 1 || stored.IdleSeconds != 600 {
		t.Fatal("config not persisted")
	}
	request = httptest.NewRequest("PUT", "http://adapter/telegram/config", strings.NewReader(`{"allowed_user_ids":[],"idle_seconds":600}`))
	response = httptest.NewRecorder()
	a.putConfig(response, request)
	if a.config.Token != "private-token" {
		t.Fatal("omitted token did not preserve secret")
	}
	response = httptest.NewRecorder()
	a.status(response, httptest.NewRequest("GET", "http://adapter/telegram/status", nil))
	if !strings.Contains(response.Body.String(), `"state":"unconfigured"`) {
		t.Fatal("empty whitelist enables polling")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestTokenNeverEntersAPIDiagnostics(t *testing.T) {
	for _, document := range []bool{false, true} {
		for _, malformed := range []bool{false, true} {
			t.Run(fmt.Sprintf("document=%t/malformed=%t", document, malformed), func(t *testing.T) {
				token := "123:captured-token"
				if malformed {
					token += "\n"
				}
				a := &adapter{config: config{Token: token}}
				a.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					a.mu.Lock()
					a.config.Token = "rotated-token"
					a.mu.Unlock()
					body, err := json.Marshal(map[string]any{"ok": false, "description": token + " " + url.QueryEscape(token)})
					if err != nil {
						t.Fatal(err)
					}
					return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
				})}
				var err error
				if document {
					err = a.upload(context.Background(), 1, "report.txt", []byte("report"))
				} else {
					err = a.api(context.Background(), "getMe", nil, nil)
				}
				if err == nil {
					t.Fatal("request failure lost")
				}
				a.setState("error", err)
				response := httptest.NewRecorder()
				a.status(response, httptest.NewRequest("GET", "http://adapter/telegram/status", nil))
				for _, secret := range []string{token, url.QueryEscape(token), "captured-token"} {
					if strings.Contains(err.Error(), secret) || strings.Contains(response.Body.String(), secret) {
						t.Fatal("request token leaked after rotation or malformed URL")
					}
				}
			})
		}
	}
}
