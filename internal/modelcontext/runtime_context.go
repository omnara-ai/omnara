package modelcontext

import (
	"encoding/json"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func MachinePoolContextEnabled(specs []ToolSpec) bool {
	return HasTool(specs, toolcatalog.ToolNameCreateMachine)
}

func InteractionRoutingContent(routing *InteractionRoutingContext) string {
	if routing == nil || routing.Destination == nil {
		return "New questions and permission prompts will appear in the Omnara dashboard only. Use list_interaction_handlers to discover available destinations and set_interaction_handler to change this."
	}
	body, err := json.Marshal(routing.Destination)
	if err != nil {
		return "The current interaction destination could not be serialized; use list_interaction_handlers."
	}
	return "Default destination for new questions and permission prompts: " + string(body) + ". They also remain available in the Omnara dashboard. Ordinary provider messages do not change this choice. A newly accepted input with an origin may change it; set_interaction_handler can change it explicitly."
}

func AvailableMachinePoolsContent(pools []MachinePoolRef) string {
	if len(pools) == 0 {
		return "The `create_machine` tool is enabled, but no machine pools are currently available to this agent."
	}
	body, err := json.Marshal(pools)
	if err != nil {
		return "Machine pools are available but could not be serialized."
	}
	return "Available machine pools for create_machine. Use machine_pool_name only when multiple pools are available: " +
		string(body)
}
