//go:build windows

package machinedaemon

import (
	"context"
	"errors"
	"os/exec"
)

var errWindowsDetachedProcessUnsupported = errors.New(
	"detached process supervision is not supported on Windows",
)

const processContainmentKind = "unsupported"

type processContainment struct{}

func validateProcessCommandPlatform() error {
	return errWindowsDetachedProcessUnsupported
}

func setupProcessCommand(*exec.Cmd) error {
	return errWindowsDetachedProcessUnsupported
}

func afterStartProcessCommand(*localProcessRunner) error {
	return errWindowsDetachedProcessUnsupported
}

func releaseProcessCommand(context.Context, *localProcessRunner) error {
	return errWindowsDetachedProcessUnsupported
}

func interruptProcessCommand(*localProcessRunner) error {
	return errInterruptUnsupported
}

func requestTerminateProcessCommand(context.Context, *localProcessRunner) error {
	return errWindowsDetachedProcessUnsupported
}

func waitProcessCommandLeaderExit(*localProcessRunner) error {
	return errWindowsDetachedProcessUnsupported
}

func closeAndReapProcessCommand(
	context.Context,
	*localProcessRunner,
	error,
) processClosureResult {
	return processClosureResult{
		WaitErr:        errWindowsDetachedProcessUnsupported,
		ContainmentErr: errWindowsDetachedProcessUnsupported,
	}
}

func terminateAndReapProcessCommand(
	context.Context,
	*localProcessRunner,
) processClosureResult {
	return processClosureResult{
		WaitErr:        errWindowsDetachedProcessUnsupported,
		ContainmentErr: errWindowsDetachedProcessUnsupported,
	}
}

func closeProcessCommandContainment(
	context.Context,
	*localProcessRunner,
) error {
	return errWindowsDetachedProcessUnsupported
}

func processCommandContainmentID(runner *localProcessRunner) int {
	if runner == nil || runner.cmd == nil || runner.cmd.Process == nil {
		return 0
	}
	return runner.cmd.Process.Pid
}

func closeRecordedProcessContainment(context.Context, int, string) error {
	return errWindowsDetachedProcessUnsupported
}

func exitSignalFromError(error) string {
	return ""
}
