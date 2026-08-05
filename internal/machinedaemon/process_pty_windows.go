//go:build windows

package machinedaemon

import "os/exec"

func startPTYProcessCommand(command *exec.Cmd, runner *localProcessRunner) error {
	return errPTYUnsupported
}
