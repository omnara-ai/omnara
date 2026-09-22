package modelcontext

import (
	"encoding/json"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func MachinePoolContextEnabled(specs []ToolSpec) bool {
	return HasTool(specs, toolcatalog.ToolNameCreateMachine)
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
