package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/WhaleMesh/WhaleMesh/internal/llm"
	"github.com/WhaleMesh/WhaleMesh/pkg/mesh"
)

type selectedModel struct {
	mesh.Binding
	Model         string
	Revision      string
	UpstreamModel string
	FreeOnly      bool
}

// modelFor captures one exact catalog profile and revision for every inference in a run.
func (s *runtime) modelFor(ctx context.Context, in Request) (selectedModel, error) {
	manifest := s.node.Manifest()
	requirement := mesh.Requirement{ID: "agent.model", Capability: "llm.invoke", Interface: "llm.invoke", Version: "^1.0.0", Scopes: []string{"llm.invoke", "llm.models.list"}}
	for _, declared := range manifest.Requires {
		if declared.ID == "agent.model" {
			requirement = declared
			break
		}
	}
	if requirement.Pin == "" {
		requirement.Pin = in.Provider
	}
	requirement.Many = false
	constraints := requirement.Attributes
	freeOnly := (in.Model == "" && in.Provider == "") || constraints["cost_class"] == "free"
	if freeOnly && constraints["cost_class"] != "" && constraints["cost_class"] != "free" {
		return selectedModel{}, errors.New("automatic inference requires a free model; configured cost constraint conflicts")
	}
	// Model traits belong to the selected catalog profile, not necessarily the
	// provider's active profile. Keep provider-only constraints in the resolver.
	requirement.Attributes = make(map[string]string, len(constraints))
	for key, value := range constraints {
		switch key {
		case "cost_class", "supports_tools", "supports_streaming", "active_model":
		default:
			requirement.Attributes[key] = value
		}
	}
	candidates, err := s.node.Candidates(requirement)
	if err != nil {
		return selectedModel{}, fmt.Errorf("no eligible model; select provider/model explicitly for paid or unknown-cost inference: %w", err)
	}
	var eligible []selectedModel
	paid := false
	for _, candidate := range candidates {
		if in.Provider != "" && !matchesProvider(candidate, in.Provider) {
			continue
		}
		var descriptor *mesh.Capability
		for i := range candidate.Node.Provides {
			if candidate.Node.Provides[i].ID == "llm.models.list" && candidate.Node.Provides[i].Interface == "llm.models.list" && mesh.CompatibleVersion(candidate.Node.Provides[i].Version, "^1.0.0") {
				descriptor = &candidate.Node.Provides[i]
				break
			}
		}
		if descriptor == nil {
			continue
		}
		var catalog llm.PublicConfig
		if err = s.node.Call(ctx, mesh.Binding{Node: candidate.Node, Capability: *descriptor}, "GET", descriptor.Endpoint, nil, &catalog); err != nil {
			continue
		}
		if catalog.Revision == "" || (in.ModelRevision != "" && catalog.Revision != in.ModelRevision) {
			continue
		}
		modelID := in.Model
		if modelID == "" {
			modelID = catalog.ActiveModelID
		}
		for _, profile := range catalog.Models {
			if profile.ID != modelID || (freeOnly && profile.CostClass != "free") {
				continue
			}
			// Never mutate the manifest's shared attribute map.
			attributes := make(map[string]string, len(candidate.Capability.Attributes)+5)
			for key, value := range candidate.Capability.Attributes {
				attributes[key] = value
			}
			attributes["supports_tools"] = strconv.FormatBool(modelFeature(profile, "tools"))
			attributes["supports_streaming"] = strconv.FormatBool(modelFeature(profile, "streaming"))
			attributes["cost_class"] = profile.CostClass
			attributes["active_model"] = profile.ID
			attributes["model_revision"] = catalog.Revision
			matches := true
			for key, value := range constraints {
				if key != "stateful" && key != "sticky" && attributes[key] != value {
					matches = false
					break
				}
			}
			if !matches {
				continue
			}
			candidate.Capability.Attributes = attributes
			paid = paid || profile.CostClass != "free"
			eligible = append(eligible, selectedModel{Binding: candidate, Model: profile.ID, Revision: catalog.Revision, UpstreamModel: profile.Model, FreeOnly: freeOnly})
			break
		}
	}
	if len(eligible) == 0 {
		return selectedModel{}, fmt.Errorf("requested model %q or revision unavailable on authorized providers", in.Model)
	}
	if len(eligible) > 1 && paid && in.Provider == "" {
		return selectedModel{}, errors.New("model exists on multiple providers with paid or unknown costs; select provider explicitly")
	}
	requirement.Providers = make([]string, 0, len(eligible))
	for _, choice := range eligible {
		requirement.Providers = append(requirement.Providers, mesh.IdentityKey(choice.Node.Identity))
	}
	selected, err := s.node.Resolve(requirement)
	if err != nil {
		return selectedModel{}, err
	}
	if len(selected) != 1 {
		return selectedModel{}, errors.New("model execution requires a one-provider selection policy")
	}
	for _, choice := range eligible {
		if mesh.IdentityKey(choice.Node.Identity) == mesh.IdentityKey(selected[0].Node.Identity) {
			attributes := choice.Capability.Attributes
			choice.Binding = selected[0]
			choice.Capability.Attributes = attributes
			return choice, nil
		}
	}
	return selectedModel{}, errors.New("selected model provider changed during catalog lookup")
}
func matchesProvider(binding mesh.Binding, provider string) bool {
	return binding.Node.Identity.Principal == provider || mesh.IdentityKey(binding.Node.Identity) == provider || binding.Node.Identity.Name == provider
}
func modelFeature(profile llm.PublicProfile, feature string) bool {
	for _, value := range profile.Capabilities {
		if value == feature {
			return true
		}
	}
	return false
}
