package mesh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func boundaryNode(t *testing.T, f *networkFixture, principal string, configure ...func(*Config)) *Node {
	t.Helper()
	c := Config{
		Manifest: Manifest{Identity: Identity{Namespace: "default", Name: principal, Principal: principal, HostID: "boundary-host"}},
		Listen:   "127.0.0.1:0", PrivateKey: f.keys[principal], Trust: f.trust,
		Interval: 20 * time.Millisecond, TTL: 150 * time.Millisecond,
	}
	for _, setup := range configure {
		setup(&c)
	}
	n, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = n.Close(ctx)
		n.client.CloseIdleConnections()
	})
	return n
}

func boundarySigned(n *Node, sequence uint64, change func(*Manifest)) SignedManifest {
	s := SignedManifest{Manifest: n.Manifest(), Sequence: sequence}
	if change != nil {
		change(&s.Manifest)
	}
	s.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(n.key, manifestPayload(s)))
	return s
}

func boundaryAccept(t *testing.T, n *Node, signed SignedManifest) {
	t.Helper()
	if err := n.accept(signed); err != nil {
		t.Fatal(err)
	}
}

func boundaryHTTP(t *testing.T, n *Node) string {
	t.Helper()
	server := httptest.NewServer(n.Handler())
	t.Cleanup(server.Close)
	if err := n.AddAdvertise(server.URL); err != nil {
		t.Fatal(err)
	}
	return server.URL
}

func TestOriginSequenceDoesNotRefreshLocalTTL(t *testing.T) {
	f := meshNetwork(t, "observer", "origin")
	observer := boundaryNode(t, f, "observer")
	origin := boundaryNode(t, f, "origin")
	fresh := boundarySigned(origin, 20, func(m *Manifest) {
		m.Transport.Advertise = []string{"http://127.0.0.1:12345"}
	})
	boundaryAccept(t, observer, fresh)
	key := IdentityKey(fresh.Manifest.Identity)
	initial := observer.peers[key]
	time.Sleep(10 * time.Millisecond)
	boundaryAccept(t, observer, fresh)
	older := boundarySigned(origin, 19, func(m *Manifest) {
		m.Transport.Advertise = []string{"http://127.0.0.1:12346"}
		m.Lifecycle.Ready = false
	})
	boundaryAccept(t, observer, older)
	if got := observer.peers[key]; !got.LastSeen.Equal(initial.LastSeen) || !got.Deadline.Equal(initial.Deadline) || got.Sequence != 20 {
		t.Fatalf("duplicate or out-of-order sequence refreshed origin: before=%+v after=%+v", initial, got)
	}
	if !reflect.DeepEqual(observer.targets["origin"], fresh.Manifest.Transport.Advertise) {
		t.Fatal("out-of-order origin changed discovery targets")
	}
	time.Sleep(max(0, time.Until(initial.Deadline)) + 5*time.Millisecond)
	boundaryAccept(t, observer, fresh)
	boundaryAccept(t, observer, older)
	if len(observer.Peers()) != 0 {
		t.Fatal("forwarded stale origin revived expired membership")
	}
	boundaryAccept(t, observer, boundarySigned(origin, 21, nil))
	if peers := observer.Peers(); len(peers) != 1 || peers[0].Sequence != 21 || !peers[0].LastSeen.After(initial.LastSeen) {
		t.Fatalf("new origin sequence failed to renew membership: %+v", peers)
	}
}

