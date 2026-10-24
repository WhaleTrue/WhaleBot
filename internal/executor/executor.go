package executor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/WhaleMesh/WhaleMesh/pkg/mesh"
)

const labelPrefix = "io.whalemesh."

type Approval struct {
	ID        string    `json:"id"`
	Issuer    string    `json:"issuer"`
	Subject   string    `json:"subject"`
	Namespace string    `json:"namespace"`
	Action    string    `json:"action"`
	Resource  string    `json:"resource"`
	ExpiresAt time.Time `json:"expires_at"`
	Signature string    `json:"signature,omitempty"`
}

func ApprovalPayload(a Approval) ([]byte, error) { a.Signature = ""; return json.Marshal(a) }

type Job struct {
	ID          string    `json:"id"`
	Principal   string    `json:"principal"`
	Kind        string    `json:"kind"`
	ContainerID string    `json:"container_id,omitempty"`
	EngineID    string    `json:"engine_id,omitempty"`
	State       string    `json:"state"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	Stdout      string    `json:"stdout,omitempty"`
	Stderr      string    `json:"stderr,omitempty"`
	ExitCode    int       `json:"exit_code"`
	Error       string    `json:"error,omitempty"`
}
type desired struct {
	Deployment mesh.Deployment `json:"deployment"`
	Principal  string          `json:"principal"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Error      string          `json:"error,omitempty"`
}
type state struct {
	Approvals   map[string]Approval  `json:"approvals"`
	Deployments map[string]desired   `json:"deployments"`
	Activity    map[string]time.Time `json:"activity"`
	Jobs        map[string]Job       `json:"jobs"`
}
type Options struct {
	Context                              context.Context
	Engine                               *Engine
	Network, NodeImage, IdentitiesDir    string
	AllowedImages                        []string
	ApprovalKeys                         map[string]ed25519.PublicKey
	MaxMemoryBytes, MaxNanoCPUs, MaxPids int64
}
type Executor struct {
	node      *mesh.Node
	engine    *Engine
	options   Options
	dir       string
	mu        sync.Mutex
	operation sync.Mutex
	state     state
	ctx       context.Context
	jobs      chan struct{}
}

