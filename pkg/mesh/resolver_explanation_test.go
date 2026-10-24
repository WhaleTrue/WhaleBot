package mesh

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func explanationFixture(t *testing.T) (*Node, *Node, *Node) {
	t.Helper()
	f := meshNetwork(t, "consumer", "alpha", "beta")
	consumer := boundaryNode(t, f, "consumer", func(c *Config) { c.TTL = 5 * time.Minute })
	provider := func(principal string) *Node {
		n := boundaryNode(t, f, principal, func(c *Config) {
			c.Manifest.Provides = []Capability{echoCapability("")}
		})
		boundaryAccept(t, consumer, boundarySigned(n, 1, nil))
		return n
	}
	return consumer, provider("alpha"), provider("beta")
}

func TestExplainDoesNotAdvanceOrRecordSelection(t *testing.T) {
	n, _, _ := explanationFixture(t)
	for _, policy := range []string{"round-robin", "random", "weighted"} {
		r := Requirement{ID: policy, Capability: "echo.read", Policy: policy}
		before := n.rounds[r.ID]
		first, err := n.Explain(r)
		if err != nil || len(first.Selected) != 1 {
			t.Fatalf("preview %s: %+v %v", policy, first, err)
		}
		if !explanationHasCode(first.Providers, "selected") || !explanationHasCode(first.Providers, "eligible_not_selected") {
			t.Fatal("preview does not distinguish chosen provider from eligible alternatives")
		}
		for range 3 {
			again, err := n.Explain(r)
			if err != nil || !reflect.DeepEqual(first, again) {
				t.Fatalf("preview changed %s: %v", policy, err)
			}
		}
		if n.rounds[r.ID] != before || len(n.RecordedBindings()) != 0 {
			t.Fatal("preview changed schedules or recorded bindings")
		}
		actual, err := n.Resolve(r)
		if err != nil || !reflect.DeepEqual(actual, first.Selected) {
			t.Fatalf("preview differs from next Resolve for %s: %v", policy, err)
		}
		delete(n.bindings, r.ID)
	}
}

func TestExplainAndReadinessDoNotCreateStickyState(t *testing.T) {
	n, _, _ := explanationFixture(t)
	n.config.DataDir = t.TempDir()
	r := Requirement{ID: "state", Capability: "echo.read", Sticky: true}
	n.manifest.Requires = []Requirement{r}
	preview, err := n.Explain(r)
	if err != nil || !preview.Sticky || len(preview.Selected) != 1 {
		t.Fatalf("sticky preview: %+v %v", preview, err)
	}
	n.refreshReadiness()
	if len(n.sticky) != 0 || len(n.bindings) != 0 {
		t.Fatal("read-only resolution created persistent or recorded state")
	}
	path := filepath.Join(n.config.DataDir, "resolver.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("read-only resolution wrote resolver state: %v", err)
	}
	if _, err := n.Resolve(r); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil || n.sticky[r.ID] == "" {
		t.Fatalf("real resolution did not persist sticky state: %v", err)
	}
	pin := n.sticky[r.ID]
	recorded := n.RecordedBindings()
	if _, err := n.Explain(r); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) || n.sticky[r.ID] != pin || !reflect.DeepEqual(recorded, n.RecordedBindings()) {
		t.Fatal("preview changed existing sticky state or recorded bindings")
	}
	n.sticky[r.ID] = "absent-boundary"
	blocked, err := n.Explain(r)
	if err == nil || len(blocked.Selected) != 0 || n.sticky[r.ID] != "absent-boundary" {
		t.Fatal("preview migrated unavailable sticky boundary")
	}
}

func explanationHasCode(items []ProviderEvidence, code string) bool {
	for _, item := range items {
		for _, reason := range item.Reasons {
			if reason.Code == code || explanationHasCode(reason.Providers, code) {
				return true
			}
		}
	}
	return false
}

func TestExplainDistinguishesRejectionsAndRetainsAlternatives(t *testing.T) {
	n, alpha, beta := explanationFixture(t)
	for _, tc := range []struct {
		code   string
		change func(*Requirement)
	}{
		{"interface", func(r *Requirement) { r.Interface = "other.read" }},
		{"version", func(r *Requirement) { r.Version = "^99.0.0" }},
		{"pin", func(r *Requirement) { r.Pin = "absent" }},
		{"namespace", func(r *Requirement) { r.Namespace = "other" }},
		{"locality", func(r *Requirement) { r.Locality = "same-node" }},
		{"attribute", func(r *Requirement) { r.Attributes = map[string]string{"zone": "absent"} }},
		{"scope", func(r *Requirement) { r.Scopes = []string{"not.granted"} }},
	} {
		r := Requirement{Capability: "echo.read"}
		tc.change(&r)
		if tc.code == "scope" {
			grant := n.trust["consumer"]
			grant.Scopes = []string{"echo.read"}
			n.trust["consumer"] = grant
		}
		e, err := n.Explain(r)
		if err == nil || !explanationHasCode(e.Providers, tc.code) {
			t.Fatalf("missing distinct %s evidence: %+v %v", tc.code, e, err)
		}
	}
	if err := n.UpdateCapabilities([]Capability{echoCapability("")}); err != nil {
		t.Fatal(err)
	}
	if err := n.UpdateRequirements([]Requirement{{ID: "back-to-alpha", Capability: "echo.read", Pin: "alpha"}}); err != nil {
		t.Fatal(err)
	}
	query := Requirement{Capability: "echo.read", Providers: []string{IdentityKey(alpha.Manifest().Identity), IdentityKey(beta.Manifest().Identity)}}
	boundaryAccept(t, n, boundarySigned(alpha, 2, func(m *Manifest) {
		m.Lifecycle.Ready = false
		m.Requires = []Requirement{{ID: "back-to-consumer", Capability: "echo.read", Pin: "consumer"}}
	}))
	e, err := n.Explain(query)
	if err != nil || len(e.Selected) != 1 || e.Selected[0].Node.Identity.Principal != "beta" || !explanationHasCode(e.Providers, "required_cycle") || !explanationHasCode(e.Providers, "node_not_ready") {
		t.Fatalf("cycle hidden or valid alternative lost: %+v %v", e, err)
	}
	boundaryAccept(t, n, boundarySigned(beta, 2, func(m *Manifest) {
		m.Requires = []Requirement{{ID: "missing", Capability: "absent"}}
	}))
	e, err = n.Explain(query)
	if err == nil || !explanationHasCode(e.Providers, "required_dependency") || !explanationHasCode(e.Providers, "required_cycle") {
		t.Fatalf("missing dependency evidence: %+v %v", e, err)
	}
}