func TestLogicalNameDoesNotMergePrincipalsOrParallelInstances(t *testing.T) {
	f := meshNetwork(t, "consumer", "alpha", "beta")
	consumer := boundaryNode(t, f, "consumer", func(c *Config) { c.TTL = 5 * time.Second })
	instances := []*Node{}
	for _, principal := range []string{"alpha", "alpha", "beta"} {
		n := boundaryNode(t, f, principal, func(c *Config) {
			c.Manifest.Identity.Name = "shared-logical-name"
			c.Manifest.Provides = []Capability{echoCapability("")}
		})
		n.Handle("POST", "/echo", []string{"echo.read"}, func(w http.ResponseWriter, r *http.Request) {
			WriteJSON(w, 200, map[string]string{"instance": n.Manifest().Identity.InstanceID, "principal": n.Manifest().Identity.Principal})
		})
		boundaryHTTP(t, n)
		boundaryAccept(t, consumer, boundarySigned(n, 1, nil))
		instances = append(instances, n)
	}
	if instances[0].Manifest().Identity.InstanceID == instances[1].Manifest().Identity.InstanceID {
		t.Fatal("parallel instances share incarnation identity")
	}
	bindings, err := consumer.Resolve(Requirement{Capability: "echo.read", Many: true})
	if err != nil || len(bindings) != 3 || len(consumer.Peers()) != 3 {
		t.Fatalf("same logical name merged independent origins: bindings=%d peers=%d error=%v", len(bindings), len(consumer.Peers()), err)
	}
	seen := map[string]bool{}
	for _, binding := range bindings {
		var output struct{ Instance, Principal string }
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := consumer.Call(ctx, binding, "POST", "/echo", nil, &output)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if output.Instance != binding.Node.Identity.InstanceID || output.Principal != binding.Node.Identity.Principal || seen[output.Instance] {
			t.Fatalf("binding reached wrong or duplicate instance: %+v", output)
		}
		seen[output.Instance] = true
	}
	bindings, err = consumer.Resolve(Requirement{Capability: "echo.read", Pin: "alpha", Many: true})
	if err != nil || len(bindings) != 2 {
		t.Fatalf("principal pin must keep both parallel instances: %d, %v", len(bindings), err)
	}
	bindings, err = consumer.Resolve(Requirement{Capability: "echo.read", Pin: IdentityKey(instances[1].Manifest().Identity)})
	if err != nil || len(bindings) != 1 || bindings[0].Node.Identity.InstanceID != instances[1].Manifest().Identity.InstanceID {
		t.Fatalf("instance pin is ambiguous: %+v, %v", bindings, err)
	}
}

func TestRemoteReadinessAndRequiredDependencyCycles(t *testing.T) {
	f := meshNetwork(t, "consumer", "alpha", "beta")
	consumer := boundaryNode(t, f, "consumer", func(c *Config) { c.TTL = 5 * time.Second })
	alpha := boundaryNode(t, f, "alpha", func(c *Config) { c.Manifest.Provides = []Capability{echoCapability("")} })
	beta := boundaryNode(t, f, "beta", func(c *Config) { c.Manifest.Provides = []Capability{echoCapability("")} })
	requirement := Requirement{Capability: "echo.read", Pin: "alpha"}
	for i, change := range []func(*Manifest){
		func(m *Manifest) { m.Lifecycle.Ready = false },
		func(m *Manifest) { m.Provides[0].Ready = false },
		func(m *Manifest) { m.Lifecycle.Draining = true },
	} {
		boundaryAccept(t, consumer, boundarySigned(alpha, uint64(i+1), change))
		if _, err := consumer.Resolve(requirement); err == nil {
			t.Fatalf("unready or draining remote resolved in case %d", i)
		}
	}
	boundaryAccept(t, consumer, boundarySigned(alpha, 4, nil))
	if _, err := consumer.Resolve(requirement); err != nil {
		t.Fatalf("ready remote excluded: %v", err)
	}
	boundaryAccept(t, consumer, boundarySigned(alpha, 5, func(m *Manifest) {
		m.Requires = []Requirement{{ID: "need-beta", Capability: "echo.read", Pin: "beta"}}
	}))
	boundaryAccept(t, consumer, boundarySigned(beta, 1, func(m *Manifest) {
		m.Requires = []Requirement{{ID: "need-alpha", Capability: "echo.read", Pin: "alpha"}}
	}))
	if _, err := consumer.Resolve(requirement); err == nil {
		t.Fatal("required dependency cycle resolved")
	}
	if err := consumer.UpdateRequirements([]Requirement{{ID: "root", Capability: "echo.read", Pin: "alpha"}}); err != nil {
		t.Fatal(err)
	}
	consumer.refreshReadiness()
	if consumer.Manifest().Lifecycle.Ready {
		t.Fatal("consumer remains ready despite required dependency cycle")
	}
	boundaryAccept(t, consumer, boundarySigned(beta, 2, func(m *Manifest) {
		m.Requires = []Requirement{{ID: "optional-alpha", Capability: "echo.read", Pin: "alpha", Optional: true}}
	}))
	consumer.refreshReadiness()
	if _, err := consumer.Resolve(requirement); err != nil || !consumer.Manifest().Lifecycle.Ready {
		t.Fatalf("optional back edge blocks otherwise ready graph: %v", err)
	}
	boundaryAccept(t, consumer, boundarySigned(beta, 3, func(m *Manifest) { m.Lifecycle.Ready = false }))
	consumer.refreshReadiness()
	if _, err := consumer.Resolve(requirement); err == nil || consumer.Manifest().Lifecycle.Ready {
		t.Fatal("transitive unready provider failed to invalidate required graph")
	}
}

