package mesh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompositeNodeResolvesItsOwnCapabilities(t *testing.T) {
	f := meshNetwork(t, "composite")
	node := boundaryNode(t, f, "composite", func(c *Config) {
		c.Manifest.Provides = []Capability{echoCapability("")}
		c.Manifest.Requires = []Requirement{{ID: "local", Capability: "echo.read", Interface: "echo.read", Version: "^1.0.0"}}
	})
	node.Handle("POST", "/echo", []string{"echo.read"}, func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Value string }
		if ReadJSON(w, r, &input) != nil {
			return
		}
		WriteJSON(w, 200, map[string]string{"value": input.Value, "principal": node.Principal(r)})
	})
	boundaryHTTP(t, node)
	bindings, err := node.Resolve(node.Manifest().Requires[0])
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var output struct{ Value, Principal string }
	if err = node.Call(ctx, bindings[0], "POST", "/echo", map[string]string{"value": "cohosted"}, &output); err != nil || output.Value != "cohosted" || output.Principal != "composite" {
		t.Fatalf("local capability failed: %+v %v", output, err)
	}
	explanation, err := node.Explain(node.Manifest().Requires[0])
	if err != nil || explanationHasCode(explanation.Providers, "required_cycle") {
		t.Fatalf("local provider falsely cyclic: %+v %v", explanation, err)
	}
}

