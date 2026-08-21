package modelcontext

import (
	"encoding/json"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func MachinePoolContextEnabled(specs []ToolSpec) bool {
	return HasTool(specs, toolcatalog.ToolNameCreateMachine)
}

func IntegrationTargetContextEnabled(specs []ToolSpec) bool {
	return HasAnyTool(
		specs,
		toolcatalog.ToolNameSendIntegrationMessage,
		toolcatalog.ToolNameSetIntegrationTarget,
	)
}

func IntegrationTargetsContent(targets []IntegrationTargetRef) string {
	if len(targets) == 0 {
		return "No external integration targets are currently available for this agent."
	}
	body, err := json.Marshal(targets)
	if err != nil {
		return "External integration targets are present but could not be serialized."
	}
	return "External integration targets available for this agent. The current target is used by default " +
		"for outbound integration messages and interaction prompts: " + string(body)
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
