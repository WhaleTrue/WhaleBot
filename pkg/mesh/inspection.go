package mesh

import (
	"encoding/json"
	"net/http"
)

// Inspection contains this node's observation, not an authoritative global view.
type Inspection struct {
	Manifest      Manifest          `json:"manifest"`
	Peers         []Peer            `json:"peers"`
	Bindings      []Binding         `json:"bindings"`
	Recorded      []RecordedBinding `json:"recorded_bindings"`
	Resolutions   []Explanation     `json:"resolutions"`
	Requests      uint64            `json:"requests"`
	Failures      uint64            `json:"failures"`
	DroppedEvents uint64            `json:"dropped_events"`
}

func (n *Node) inspection(w http.ResponseWriter, r *http.Request) {
	manifest := n.Manifest()
	result := Inspection{Manifest: manifest, Peers: n.Peers(), Bindings: n.Bindings(), Recorded: n.RecordedBindings(), Requests: n.requests.Load(), Failures: n.failures.Load(), DroppedEvents: n.dropped.Load()}
	for _, requirement := range manifest.Requires {
		explanation, _ := n.Explain(requirement)
		result.Resolutions = append(result.Resolutions, explanation)
	}
	WriteJSON(w, http.StatusOK, result)
}
func withInspection(capabilities []Capability, grant TrustGrant) []Capability {
	if !patternAllowed(grant.Provides, "mesh.inspect") {
		return capabilities
	}
	read := Semantics{ReadOnly: true, Idempotent: true}
	capability := Capability{Schema: "whalemesh.capability/v1", ID: "mesh.inspect", Interface: "mesh.inspect", Version: "1.0.0", Endpoint: "/mesh/status", Ready: true, Scopes: []string{"mesh.inspect"}, Semantics: read,
		InputSchema:  json.RawMessage(`{"type":"null"}`),
		OutputSchema: json.RawMessage(`{"anyOf":[{"type":"object","required":["manifest","peers","bindings","recorded_bindings","resolutions","requests","failures","dropped_events"],"additionalProperties":false,"properties":{"manifest":{"type":"object"},"peers":{"type":["array","null"],"items":{"type":"object"}},"bindings":{"type":["array","null"],"items":{"type":"object"}},"recorded_bindings":{"type":["array","null"],"items":{"type":"object"}},"resolutions":{"type":["array","null"],"items":{"type":"object"}},"requests":{"type":"integer","minimum":0},"failures":{"type":"integer","minimum":0},"dropped_events":{"type":"integer","minimum":0}}},{"type":"object","required":["type","identity","sequence","at"],"additionalProperties":false,"properties":{"type":{"type":"string"},"identity":{"type":"object"},"sequence":{"type":"integer","minimum":0},"at":{"type":"string","format":"date-time"},"reason":{"type":"string"}}}]}`),
		Operations:   []Operation{{Name: "status", Method: "GET", Path: "/mesh/status", Scopes: []string{"mesh.inspect"}, Semantics: read}, {Name: "events", Method: "GET", Path: "/mesh/events", Scopes: []string{"mesh.inspect"}, Semantics: Semantics{ReadOnly: true, Idempotent: true, Streaming: true}}},
	}
	for index := range capabilities {
		if capabilities[index].ID == capability.ID {
			capabilities[index] = capability
			return capabilities
		}
	}
	return append(capabilities, capability)
}
