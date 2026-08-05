//go:build windows

package machinedaemon

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func detachRunnerCommand(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS | windows.CREATE_BREAKAWAY_FROM_JOB,
	}
	return nil
}