func TestDiscoveryTargetsReplacePerPrincipalWithoutURLHistory(t *testing.T) {
	f := meshNetwork(t, "observer", "churn", "honest")
	seeds := []string{"http://127.0.0.1:19000"}
	observer := boundaryNode(t, f, "observer", func(c *Config) { c.Seeds = seeds })
	churn := boundaryNode(t, f, "churn")
	honest := boundaryNode(t, f, "honest")
	boundaryAccept(t, observer, boundarySigned(honest, 1, func(m *Manifest) { m.Transport.Advertise = []string{"http://127.0.0.1:19001"} }))
	retained := "http://127.0.0.1:19002"
	failure := time.Now()
	for sequence := uint64(1); sequence <= 64; sequence++ {
		address := fmt.Sprintf("http://127.0.0.1:%d", 20000+sequence)
		previous := append([]string(nil), observer.targets["churn"]...)
		observer.routeFailures[retained] = failure
		for _, old := range previous {
			observer.routeFailures[old] = failure
		}
		signed := boundarySigned(churn, sequence, func(m *Manifest) { m.Transport.Advertise = []string{retained, address} })
		boundaryAccept(t, observer, signed)
		if len(observer.targets) != 2 || !reflect.DeepEqual(observer.targets["churn"], signed.Manifest.Transport.Advertise) {
			t.Fatalf("principal target history grows at sequence %d: %+v", sequence, observer.targets)
		}
		if !observer.routeFailures[retained].Equal(failure) {
			t.Fatal("replacement cleared retained URL failure state")
		}
		for _, old := range previous {
			if old != retained {
				if _, exists := observer.routeFailures[old]; exists {
					t.Fatal("replaced URL remains in failure cache")
				}
			}
		}
	}
	addresses := make([]string, 16)
	for i := range addresses {
		addresses[i] = fmt.Sprintf("http://127.0.0.1:%d", 21000+i)
	}
	boundaryAccept(t, observer, boundarySigned(churn, 65, func(m *Manifest) { m.Transport.Advertise = addresses }))
	oversized := boundarySigned(churn, 66, func(m *Manifest) {
		m.Transport.Advertise = append(m.Transport.Advertise, addresses...)
		m.Transport.Advertise = append(m.Transport.Advertise, "http://127.0.0.1:29997")
	})
	if err := observer.accept(oversized); err == nil || !reflect.DeepEqual(observer.targets["churn"], addresses) {
		t.Fatal("more than sixteen advertised targets bypassed principal bounds")
	}
	before := append([]string(nil), observer.targets["churn"]...)
	forged := boundarySigned(churn, 66, func(m *Manifest) { m.Transport.Advertise = []string{"http://127.0.0.1:29999"} })
	forged.Manifest.Transport.Advertise[0] = "http://127.0.0.1:29998"
	if err := observer.accept(forged); err == nil || !reflect.DeepEqual(observer.targets["churn"], before) {
		t.Fatal("invalid signature replaced discovery targets")
	}
	if !reflect.DeepEqual(observer.config.Seeds, seeds) || !reflect.DeepEqual(observer.targets["honest"], []string{"http://127.0.0.1:19001"}) {
		t.Fatal("one principal displaced bootstrap or another principal's targets")
	}
}

