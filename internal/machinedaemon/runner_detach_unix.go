//go:build !windows

package machinedaemon

import (
	"os/exec"
	"syscall"
)

func detachRunnerCommand(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}
