package machinedaemon

import (
	"errors"
	"fmt"
	"os/exec"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

func exitCodeFromError(err error, exitCode *int) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	*exitCode = exitErr.ExitCode()
	return true
}

func processTerminalErrorMessage(exit processRunnerExit) string {
	if exit.StateReasonMessage != "" {
		return exit.StateReasonMessage
	}
	if exit.StateReasonCode != "" {
		return exit.StateReasonCode
	}
	if exit.ExitSignal != "" {
		return exit.ExitSignal
	}
	if exit.ExitCode != nil && *exit.ExitCode != 0 {
		return fmt.Sprintf("exit code %d", *exit.ExitCode)
	}
	if exit.State != "" && exit.State != daemonprotocol.ProcessStateExited {
		return string(exit.State)
	}
	return ""
}
