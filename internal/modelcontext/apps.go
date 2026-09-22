package modelcontext

import (
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func hasInteractionHandler(contract agentconfig.RuntimeContract) bool {
	return len(contract.InteractionHandlers) > 0
}

func interactionRoutingContext(
	page agentconfig.InteractionHandlerPage,
	contract agentconfig.RuntimeContract,
) *InteractionRoutingContext {
	routing := &InteractionRoutingContext{}
	selection := page.Selection
	if selection != nil {
		if handler, ok := contract.InteractionHandlers[selection.Handler]; ok {
			appID, err := publicid.Encode(publicid.KindProjectApp, handler.AppID)
			if err == nil && appID == selection.AppID {
				routing.Destination = &InteractionDestinationRef{Handler: selection.Handler, Args: selection.Args}
			}
		}
	}
	return routing
}
