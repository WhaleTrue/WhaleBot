package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/WhaleMesh/WhaleMesh/pkg/mesh"
)

func capability(id, path, method string, sem mesh.Semantics) mesh.Capability {
	return mesh.Capability{Schema: "whalemesh.capability/v1", ID: id, Interface: id, Version: "1.0.0", Endpoint: path, Ready: true, Semantics: sem, Operations: []mesh.Operation{{Name: id, Method: method, Path: path, Semantics: sem, Scopes: []string{id}}}}
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, code string, err error) {
	mesh.WriteError(w, status, code, err.Error())
}
func event(w http.ResponseWriter, name string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b); err != nil {
		return err
	}
	return http.NewResponseController(w).Flush()
}
func Register(node *mesh.Node, dataDir string) ([]mesh.Capability, error) {
	s, err := openStore(dataDir)
	if err != nil {
		return nil, err
	}
	node.Handle("GET", "/llm/config", []string{"llm.config.read"}, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.public()) })
	node.Handle("GET", "/llm/models", []string{"llm.models.list"}, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.models()) })
	node.Handle("PUT", "/llm/config", []string{"llm.config.write"}, func(w http.ResponseWriter, r *http.Request) {
		var input Config
		if err := decode(http.MaxBytesReader(w, r.Body, 1<<20), &input); err != nil {
			writeError(w, 400, "invalid_json", err)
			return
		}
		if err := s.replace(input); err != nil {
			writeError(w, 400, "invalid_config", err)
			return
		}
		provides := node.Manifest().Provides
		ready, attributes := s.invocationMetadata()
		for i := range provides {
			if provides[i].ID == "llm.invoke" {
				provides[i].Ready = ready
				provides[i].Attributes = attributes
			}
		}
		if err := node.UpdateCapabilities(provides); err != nil {
			writeError(w, 500, "capability_update_failed", err)
			return
		}
		writeJSON(w, s.public())
	})
	node.Handle("POST", "/llm/invoke", []string{"llm.invoke"}, func(w http.ResponseWriter, r *http.Request) {
		var input Request
		if err := decode(http.MaxBytesReader(w, r.Body, 4<<20), &input); err != nil {
			writeError(w, 400, "invalid_json", err)
			return
		}
		if err := validateRequest(input); err != nil {
			writeError(w, 400, "invalid_request", err)
			return
		}
		mock := os.Getenv("LLM_MOCK") == "1"
		p, err := s.profile(input, mock)
		if err != nil {
			writeError(w, 409, "model_selection_rejected", err)
			return
		}
		var emit func(Delta) error
		if input.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			emit = func(d Delta) error { return event(w, "delta", d) }
		}
		var output Response
		if mock {
			err = nil
			output = Response{Message: Message{Role: "assistant", Content: "[MOCK: LLM_MOCK=1] " + input.Messages[len(input.Messages)-1].Content}}
			if emit != nil {
				err = emit(Delta{Content: output.Message.Content})
			}
		} else {
			output, err = upstream(r.Context(), p, input, emit)
		}
		if err != nil {
			if input.Stream {
				_ = event(w, "error", map[string]string{"code": "upstream_error", "message": err.Error()})
			} else {
				writeError(w, 502, "upstream_error", err)
			}
			return
		}
		if input.Stream {
			_ = event(w, "done", output)
		} else {
			writeJSON(w, output)
		}
	})
	invoke := capability("llm.invoke", "/llm/invoke", "POST", mesh.Semantics{SideEffecting: true, Streaming: true})
	invoke.Scopes = []string{"llm.invoke"}
	invoke.Ready, invoke.Attributes = s.invocationMetadata()
	invoke.InputSchema = mesh.JSONSchemaFor(Request{})
	invoke.OutputSchema = mesh.JSONSchemaFor(Response{})
	models := capability("llm.models.list", "/llm/models", "GET", mesh.Semantics{ReadOnly: true, Idempotent: true})
	models.Semantics = mesh.Semantics{Mutating: true}
	models.Scopes = []string{"llm.models.list", "llm.config.read", "llm.config.write"}
	models.InputSchema = json.RawMessage(`{"description":"GET models/config has no JSON input; PUT config accepts the configuration document.","anyOf":[{"type":"null"},` + string(mesh.JSONSchemaFor(Config{})) + `]}`)
	models.OutputSchema = mesh.JSONSchemaFor(PublicConfig{})
	models.Operations = append(models.Operations, mesh.Operation{Name: "config.read", Method: "GET", Path: "/llm/config", Scopes: []string{"llm.config.read"}, Semantics: mesh.Semantics{ReadOnly: true, Idempotent: true}}, mesh.Operation{Name: "config.write", Method: "PUT", Path: "/llm/config", Scopes: []string{"llm.config.write"}, TransportSecurity: "tls", Semantics: mesh.Semantics{Mutating: true}})
	return []mesh.Capability{invoke, models}, nil
}

func hasFeature(features []string, feature string) bool {
	for _, value := range features {
		if value == feature {
			return true
		}
	}
	return false
}
func (s *store) models() PublicConfig {
	if os.Getenv("LLM_MOCK") == "1" {
		config := s.public()
		return PublicConfig{Revision: "mock:" + config.Revision, ActiveModelID: "mock", Models: []PublicProfile{{ID: "mock", Name: "Development mock (no inference)", Model: "mock", CostClass: "free", Capabilities: []string{"streaming"}}}}
	}
	return s.public()
}
func (s *store) invocationMetadata() (bool, map[string]string) {
	config := s.models()
	attributes := map[string]string{"model_switching": "explicit-only", "model_revision": config.Revision, "mock": "false", "cost_class": "unknown", "supports_tools": "false", "supports_streaming": "false"}
	if os.Getenv("LLM_MOCK") == "1" {
		attributes["mock"] = "true"
		attributes["cost_class"] = "free"
		attributes["active_model"] = "mock"
		attributes["supports_streaming"] = "true"
		return true, attributes
	}
	for _, profile := range config.Models {
		if profile.ID == config.ActiveModelID {
			attributes["active_model"] = profile.ID
			if profile.CostClass != "" {
				attributes["cost_class"] = profile.CostClass
			}
			attributes["supports_tools"] = fmt.Sprint(hasFeature(profile.Capabilities, "tools"))
			attributes["supports_streaming"] = fmt.Sprint(hasFeature(profile.Capabilities, "streaming"))
		}
	}
	return len(config.Models) > 0, attributes
}
