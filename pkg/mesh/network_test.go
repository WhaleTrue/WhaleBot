package mesh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type networkFixture struct {
	keys  map[string]ed25519.PrivateKey
	trust map[string]TrustGrant
}

func meshNetwork(t *testing.T, names ...string) *networkFixture {
	t.Helper()
	f := &networkFixture{keys: map[string]ed25519.PrivateKey{}, trust: map[string]TrustGrant{}}
	for _, name := range names {
		public, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		f.keys[name] = key
		f.trust[name] = TrustGrant{PublicKey: base64.StdEncoding.EncodeToString(public), Namespace: "default", Scopes: []string{"mesh.peer", "mesh.inspect", "echo.read"}, Provides: []string{"echo.read"}}
	}
	return f
}
func echoCapability(state string) Capability {
	c := Capability{Schema: CapabilitySchema, ID: "echo.read", Interface: "echo.read", Version: "1.0.0", Endpoint: "/echo", Ready: true, Scopes: []string{"echo.read"}, Semantics: Semantics{ReadOnly: true, Idempotent: true}, Operations: []Operation{{Name: "echo", Method: "POST", Path: "/echo", Scopes: []string{"echo.read"}, Semantics: Semantics{ReadOnly: true, Idempotent: true}}}}
	c.InputSchema = JSONSchemaFor(map[string]any{})
	c.OutputSchema = JSONSchemaFor(map[string]any{})
	if state != "" {
		c.Attributes = map[string]string{"stateful": "true", "state_id": state}
	}
	return c
}
func (f *networkFixture) node(t *testing.T, name string, seeds []string, provide bool, state string, configure ...func(*Node)) *Node {
	t.Helper()
	m := Manifest{Identity: Identity{Namespace: "default", Name: name, Principal: name, HostID: "test-host"}}
	if provide {
		m.Provides = []Capability{echoCapability(state)}
	}
	n, err := New(Config{Manifest: m, Listen: "127.0.0.1:0", Seeds: seeds, PrivateKey: f.keys[name], Trust: f.trust, Interval: 30 * time.Millisecond, TTL: 300 * time.Millisecond, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if provide {
		n.Handle("POST", "/echo", []string{"echo.read"}, func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Value string `json:"value"`
			}
			if ReadJSON(w, r, &in) != nil {
				return
			}
			WriteJSON(w, 200, map[string]string{"value": in.Value, "provider": name})
		})
	}
	for _, setup := range configure {
		setup(n)
	}
	if err = n.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = n.Close(ctx)
	})
	return n
}
func awaitMesh(t *testing.T, predicate func() bool) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if predicate() {
			return
		}
		select {
		case <-timer.C:
			t.Fatal("mesh condition did not converge")
		case <-ticker.C:
		}
	}
}
func testCall(t *testing.T, caller *Node, b Binding, value string) {
	t.Helper()
	var output struct{ Value, Provider string }
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := caller.Call(ctx, b, "POST", "/echo", map[string]string{"value": value}, &output); err != nil {
		t.Fatal(err)
	}
	if output.Value != value || output.Provider != b.Node.Identity.Principal {
		t.Fatalf("wrong provider/result: %+v", output)
	}
}

