package toolcatalog

import (
	"regexp"
	"slices"
)

const (
	ArtifactVFSRoot                = "/artifacts"
	ToolNamePattern                = `^[A-Za-z_][A-Za-z0-9_]{0,63}$`
	ToolNameRunCommand             = "run_command"
	ToolNameWriteProcess           = "write_process"
	ToolNameReadProcess            = "read_process"
	ToolNameStopProcess            = "stop_process"
	ToolNameListProcesses          = "list_processes"
	ToolNameCreateMachine          = "create_machine"
	ToolNameDeleteMachine          = "delete_machine"
	ToolNameListMachines           = "list_machines"
	ToolNameInspectMachine         = "inspect_machine"
	ToolNameAskQuestion            = "ask_question"
	ToolNameSendIntegrationMessage = "send_integration_message"
	ToolNameSetIntegrationTarget   = "set_integration_target"
	ToolNameWebSearch              = "web_search"
	ToolNameWebFetch               = "web_fetch"
	ToolNameReadFile               = "read_file"
	ToolNameSearchFiles            = "search_files"
	ToolNameUploadFile             = "upload_file"
	ToolNameDownloadFile           = "download_file"
	ToolNameSkill                  = "skill"
	ToolNameSpawnAgent             = "spawn_agent"
	ToolNameReadAgent              = "read_agent"
	ToolNameSendAgentMessage       = "send_agent_message"
	ToolNameStopAgent              = "stop_agent"
	ToolNameListAgents             = "list_agents"
	ToolNameToolSearch             = "tool_search"
	ToolNameCallDeferredTool       = "call_deferred_tool"
	ToolSearchMaxPatternLength     = 200
	ToolSearchDefaultResults       = 5
	ToolSearchMaxResults           = 50
)

func MachineToolNames() []string {
	return []string{
		ToolNameRunCommand,
		ToolNameWriteProcess,
		ToolNameReadProcess,
		ToolNameStopProcess,
		ToolNameListProcesses,
		ToolNameListMachines,
		ToolNameInspectMachine,
		ToolNameUploadFile,
		ToolNameDownloadFile,
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
	return slices.Contains(MachineToolNames(), name) || slices.Contains(MachinePoolToolNames(), name) ||
		IsSubagentToolName(name) || name == ToolNameSkill || name == ToolNameSendIntegrationMessage ||
		name == ToolNameReadFile || name == ToolNameSearchFiles
}

var toolNamePattern = regexp.MustCompile(ToolNamePattern)
