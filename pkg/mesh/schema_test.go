package mesh

import (
	"encoding/json"
	"strings"
	"testing"
)

func schemaManifest() Manifest {
	return Manifest{Schema: NodeSchema, Identity: Identity{Namespace: "default", Name: "hello", InstanceID: "instance-1", Principal: "hello-0"}, Transport: Transport{Protocols: []string{"http"}, Advertise: []string{"http://hello:8080"}}, Security: Security{Identity: "hello-0", TrustNamespace: "default"}, Lifecycle: Lifecycle{Alive: true, Ready: true}, Provides: []Capability{{Schema: CapabilitySchema, ID: "hello.invoke", Interface: "hello.invoke", Version: "1.0.0", Endpoint: "/hello", Ready: true, InputSchema: JSONSchemaFor(struct {
		Name string `json:"name"`
	}{}), OutputSchema: JSONSchemaFor(struct {
		Message string `json:"message"`
	}{}), Scopes: []string{"hello.invoke"}, Operations: []Operation{{Name: "greet", Method: "POST", Path: "/hello", Scopes: []string{"hello.invoke"}, Semantics: Semantics{ReadOnly: true, Idempotent: true}}}, Semantics: Semantics{ReadOnly: true, Idempotent: true}}}}
}
func schemaDeployment() Deployment {
	return Deployment{Schema: DeploymentSchema, ID: "hello", Namespace: "default", Node: NodeTemplate{Name: "hello"}, Image: "whalemesh/hello:local", Resources: Resources{MemoryBytes: 64 << 20, NanoCPUs: 100000000, PidsLimit: 64}, Network: NetworkPolicy{Name: "whalemesh"}, Replicas: 1, Scope: "task", TaskID: "task-1", TTLSeconds: 300, RestartPolicy: "no", DesiredState: "running"}
}
func TestManifestContract(t *testing.T) {
	if err := ValidateManifest(schemaManifest()); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*Manifest)
	}{
		{"version", func(m *Manifest) { m.Schema = "whalemesh.node/v2" }},
		{"principal", func(m *Manifest) { m.Security.Identity = "other" }},
		{"duplicate capability", func(m *Manifest) { m.Provides = append(m.Provides, m.Provides[0]) }},
		{"private metadata", func(m *Manifest) { m.Metadata = map[string]string{"api_key": "sensitive"} }},
		{"URL credential", func(m *Manifest) { m.Transport.Advertise = []string{"http://user:password@hello:8080"} }},
		{"invalid nested schema", func(m *Manifest) { m.Provides[0].InputSchema = json.RawMessage(`{"type":"unknown"}`) }},
		{"contradictory semantics", func(m *Manifest) { m.Provides[0].Semantics.Mutating = true }},
		{"operation scope union", func(m *Manifest) {
			m.Provides[0].Operations = []Operation{{Name: "greet", Method: "POST", Path: "/hello", Scopes: []string{"ungranted.scope"}}}
		}},
		{"unsafe endpoint", func(m *Manifest) { m.Provides[0].Endpoint = "/hello/../admin" }},
		{"encoded operation", func(m *Manifest) {
			m.Provides[0].Operations = []Operation{{Name: "greet", Method: "POST", Path: "/hello%2fadmin"}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := schemaManifest()
			test.change(&value)
			if err := ValidateManifest(value); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
	data, _ := json.Marshal(schemaManifest())
	for _, invalid := range []string{strings.Replace(string(data), `"identity":{`, `"identity":{"private_key":"bad",`, 1), strings.Replace(string(data), `"schema":"whalemesh.node/v1"`, `"schema":"whalemesh.node/v1","schema":"whalemesh.node/v1"`, 1), string(data) + ` {}`, strings.Repeat(" ", MaxDocumentBytes+1)} {
		if _, err := DecodeManifest([]byte(invalid)); err == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
}
func TestSchemaCompiler(t *testing.T) {
	for _, valid := range []string{`true`, `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"x":{"$ref":"#/$defs/value"}},"$defs":{"value":{"type":"string"}}}`} {
		if err := ValidateJSONSchema(json.RawMessage(valid)); err != nil {
			t.Fatal(err)
		}
	}
	for _, invalid := range []string{`null`, `{"type":"invalid"}`, `{"$schema":"http://json-schema.org/draft-07/schema#"}`, `{"$ref":"https://example.com/external.json"}`, `{"maxLength":-1}`, `{"type":"string","type":"number"}`} {
		if err := ValidateJSONSchema(json.RawMessage(invalid)); err == nil {
			t.Fatalf("invalid schema accepted: %s", invalid)
		}
	}
}
func TestRequirementCompatibility(t *testing.T) {
	for _, test := range []struct {
		offered, required string
		want              bool
	}{
		{"1.2.3", "1.2.3", true}, {"1.2.4", "1.2.3", false}, {"1.9.0", "^1.2.3", true}, {"2.0.0", "^1.2.3", false},
		{"0.2.4", "^0.2.3", true}, {"0.3.0", "^0.2.3", false}, {"0.0.4", "^0.0.3", false},
		{"1.2.9", "~1.2.3", true}, {"1.3.0", "~1.2.3", false}, {"1.5.0", ">=1.0.0 <2.0.0", true},
		{"1.5.0", ">=1.0.0, <2.0.0", true}, {"1.0.0-alpha", "*", false}, {"1.0.0-alpha.2", ">=1.0.0-alpha.1", true},
		{"1.0.0-alpha.2", ">=1.0.0-alpha.1 <2.0.0", true}, {"1.0.0", ">=1.0.0,,<2.0.0", false},
		{"01.0.0", "*", false}, {"1.0.0-01", "*", false}, {"1.0.0", "garbage", false}, {"1.0.0", "^", false},
	} {
		if got := CompatibleVersion(test.offered, test.required); got != test.want {
			t.Errorf("CompatibleVersion(%q,%q)=%v", test.offered, test.required, got)
		}
	}
	if err := ValidateRequirement(Requirement{Interface: "hello.invoke", Version: "^1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRequirement(Requirement{Capability: "hello.invoke", Version: ">=broken"}); err == nil {
		t.Fatal("invalid version requirement accepted")
	}
	if CompatibleInterface(schemaManifest().Provides[0], Requirement{Interface: "other", Version: "*"}) {
		t.Fatal("incompatible interface accepted")
	}
}
func TestDeploymentBoundary(t *testing.T) {
	value := schemaDeployment()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeDeployment(data); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*Deployment)
	}{
		{"unbounded task", func(d *Deployment) { d.TTLSeconds = 0 }},
		{"node name", func(d *Deployment) { d.Node.Name = "../other" }},
		{"host network", func(d *Deployment) { d.Network.Name = "host" }},
		{"resource", func(d *Deployment) { d.Resources.PidsLimit = 0 }},
		{"replicas", func(d *Deployment) { d.Replicas = 17 }},
		{"host volume", func(d *Deployment) { d.Volumes = []VolumeMount{{Source: "/var/run/docker.sock", Target: "/data"}} }},
		{"identity mount", func(d *Deployment) { d.Volumes = []VolumeMount{{Source: "data", Target: "/identity"}} }},
		{"identity ancestor", func(d *Deployment) { d.Volumes = []VolumeMount{{Source: "data", Target: "/run"}} }},
		{"traversal", func(d *Deployment) { d.Volumes = []VolumeMount{{Source: "data", Target: "/data/../etc"}} }},
		{"secret bytes", func(d *Deployment) { d.Env = map[string]string{"API_KEY": "secret"} }},
		{"identity override", func(d *Deployment) { d.Env = map[string]string{"MESH_IDENTITY_FILE": "/tmp/key"} }},
		{"bad secret target", func(d *Deployment) { d.Secrets = []SecretRef{{Name: "model", Target: "/tmp/token"}} }},
		{"reserved label", func(d *Deployment) { d.Labels = map[string]string{"whalemesh.owner": "forged"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := schemaDeployment()
			test.change(&value)
			if err := ValidateDeployment(value); err == nil {
				t.Fatal("unsafe deployment accepted")
			}
		})
	}
	invalid := strings.Replace(string(data), `"resources":{`, `"privileged":true,"resources":{`, 1)
	if _, err := DecodeDeployment([]byte(invalid)); err == nil {
		t.Fatal("unknown privileged field accepted")
	}
}

func TestDocumentDepth(t *testing.T) {
	_, err := parseDocument([]byte(strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66)))
	if err == nil {
		t.Fatalf("depth limit not enforced: %v", err)
	}
}

func TestCapabilityRequiresCompleteWireContract(t *testing.T) {
	base := schemaManifest().Provides[0]
	encoded, _ := json.Marshal(base)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"schema", "id", "interface", "version", "endpoint", "input_schema", "output_schema", "operations", "ready", "attributes", "scopes", "semantics"} {
		t.Run("missing "+name, func(t *testing.T) {
			incomplete := make(map[string]json.RawMessage, len(fields))
			for key, value := range fields {
				incomplete[key] = value
			}
			delete(incomplete, name)
			raw, _ := json.Marshal(incomplete)
			if _, err := DecodeCapability(raw); err == nil {
				t.Fatalf("missing %s accepted", name)
			}
		})
	}
	for _, name := range []string{"input_schema", "output_schema"} {
		invalid := base
		if name == "input_schema" {
			invalid.InputSchema = json.RawMessage("null")
		} else {
			invalid.OutputSchema = json.RawMessage("null")
		}
		if err := ValidateCapability(invalid); err == nil {
			t.Fatalf("null %s accepted", name)
		}
	}
	base.InputSchema = json.RawMessage(`{"type":"null"}`)
	base.Semantics.Streaming = true
	base.Operations[0].Semantics.Streaming = true
	raw, _ := json.Marshal(base)
	decoded, err := DecodeCapability(raw)
	if err != nil || !decoded.Semantics.Streaming || string(decoded.InputSchema) != `{"type":"null"}` {
		t.Fatalf("no-input streaming contract rejected: %+v %v", decoded, err)
	}
}
