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

func ProcessContextEnabled(specs []ToolSpec) bool {
	return HasAnyTool(
		specs,
		toolcatalog.ToolNameRunCommand,
		toolcatalog.ToolNameWriteProcess,
		toolcatalog.ToolNameReadProcess,
		toolcatalog.ToolNameStopProcess,
		toolcatalog.ToolNameListProcesses,
	)
}

func MachineContextEnabled(specs []ToolSpec) bool {
	return HasAnyTool(
		specs,
		toolcatalog.ToolNameRunCommand,
		toolcatalog.ToolNameCreateMachine,
		toolcatalog.ToolNameDeleteMachine,
		toolcatalog.ToolNameListMachines,
		toolcatalog.ToolNameInspectMachine,
	)
}

func HasExecutionContext(bundle Bundle) bool {
	return len(bundle.ActiveProcesses) > 0 || len(bundle.AttachedMachines) > 0
}

func ExecutionContextContent(processes []ActiveProcessRef, machines []AttachedMachineRef) string {
	body, err := json.Marshal(struct {
		ActiveProcesses  []ActiveProcessRef   `json:"active_processes,omitempty"`
		AttachedMachines []AttachedMachineRef `json:"attached_machines,omitempty"`
	}{ActiveProcesses: processes, AttachedMachines: machines})
	if err != nil {
		return "Active execution context is present but could not be serialized."
	}
	return "Active execution observations. These are not transcript messages; use the latest tool results " +
		"and visible process state together when reasoning about active work: " + string(body)
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