func TestDiscoveryConcurrentIncarnationAdmission(t *testing.T) {
	f := meshNetwork(t, "observer", "busy", "honest")
	observer := boundaryNode(t, f, "observer", func(c *Config) { c.TTL = time.Minute })
	busy := boundaryNode(t, f, "busy")
	var first SignedManifest
	for i := 1; i <= 16; i++ {
		signed := boundarySigned(busy, 1, func(m *Manifest) { m.Identity.InstanceID = fmt.Sprintf("%032x", i) })
		boundaryAccept(t, observer, signed)
		if i == 1 {
			first = signed
		}
	}
	overflow := boundarySigned(busy, 1, func(m *Manifest) { m.Identity.InstanceID = fmt.Sprintf("%032x", 17) })
	if err := observer.accept(overflow); err == nil || !strings.Contains(err.Error(), "concurrent incarnations") {
		t.Fatalf("seventeenth active incarnation admitted: %v", err)
	}
	if len(observer.watermarks) != 16 || len(observer.Peers()) != 16 {
		t.Fatal("rejected active incarnation consumed history or membership")
	}
	boundaryAccept(t, observer, boundarySigned(busy, 2, func(m *Manifest) { m.Identity.InstanceID = first.Manifest.Identity.InstanceID }))
	boundaryAccept(t, observer, boundarySigned(busy, 3, func(m *Manifest) {
		m.Identity.InstanceID = first.Manifest.Identity.InstanceID
		m.Lifecycle.Draining = true
		m.Lifecycle.Ready = false
	}))
	boundaryAccept(t, observer, overflow)
	resurrected := boundarySigned(busy, 4, func(m *Manifest) {
		m.Identity.InstanceID = first.Manifest.Identity.InstanceID
	})
	if err := observer.accept(resurrected); err == nil || !strings.Contains(err.Error(), "concurrent incarnations") {
		t.Fatalf("known drained origin bypassed active incarnation quota: %v", err)
	}
	if mark := observer.watermarks[IdentityKey(first.Manifest.Identity)]; mark.Sequence != 3 {
		t.Fatal("rejected resurrection advanced origin watermark")
	}
	honest := boundaryNode(t, f, "honest")
	boundaryAccept(t, observer, boundarySigned(honest, 1, nil))
	if len(observer.watermarks) != 18 {
		t.Fatal("draining replacement or independent principal admission failed")
	}
}

