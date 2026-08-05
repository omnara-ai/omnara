package modelcontext

import (
	"strings"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func DefaultSystemPrompt() string {
	return "You are an autonomous agent running in Omnara. " +
		"Complete the user's work using the capabilities available in this model call, " +
		"and summarize the result clearly."
}

func capabilityGuidance(specs []ToolSpec) string {
	var guidance []string
	if HasTool(specs, toolcatalog.ToolNameRunCommand) {
		guidance = append(guidance,
			"When a daemon-backed machine is attached, use `run_command` to start commands and persist useful work in files.")
	}
	processActions := make([]string, 0, 3)
	if HasTool(specs, toolcatalog.ToolNameWriteProcess) {
		processActions = append(processActions, "use `write_process` to send stdin or PTY input")
	}
	if HasTool(specs, toolcatalog.ToolNameReadProcess) {
		processActions = append(processActions, "use `read_process` to observe output")
	}
	if HasTool(specs, toolcatalog.ToolNameStopProcess) {
		processActions = append(processActions, "use `stop_process` to interrupt or terminate it")
	}
	if len(processActions) > 0 {
		guidance = append(guidance, "For an existing process, "+strings.Join(processActions, ", ")+".")
	}
	return strings.Join(guidance, "\n")
}
