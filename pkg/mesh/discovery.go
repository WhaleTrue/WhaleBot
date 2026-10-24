package mesh

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"
)

type SignedManifest struct {
	Manifest  Manifest `json:"manifest"`
	Sequence  uint64   `json:"sequence"`
	Signature string   `json:"signature"`
}
type member struct {
	SignedManifest
	LastSeen  time.Time
	Deadline  time.Time
	wireBytes int
}
type watermark struct {
	Sequence  uint64 `json:"sequence"`
	Principal string `json:"principal"`
}
type exchange struct {
	Members []SignedManifest `json:"members"`
}

func manifestPayload(s SignedManifest) []byte {
	s.Signature = ""
	data, _ := json.Marshal(s)
	return append([]byte("whalemesh.membership/v1\n"), data...)
}
func validateProvides(m Manifest, grant TrustGrant) error {
	for _, c := range m.Provides {
		if !patternAllowed(grant.Provides, c.ID) {
			return fmt.Errorf("principal %s cannot provide %s", m.Identity.Principal, c.ID)
		}
	}
	return nil
}
func (n *Node) announcementLocked() SignedManifest {
	n.sequence++
	signed := SignedManifest{Manifest: cloneManifest(n.manifest), Sequence: n.sequence}
	signed.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(n.key, manifestPayload(signed)))
	return signed
}
func (n *Node) snapshot() exchange {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	const maxSnapshotBytes = 8 << 20
	const maxSnapshotRecords = 256
	result := exchange{Members: []SignedManifest{n.announcementLocked()}}
	keys := make([]string, 0, len(n.peers))
	for key, peer := range n.peers {
		if peer.Deadline.After(now) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	// Reserve one complete maximum-sized local manifest, including its envelope.
	remaining := maxSnapshotBytes - MaxDocumentBytes - 512
	if len(keys) > 0 {
		start := n.snapshotCursor % len(keys)
		visited := 0
		for visited < len(keys) && len(result.Members) < maxSnapshotRecords {
			key := keys[(start+visited)%len(keys)]
			peer := n.peers[key]
			visited++
			size := peer.wireBytes
			if size == 0 {
				size = len(manifestPayload(peer.SignedManifest)) + len(peer.Signature) + 64
			}
			if size > remaining {
				visited--
				break
			}
			signed := peer.SignedManifest
			signed.Manifest = cloneManifest(signed.Manifest)
			result.Members = append(result.Members, signed)
			remaining -= size
		}
		n.snapshotCursor = (start + visited) % len(keys)
	}
	return result
}
func (n *Node) accept(s SignedManifest) error {
	now := time.Now()
	m := s.Manifest
	grant, ok := n.trust[m.Identity.Principal]
	if !ok || grant.Namespace != m.Identity.Namespace || grant.Namespace != n.config.Manifest.Identity.Namespace {
		return fmt.Errorf("untrusted manifest namespace or principal")
	}
	if m.Security.Identity != m.Identity.Principal || m.Security.TrustNamespace != grant.Namespace {
		return fmt.Errorf("manifest security identity mismatch")
	}
	if s.Sequence == 0 {
		return fmt.Errorf("manifest sequence must be positive")
	}
	if err := ValidateManifest(m); err != nil {
		return err
	}
	if err := validateProvides(m, grant); err != nil {
		return err
	}
	for _, scope := range m.Security.Permissions {
		if !scopeAllowed(grant, scope) {
			return fmt.Errorf("manifest claims permission outside trust grant: %s", scope)
		}
	}
	key, _ := base64.StdEncoding.DecodeString(grant.PublicKey)
	signature, err := base64.StdEncoding.DecodeString(s.Signature)
	payload := manifestPayload(s)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(key), payload, signature) {
		return fmt.Errorf("invalid manifest signature")
	}
	for _, address := range m.Transport.Advertise {
		if err := validateAddress(address); err != nil {
			return err
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	identity := IdentityKey(m.Identity)
	if identity == IdentityKey(n.manifest.Identity) {
		return nil
	}
	old, known := n.watermarks[identity]
	if known && s.Sequence <= old.Sequence {
		return nil
	}
	if !known {
		quota := 65536 / len(n.trust)
		used := 0
		for _, mark := range n.watermarks {
			if mark.Principal == m.Identity.Principal {
				used++
			}
		}
		if used >= quota || len(n.watermarks) >= 65536 {
			return fmt.Errorf("principal incarnation history quota reached")
		}
	}
	current, present := n.peers[identity]
	alreadyActive := present && current.Deadline.After(now) && current.Manifest.Lifecycle.Alive && !current.Manifest.Lifecycle.Draining
	if !alreadyActive && m.Lifecycle.Alive && !m.Lifecycle.Draining {
		active := 0
		for _, peer := range n.peers {
			if peer.Manifest.Identity.Principal == m.Identity.Principal && peer.Deadline.After(now) && peer.Manifest.Lifecycle.Alive && !peer.Manifest.Lifecycle.Draining {
				active++
			}
		}
		if active >= 16 {
			return fmt.Errorf("principal has too many concurrent incarnations")
		}
	}
	n.watermarks[identity] = watermark{Sequence: s.Sequence, Principal: m.Identity.Principal}
	n.membershipVersion++
	kind := "joined"
	if _, exists := n.peers[identity]; exists {
		kind = "updated"
	}
	s.Manifest = cloneManifest(m)
	n.peers[identity] = member{SignedManifest: s, LastSeen: now, Deadline: now.Add(n.config.TTL), wireBytes: len(payload) + len(s.Signature) + 64}
	for _, oldAddress := range n.targets[m.Identity.Principal] {
		retained := false
		for _, address := range m.Transport.Advertise {
			if address == oldAddress {
				retained = true
				break
			}
		}
		if !retained {
			delete(n.routeFailures, oldAddress)
		}
	}
	n.targets[m.Identity.Principal] = s.Manifest.Transport.Advertise
	if m.Lifecycle.Draining {
		kind = "draining"
	}
	n.emitLocked(kind, m.Identity, s.Sequence, "")
	return nil
}
func (n *Node) handleExchange(w http.ResponseWriter, r *http.Request) {
	if r.Context().Err() != nil {
		return
	}
	var input exchange
	if err := ReadJSON(w, r, &input); err != nil {
		return
	}
	if len(input.Members) > 256 {
		WriteError(w, 413, "membership_limit", "at most 256 announcements per exchange")
		return
	}
	for _, s := range input.Members {
		if r.Context().Err() != nil {
			return
		}
		if err := n.accept(s); err != nil {
			n.failures.Add(1)
			WriteError(w, 403, "invalid_membership", err.Error())
			return
		}
	}
	if r.Context().Err() != nil {
		return
	}
	WriteJSON(w, 200, n.snapshot())
}
func (n *Node) gossipLoop(ctx context.Context) {
	n.exchangeAll(ctx)
	ticker := time.NewTicker(n.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.mu.Lock()
			now := time.Now()
			for k, p := range n.peers {
				if !p.Deadline.After(now) {
					delete(n.peers, k)
					n.emitLocked("expired", p.Manifest.Identity, p.Sequence, "no new origin sequence within local TTL")
				}
			}
			n.mu.Unlock()
			n.refreshReadiness()
			n.exchangeAll(ctx)
		}
	}
}
func (n *Node) exchangeAll(ctx context.Context) {
	packet := n.snapshot()
	n.mu.RLock()
	groups := make([][]string, 0, len(n.targets)+len(n.config.Seeds))
	for _, seed := range n.config.Seeds {
		groups = append(groups, []string{seed})
	}
	for _, addresses := range n.targets {
		groups = append(groups, append([]string(nil), addresses...))
	}
	own := append([]string(nil), n.manifest.Transport.Advertise...)
	n.mu.RUnlock()
	unique := map[string]bool{}
	for _, addresses := range groups {
		address := n.selectAddress(addresses)
		if address == "" {
			continue
		}
		self := false
		for _, local := range own {
			if address == local {
				self = true
				break
			}
		}
		if !self {
			unique[address] = true
		}
	}
	targets := make([]string, 0, len(unique))
	for address := range unique {
		targets = append(targets, address)
	}
	sort.Strings(targets)
	if len(targets) == 0 {
		return
	}
	n.mu.Lock()
	start := n.gossipCursor % len(targets)
	count := min(8, len(targets))
	n.gossipCursor = (start + count) % len(targets)
	n.mu.Unlock()
	// One endpoint per principal, at most eight peers, bounded wall time per round.
	// Retain only its latest advertisements for partition recovery, not URL history.
	budget := max(n.config.Interval, 250*time.Millisecond)
	budget = min(budget, 5*time.Second, n.config.TTL/3)
	round, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var workers sync.WaitGroup
	for i := range count {
		address := targets[(start+i)%len(targets)]
		workers.Add(1)
		go func() {
			defer workers.Done()
			var response exchange
			if err := n.requestJSON(round, address, "discovery", "POST", "/mesh/exchange", packet, &response); err != nil {
				n.failures.Add(1)
				return
			}
			if len(response.Members) > 4096 {
				n.failures.Add(1)
				return
			}
			for _, signed := range response.Members {
				if err := n.accept(signed); err != nil {
					n.failures.Add(1)
				}
			}
		}()
	}
	workers.Wait()
}
func validateAddress(address string) error {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid advertised HTTP address %q", address)
	}
	return nil
}
