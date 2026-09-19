package modelcontext

import (
	"encoding/json"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func hasInteractionHandler(contract agentconfig.RuntimeContract) bool {
	for _, resource := range contract.AppResources {
		if resource.Enabled && resource.InteractionHandler != nil {
			return true
		}
	}
	return false
}

func interactionRoutingContext(
	destinations executionstore.InteractionDestinations,
	contract agentconfig.RuntimeContract,
) *InteractionRoutingContext {
	routing := &InteractionRoutingContext{}
	for _, option := range destinations.Destinations {
		destination := option.Destination
		resource := contract.AppResources[destination.ResourceKey]
		if destination.ResourceKey == destinations.Current.ResourceKey &&
			destination.IntegrationTargetID == destinations.Current.IntegrationTargetID &&
			resource.Enabled &&
			resource.InteractionHandler != nil &&
			resource.InteractionHandler.Definition == destination.HandlerDefinition {
			id, err := publicid.Encode(publicid.KindIntegrationTarget, destination.IntegrationTargetID)
			if err != nil {
				continue
			}
			routing.Destination = &InteractionDestinationRef{Resource: destination.ResourceKey, TargetID: id}
			break
		}
	}
	return routing
}

// Scope is part of the immutable config, so it belongs beside the instruction.
// Mutable interaction selection is rendered separately at the request boundary.
func appResourcesContent(contract agentconfig.RuntimeContract, specs []ToolSpec) string {
	type resourceContext struct {
		Definition string               `json:"definition"`
		Scope      *appdefinition.Scope `json:"scope,omitempty"`
		Tools      []string             `json:"tools,omitempty"`
	}
	resources := map[string]resourceContext{}
	for key, resource := range contract.AppResources {
		if !resource.Enabled {
			continue
		}
		var names []string
		for _, name := range resource.Tools {
			if HasTool(specs, name) {
				names = append(names, name)
			}
		}
		resources[key] = resourceContext{Definition: resource.Definition, Scope: resource.Scope, Tools: names}
	}
	if len(resources) == 0 {
		return ""
	}
	body, err := json.Marshal(resources)
	if err != nil {
		return "App resources could not be serialized."
	}
	return "App resources available in this config (resource keys select the provider tools' permitted destinations). Use the matching app tools to communicate with external participants; ordinary assistant text stays in Omnara. " + string(
		body,
	)
}