func TestTopologySubscriptionsBroadcastAndRelease(t *testing.T) {
	f := meshNetwork(t, "observer")
	node := boundaryNode(t, f, "observer", func(c *Config) { c.Manifest.Identity.Labels = map[string]string{"boundary": "original"} })
	firstContext, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	first, err := node.Events(firstContext)
	if err != nil {
		t.Fatal(err)
	}
	secondContext, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	second, err := node.Events(secondContext)
	if err != nil {
		t.Fatal(err)
	}
	if err = node.UpdateRequirements([]Requirement{{ID: "optional", Capability: "absent", Optional: true}}); err != nil {
		t.Fatal(err)
	}
	receive := func(events <-chan TopologyEvent) TopologyEvent {
		t.Helper()
		select {
		case event := <-events:
			return event
		case <-time.After(time.Second):
			t.Fatal("topology broadcast missing")
			return TopologyEvent{}
		}
	}
	a, b := receive(first), receive(second)
	a.Identity.Labels["boundary"] = "changed"
	if b.Type != "requirements" || b.Identity.Labels["boundary"] != "original" || node.Manifest().Identity.Labels["boundary"] != "original" {
		t.Fatal("subscriber values share mutable identity state")
	}
	cancelFirst()
	select {
	case _, ok := <-first:
		if ok {
			t.Fatal("canceled subscription remains open")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled subscription leaked")
	}
	if err = node.UpdateRequirements(nil); err != nil {
		t.Fatal(err)
	}
	if receive(second).Type != "requirements" {
		t.Fatal("canceling one subscriber canceled another")
	}
}

type observedBody struct {
	io.Reader
	reads int
}

func (b *observedBody) Read(p []byte) (int, error) { b.reads++; return b.Reader.Read(p) }
func (b *observedBody) Close() error               { return nil }

func TestDiscoveryTargetCannotAuthorizeBusinessMutation(t *testing.T) {
	f := meshNetwork(t, "caller", "provider")
	caller := boundaryNode(t, f, "caller")
	provider := boundaryNode(t, f, "provider")
	var mutations atomic.Int32
	provider.Handle("POST", "/mesh/admin", []string{"echo.read"}, func(w http.ResponseWriter, r *http.Request) {
		mutations.Add(1)
		WriteJSON(w, 200, map[string]bool{"updated": true})
	})
	body := []byte(`{"value":"change"}`)
	request := httptest.NewRequest("POST", "http://provider/mesh/admin", nil)
	observed := &observedBody{Reader: bytes.NewReader(body)}
	request.Body = observed
	request.Header.Set("X-Mesh-Target", "discovery")
	if err := caller.SignRequest(request, body); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	provider.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || mutations.Load() != 0 || observed.reads != 0 {
		t.Fatal("discovery alias reached business body or mutation")
	}
	request = httptest.NewRequest("POST", "http://provider/mesh/admin", bytes.NewReader(body))
	request.Header.Set("X-Mesh-Target", IdentityKey(provider.Manifest().Identity))
	if err := caller.SignRequest(request, body); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	provider.Handler().ServeHTTP(response, request)
	if response.Code != 200 || mutations.Load() != 1 {
		t.Fatal("exact authenticated target cannot perform authorized mutation")
	}
}

func TestURIIdentityComponentsCannotOverwriteAnotherPrincipal(t *testing.T) {
	f := meshNetwork(t, "consumer", "alpha", "alpha/b")
	consumer := boundaryNode(t, f, "consumer", func(c *Config) { c.TTL = time.Minute })
	a := boundaryNode(t, f, "alpha", func(c *Config) {
		c.Manifest.Identity.Name = "b"
		c.Manifest.Provides = []Capability{echoCapability("")}
	})
	b := boundaryNode(t, f, "alpha/b", func(c *Config) {
		c.Manifest.Identity.Name = "c"
		c.Manifest.Provides = []Capability{echoCapability("")}
	})
	first := boundarySigned(a, 1, func(m *Manifest) { m.Identity.InstanceID = "c/d/e" })
	second := boundarySigned(b, 2, func(m *Manifest) { m.Identity.InstanceID = "d/e" })
	boundaryAccept(t, consumer, first)
	boundaryAccept(t, consumer, second)
	bindings, err := consumer.Resolve(Requirement{Capability: "echo.read", Many: true})
	if err != nil || len(bindings) != 2 {
		t.Fatalf("identity delimiter collision overwrote a principal: %+v %v", bindings, err)
	}
	for _, identity := range []Identity{first.Manifest.Identity, second.Manifest.Identity} {
		selected, err := consumer.Resolve(Requirement{Capability: "echo.read", Pin: IdentityKey(identity)})
		if err != nil || len(selected) != 1 || selected[0].Node.Identity.Principal != identity.Principal {
			t.Fatalf("canonical pin selected another principal: %+v %v", selected, err)
		}
	}
}

func TestEligibilityEnumerationPreservesStatefulBinding(t *testing.T) {
	f := meshNetwork(t, "consumer", "alpha", "beta")
	consumer := boundaryNode(t, f, "consumer", func(c *Config) { c.TTL = time.Minute })
	for _, principal := range []string{"alpha", "beta"} {
		provider := boundaryNode(t, f, principal, func(c *Config) { c.Manifest.Provides = []Capability{echoCapability(principal + "-data")} })
		boundaryAccept(t, consumer, boundarySigned(provider, 1, nil))
	}
	requirement := Requirement{ID: "durable", Capability: "echo.read", Policy: "sticky"}
	candidates, err := consumer.Candidates(requirement)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("enumeration: %d %v", len(candidates), err)
	}
	if len(consumer.sticky) != 0 || len(consumer.RecordedBindings()) != 0 {
		t.Fatal("enumeration created a binding")
	}
	requirement.Providers = []string{IdentityKey(candidates[1].Node.Identity)}
	selected, err := consumer.Resolve(requirement)
	if err != nil || len(selected) != 1 || selected[0].Node.Identity.Principal != candidates[1].Node.Identity.Principal {
		t.Fatalf("eligibility allowlist ignored: %+v %v", selected, err)
	}
	requirement.Providers = []string{IdentityKey(candidates[0].Node.Identity)}
	if _, err = consumer.Resolve(requirement); err == nil {
		t.Fatal("changing eligible candidates silently migrated stateful data")
	}
}

func TestMembershipSnapshotBoundsBytesAndRotatesLargeRecords(t *testing.T) {
	f := meshNetwork(t, "consumer", "provider")
	consumer := boundaryNode(t, f, "consumer", func(c *Config) { c.TTL = time.Minute })
	provider := boundaryNode(t, f, "provider", func(c *Config) { c.Manifest.Provides = []Capability{echoCapability("")} })
	schema, _ := json.Marshal(map[string]any{"type": "object", "description": strings.Repeat("x", 750<<10)})
	for i := range 13 {
		boundaryAccept(t, consumer, boundarySigned(provider, 1, func(m *Manifest) {
			m.Identity.InstanceID = fmt.Sprintf("replica-%02d", i)
			m.Provides[0].InputSchema = schema
		}))
	}
	seen := make(map[string]bool)
	for range 3 {
		snapshot := consumer.snapshot()
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > 8<<20 {
			t.Fatalf("membership packet exceeds bounded transport budget: %d", len(encoded))
		}
		for _, record := range snapshot.Members {
			if record.Manifest.Identity.Principal == "provider" {
				seen[record.Manifest.Identity.InstanceID] = true
			}
		}
	}
	if len(seen) != 13 {
		t.Fatalf("bounded snapshots starve records: saw %d of 13", len(seen))
	}
}