func TestDiscoveryPrincipalHistoryQuotaReservesOtherPrincipals(t *testing.T) {
	f := meshNetwork(t, "observer", "attacker", "honest")
	attackerGrant := f.trust["attacker"]
	attackerGrant.Scopes = []string{"mesh.peer"}
	attackerGrant.Provides = nil
	f.trust["attacker"] = attackerGrant
	for len(f.trust) < 4096 {
		f.trust[fmt.Sprintf("reserved-%04d", len(f.trust))] = f.trust["honest"]
	}
	observer := boundaryNode(t, f, "observer")
	attacker := boundaryNode(t, f, "attacker")
	quota := 65536 / len(f.trust)
	for i := range quota {
		signed := boundarySigned(attacker, 1, func(m *Manifest) {
			m.Identity.InstanceID = fmt.Sprintf("%032x", i+1)
			m.Lifecycle.Draining = true
			m.Lifecycle.Ready = false
		})
		boundaryAccept(t, observer, signed)
		if mark := observer.watermarks[IdentityKey(signed.Manifest.Identity)]; mark.Principal != "attacker" || mark.Sequence != 1 {
			t.Fatalf("origin history omitted principal accounting: %+v", mark)
		}
	}
	overflow := boundarySigned(attacker, 1, func(m *Manifest) {
		m.Identity.InstanceID = fmt.Sprintf("%032x", quota+1)
		m.Lifecycle.Draining = true
	})
	if err := observer.accept(overflow); err == nil || !strings.Contains(err.Error(), "history quota") {
		t.Fatalf("mesh.peer principal exceeded reserved history quota %d: %v", quota, err)
	}
	if len(observer.watermarks) != quota {
		t.Fatal("rejected history consumed admission capacity")
	}
	honest := boundaryNode(t, f, "honest")
	honestSigned := boundarySigned(honest, 1, nil)
	boundaryAccept(t, observer, honestSigned)
	if mark := observer.watermarks[IdentityKey(honestSigned.Manifest.Identity)]; mark.Principal != "honest" || len(observer.watermarks) != quota+1 {
		t.Fatal("attacker exhausted another principal's reserved history")
	}
}

func TestDiscoveryGlobalHistoryCeilingAfterTrustRotation(t *testing.T) {
	f := meshNetwork(t, "observer", "existing", "newcomer")
	existing := boundaryNode(t, f, "existing")
	first := boundarySigned(existing, 1, nil)
	// Persisted history can include principals removed by a later trust rotation.
	history := make(map[string]watermark, 65536)
	history[IdentityKey(first.Manifest.Identity)] = watermark{Sequence: 1, Principal: "existing"}
	for i := 1; i < 65536; i++ {
		history[fmt.Sprintf("default/retired/node/%032x", i)] = watermark{Sequence: 1, Principal: "retired"}
	}
	dataDir := t.TempDir()
	if err := writeAtomic(filepath.Join(dataDir, "membership.json"), history); err != nil {
		t.Fatal(err)
	}
	observer := boundaryNode(t, f, "observer", func(c *Config) {
		c.DataDir = dataDir
		c.TTL = time.Minute
	})
	newcomer := boundaryNode(t, f, "newcomer")
	if err := observer.accept(boundarySigned(newcomer, 1, nil)); err == nil || !strings.Contains(err.Error(), "history quota") {
		t.Fatalf("global history ceiling admitted new origin after trust rotation: %v", err)
	}
	boundaryAccept(t, observer, boundarySigned(existing, 2, nil))
	if len(observer.watermarks) != 65536 || observer.watermarks[IdentityKey(first.Manifest.Identity)].Sequence != 2 || len(observer.Peers()) != 1 {
		t.Fatal("full history blocked existing origin progress or grew past global ceiling")
	}
}

func TestExchangeRoundsUseEightPeersAndRotate(t *testing.T) {
	f := meshNetwork(t, "caller", "peer")
	var hits [12]atomic.Int32
	seeds := make([]string, 0, len(hits))
	for i := range hits {
		peer := boundaryNode(t, f, "peer")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/mesh/exchange" {
				hits[i].Add(1)
			}
			peer.Handler().ServeHTTP(w, r)
		}))
		t.Cleanup(server.Close)
		if err := peer.AddAdvertise(server.URL); err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, server.URL)
	}
	caller := boundaryNode(t, f, "caller", func(c *Config) {
		c.Seeds = seeds
		c.Interval = time.Second
		c.TTL = 10 * time.Second
	})
	caller.exchangeAll(context.Background())
	total := int32(0)
	for i := range hits {
		total += hits[i].Load()
	}
	if total != 8 {
		t.Fatalf("first round contacted %d peers, want 8", total)
	}
	caller.exchangeAll(context.Background())
	total = 0
	for i := range hits {
		count := hits[i].Load()
		if count == 0 || count > 2 {
			t.Fatalf("round-robin missed or repeated endpoint %d: %d", i, count)
		}
		total += count
	}
	if total != 16 || len(caller.Peers()) != 12 {
		t.Fatalf("bounded rounds failed authenticated membership exchange: requests=%d peers=%d", total, len(caller.Peers()))
	}
}

