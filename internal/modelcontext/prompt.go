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

func capabilityGuidance(specs []ToolSpec, gitCredentials bool) string {
	var guidance []string
	if HasTool(specs, toolcatalog.ToolNameRunCommand) {
		guidance = append(guidance,
			"When a daemon-backed machine is attached, use `run_command` to start commands and persist useful work in files. "+
				"A command still running when `run_command` returns sends you a message when it ends, so do not poll it "+
				"or wait on it. Use `wait_ms: 0` for long-running commands or ones whose duration you do not know, "+
				"and a positive `wait_ms` only for a command you expect to finish within that time; then do other work "+
				"or finish your turn. To wait a set time, run `sleep <seconds>` with `wait_ms: 0` and finish your turn; "+
				"its exit message wakes you.")
		if gitCredentials {
			guidance = append(guidance,
				"Git commands you run with `run_command` on any attached machine are already authenticated to "+
					"github.com over HTTPS. Use https://github.com/OWNER/REPO.git URLs; these credentials don't "+
					"cover SSH remotes or the `gh` CLI. Never retrieve, print, or copy these credentials, or put "+
					"tokens in URLs. When the work needs code from a GitHub repository you can access, clone it onto "+
					"an attached machine unless it's already checked out there.")
		}
	}
	processActions := make([]string, 0, 4)
	if HasTool(specs, toolcatalog.ToolNameWriteProcess) {
		processActions = append(processActions, "use `write_process` to send stdin or PTY input")
	}
	if HasTool(specs, toolcatalog.ToolNameReadProcess) {
		processActions = append(processActions, "use `read_process` to observe output")
	}
	if HasTool(specs, toolcatalog.ToolNameWatchProcess) {
		processActions = append(processActions,
			"use `watch_process` to get a message when its output matches a pattern, such as a server's ready line")
	}
	if HasTool(specs, toolcatalog.ToolNameStopProcess) {
		processActions = append(processActions, "use `stop_process` to interrupt or terminate it")
	}
	if len(processActions) > 0 {
		guidance = append(guidance, "For an existing process, "+strings.Join(processActions, ", ")+".")
	}
	return strings.Join(guidance, "\n")
}