func TestPeerExchangeSurvivesSeedAndProviderRestart(t *testing.T) {
	f := meshNetwork(t, "seed", "consumer", "provider")
	seed := f.node(t, "seed", nil, false, "")
	endpoint := seed.Manifest().Transport.Advertise
	consumer := f.node(t, "consumer", endpoint, false, "")
	provider := f.node(t, "provider", endpoint, true, "persistent-volume")
	requirement := Requirement{ID: "state", Capability: "echo.read", Version: "^1.0.0", Sticky: true, Scopes: []string{"echo.read"}}
	awaitMesh(t, func() bool { b, e := consumer.Resolve(requirement); return e == nil && len(b) == 1 })
	bindings, err := consumer.Resolve(requirement)
	if err != nil {
		t.Fatal(err)
	}
	testCall(t, consumer, bindings[0], "before-seed-exit")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	if err = seed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	// More than one expiry interval proves seed-independent origin progress.
	timer := time.NewTimer(700 * time.Millisecond)
	<-timer.C
	bindings, err = consumer.Resolve(requirement)
	if err != nil {
		t.Fatal(err)
	}
	testCall(t, consumer, bindings[0], "after-seed-exit")
	oldInstance := provider.Manifest().Identity.InstanceID
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	if err = provider.Close(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	awaitMesh(t, func() bool { _, e := consumer.Resolve(requirement); return e != nil })
	replacement := f.node(t, "provider", consumer.Manifest().Transport.Advertise, true, "persistent-volume")
	if replacement.Manifest().Identity.InstanceID == oldInstance {
		t.Fatal("restart reused running incarnation")
	}
	awaitMesh(t, func() bool {
		b, e := consumer.Resolve(requirement)
		return e == nil && len(b) == 1 && b[0].Node.Identity.InstanceID != oldInstance
	})
	bindings, err = consumer.Resolve(requirement)
	if err != nil {
		t.Fatal(err)
	}
	testCall(t, consumer, bindings[0], "recovered-same-state")
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	_ = replacement.Close(ctx)
	cancel()
	f.node(t, "provider", consumer.Manifest().Transport.Advertise, true, "different-volume")
	awaitMesh(t, func() bool {
		for _, p := range consumer.Peers() {
			if p.Manifest.Identity.Principal == "provider" && p.Manifest.Provides[0].Attributes["state_id"] == "different-volume" {
				return true
			}
		}
		return false
	})
	if _, err = consumer.Resolve(requirement); err == nil {
		t.Fatal("stateful dependency migrated to unrelated data")
	}
}

func TestPartitionExpiryAndReconciliation(t *testing.T) {
	f := meshNetwork(t, "one", "two")
	var blocked atomic.Bool
	proxy := func() func(*Node) {
		var target atomic.Pointer[Node]
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if blocked.Load() {
				http.Error(w, "partition", 503)
				return
			}
			node := target.Load()
			if node == nil {
				http.Error(w, "starting", 503)
				return
			}
			node.Handler().ServeHTTP(w, r)
		}))
		t.Cleanup(server.Close)
		return func(n *Node) {
			target.Store(n)
			if err := n.AddAdvertise(server.URL); err != nil {
				t.Fatal(err)
			}
		}
	}
	one := f.node(t, "one", nil, false, "", proxy())
	two := f.node(t, "two", one.Manifest().Transport.Advertise, true, "", proxy())
	awaitMesh(t, func() bool { return len(one.Peers()) == 1 && len(two.Peers()) == 1 })
	blocked.Store(true)
	awaitMesh(t, func() bool { return len(one.Peers()) == 0 && len(two.Peers()) == 0 })
	blocked.Store(false)
	awaitMesh(t, func() bool { return len(one.Peers()) == 1 && len(two.Peers()) == 1 })
	b, err := one.Resolve(Requirement{Capability: "echo.read"})
	if err != nil {
		t.Fatal(err)
	}
	testCall(t, one, b[0], "partition-healed")
}