func TestExchangeRoundCancelsSlowSocketsWithinSharedBudget(t *testing.T) {
	f := meshNetwork(t, "caller")
	var entered, finished atomic.Int32
	release := make(chan struct{})
	seeds := []string{}
	for range 12 {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			entered.Add(1)
			defer finished.Add(1)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}))
		t.Cleanup(server.Close)
		seeds = append(seeds, server.URL)
	}
	t.Cleanup(func() { close(release) })
	caller := boundaryNode(t, f, "caller", func(c *Config) {
		c.Seeds = seeds
		c.Interval = 10 * time.Millisecond
		c.TTL = 300 * time.Millisecond
	})
	start := time.Now()
	caller.exchangeAll(context.Background())
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("exchange used per-peer timeout instead of shared 50ms budget: %s", elapsed)
	}
	awaitMesh(t, func() bool { return finished.Load() == entered.Load() })
	count := entered.Load()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	caller.exchangeAll(ctx)
	if entered.Load() != count {
		t.Fatalf("already canceled round opened additional requests: %d", count)
	}
}

func TestAuthenticatedCallPropagatesTraceWithFreshRequestIDs(t *testing.T) {
	f := meshNetwork(t, "caller", "middle", "leaf")
	caller := boundaryNode(t, f, "caller")
	middle := boundaryNode(t, f, "middle", func(c *Config) { c.Manifest.Provides = []Capability{echoCapability("")} })
	leaf := boundaryNode(t, f, "leaf", func(c *Config) { c.Manifest.Provides = []Capability{echoCapability("")} })
	type observation struct {
		Trace, Request, Principal string
		Deadline                  time.Time
	}
	observed := make(chan observation, 2)
	observe := func(n *Node, r *http.Request) observation {
		deadline, _ := r.Context().Deadline()
		return observation{TraceID(r.Context()), RequestID(r.Context()), n.Principal(r), deadline}
	}
	leaf.Handle("POST", "/echo", []string{"echo.read"}, func(w http.ResponseWriter, r *http.Request) {
		value := observe(leaf, r)
		observed <- value
		WriteJSON(w, 200, map[string]string{"trace": value.Trace, "request": value.Request})
	})
	boundaryHTTP(t, leaf)
	leafBinding := Binding{Node: leaf.Manifest(), Capability: leaf.Manifest().Provides[0]}
	middle.Handle("POST", "/echo", []string{"echo.read"}, func(w http.ResponseWriter, r *http.Request) {
		observed <- observe(middle, r)
		var output map[string]string
		if err := middle.Call(r.Context(), leafBinding, "POST", "/echo", nil, &output); err != nil {
			WriteError(w, 502, "downstream", err.Error())
			return
		}
		WriteJSON(w, 200, output)
	})
	boundaryHTTP(t, middle)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	response, err := caller.Stream(ctx, Binding{Node: middle.Manifest(), Capability: middle.Manifest().Provides[0]}, "POST", "/echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var output map[string]string
	if err := decodeResponse(response, &output); err != nil {
		t.Fatal(err)
	}
	first, second := <-observed, <-observed
	if first.Trace == "" || first.Request == "" || second.Request == "" || first.Trace != second.Trace || first.Request == second.Request {
		t.Fatalf("bad trace/request chain: first=%+v second=%+v", first, second)
	}
	if first.Principal != "caller" || second.Principal != "middle" || output["trace"] != first.Trace || output["request"] != second.Request {
		t.Fatalf("authenticated context lost principal or correlation: %+v %+v %+v", first, second, output)
	}
	if response.Header.Get("X-Trace-ID") != first.Trace || response.Header.Get("X-Request-ID") != first.Request {
		t.Fatal("response correlation differs from authenticated request context")
	}
	if first.Deadline.IsZero() || second.Deadline.IsZero() || first.Deadline.After(deadline) || second.Deadline.After(first.Deadline) {
		t.Fatalf("signed deadline extended across downstream call: local=%s middle=%s leaf=%s", deadline, first.Deadline, second.Deadline)
	}
}

func TestCallCancellationReachesAuthenticatedHandler(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			f := meshNetwork(t, "caller", "provider")
			caller := boundaryNode(t, f, "caller")
			provider := boundaryNode(t, f, "provider", func(c *Config) { c.Manifest.Provides = []Capability{echoCapability("")} })
			entered := make(chan struct{}, 1)
			stopped := make(chan error, 1)
			provider.Handle("POST", "/echo", []string{"echo.read"}, func(w http.ResponseWriter, r *http.Request) {
				entered <- struct{}{}
				<-r.Context().Done()
				stopped <- r.Context().Err()
			})
			boundaryHTTP(t, provider)
			duration := 2 * time.Second
			if mode == "deadline" {
				duration = 200 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), duration)
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- caller.Call(ctx, Binding{Node: provider.Manifest(), Capability: provider.Manifest().Provides[0]}, "POST", "/echo", nil, nil)
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("authenticated handler never received request")
			}
			if mode == "cancel" {
				cancel()
			}
			select {
			case err := <-result:
				// Signed deadlines have millisecond precision; the server can close first.
				if err == nil || mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("call did not honor %s context: %v", mode, err)
				}
			case <-time.After(time.Second):
				t.Fatal("caller ignored context cancellation")
			}
			select {
			case err := <-stopped:
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("handler cancellation error: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("remote authenticated handler ignored cancellation")
			}
			caller.mu.RLock()
			active := len(caller.active)
			caller.mu.RUnlock()
			if active != 0 {
				t.Fatal("canceled call leaked active binding load")
			}
		})
	}
}

