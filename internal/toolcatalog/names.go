package toolcatalog

import (
	"regexp"
	"slices"
)

const (
	ArtifactVFSRoot            = "/artifacts"
	ToolNamePattern            = `^[A-Za-z_][A-Za-z0-9_]{0,63}$`
	ToolNameRunCommand         = "run_command"
	ToolNameWriteProcess       = "write_process"
	ToolNameReadProcess        = "read_process"
	ToolNameStopProcess        = "stop_process"
	ToolNameWatchProcess       = "watch_process"
	ToolNameListProcesses      = "list_processes"
	ToolNameCreateMachine      = "create_machine"
	ToolNameDeleteMachine      = "delete_machine"
	ToolNameListMachines       = "list_machines"
	ToolNameInspectMachine     = "inspect_machine"
	ToolNameAskQuestion        = "ask_question"
	ToolNameWebSearch          = "web_search"
	ToolNameWebFetch           = "web_fetch"
	ToolNameStorageList        = "storage_list"
	ToolNameStorageWrite       = "storage_write"
	ToolNameStorageRead        = "storage_read"
	ToolNameStorageSearch      = "storage_search"
	ToolNameStorageUpload      = "storage_upload"
	ToolNameStorageDownload    = "storage_download"
	ToolNameSkill              = "skill"
	ToolNameSpawnAgent         = "spawn_agent"
	ToolNameReadAgent          = "read_agent"
	ToolNameSendAgentMessage   = "send_agent_message"
	ToolNameStopAgent          = "stop_agent"
	ToolNameListAgents         = "list_agents"
	ToolNameToolSearch         = "tool_search"
	ToolNameCallDeferredTool   = "call_deferred_tool"
	ToolSearchMaxPatternLength = 200
	ToolSearchDefaultResults   = 5
	ToolSearchMaxResults       = 50
)

func MachineToolNames() []string {
	return []string{
		ToolNameRunCommand,
		ToolNameWriteProcess,
		ToolNameReadProcess,
		ToolNameStopProcess,
		ToolNameWatchProcess,
		ToolNameListProcesses,
		ToolNameListMachines,
		ToolNameInspectMachine,
		ToolNameStorageUpload,
		ToolNameStorageDownload,
	}
}

func MachinePoolToolNames() []string {
	return []string{ToolNameCreateMachine, ToolNameDeleteMachine}
}

func IsReservedWireToolName(name string) bool {
	return name == ToolNameCallDeferredTool
}

func SubagentToolNames() []string {
	return []string{
		ToolNameSpawnAgent,
		ToolNameReadAgent,
		ToolNameSendAgentMessage,
		ToolNameStopAgent,
		ToolNameListAgents,
	}
}

func IsSubagentToolName(name string) bool {
	return slices.Contains(SubagentToolNames(), name)
}

func implicit(name string) bool {
	return UsesIntegrationToolNamespace(name) || slices.Contains(MachineToolNames(), name) ||
		slices.Contains(MachinePoolToolNames(), name) ||
		IsSubagentToolName(name) || name == ToolNameSkill ||
		name == ToolNameStorageList || name == ToolNameStorageRead || name == ToolNameStorageSearch ||
		name == ToolNameStorageWrite || name == ToolNameToolSearch
}

var toolNamePattern = regexp.MustCompile(ToolNamePattern)