func Register(node *mesh.Node, dataDir string) ([]mesh.Capability, error) {
	return RegisterContext(context.Background(), node, dataDir)
}
func RegisterContext(ctx context.Context, node *mesh.Node, dataDir string) ([]mesh.Capability, error) {
	o := Options{Context: ctx, Network: os.Getenv("EXECUTOR_NETWORK"), NodeImage: os.Getenv("EXECUTOR_NODE_IMAGE"), IdentitiesDir: os.Getenv("EXECUTOR_IDENTITIES_DIR"), AllowedImages: strings.Split(os.Getenv("EXECUTOR_ALLOWED_IMAGES"), ",")}
	if file := os.Getenv("EXECUTOR_APPROVAL_KEYS_FILE"); file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var raw map[string]string
		if err = json.Unmarshal(b, &raw); err != nil {
			return nil, err
		}
		o.ApprovalKeys = map[string]ed25519.PublicKey{}
		for issuer, value := range raw {
			k, err := base64.StdEncoding.DecodeString(value)
			if err != nil || len(k) != ed25519.PublicKeySize {
				return nil, errors.New("invalid approval public key")
			}
			o.ApprovalKeys[issuer] = ed25519.PublicKey(k)
		}
	}
	engine, err := NewEngine(os.Getenv("DOCKER_HOST"), os.Getenv("EXECUTOR_ALLOW_LOCAL_TCP") == "true")
	if err != nil {
		return nil, err
	}
	o.Engine = engine
	lifetime, cancel := context.WithCancel(ctx)
	o.Context = lifetime
	e, err := New(node, dataDir, o)
	if err != nil {
		cancel()
		return nil, err
	}
	caps := e.register()
	if err = node.AddWorker(func(lifecycle context.Context) error {
		stop := context.AfterFunc(lifecycle, cancel)
		defer stop()
		defer cancel()
		e.loop()
		return nil
	}); err != nil {
		cancel()
		return nil, err
	}
	return caps, nil
}
func New(node *mesh.Node, dataDir string, o Options) (*Executor, error) {
	if o.Context == nil {
		o.Context = context.Background()
	}
	if o.Engine == nil {
		return nil, errors.New("Docker Engine required")
	}
	if o.Network == "" {
		return nil, errors.New("EXECUTOR_NETWORK required; host/default networks are never implicit")
	}
	if o.NodeImage == "" {
		return nil, errors.New("EXECUTOR_NODE_IMAGE required")
	}
	if o.MaxMemoryBytes == 0 {
		o.MaxMemoryBytes = 2 << 30
	}
	if o.MaxNanoCPUs == 0 {
		o.MaxNanoCPUs = 2e9
	}
	if o.MaxPids == 0 {
		o.MaxPids = 256
	}
	dir := filepath.Join(dataDir, "executor")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	e := &Executor{node: node, engine: o.Engine, options: o, dir: dir, ctx: o.Context, jobs: make(chan struct{}, 8), state: state{Approvals: map[string]Approval{}, Deployments: map[string]desired{}, Activity: map[string]time.Time{}, Jobs: map[string]Job{}}}
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err == nil {
		if err = json.Unmarshal(b, &e.state); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if e.state.Approvals == nil {
		e.state.Approvals = map[string]Approval{}
	}
	if e.state.Deployments == nil {
		e.state.Deployments = map[string]desired{}
	}
	if e.state.Activity == nil {
		e.state.Activity = map[string]time.Time{}
	}
	if e.state.Jobs == nil {
		e.state.Jobs = map[string]Job{}
	}
	for id, j := range e.state.Jobs {
		if j.State == "running" || j.State == "queued" {
			j.State = "interrupted"
			j.Error = "executor restarted; inspect EngineID before retrying execution"
			j.FinishedAt = time.Now().UTC()
			e.state.Jobs[id] = j
		}
	}
	if err = e.saveLocked(); err != nil {
		return nil, err
	}
	return e, nil
}
func (e *Executor) saveLocked() error {
	b, err := json.Marshal(e.state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(e.dir, ".state-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(e.dir, "state.json"))
}
func (e *Executor) approve(a Approval) error {
	key := e.options.ApprovalKeys[a.Issuer]
	if len(key) != ed25519.PublicKeySize {
		return errors.New("untrusted approval issuer")
	}
	if a.ID == "" || a.Subject == "" || a.Namespace != e.node.Manifest().Identity.Namespace || a.Action != "image.use" || a.Resource == "" || !a.ExpiresAt.After(time.Now()) {
		return errors.New("invalid approval claims")
	}
	payload, _ := ApprovalPayload(a)
	signature, err := base64.StdEncoding.DecodeString(a.Signature)
	if err != nil || !ed25519.Verify(key, payload, signature) {
		return errors.New("invalid approval signature")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if previous, ok := e.state.Approvals[a.ID]; ok && previous != a {
		return errors.New("approval ID already exists")
	}
	e.state.Approvals[a.ID] = a
	return e.saveLocked()
}
func (e *Executor) imageAllowed(image, principal string, ids []string) error {
	if image == e.options.NodeImage {
		return nil
	}
	for _, allowed := range e.options.AllowedImages {
		if image != "" && image == strings.TrimSpace(allowed) {
			return nil
		}
	}
	if !strings.Contains(image, "@sha256:") {
		return errors.New("external images require immutable digest and signed image.use approval")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, id := range ids {
		a, ok := e.state.Approvals[id]
		if !ok {
			continue
		}
		key := e.options.ApprovalKeys[a.Issuer]
		payload, _ := ApprovalPayload(a)
		signature, _ := base64.StdEncoding.DecodeString(a.Signature)
		if len(key) == ed25519.PublicKeySize && ed25519.Verify(key, payload, signature) && a.Subject == principal && a.Namespace == e.node.Manifest().Identity.Namespace && a.Action == "image.use" && a.Resource == image && a.ExpiresAt.After(time.Now()) {
			return nil
		}
	}
	return errors.New("trusted image.use approval required")
}
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	d := json.NewDecoder(io.LimitReader(r.Body, 24<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("exactly one JSON object required")
	}
	return nil
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, err error) {
	respond(w, 400, map[string]any{"error": map[string]string{"code": "executor_error", "message": err.Error()}})
}

type delegatedOwnerKey struct{}

func (e *Executor) admin(r *http.Request) bool {
	return r.Context().Value(delegatedOwnerKey{}) == nil && e.node.Authorized(r, "executor.admin")
}
func (e *Executor) register() []mesh.Capability {
	type route struct {
		method, path, scope string
		handler             http.HandlerFunc
		read                bool
	}
	routes := []route{
		{"GET", "/executor/containers", "container.manage", e.listHTTP, true}, {"POST", "/executor/containers/action", "container.manage", e.actionHTTP, false}, {"GET", "/executor/logs", "container.manage", e.logsHTTP, true},
		{"POST", "/executor/exec", "container.exec", e.execHTTP, false}, {"GET", "/executor/jobs", "container.exec", e.jobHTTP, true},
		{"POST", "/executor/files", "container.files", e.filesHTTP, false}, {"POST", "/executor/artifacts/export", "artifact.export", e.exportHTTP, false}, {"GET", "/executor/artifacts/download", "artifact.export", e.downloadHTTP, true},
		{"GET", "/executor/images", "container.images", e.imagesHTTP, true}, {"POST", "/executor/images/pull", "container.images", e.pullHTTP, false}, {"GET", "/executor/images/jobs", "container.images", e.jobHTTP, true},
		{"GET", "/executor/deployments", "deployment.reconcile", e.deploymentsHTTP, true}, {"PUT", "/executor/deployments", "deployment.reconcile", e.deploymentsHTTP, false}, {"DELETE", "/executor/deployments", "deployment.reconcile", e.deploymentsHTTP, false},
		{"POST", "/executor/approvals", "executor.admin", e.approvalHTTP, false}, {"DELETE", "/executor/approvals", "executor.admin", e.approvalHTTP, false},
	}
	capabilities := map[string]*mesh.Capability{}
	for _, r := range routes {
		e.node.Handle(r.method, r.path, []string{r.scope}, r.handler)
		if r.scope == "executor.admin" {
			continue
		}
		c := capabilities[r.scope]
		if c == nil {
			c = &mesh.Capability{Schema: mesh.CapabilitySchema, ID: r.scope, Interface: r.scope + ".v1", Version: "1.0.0", Endpoint: r.path, Ready: true, Scopes: []string{r.scope}, Semantics: mesh.Semantics{Mutating: true, SideEffecting: true}}
			capabilities[r.scope] = c
		}
		c.Operations = append(c.Operations, mesh.Operation{Name: strings.ToLower(r.method) + " " + r.path, Method: r.method, Path: r.path, Scopes: []string{r.scope}, Semantics: mesh.Semantics{ReadOnly: r.read, Mutating: !r.read, SideEffecting: !r.read}})
	}
	attachTools(capabilities)
	result := make([]mesh.Capability, 0, len(capabilities))
	for _, c := range capabilities {
		result = append(result, *c)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func (e *Executor) approvalHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == "DELETE" {
		e.mu.Lock()
		delete(e.state.Approvals, r.URL.Query().Get("id"))
		err := e.saveLocked()
		e.mu.Unlock()
		if err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, map[string]bool{"revoked": true})
		return
	}
	var a Approval
	if err := decode(r, &a); err != nil {
		fail(w, err)
		return
	}
	if err := e.approve(a); err != nil {
		fail(w, err)
		return
	}
	respond(w, 201, a)
}
func (e *Executor) touch(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state.Activity[id] = time.Now().UTC()
	return e.saveLocked()
}
func deploymentKey(namespace, id string) string { return namespace + "/" + id }
func identifier(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func (e *Executor) labels(principal, deployment, scope, session, task string) map[string]string {
	return map[string]string{labelPrefix + "managed": "true", labelPrefix + "executor": e.node.Manifest().Identity.Principal, labelPrefix + "executor_key": e.node.KeyID(), labelPrefix + "namespace": e.node.Manifest().Identity.Namespace, labelPrefix + "principal": principal, labelPrefix + "deployment": deployment, labelPrefix + "scope": scope, labelPrefix + "session": session, labelPrefix + "task": task}
}
func (e *Executor) owned(labels map[string]string, principal string, admin bool) bool {
	return labels[labelPrefix+"managed"] == "true" && labels[labelPrefix+"executor"] == e.node.Manifest().Identity.Principal && labels[labelPrefix+"executor_key"] == e.node.KeyID() && labels[labelPrefix+"namespace"] == e.node.Manifest().Identity.Namespace && (admin || labels[labelPrefix+"principal"] == principal)
}