func boundaryCertificate(t *testing.T) (string, string) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mesh socket test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certificatePath, keyPath := filepath.Join(directory, "certificate.pem"), filepath.Join(directory, "key.pem")
	if err := os.WriteFile(certificatePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	return certificatePath, keyPath
}

func TestVerifiedTLSAndExplicitSecretTransport(t *testing.T) {
	f := meshNetwork(t, "caller", "provider")
	for principal, grant := range f.trust {
		grant.Scopes = append(grant.Scopes, "secrets.read")
		grant.Provides = append(grant.Provides, "secrets.resolve")
		f.trust[principal] = grant
	}
	secret := echoCapability("")
	secret.ID, secret.Interface = "secrets.resolve", "secrets.resolve"
	secret.TransportSecurity = "tls"
	secret.Scopes = []string{"secrets.read"}
	secret.Operations[0].Scopes = []string{"secrets.read"}
	certificate, key := boundaryCertificate(t)
	provider := boundaryNode(t, f, "provider", func(c *Config) {
		c.Manifest.Provides = []Capability{secret}
		c.TLSCert, c.TLSKey = certificate, key
		c.Interval, c.TTL = time.Second, 5*time.Second
	})
	var calls atomic.Int32
	provider.Handle("POST", "/echo", []string{"secrets.read"}, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		WriteJSON(w, 200, map[string]any{"tls": r.TLS != nil, "principal": provider.Principal(r)})
	})
	if err := provider.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	caller := boundaryNode(t, f, "caller", func(c *Config) { c.CAFile = certificate })
	binding := Binding{Node: provider.Manifest(), Capability: secret}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var output struct {
		TLS       bool
		Principal string
	}
	if err := caller.Call(ctx, binding, "POST", "/echo", nil, &output); err != nil || !output.TLS || output.Principal != "caller" {
		t.Fatalf("verified TLS invocation failed: %+v, %v", output, err)
	}
	connection, err := caller.DialHTTP(ctx, binding.Node.Transport.Advertise[0])
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	untrusted := boundaryNode(t, f, "caller")
	if err := untrusted.Call(ctx, binding, "POST", "/echo", nil, nil); err == nil {
		t.Fatal("unknown TLS authority accepted")
	}
	if connection, err := untrusted.DialHTTP(ctx, binding.Node.Transport.Advertise[0]); err == nil {
		_ = connection.Close()
		t.Fatal("DialHTTP accepted unknown TLS authority")
	}
	wrongHost := binding
	wrongHost.Node = cloneManifest(binding.Node)
	address, err := url.Parse(binding.Node.Transport.Advertise[0])
	if err != nil {
		t.Fatal(err)
	}
	address.Host = net.JoinHostPort("localhost", address.Port())
	wrongHost.Node.Transport.Advertise = []string{address.String()}
	var hostnameError x509.HostnameError
	if err := caller.Call(ctx, wrongHost, "POST", "/echo", nil, nil); !errors.As(err, &hostnameError) {
		t.Fatalf("TLS hostname mismatch not rejected: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("failed TLS verification reached secret handler")
	}
	request, err := http.NewRequestWithContext(ctx, "POST", binding.Node.Transport.Advertise[0]+"/echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := caller.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("TLS bypassed node authentication: %d", response.StatusCode)
	}
	request, err = http.NewRequestWithContext(ctx, "POST", binding.Node.Transport.Advertise[0]+"/echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Mesh-Target", IdentityKey(binding.Node.Identity))
	if err := caller.SignRequest(request, nil); err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Trace-ID", "changed-after-signing")
	response, err = caller.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || calls.Load() != 1 {
		t.Fatal("correlation mutation bypassed signed TLS authentication")
	}
	var plainRequests atomic.Int32
	plainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { plainRequests.Add(1); provider.Handler().ServeHTTP(w, r) }))
	t.Cleanup(plainServer.Close)
	plainAddress := plainServer.URL
	plainBinding := binding
	plainBinding.Node = cloneManifest(binding.Node)
	plainBinding.Node.Transport.Advertise = []string{plainAddress}
	if err := caller.Call(ctx, plainBinding, "POST", "/echo", nil, nil); err == nil || plainRequests.Load() != 0 {
		t.Fatalf("client allowed plaintext secrets by default: %v", err)
	}
	plainCaller := boundaryNode(t, f, "caller", func(c *Config) { c.AllowPlaintextSensitive = true })
	var remote *RemoteError
	if err := plainCaller.Call(ctx, plainBinding, "POST", "/echo", nil, nil); !errors.As(err, &remote) || remote.Status != 403 || remote.Code != "tls_required" {
		t.Fatalf("server allowed caller-only plaintext exception: %v", err)
	}
	plainProvider := boundaryNode(t, f, "provider", func(c *Config) {
		c.Manifest.Provides = []Capability{secret}
		c.AllowPlaintextSensitive = true
	})
	plainProvider.Handle("POST", "/echo", []string{"secrets.read"}, func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, 200, map[string]any{"tls": r.TLS != nil, "principal": plainProvider.Principal(r)})
	})
	boundaryHTTP(t, plainProvider)
	exception := Binding{Node: plainProvider.Manifest(), Capability: secret}
	if err := caller.Call(ctx, exception, "POST", "/echo", nil, nil); err == nil {
		t.Fatal("server-only plaintext exception disabled caller policy")
	}
	if err := plainCaller.Call(ctx, exception, "POST", "/echo", nil, &output); err != nil || output.TLS || output.Principal != "caller" {
		t.Fatalf("explicit two-sided isolated-bridge exception failed: %+v, %v", output, err)
	}
}
