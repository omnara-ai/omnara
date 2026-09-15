package toolcatalog

import (
	"regexp"
	"slices"
)

const (
	ToolNamePattern            = `^[A-Za-z_][A-Za-z0-9_]{0,63}$`
	ToolNameRunCommand         = "run_command"
	ToolNameWriteProcess       = "write_process"
	ToolNameReadProcess        = "read_process"
	ToolNameStopProcess        = "stop_process"
	ToolNameListProcesses      = "list_processes"
	ToolNameCreateMachine      = "create_machine"
	ToolNameDeleteMachine      = "delete_machine"
	ToolNameListMachines       = "list_machines"
	ToolNameInspectMachine     = "inspect_machine"
	ToolNameAskQuestion        = "ask_question"
	ToolNameSendChannelMessage = "send_channel_message"
	ToolNameListChannels       = "list_channels"
	ToolNameGetChannel         = "get_channel"
	ToolNameReadChannel        = "read_channel"
	ToolNameSetCurrentChannel  = "set_current_channel"
	ToolNameWebSearch          = "web_search"
	ToolNameWebFetch           = "web_fetch"
	ToolNameUploadFile         = "upload_file"
	ToolNameDownloadFile       = "download_file"
	ToolNameSkill              = "skill"
	ArtifactVFSRoot            = "/artifacts"
	ToolNameSpawnAgent         = "spawn_agent"
	ToolNameReadAgent          = "read_agent"
	ToolNameSendAgentMessage   = "send_agent_message"
	ToolNameStopAgent          = "stop_agent"
	ToolNameListAgents         = "list_agents"
)

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

var toolNamePattern = regexp.MustCompile(ToolNamePattern)

// IsBindingManagedTool reports whether live channel bindings, rather than agent
// configuration, control whether the tool is available to an agent.
func IsBindingManagedTool(name string) bool {
	return name == ToolNameListChannels || name == ToolNameGetChannel ||
		name == ToolNameSetCurrentChannel || name == ToolNameSendChannelMessage || name == ToolNameReadChannel
}
