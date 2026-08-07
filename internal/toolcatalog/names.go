package toolcatalog

import "regexp"

const (
	MaxAskQuestions                = 20
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
	ToolNameReadArtifact           = "read_artifact"
	ToolNameSearchArtifact         = "search_artifact"
	ToolNameSkill                  = "skill"
)

func IsBoundedPullToolName(name string) bool {
	switch name {
	case ToolNameReadProcess, ToolNameListProcesses, ToolNameReadArtifact, ToolNameSearchArtifact:
		return true
	default:
		return false
	}
}

var toolNamePattern = regexp.MustCompile(ToolNamePattern)
