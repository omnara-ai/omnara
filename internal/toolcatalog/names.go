package toolcatalog

import "regexp"

const (
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
	ToolNameUploadArtifact         = "upload_artifact"
	ToolNameDownloadArtifact       = "download_artifact"
	ToolNameSkill                  = "skill"
)

var toolNamePattern = regexp.MustCompile(ToolNamePattern)
