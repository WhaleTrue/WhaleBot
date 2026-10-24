package executor

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/WhaleTrue/WhaleBot/pkg/mesh"
)

func policyExecutor(t *testing.T) (*Executor, ed25519.PrivateKey) {
	t.Helper()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	node, err := mesh.New(mesh.Config{Manifest: mesh.Manifest{Identity: mesh.Identity{Namespace: "test", Name: "executor", Principal: "executor"}}, PrivateKey: key, Trust: map[string]mesh.TrustGrant{"executor": {PublicKey: base64.StdEncoding.EncodeToString(public), Namespace: "test", Scopes: []string{"mesh.inspect"}}}})
	if err != nil {
		t.Fatal(err)
	}
	issuer, signing, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &Executor{node: node, dir: t.TempDir(), options: Options{NodeImage: "mesh:approved", AllowedImages: []string{"hello:approved"}, ApprovalKeys: map[string]ed25519.PublicKey{"admin": issuer}}, state: state{Approvals: map[string]Approval{}, Deployments: map[string]desired{}, Activity: map[string]time.Time{}, Jobs: map[string]Job{}}}, signing
}
func TestApprovalBindsIssuerOwnerNamespaceAndImmutableImage(t *testing.T) {
	e, key := policyExecutor(t)
	image := "example/tool@sha256:" + strings.Repeat("a", 64)
	if err := e.imageAllowed("hello:approved", "alice", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.imageAllowed(image, "alice", nil); err == nil {
		t.Fatal("external image accepted without trusted approval")
	}
	approval := Approval{ID: "approval-1", Issuer: "admin", Subject: "alice", Namespace: "test", Action: "image.use", Resource: image, ExpiresAt: time.Now().Add(time.Hour)}
	payload, err := ApprovalPayload(approval)
	if err != nil {
		t.Fatal(err)
	}
	approval.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload))
	forged := approval
	forged.Subject = "bob"
	if err = e.approve(forged); err == nil {
		t.Fatal("unsigned owner substitution accepted")
	}
	if err = e.approve(approval); err != nil {
		t.Fatal(err)
	}
	if err = e.imageAllowed(image, "alice", []string{approval.ID}); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []struct{ image, owner string }{{image, "bob"}, {"example/other@sha256:" + strings.Repeat("b", 64), "alice"}, {"example/tool:latest", "alice"}} {
		if err = e.imageAllowed(attempt.image, attempt.owner, []string{approval.ID}); err == nil {
			t.Fatalf("approval escaped subject/resource: %+v", attempt)
		}
	}
	expired := approval
	expired.ID = "expired"
	expired.ExpiresAt = time.Now().Add(-time.Second)
	payload, _ = ApprovalPayload(expired)
	expired.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload))
	if err = e.approve(expired); err == nil {
		t.Fatal("expired approval accepted")
	}
	foreign := approval
	foreign.ID = "foreign"
	foreign.Namespace = "other"
	payload, _ = ApprovalPayload(foreign)
	foreign.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload))
	if err = e.approve(foreign); err == nil {
		t.Fatal("cross-namespace approval accepted")
	}
}
func TestOwnedResourcesNeverIncludeUnmanagedOrForeignContainers(t *testing.T) {
	e, _ := policyExecutor(t)
	labels := e.labels("alice", "task", "session", "session-a", "")
	if !e.owned(labels, "alice", false) {
		t.Fatal("owner cannot use its managed resource")
	}
	if e.owned(labels, "bob", false) {
		t.Fatal("foreign owner can use resource")
	}
	if !e.owned(labels, "admin", true) {
		t.Fatal("authorized namespace administrator cannot inspect managed resource")
	}
	if e.owned(map[string]string{}, "admin", true) {
		t.Fatal("admin can control unmanaged Docker")
	}
	labels[labelPrefix+"namespace"] = "other"
	if e.owned(labels, "admin", true) {
		t.Fatal("admin can control foreign namespace")
	}
	labels = e.labels("alice", "task", "global", "", "")
	labels[labelPrefix+"executor"] = "other-executor"
	if e.owned(labels, "admin", true) {
		t.Fatal("executor can control another executor's resources")
	}
}
func TestWorkspaceBinaryRoundTripAndEscapeRejection(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	data := []byte{0, 1, 2, 0xff, '\n'}
	if _, err = fileOperation(root, FileRequest{Operation: "write", Path: "/workspace/nested/data.bin", ContentBase64: base64.StdEncoding.EncodeToString(data)}); err != nil {
		t.Fatal(err)
	}
	result, err := fileOperation(root, FileRequest{Operation: "read", Path: "nested/data.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.Content, data) {
		t.Fatalf("binary corrupted: %x", result.Content)
	}
	for _, path := range []string{"../outside", "/etc/passwd", "/workspace/../../outside", "a\\..\\outside"} {
		if _, err = fileOperation(root, FileRequest{Operation: "read", Path: path}); err == nil {
			t.Fatalf("unsafe path accepted: %s", path)
		}
	}
	outside := t.TempDir()
	if err = os.WriteFile(filepath.Join(outside, "private"), []byte("outside-value"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(directory, "escape")); err != nil {
		t.Logf("symlink creation unavailable on host: %v", err)
		return
	}
	if _, err = fileOperation(root, FileRequest{Operation: "read", Path: "escape/private"}); err == nil {
		t.Fatal("symlink escaped workspace")
	}
	if _, err = fileOperation(root, FileRequest{Operation: "write", Path: "escape/private", ContentBase64: base64.StdEncoding.EncodeToString([]byte("changed"))}); err == nil {
		t.Fatal("symlink write escaped workspace")
	}
	unchanged, err := os.ReadFile(filepath.Join(outside, "private"))
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != "outside-value" {
		t.Fatal("outside file changed")
	}
}

func TestDeploymentTemplateUsesAssignedIdentityAndMergesRequirements(t *testing.T) {
	e, _ := policyExecutor(t)
	e.options.IdentitiesDir = t.TempDir()
	d := mesh.Deployment{
		Schema: mesh.DeploymentSchema, ID: "hello", Namespace: "test",
		Node:     mesh.NodeTemplate{Name: "logical-hello", Labels: map[string]string{"example": "hello"}, Annotations: map[string]string{"description": "SDK node"}, Requires: []mesh.Requirement{{ID: "model", Capability: "llm.invoke", Scopes: []string{"llm.invoke"}}}},
		Requires: []mesh.Requirement{{ID: "store", Capability: "session.read", Scopes: []string{"session.read"}}},
		Image:    "hello:approved", Resources: mesh.Resources{MemoryBytes: 64 << 20, NanoCPUs: 100000000, PidsLimit: 64},
		Network: mesh.NetworkPolicy{Name: "mesh"}, Replicas: 1, Scope: "global", RestartPolicy: "no", DesiredState: "running",
	}
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mesh.DecodeDeployment(data); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"manifest":{}`, `"node":{"name":"hello","principal":"forged"}`, `"node":{"name":"hello","provides":[]}`, `"node":{"name":"hello","labels":{"api_key":"secret"}}`, `"node":{"name":"hello","requires":[{"capability":"llm.invoke","version":">=broken"}]}`} {
		var document map[string]json.RawMessage
		if err = json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		var injected map[string]json.RawMessage
		if err = json.Unmarshal([]byte("{"+field+"}"), &injected); err != nil {
			t.Fatal(err)
		}
		for key, value := range injected {
			document[key] = value
		}
		invalid, _ := json.Marshal(document)
		if _, err = mesh.DecodeDeployment(invalid); err == nil {
			t.Fatalf("unsafe template accepted: %s", field)
		}
	}
	duplicate := d
	duplicate.Requires = d.Node.Requires
	if err = mesh.ValidateDeployment(duplicate); err == nil {
		t.Fatal("duplicate merged requirement ID accepted")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(e.options.IdentitiesDir, "hello-0")
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, value any) {
		t.Helper()
		encoded, encodeErr := json.Marshal(value)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if writeErr := os.WriteFile(filepath.Join(directory, name), encoded, 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	write("identity.json", map[string]string{"principal": "assigned-child", "private_key": base64.StdEncoding.EncodeToString(private)})
	grant := mesh.TrustGrant{Namespace: "test", PublicKey: base64.StdEncoding.EncodeToString(public), Scopes: []string{"llm.*", "session.read"}}
	write("trust.json", map[string]mesh.TrustGrant{"assigned-child": grant})
	if _, _, err = e.identity(d, 0); err != nil {
		t.Fatalf("child scopes incorrectly checked against executor: %v", err)
	}
	missing := d
	missing.Requires = []mesh.Requirement{{Capability: "admin.invoke", Scopes: []string{"executor.admin"}}}
	if _, _, err = e.identity(missing, 0); err == nil {
		t.Fatal("assigned identity missing required scope accepted")
	}
	grant.Namespace = "foreign"
	write("trust.json", map[string]mesh.TrustGrant{"assigned-child": grant})
	if _, _, err = e.identity(d, 0); err == nil {
		t.Fatal("foreign namespace pool identity accepted")
	}
	grant.Namespace = "test"
	write("trust.json", map[string]mesh.TrustGrant{"assigned-child": grant})
	manifest := childManifest(d, "trusted-host")
	write("manifest.json", manifest)
	manifestData, _ := json.Marshal(manifest)
	if _, err = mesh.DecodeManifest(manifestData); err == nil {
		t.Fatal("public running-manifest decoder accepted incomplete configuration")
	}
	for key, value := range map[string]string{
		"MESH_MANIFEST":      filepath.Join(directory, "manifest.json"),
		"MESH_IDENTITY_FILE": filepath.Join(directory, "identity.json"),
		"MESH_TRUST_FILE":    filepath.Join(directory, "trust.json"),
		"MESH_NAMESPACE":     "", "MESH_NAME": "", "MESH_HOST_ID": "", "MESH_ADVERTISE": "", "MESH_SEEDS": "",
		"MESH_TLS_CERT": "", "MESH_TLS_KEY": "", "MESH_CA_FILE": "", "MESH_INTERVAL": "", "MESH_TTL": "",
		"MESH_DATA": t.TempDir(),
	} {
		t.Setenv(key, value)
	}
	config, err := mesh.LoadConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	node, err := mesh.New(config)
	if err != nil {
		t.Fatal(err)
	}
	running := node.Manifest()
	if running.Identity.Principal != "assigned-child" || running.Identity.Name != d.Node.Name || running.Identity.HostID != "trusted-host" || running.Identity.InstanceID == "" || len(running.Requires) != 2 || running.Requires[0].ID != "model" || running.Requires[1].ID != "store" || running.Identity.Labels["example"] != "hello" {
		t.Fatalf("child configuration lost intent or trusted identity: %+v", running)
	}
	if len(running.Provides) != 0 || running.Security.Identity != "assigned-child" {
		t.Fatal("template invents capabilities or identity")
	}
	next, err := mesh.New(config)
	if err != nil {
		t.Fatal(err)
	}
	if next.Manifest().Identity.InstanceID == running.Identity.InstanceID {
		t.Fatal("child startup reused instance")
	}
	for _, invalid := range []string{`{"identity":{"name":"hello","name":"other"}}`, `{"unknown":true}`, strings.Repeat(" ", mesh.MaxDocumentBytes+1)} {
		if err = os.WriteFile(filepath.Join(directory, "manifest.json"), []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = mesh.LoadConfigFromEnv(); err == nil {
			t.Fatal("unsafe private manifest configuration accepted")
		}
	}
}
