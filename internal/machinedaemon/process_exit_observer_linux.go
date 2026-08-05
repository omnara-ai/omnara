//go:build linux

package machinedaemon

import (
	"errors"

	"golang.org/x/sys/unix"
)

type linuxProcessExitObserver struct {
	pid int
}

func supportsSafeProcessExitObservation() bool {
	return true
}

func newProcessExitObserver(pid int) (processExitObserver, error) {
	if pid <= 0 {
		return nil, errors.New("process id is invalid")
	}
	return linuxProcessExitObserver{pid: pid}, nil
}

func (o linuxProcessExitObserver) Wait() error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(
			unix.P_PID,
			o.pid,
			&info,
			unix.WEXITED|unix.WNOWAIT,
			nil,
		)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err
	}
}

func (linuxProcessExitObserver) Close() error {
	return nil
}
