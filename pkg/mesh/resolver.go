package mesh

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func validPolicy(policy string) bool {
	switch policy {
	case "", "default", "priority", "local-preferred", "explicit", "weighted", "first", "first-healthy", "prefer-local", "local-first", "round-robin", "random", "least-load", "least-loaded", "pinned", "sticky", "broadcast", "all", "failover", "consistent-hash":
		return true
	}
	return false
}
func requirementKey(r Requirement) string {
	if r.ID != "" {
		return r.ID
	}
	data, _ := json.Marshal(r)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func bindingKey(b Binding) string { return IdentityKey(b.Node.Identity) + "/" + b.Capability.ID }
func stickyBindingKey(b Binding) string {
	if state := b.Capability.Attributes["state_id"]; state != "" {
		return logicalKey(b.Node.Identity) + "/" + url.QueryEscape(state) + "/" + url.QueryEscape(b.Capability.ID)
	}
	return bindingKey(b)
}
func (n *Node) Resolve(r Requirement) ([]Binding, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.resolveLocked(r, true)
}

// Candidates returns eligible call snapshots without selecting, scheduling or
// pinning. Consumers can inspect catalogs, then Resolve with a Providers allowlist.
func (n *Node) Candidates(r Requirement) ([]Binding, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.resolveWithExplanationLocked(r, false, nil, true)
}
func (n *Node) resolveLocked(r Requirement, record bool) ([]Binding, error) {
	return n.resolveWithExplanationLocked(r, record, nil, false)
}
func (n *Node) resolveWithExplanationLocked(r Requirement, record bool, explanation *Explanation, enumerate bool) ([]Binding, error) {
	key := requirementKey(r)
	if override, ok := n.policies[key]; ok {
		r.Policy = override.Policy
		r.Pin = override.Pin
	}
	if explanation != nil {
		explanation.Requirement = r
		explanation.RequirementKey = key
	}
	if explanation != nil {
		explanation.Policy = valueDefault(r.Policy, "prefer-local")
		if explanation.Policy == "default" {
			explanation.Policy = "prefer-local"
		}
		explanation.Sticky = r.Sticky || r.Policy == "sticky" || r.Attributes["stateful"] == "true" || r.Attributes["sticky"] == "required"
		explanation.StickyPin = n.sticky[key]
		explanation.Rationale = "no eligible provider; inspect provider rejection evidence"
	}
	if !validPolicy(r.Policy) {
		return nil, fmt.Errorf("unknown resolver policy %q", r.Policy)
	}
	if (r.Policy == "explicit" || r.Policy == "pinned") && r.Pin == "" {
		return nil, fmt.Errorf("explicit policy requires a provider pin")
	}
	if err := ValidateRequirement(r); err != nil {
		return nil, err
	}
	if r.Namespace == "" {
		r.Namespace = n.manifest.Identity.Namespace
	}
	if r.Namespace != n.manifest.Identity.Namespace && explanation == nil {
		return nil, fmt.Errorf("cross-namespace resolution denied")
	}
	manifests := []Manifest{n.manifest}
	now := time.Now()
	for _, peer := range n.peers {
		if peer.Deadline.After(now) {
			manifests = append(manifests, peer.Manifest)
		}
	}
	if explanation != nil {
		explanation.Requirement = r
		for i := range manifests {
			manifests[i] = cloneManifest(manifests[i])
		}
	}
	sort.Slice(manifests, func(i, j int) bool { return IdentityKey(manifests[i].Identity) < IdentityKey(manifests[j].Identity) })
	var evidence *[]ProviderEvidence
	if explanation != nil {
		evidence = &explanation.Providers
	}
	candidates := n.candidates(r, n.manifest, manifests, map[string]bool{IdentityKey(n.manifest.Identity): true}, evidence)
	if r.Namespace != n.manifest.Identity.Namespace {
		return nil, fmt.Errorf("cross-namespace resolution denied")
	}
	if len(candidates) == 0 {
		if record {
			delete(n.bindings, key)
		}
		if r.Optional {
			return []Binding{}, nil
		}
		return nil, fmt.Errorf("requirement %s unavailable: no ready compatible trusted provider (dependency cycle, scopes, locality or pin may exclude candidates)", key)
	}
	sort.Slice(candidates, func(i, j int) bool { return bindingKey(candidates[i]) < bindingKey(candidates[j]) })
	policy := r.Policy
	if policy == "" || policy == "default" {
		policy = "prefer-local"
	}
	if explanation != nil {
		explanation.Policy = policy
		explanation.Requirement = r
	}
	if policy == "prefer-local" || policy == "local-first" || policy == "local-preferred" {
		sort.SliceStable(candidates, func(i, j int) bool {
			return localityRank(n.manifest, candidates[i].Node) < localityRank(n.manifest, candidates[j].Node)
		})
	}
	if policy == "priority" {
		sort.SliceStable(candidates, func(i, j int) bool {
			return attributeNumber(candidates[i], "priority", 0) > attributeNumber(candidates[j], "priority", 0)
		})
	}
	if policy == "least-load" || policy == "least-loaded" {
		sort.SliceStable(candidates, func(i, j int) bool { return n.bindingLoad(candidates[i]) < n.bindingLoad(candidates[j]) })
	}
	sticky := r.Sticky || policy == "sticky" || r.Attributes["stateful"] == "true" || r.Attributes["sticky"] == "required"
	for _, candidate := range candidates {
		if candidate.Capability.Attributes["stateful"] == "true" || candidate.Capability.Attributes["sticky"] == "required" {
			sticky = true
			break
		}
	}
	if explanation != nil {
		explanation.Sticky = sticky
		explanation.StickyPin = n.sticky[key]
		explanation.Rationale = fmt.Sprintf("policy=%s; ordered by full identity with policy ranking; schedule=%d", policy, n.rounds[key])
		if sticky {
			explanation.Rationale += "; saved boundary retained; absent boundary previews first candidate without creating a pin; no automatic stateful migration"
		}
		for i, candidate := range candidates {
			for j := range explanation.Providers {
				provider := &explanation.Providers[j]
				if IdentityKey(provider.Node)+"/"+provider.Capability.ID == bindingKey(candidate) {
					provider.Rank = i + 1
					provider.Priority = attributeNumber(candidate, "priority", 0)
					provider.Load = n.bindingLoad(candidate)
					provider.Weight = math.Max(0, attributeNumber(candidate, "weight", 1))
				}
			}
		}
	}
	if !enumerate && sticky && (r.Many || policy == "broadcast" || policy == "all") {
		return nil, fmt.Errorf("sticky stateful requirement cannot broadcast")
	}
	var selected []Binding
	if enumerate {
		selected = candidates
	} else if sticky {
		pinned := n.sticky[key]
		if pinned != "" {
			for _, candidate := range candidates {
				if stickyBindingKey(candidate) == pinned {
					selected = []Binding{candidate}
					break
				}
			}
			if len(selected) == 0 {
				return nil, fmt.Errorf("sticky binding %s unavailable; explicit policy/pin change required, automatic stateful migration denied", pinned)
			}
		} else {
			selected = []Binding{candidates[0]}
			if record {
				n.sticky[key] = stickyBindingKey(selected[0])
				if err := n.saveResolverLocked(); err != nil {
					delete(n.sticky, key)
					return nil, err
				}
			}
		}
	} else if r.Many || policy == "broadcast" || policy == "all" {
		selected = candidates
	} else {
		index := 0
		switch policy {
		case "round-robin":
			index = int(n.rounds[key] % uint64(len(candidates)))
			if record {
				n.rounds[key]++
			}
		case "random":
			sum := sha256.Sum256([]byte(n.manifest.Identity.InstanceID + key + strconv.FormatUint(n.rounds[key], 10)))
			index = int(uint64(sum[0])<<8|uint64(sum[1])) % len(candidates)
			if record {
				n.rounds[key]++
			}
		case "consistent-hash":
			sum := sha256.Sum256([]byte(n.manifest.Identity.Principal + key))
			index = int(uint64(sum[0])<<8|uint64(sum[1])) % len(candidates)
		case "pinned", "explicit":
			if r.Pin == "" {
				return nil, fmt.Errorf("explicit/pinned policy requires pin")
			}
		case "weighted":
			total := 0.0
			for _, candidate := range candidates {
				total += math.Max(0, attributeNumber(candidate, "weight", 1))
			}
			if total == 0 {
				return nil, fmt.Errorf("weighted policy has no positive provider weights")
			}
			// Fixed deterministic weighted schedule; ties retain identity order.
			fraction := float64(n.rounds[key]%1000000) / 1000000
			point := fraction * total
			for i, candidate := range candidates {
				point -= math.Max(0, attributeNumber(candidate, "weight", 1))
				if point < 0 {
					index = i
					break
				}
			}
			if record {
				n.rounds[key] = (n.rounds[key] + 618033) % 1000000
			}
		}
		selected = []Binding{candidates[index]}
	}
	result := make([]Binding, len(selected))
	for i, b := range selected {
		b.Node = cloneManifest(b.Node)
		b.Capability = cloneManifest(Manifest{Provides: []Capability{b.Capability}}).Provides[0]
		b.Reason = fmt.Sprintf("policy=%s; namespace=%s; interface=%s; version=%s; locality=%s; trusted principal=%s; dependency graph ready", policy, r.Namespace, b.Capability.Interface, b.Capability.Version, valueDefault(r.Locality, "any"), b.Node.Identity.Principal)
		if sticky {
			if !record && n.sticky[key] == "" {
				b.Reason += "; sticky preview only, no pin persisted; no automatic stateful failover"
			} else {
				b.Reason += "; persistent sticky binding, no automatic stateful failover"
			}
		}
		result[i] = b
		if enumerate {
			result[i].Reason = "eligible candidate; no scheduling or binding change"
		}
	}
	if record {
		for i := range selected {
			selected[i].Reason = result[i].Reason
		}
		n.bindings[key] = selected
	}
	return result, nil
}
func (n *Node) candidates(r Requirement, requester Manifest, manifests []Manifest, stack map[string]bool, evidence *[]ProviderEvidence) []Binding {
	if r.Namespace == "" {
		r.Namespace = requester.Identity.Namespace
	}
	var out []Binding
	var allowed map[string]bool
	if len(r.Providers) > 0 {
		allowed = make(map[string]bool, len(r.Providers))
		for _, id := range r.Providers {
			allowed[id] = true
		}
	}
	for _, provider := range manifests {
		id := IdentityKey(provider.Identity)
		for _, capability := range provider.Provides {
			if r.Capability != "" && r.Capability != capability.ID {
				continue
			}
			if r.Capability == "" && r.Interface != "" && r.Interface != capability.Interface {
				continue
			}
			item := ProviderEvidence{Node: provider.Identity, Capability: capability}
			rejected := false
			reject := func(code, message string) {
				rejected = true
				if evidence != nil {
					item.Reasons = append(item.Reasons, ProviderReason{Code: code, Message: message})
				}
			}
			if provider.Identity.Namespace != r.Namespace || provider.Identity.Namespace != requester.Identity.Namespace {
				reject("namespace", "provider and requester must share the requested namespace")
			}
			if !provider.Lifecycle.Alive {
				reject("not_alive", "provider is not alive")
			}
			if provider.Lifecycle.Draining {
				reject("draining", "provider is draining")
			}
			if !provider.Lifecycle.Ready && !sameInstance(provider.Identity, requester.Identity) {
				reject("node_not_ready", "remote provider is not ready")
			}
			if !localityMatches(r.Locality, requester, provider) {
				reject("locality", "provider does not satisfy requested locality")
			}
			if r.Pin != "" && r.Pin != provider.Identity.Name && r.Pin != provider.Identity.Principal && r.Pin != id {
				reject("pin", "provider does not match explicit pin")
			}
			if allowed != nil && !allowed[id] {
				reject("provider_set", "provider is not in the eligible provider allowlist")
			}
			if !capability.Ready {
				reject("capability_not_ready", "capability is not ready")
			}
			selectorMatches := true
			if r.Capability != "" && r.Capability != capability.ID {
				reject("capability", "capability ID does not match")
				selectorMatches = false
			}
			if r.Interface != "" && r.Interface != capability.Interface {
				reject("interface", "interface does not match")
				selectorMatches = false
			}
			if !CompatibleVersion(capability.Version, r.Version) {
				reject("version", "capability version is incompatible")
			}
			for key, value := range r.Attributes {
				if key != "stateful" && key != "sticky" && capability.Attributes[key] != value {
					reject("attribute", "attribute "+key+" does not match")
				}
			}
			grant, trusted := n.trust[requester.Identity.Principal]
			if !trusted {
				reject("untrusted_requester", "requester has no trust grant")
			} else {
				for _, scope := range r.Scopes {
					if !scopeAllowed(grant, scope) {
						reject("scope", "requester lacks required scope "+scope)
					}
				}
				if len(r.Scopes) == 0 && len(capability.Scopes) > 0 {
					allowed := false
					for _, scope := range capability.Scopes {
						allowed = allowed || scopeAllowed(grant, scope)
					}
					if !allowed {
						reject("scope", "requester lacks every advertised capability scope")
					}
				}
			}
			// Inspect matching declarations even when unready, so readiness cannot hide cycles.
			if selectorMatches && !sameInstance(provider.Identity, requester.Identity) && (evidence != nil || !rejected) {
				hasRequired := false
				for _, dependency := range provider.Requires {
					hasRequired = hasRequired || !dependency.Optional
				}
				if stack[id] && hasRequired {
					reject("required_cycle", "required dependency path revisits "+id)
				} else {
					wasSeen := stack[id]
					stack[id] = true
					for _, dependency := range provider.Requires {
						if dependency.Optional {
							continue
						}
						var nested []ProviderEvidence
						var details *[]ProviderEvidence
						if evidence != nil {
							details = &nested
						}
						if len(n.candidates(dependency, provider, manifests, stack, details)) == 0 {
							rejected = true
							if evidence != nil {
								item.Reasons = append(item.Reasons, ProviderReason{Code: "required_dependency", Message: "required dependency has no eligible provider", RequirementKey: requirementKey(dependency), Providers: nested})
							}
						}
					}
					if !wasSeen {
						delete(stack, id)
					}
				}
			}
			item.Eligible = !rejected
			if item.Eligible {
				out = append(out, Binding{Node: provider, Capability: capability})
			}
			if evidence != nil {
				sort.SliceStable(item.Reasons, func(i, j int) bool {
					a, b := item.Reasons[i], item.Reasons[j]
					return a.Code+a.Message+a.RequirementKey < b.Code+b.Message+b.RequirementKey
				})
				*evidence = append(*evidence, item)
			}
		}
	}
	if evidence != nil {
		sort.Slice(*evidence, func(i, j int) bool {
			a, b := (*evidence)[i], (*evidence)[j]
			return IdentityKey(a.Node)+"/"+a.Capability.ID < IdentityKey(b.Node)+"/"+b.Capability.ID
		})
	}
	return out
}
func sameInstance(a, b Identity) bool {
	return a.Namespace == b.Namespace && a.Principal == b.Principal && a.Name == b.Name && a.InstanceID == b.InstanceID
}
func localityRank(a, b Manifest) int {
	if sameInstance(a.Identity, b.Identity) {
		return 0
	}
	if a.Identity.HostID != "" && a.Identity.HostID == b.Identity.HostID {
		return 1
	}
	return 2
}
func localityMatches(locality string, a, b Manifest) bool {
	switch locality {
	case "", "any", "prefer-local":
		return true
	case "node", "same-node":
		return sameInstance(a.Identity, b.Identity)
	case "host", "local", "same-host":
		return a.Identity.HostID != "" && a.Identity.HostID == b.Identity.HostID
	case "remote":
		return a.Identity.HostID != b.Identity.HostID
	default:
		return false
	}
}
func (n *Node) bindingLoad(b Binding) float64 {
	return attributeNumber(b, "load", 0) + float64(n.active[bindingKey(b)])
}
func (n *Node) saveResolverLocked() error {
	if n.config.DataDir == "" {
		return nil
	}
	return writeAtomic(filepath.Join(n.config.DataDir, "resolver.json"), resolverState{Sticky: n.sticky, Policies: n.policies})
}
func (n *Node) SetPolicy(id, policy, pin string) error {
	if !validPolicy(policy) {
		return fmt.Errorf("unknown resolver policy %q", policy)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	found := false
	for _, r := range n.manifest.Requires {
		if r.ID == id {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("requirement %q not owned by this node", id)
	}
	old, existed := n.policies[id]
	oldSticky := n.sticky[id]
	n.policies[id] = policyOverride{Policy: policy, Pin: pin}
	delete(n.sticky, id)
	if err := n.saveResolverLocked(); err != nil {
		if existed {
			n.policies[id] = old
		} else {
			delete(n.policies, id)
		}
		if oldSticky != "" {
			n.sticky[id] = oldSticky
		}
		return err
	}
	n.emitLocked("policy", n.manifest.Identity, n.sequence, "requirement="+id+" policy="+policy+" pin="+pin)
	return nil
}
func (n *Node) Bindings() []Binding {
	n.mu.RLock()
	defer n.mu.RUnlock()
	keys := make([]string, 0, len(n.bindings))
	for k := range n.bindings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Binding
	for _, key := range keys {
		for _, b := range n.bindings[key] {
			b.Node = cloneManifest(b.Node)
			b.Capability = cloneManifest(Manifest{Provides: []Capability{b.Capability}}).Provides[0]
			out = append(out, b)
		}
	}
	return out
}
func (n *Node) refreshReadiness() {
	n.mu.Lock()
	defer n.mu.Unlock()
	ready := !n.manifest.Lifecycle.Draining
	for _, r := range n.manifest.Requires {
		if r.Optional {
			continue
		}
		bindings, err := n.resolveLocked(r, false)
		if err != nil || len(bindings) == 0 {
			ready = false
			break
		}
	}
	if n.manifest.Lifecycle.Ready != ready {
		n.manifest.Lifecycle.Ready = ready
		reason := "required dependencies ready"
		if !ready {
			reason = "required dependency unavailable or cyclic"
		}
		n.emitLocked("readiness", n.manifest.Identity, n.sequence, reason)
	}
}

// PolicyNames exposes selection policies, not node deployment roles.
func PolicyNames() []string {
	return strings.Fields("default priority local-preferred explicit weighted first first-healthy prefer-local local-first round-robin random least-load least-loaded pinned sticky broadcast all failover consistent-hash")
}
func attributeNumber(b Binding, key string, fallback float64) float64 {
	value, err := strconv.ParseFloat(b.Capability.Attributes[key], 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback
	}
	return value
}
