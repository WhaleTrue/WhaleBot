package mesh

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type Node struct {
	mu                sync.RWMutex
	manifest          Manifest
	key               ed25519.PrivateKey
	keyID             string
	trust             map[string]TrustGrant
	config            Config
	mux               *http.ServeMux
	server            *http.Server
	listener          net.Listener
	client            *http.Client
	peers             map[string]member
	watermarks        map[string]watermark
	membershipVersion uint64
	membershipFlushed uint64
	membershipPersist sync.Mutex
	targets           map[string][]string
	gossipCursor      int
	routeFailures     map[string]time.Time
	nonces            map[string]time.Time
	sticky            map[string]string
	policies          map[string]policyOverride
	nonceCounts       map[string]int
	nextNonceSweep    time.Time
	rounds            map[string]uint64
	active            map[string]int
	inflight          map[string]int
	inflightTotal     int
	bindings          map[string][]Binding
	workers           []func(context.Context) error
	subscribers       map[uint64]chan TopologyEvent
	nextSubscriber    uint64
	done              chan struct{}
	snapshotCursor    int
	ctx               context.Context
	cancel            context.CancelFunc
	started           bool
	closed            bool
	sequence          uint64
	wg                sync.WaitGroup
	requests          atomic.Uint64
	failures          atomic.Uint64
	dropped           atomic.Uint64
}
type policyOverride struct {
	Policy string `json:"policy"`
	Pin    string `json:"pin"`
}
type resolverState struct {
	Sticky   map[string]string         `json:"sticky"`
	Policies map[string]policyOverride `json:"policies"`
}

