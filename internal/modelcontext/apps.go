package modelcontext

import "github.com/omnara-ai/omnara/internal/agentconfig"

func hasInteractionHandler(contract agentconfig.RuntimeContract) bool {
	return len(contract.InteractionHandlers) > 0
}

func interactionRoutingContext(
	page agentconfig.InteractionHandlerPage,
	contract agentconfig.RuntimeContract,
) *InteractionRoutingContext {
	routing := &InteractionRoutingContext{}
	selection := page.Selection
	if selection != nil && contract.InteractionHandlers[selection.Handler].AppID == selection.AppID {
		routing.Destination = &InteractionDestinationRef{Handler: selection.Handler, Args: selection.Args}
	}
	return routing
}