func TestSignedInvocationRejectsSpoofReplayScopeAndNamespace(t *testing.T) {
	f := meshNetwork(t, "caller", "provider", "limited", "foreign")
	grant := f.trust["limited"]
	grant.Scopes = []string{"mesh.peer"}
	grant.Provides = nil
	f.trust["limited"] = grant
	grant = f.trust["foreign"]
	grant.Namespace = "other"
	f.trust["foreign"] = grant
	provider := f.node(t, "provider", nil, true, "")
	caller := f.node(t, "caller", provider.Manifest().Transport.Advertise, false, "")
	limited := f.node(t, "limited", provider.Manifest().Transport.Advertise, false, "")
	binding := Binding{Node: provider.Manifest(), Capability: provider.Manifest().Provides[0]}
	if _, err := limited.Resolve(Requirement{Capability: "echo.read"}); err == nil {
		t.Fatal("unauthorized capability resolved")
	}
	if err := limited.Call(context.Background(), binding, "POST", "/echo", map[string]string{"value": "denied"}, nil); err == nil {
		t.Fatal("unauthorized invocation succeeded")
	}
	body := []byte(`{"value":"signed"}`)
	request, err := http.NewRequest("POST", provider.Manifest().Transport.Advertise[0]+"/echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Mesh-Target", IdentityKey(provider.Manifest().Identity))
	if err = caller.SignRequest(request, body); err != nil {
		t.Fatal(err)
	}
	send := func(header http.Header, content []byte) int {
		t.Helper()
		r, err := http.NewRequest("POST", request.URL.String(), strings.NewReader(string(content)))
		if err != nil {
			t.Fatal(err)
		}
		r.Header = header.Clone()
		response, err := caller.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if status := send(request.Header, body); status != 200 {
		t.Fatalf("valid signature status %d", status)
	}
	if status := send(request.Header, body); status != 401 {
		t.Fatalf("replay status %d", status)
	}
	if err = caller.SignRequest(request, body); err != nil {
		t.Fatal(err)
	}
	forged := request.Header.Clone()
	forged.Set("X-Mesh-Principal", "limited")
	if status := send(forged, body); status != 401 {
		t.Fatalf("spoof status %d", status)
	}
	if err = caller.SignRequest(request, body); err != nil {
		t.Fatal(err)
	}
	if status := send(request.Header, []byte(`{"value":"modified"}`)); status != 401 {
		t.Fatalf("modified body status %d", status)
	}
	if err = caller.SignRequest(request, body); err != nil {
		t.Fatal(err)
	}
	forged = request.Header.Clone()
	forged.Set("X-Mesh-Namespace", "other")
	if status := send(forged, body); status != 401 {
		t.Fatalf("cross namespace status %d", status)
	}
	if err = limited.UpdateCapabilities([]Capability{echoCapability("")}); err == nil {
		t.Fatal("discovery peer advertised unauthorized provider")
	}
}

func TestResolverDeterminismVersionsAndPolicies(t *testing.T) {
	f := meshNetwork(t, "consumer", "alpha", "beta")
	// Selection is not a subsecond failure-detector benchmark.
	stable := func(node *Node) {
		node.config.Interval = 2 * time.Second
		node.config.TTL = 15 * time.Second
	}
	consumer := f.node(t, "consumer", nil, false, "", stable)
	alpha := f.node(t, "alpha", consumer.Manifest().Transport.Advertise, true, "", stable)
	beta := f.node(t, "beta", consumer.Manifest().Transport.Advertise, true, "", stable)
	c := beta.Manifest().Provides[0]
	c.Attributes = map[string]string{"priority": "20", "weight": "0"}
	if err := beta.UpdateCapabilities([]Capability{c}); err != nil {
		t.Fatal(err)
	}
	awaitMesh(t, func() bool { return len(consumer.Peers()) == 2 })
	req := Requirement{Capability: "echo.read", Version: "^1.0.0", Policy: "priority"}
	awaitMesh(t, func() bool { b, e := consumer.Resolve(req); return e == nil && b[0].Node.Identity.Principal == "beta" })
	for range 20 {
		b, e := consumer.Resolve(req)
		if e != nil || b[0].Node.Identity.Principal != "beta" {
			t.Fatal("priority selection changed")
		}
	}
	req.Policy = "explicit"
	req.Pin = "alpha"
	b, e := consumer.Resolve(req)
	if e != nil || b[0].Node.Identity.InstanceID != alpha.Manifest().Identity.InstanceID {
		t.Fatalf("explicit provider alpha unavailable or wrong: bindings=%v error=%v", b, e)
	}
	req.Version = "^2.0.0"
	if _, e = consumer.Resolve(req); e == nil {
		t.Fatal("incompatible interface version resolved")
	}
	req.Optional = true
	if b, e = consumer.Resolve(req); e != nil || len(b) != 0 {
		t.Fatal("missing optional dependency must be empty")
	}
	req = Requirement{ID: "rounds", Capability: "echo.read", Policy: "round-robin"}
	seen := map[string]bool{}
	for range 2 {
		b, e = consumer.Resolve(req)
		if e != nil {
			t.Fatal(e)
		}
		seen[b[0].Node.Identity.Principal] = true
	}
	if !seen["alpha"] || !seen["beta"] {
		t.Fatal("round robin omitted provider")
	}
	req.Policy = "weighted"
	for range 10 {
		b, e = consumer.Resolve(req)
		if e != nil || b[0].Node.Identity.Principal != "alpha" {
			t.Fatal("zero-weight provider selected")
		}
	}
	data, _ := json.Marshal(consumer.Bindings())
	if !strings.Contains(string(data), "policy=weighted") {
		t.Fatal("binding explanation missing selected policy")
	}
}
