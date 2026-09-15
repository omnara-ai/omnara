package toolcatalog

import "regexp"

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
	ToolNameUploadArtifact     = "upload_artifact"
	ToolNameDownloadArtifact   = "download_artifact"
	ToolNameSkill              = "skill"
)

var toolNamePattern = regexp.MustCompile(ToolNamePattern)

// IsBindingManagedTool reports whether live channel bindings, rather than agent
// configuration, control whether the tool is available to an agent.
func IsBindingManagedTool(name string) bool {
	return name == ToolNameListChannels || name == ToolNameGetChannel ||
		name == ToolNameSetCurrentChannel || name == ToolNameSendChannelMessage || name == ToolNameReadChannel
}
