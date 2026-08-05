//go:build darwin

package machinedaemon

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

const darwinZombieProcessState = 5

func startedProcessGroupID(pid int) (int, error) {
	groupID, err := syscall.Getpgid(pid)
	if err == nil || !errors.Is(err, syscall.ESRCH) {
		return groupID, err
	}
	// macOS Getpgid may return ESRCH for an unreaped zombie; kinfo retains its PGID.
	process, inspectErr := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if inspectErr != nil {
		return 0, errors.Join(err, inspectErr)
	}
	if process == nil || process.Eproc.Pgid <= 0 {
		return 0, errors.Join(
			err,
			fmt.Errorf("macOS process %d has no process-group identity", pid),
		)
	}
	return int(process.Eproc.Pgid), nil
}

func processGroupHasLiveMembers(groupID int) (bool, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", groupID)
	if err != nil {
		return false, fmt.Errorf(
			"read macOS process group %d: %w",
			groupID,
			err,
		)
	}
	for _, process := range processes {
		if process.Eproc.Pgid == int32(groupID) &&
			process.Proc.P_stat != darwinZombieProcessState {
			return true, nil
		}
	}
	return false, nil
}
