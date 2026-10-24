package mesh

import "sort"

// Explanation is a read-only preview of the next Resolve call on this node.
// It describes this node's observations, not remote resolver state.
type Explanation struct {
	RequirementKey string             `json:"requirement_key"`
	Requirement    Requirement        `json:"requirement"`
	Policy         string             `json:"policy"`
	Sticky         bool               `json:"sticky"`
	StickyPin      string             `json:"sticky_pin,omitempty"`
	Rationale      string             `json:"rationale"`
	Selected       []Binding          `json:"selected"`
	Providers      []ProviderEvidence `json:"providers"`
	Error          string             `json:"error,omitempty"`
}

// ProviderEvidence separates eligibility from selection. Rank is one-based in
// policy order; zero means rejected. Scores expose policy inputs, not probes.
type ProviderEvidence struct {
	Node         Identity         `json:"node"`
	Capability   Capability       `json:"capability"`
	Eligible     bool             `json:"eligible"`
	Selected     bool             `json:"selected"`
	Reasons      []ProviderReason `json:"reasons"`
	Rank         int              `json:"rank,omitempty"`
	Priority     float64          `json:"priority"`
	Load         float64          `json:"load"`
	Weight       float64          `json:"weight"`
	LocalityRank int              `json:"locality_rank"`
}

type ProviderReason struct {
	Code           string             `json:"code"`
	Message        string             `json:"message"`
	RequirementKey string             `json:"requirement_key,omitempty"`
	Providers      []ProviderEvidence `json:"providers,omitempty"`
}

// Explain uses Resolve's filtering and selection without recording bindings,
// advancing schedules, creating sticky pins or writing resolver state.
// Selection failures are returned both in Error and as an error, preserving
// rejected-provider evidence for callers displaying unavailable requirements.
func (n *Node) Explain(r Requirement) (Explanation, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	r = cloneManifest(Manifest{Requires: []Requirement{r}}).Requires[0]
	explanation := Explanation{Selected: []Binding{}, Providers: []ProviderEvidence{}}
	selected, err := n.resolveWithExplanationLocked(r, false, &explanation, false)
	switch explanation.Policy {
	case "prefer-local", "local-first", "local-preferred":
		explanation.Rationale += "; lower locality_rank wins (same instance=0, same host=1, remote=2)"
	case "priority":
		explanation.Rationale += "; higher priority wins"
	case "least-load", "least-loaded":
		explanation.Rationale += "; lower advertised load plus locally active calls wins"
	case "round-robin":
		explanation.Rationale += "; schedule modulo eligible count selects identity-ordered index"
	case "random":
		explanation.Rationale += "; local instance, requirement key and schedule hash select index"
	case "weighted":
		explanation.Rationale += "; schedule/1000000 selects cumulative nonnegative weight interval"
	case "consistent-hash":
		explanation.Rationale += "; requester principal and requirement key hash select identity-ordered index"
	case "all", "broadcast":
		explanation.Rationale += "; all eligible providers selected"
	default:
		explanation.Rationale += "; first eligible provider in deterministic order"
	}
	if explanation.Requirement.Many {
		explanation.Rationale += "; many=true selects all eligible providers unless sticky selection is denied"
	}
	if err != nil {
		explanation.Error = err.Error()
	} else if selected != nil {
		explanation.Selected = selected
	}
	chosen := make(map[string]bool, len(selected))
	for _, binding := range selected {
		chosen[bindingKey(binding)] = true
	}
	for i := range explanation.Providers {
		provider := &explanation.Providers[i]
		provider.LocalityRank = localityRank(n.manifest, Manifest{Identity: provider.Node})
		provider.Selected = chosen[IdentityKey(provider.Node)+"/"+provider.Capability.ID]
		if provider.Eligible {
			code, message := "eligible_not_selected", "eligible; effective policy selected another provider"
			if provider.Selected {
				code, message = "selected", "next selection: "+explanation.Rationale
			} else if err != nil {
				message = "eligible; selection blocked: " + err.Error()
			}
			provider.Reasons = append(provider.Reasons, ProviderReason{Code: code, Message: message})
		}
	}
	return explanation, err
}

// RecordedBinding groups the last recorded local resolution by requirement.
// It is not proof of active traffic or a remote assignment.
type RecordedBinding struct {
	RequirementKey string    `json:"requirement_key"`
	Bindings       []Binding `json:"bindings"`
}

func (n *Node) RecordedBindings() []RecordedBinding {
	n.mu.RLock()
	defer n.mu.RUnlock()
	keys := make([]string, 0, len(n.bindings))
	for key := range n.bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]RecordedBinding, 0, len(keys))
	for _, key := range keys {
		bindings := make([]Binding, 0, len(n.bindings[key]))
		for _, binding := range n.bindings[key] {
			binding.Node = cloneManifest(binding.Node)
			binding.Capability = cloneManifest(Manifest{Provides: []Capability{binding.Capability}}).Provides[0]
			bindings = append(bindings, binding)
		}
		out = append(out, RecordedBinding{RequirementKey: key, Bindings: bindings})
	}
	return out
}