func New(c Config) (*Node, error) {
	if c.Manifest.Identity.Namespace == "" {
		c.Manifest.Identity.Namespace = "default"
	}
	if c.Manifest.Identity.Name == "" {
		c.Manifest.Identity.Name = "node"
	}
	if err := loadCredentials(&c); err != nil {
		return nil, err
	}
	c.Manifest.Identity.InstanceID = randomID()
	c.Manifest.Schema = "whalemesh.node/v1"
	c.Manifest.Security.Identity = c.Manifest.Identity.Principal
	c.Manifest.Security.TrustNamespace = c.Manifest.Identity.Namespace
	c.Manifest.Security.Authentication = []string{"ed25519"}
	c.Manifest.Security.Permissions = append([]string(nil), c.Trust[c.Manifest.Identity.Principal].Scopes...)
	c.Manifest.Transport.Security = "ed25519"
	if c.TLSCert != "" {
		c.Manifest.Transport.Security = "tls+ed25519"
	}
	if len(c.Manifest.Transport.Protocols) == 0 {
		if c.TLSCert != "" {
			c.Manifest.Transport.Protocols = []string{"https"}
		} else {
			c.Manifest.Transport.Protocols = []string{"http"}
		}
	}
	if c.Manifest.Transport.Reachability == "" {
		c.Manifest.Transport.Reachability = "direct"
	}
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	c.Manifest.Transport.Listen = []string{c.Listen}
	c.Manifest.Lifecycle = Lifecycle{Alive: true, Ready: true}
	if c.Interval == 0 {
		c.Interval = 2 * time.Second
	}
	if c.TTL == 0 {
		c.TTL = 15 * time.Second
	}
	if c.Interval < 10*time.Millisecond || c.TTL < 3*c.Interval || c.TTL > 24*time.Hour {
		return nil, fmt.Errorf("require interval >=10ms and 3*interval <= TTL <=24h")
	}
	c.Manifest.Provides = withInspection(c.Manifest.Provides, c.Trust[c.Manifest.Identity.Principal])
	for i := range c.Manifest.Provides {
		if c.Manifest.Provides[i].Schema == "" {
			c.Manifest.Provides[i].Schema = "whalemesh.capability/v1"
		}
	}
	if err := ValidateManifest(c.Manifest); err != nil {
		return nil, err
	}
	if err := validateProvides(c.Manifest, c.Trust[c.Manifest.Identity.Principal]); err != nil {
		return nil, err
	}
	if len(c.Trust) > 4096 || len(c.Seeds) > 16 {
		return nil, fmt.Errorf("trust supports at most 4096 principals and bootstrap supports at most 16 seeds")
	}
	for _, seed := range c.Seeds {
		if err := validateAddress(seed); err != nil {
			return nil, err
		}
	}
	n := &Node{manifest: cloneManifest(c.Manifest), key: append(ed25519.PrivateKey(nil), c.PrivateKey...), trust: make(map[string]TrustGrant), config: c, mux: http.NewServeMux(), peers: map[string]member{}, watermarks: map[string]watermark{}, targets: map[string][]string{}, nonces: map[string]time.Time{}, sticky: map[string]string{}, policies: map[string]policyOverride{}, rounds: map[string]uint64{}, active: map[string]int{}, bindings: map[string][]Binding{}, subscribers: make(map[uint64]chan TopologyEvent), done: make(chan struct{})}
	n.routeFailures = map[string]time.Time{}
	keyHash := sha256.Sum256(c.PrivateKey[ed25519.SeedSize:])
	n.keyID = hex.EncodeToString(keyHash[:])
	c.Seeds = append([]string(nil), c.Seeds...)
	n.config.Seeds = c.Seeds
	for p, g := range c.Trust {
		g.Scopes = append([]string(nil), g.Scopes...)
		g.Provides = append([]string(nil), g.Provides...)
		n.trust[p] = g
	}
	tlsConfig, err := clientTLSConfig(c)
	if err != nil {
		return nil, err
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return nil, fmt.Errorf("both TLS certificate and key required")
	}
	if c.TLSCert != "" {
		if _, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey); err != nil {
			return nil, err
		}
	}
	n.client = &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: tlsConfig, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 60 * time.Second, MaxIdleConns: 100}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	if c.DataDir != "" {
		if err := os.MkdirAll(c.DataDir, 0700); err != nil {
			return nil, err
		}
		if data, err := os.ReadFile(filepath.Join(c.DataDir, "resolver.json")); err == nil {
			var state resolverState
			if err = json.Unmarshal(data, &state); err != nil {
				return nil, fmt.Errorf("resolver state: %w", err)
			}
			if state.Sticky != nil {
				n.sticky = state.Sticky
			}
			if state.Policies != nil {
				n.policies = state.Policies
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if data, err := os.ReadFile(filepath.Join(c.DataDir, "membership.json")); err == nil {
			if err = json.Unmarshal(data, &n.watermarks); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	n.HandlePublic("GET", "/healthz", func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, 200, map[string]bool{"alive": n.Manifest().Lifecycle.Alive})
	})
	n.Handle("POST", "/mesh/exchange", []string{"mesh.peer"}, n.handleExchange)
	n.Handle("GET", "/mesh/identity", []string{"mesh.peer"}, func(w http.ResponseWriter, r *http.Request) {
		n.mu.Lock()
		signed := n.announcementLocked()
		n.mu.Unlock()
		WriteJSON(w, 200, signed)
	})
	n.Handle("GET", "/mesh/manifest", []string{"mesh.inspect"}, func(w http.ResponseWriter, r *http.Request) { WriteJSON(w, 200, n.Manifest()) })
	n.Handle("GET", "/mesh/peers", []string{"mesh.inspect"}, func(w http.ResponseWriter, r *http.Request) { WriteJSON(w, 200, n.Peers()) })
	n.Handle("GET", "/mesh/status", []string{"mesh.inspect"}, n.inspection)
	n.Handle("GET", "/mesh/events", []string{"mesh.inspect"}, n.streamTopology)
	n.Handle("GET", "/.well-known/whalemesh", []string{"mesh.peer"}, func(w http.ResponseWriter, r *http.Request) {
		n.mu.Lock()
		signed := n.announcementLocked()
		n.mu.Unlock()
		WriteJSON(w, 200, signed)
	})
	return n, nil
}
func (n *Node) Handle(method, path string, scopes []string, handler http.HandlerFunc) {
	required := append([]string(nil), scopes...)
	n.mux.HandleFunc(method+" "+path, func(w http.ResponseWriter, r *http.Request) {
		n.requests.Add(1)
		n.authenticate(w, r, required, handler)
	})
}

// HandlePublic is reserved for browser login/static and handlers which perform
// their own authentication. It never grants a mesh principal.
func (n *Node) HandlePublic(method, path string, handler http.HandlerFunc) {
	pattern := path
	if method != "" {
		pattern = method + " " + path
	}
	n.mux.HandleFunc(pattern, handler)
}
func (n *Node) Handler() http.Handler { return n.mux }
func (n *Node) DataDir() string       { return n.config.DataDir }

// KeyID is the stable public-key fingerprint for authority-scoped resources.
func (n *Node) KeyID() string { return n.keyID }
func (n *Node) Manifest() Manifest {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return cloneManifest(n.manifest)
}
func (n *Node) AddWorker(worker func(context.Context) error) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.started || n.closed {
		return fmt.Errorf("workers must register before Start")
	}
	n.workers = append(n.workers, worker)
	return nil
}
func (n *Node) AddAdvertise(address string) error {
	if err := validateAddress(address); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, a := range n.manifest.Transport.Advertise {
		if a == address {
			return nil
		}
	}
	n.manifest.Transport.Advertise = append(n.manifest.Transport.Advertise, address)
	return nil
}
func (n *Node) Start(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.started || n.closed {
		return fmt.Errorf("node already started or closed")
	}
	listener, err := net.Listen("tcp", n.config.Listen)
	if err != nil {
		return err
	}
	if n.config.TLSCert != "" {
		certificate, loadErr := tls.LoadX509KeyPair(n.config.TLSCert, n.config.TLSKey)
		if loadErr != nil {
			_ = listener.Close()
			return loadErr
		}
		listener = tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}})
	}
	n.listener = listener
	if len(n.manifest.Transport.Advertise) == 0 {
		host, port, _ := net.SplitHostPort(listener.Addr().String())
		if host == "" || host == "::" || host == "0.0.0.0" {
			host = "127.0.0.1"
		}
		scheme := "http"
		if n.config.TLSCert != "" {
			scheme = "https"
		}
		n.manifest.Transport.Advertise = []string{scheme + "://" + net.JoinHostPort(host, port)}
	}
	for _, address := range n.manifest.Transport.Advertise {
		if len(address) >= 7 && address[:7] == "http://" {
			slog.Warn("mesh HTTP signatures do not encrypt traffic; use HTTPS outside an isolated trusted bridge", "address", address)
		}
	}
	if n.config.AllowPlaintextSensitive {
		slog.Warn("plaintext sensitive operations explicitly enabled; use only on isolated trusted bridge")
	}
	n.ctx, n.cancel = context.WithCancel(ctx)
	n.server = &http.Server{Handler: n.mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10, BaseContext: func(net.Listener) context.Context { return n.ctx }}
	n.started = true
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		if err := n.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("mesh listener failed", "error", err)
			n.cancel()
		}
	}()
	n.wg.Add(1)
	go func() { defer n.wg.Done(); n.gossipLoop(n.ctx) }()
	if n.config.DataDir != "" {
		n.wg.Add(1)
		go func() { defer n.wg.Done(); n.checkpointMembershipLoop(n.ctx) }()
	}
	for _, worker := range n.workers {
		n.wg.Add(1)
		go func(run func(context.Context) error) {
			defer n.wg.Done()
			if err := run(n.ctx); err != nil && n.ctx.Err() == nil {
				slog.Error("mesh worker stopped", "error", err)
				n.failures.Add(1)
			}
		}(worker)
	}
	n.emitLocked("started", n.manifest.Identity, 0, "")
	return nil
}
func (n *Node) Close(ctx context.Context) error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	n.manifest.Lifecycle.Draining = true
	n.manifest.Lifecycle.Ready = false
	started := n.started
	n.emitLocked("draining", n.manifest.Identity, n.sequence, "")
	close(n.done)
	n.mu.Unlock()
	if !started {
		return n.flushMembership()
	}
	// Publish a signed tombstone before stopping transports. Bound by caller deadline.
	drainCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	n.exchangeAll(drainCtx)
	cancel()
	n.cancel()
	err := n.server.Shutdown(ctx)
	if err != nil {
		_ = n.server.Close()
	}
	n.client.CloseIdleConnections()
	done := make(chan struct{})
	go func() { n.wg.Wait(); close(done) }()
	select {
	case <-done:
		err = errors.Join(err, n.flushMembership())
	case <-ctx.Done():
		if err == nil {
			err = ctx.Err()
		}
	}
	n.mu.Lock()
	n.manifest.Lifecycle.Alive = false
	n.emitLocked("stopped", n.manifest.Identity, n.sequence, "")
	n.mu.Unlock()
	return err
}
func (n *Node) UpdateCapabilities(capabilities []Capability) error {
	copy := cloneManifest(Manifest{Provides: capabilities}).Provides
	copy = withInspection(copy, n.trust[n.config.Manifest.Identity.Principal])
	for i := range copy {
		if copy[i].Schema == "" {
			copy[i].Schema = "whalemesh.capability/v1"
		}
		if err := ValidateCapability(copy[i]); err != nil {
			return err
		}
	}
	manifest := n.Manifest()
	manifest.Provides = copy
	if err := ValidateManifest(manifest); err != nil {
		return err
	}
	if err := validateProvides(manifest, n.trust[manifest.Identity.Principal]); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.manifest.Provides = copy
	n.emitLocked("capabilities", n.manifest.Identity, n.sequence, "")
	return nil
}
func (n *Node) UpdateRequirements(requirements []Requirement) error {
	copy := cloneManifest(Manifest{Requires: requirements}).Requires
	ids := map[string]bool{}
	for _, r := range copy {
		if err := ValidateRequirement(r); err != nil {
			return err
		}
		if r.ID != "" && ids[r.ID] {
			return fmt.Errorf("duplicate requirement ID %q", r.ID)
		}
		ids[r.ID] = true
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.manifest.Requires = copy
	n.emitLocked("requirements", n.manifest.Identity, n.sequence, "")
	return nil
}
func (n *Node) emitLocked(kind string, identity Identity, sequence uint64, reason string) {
	if len(n.subscribers) == 0 {
		return
	}
	at := time.Now()
	for _, events := range n.subscribers {
		if len(events) == cap(events) {
			n.dropped.Add(1)
			continue
		}
		copy := identity
		copy.Labels = cloneMap(identity.Labels)
		copy.Annotations = cloneMap(identity.Annotations)
		events <- TopologyEvent{Type: kind, Identity: copy, Sequence: sequence, At: at, Reason: reason}
	}
}
func cloneManifest(m Manifest) Manifest {
	m.Identity.Labels = cloneMap(m.Identity.Labels)
	m.Identity.Annotations = cloneMap(m.Identity.Annotations)
	m.Transport.Protocols = append([]string(nil), m.Transport.Protocols...)
	m.Transport.Listen = append([]string(nil), m.Transport.Listen...)
	m.Transport.Advertise = append([]string(nil), m.Transport.Advertise...)
	m.Security.Authentication = append([]string(nil), m.Security.Authentication...)
	m.Security.Permissions = append([]string(nil), m.Security.Permissions...)
	m.Metadata = cloneMap(m.Metadata)
	m.Provides = append([]Capability(nil), m.Provides...)
	for i := range m.Provides {
		c := &m.Provides[i]
		c.InputSchema = append(json.RawMessage(nil), c.InputSchema...)
		c.OutputSchema = append(json.RawMessage(nil), c.OutputSchema...)
		c.Operations = append([]Operation(nil), c.Operations...)
		for j := range c.Operations {
			c.Operations[j].Scopes = append([]string(nil), c.Operations[j].Scopes...)
		}
		c.Attributes = cloneMap(c.Attributes)
		c.Scopes = append([]string(nil), c.Scopes...)
		if c.Tool != nil {
			t := *c.Tool
			t.InputSchema = append(json.RawMessage(nil), t.InputSchema...)
			t.Scopes = append([]string(nil), t.Scopes...)
			c.Tool = &t
		}
	}
	m.Requires = append([]Requirement(nil), m.Requires...)
	for i := range m.Requires {
		m.Requires[i].Attributes = cloneMap(m.Requires[i].Attributes)
		m.Requires[i].Scopes = append([]string(nil), m.Requires[i].Scopes...)
		m.Requires[i].Providers = append([]string(nil), m.Requires[i].Providers...)
	}
	return m
}
func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	copy := make(map[string]string, len(m))
	for k, v := range m {
		copy[k] = v
	}
	return copy
}
func (n *Node) Peers() []Peer {
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make([]Peer, 0, len(n.peers))
	now := time.Now()
	for _, p := range n.peers {
		if p.Deadline.After(now) {
			out = append(out, Peer{Manifest: cloneManifest(p.Manifest), Sequence: p.Sequence, LastSeen: p.LastSeen})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return IdentityKey(out[i].Manifest.Identity) < IdentityKey(out[j].Manifest.Identity)
	})
	return out
}

// IdentityKey is the unambiguous namespace/principal/logical-name/instance ID.
// Identity components allow URI characters; never concatenate them unescaped.
func IdentityKey(i Identity) string {
	return logicalKey(i) + "/" + url.QueryEscape(i.InstanceID)
}
func logicalKey(i Identity) string {
	return i.Namespace + "/" + url.QueryEscape(i.Principal) + "/" + i.Name
}
func writeAtomic(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".mesh-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
