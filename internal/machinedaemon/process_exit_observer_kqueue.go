//go:build darwin

package machinedaemon

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

type kqueueProcessExitObserver struct {
	fd int
}

type completedProcessExitObserver struct{}

func supportsSafeProcessExitObservation() bool {
	return true
}

func newProcessExitObserver(pid int) (processExitObserver, error) {
	if pid <= 0 {
		return nil, errors.New("process id is invalid")
	}
	fd, err := unix.Kqueue()
	if err != nil {
		return nil, err
	}
	change := unix.Kevent_t{}
	unix.SetKevent(
		&change,
		pid,
		unix.EVFILT_PROC,
		unix.EV_ADD|unix.EV_ENABLE|unix.EV_ONESHOT,
	)
	change.Fflags = unix.NOTE_EXIT
	if _, err := unix.Kevent(fd, []unix.Kevent_t{change}, nil, nil); err != nil {
		closeErr := unix.Close(fd)
		if errors.Is(err, unix.ESRCH) {
			exited, inspectErr := darwinProcessIsZombie(pid)
			if inspectErr == nil && exited && closeErr == nil {
				return completedProcessExitObserver{}, nil
			}
			return nil, errors.Join(err, inspectErr, closeErr)
		}
		return nil, errors.Join(err, closeErr)
	}
	// NOTE_EXIT is edge-triggered, so recheck after installing the filter.
	// The unreaped child still prevents PID reuse.
	exited, err := darwinProcessIsZombie(pid)
	if err != nil {
		return nil, errors.Join(err, unix.Close(fd))
	}
	if exited {
		if err := unix.Close(fd); err != nil {
			return nil, err
		}
		return completedProcessExitObserver{}, nil
	}
	return &kqueueProcessExitObserver{fd: fd}, nil
}

func darwinProcessIsZombie(pid int) (bool, error) {
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return false, fmt.Errorf("inspect macOS process %d: %w", pid, err)
	}
	if process == nil {
		return false, fmt.Errorf("inspect macOS process %d: empty result", pid)
	}
	return process.Proc.P_stat == darwinZombieProcessState, nil
}

func (completedProcessExitObserver) Wait() error  { return nil }
func (completedProcessExitObserver) Close() error { return nil }

func (o *kqueueProcessExitObserver) Wait() error {
	events := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(o.fd, nil, events, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("process exit observer returned %d events", n)
		}
		event := events[0]
		if event.Flags&unix.EV_ERROR != 0 {
			return fmt.Errorf("process exit observer failed: errno %d", event.Data)
		}
		if event.Fflags&unix.NOTE_EXIT == 0 {
			return errors.New("process exit observer returned an unrelated event")
		}
		return nil
	}
}

func (o *kqueueProcessExitObserver) Close() error {
	if o == nil || o.fd < 0 {
		return nil
	}
	fd := o.fd
	o.fd = -1
	return unix.Close(fd)
}
