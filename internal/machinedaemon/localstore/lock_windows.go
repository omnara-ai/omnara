//go:build windows

package localstore

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

func (l *Lock) PrepareChildInheritance(
	command *exec.Cmd,
) (int, func(), error) {
	if l == nil || l.file == nil {
		return 0, nil, errors.New("lock is not held")
	}
	if command == nil {
		return 0, nil, errors.New("child command is required")
	}
	return 0, nil, errors.New("detached supervisor lock handoff is not supported on windows")
}

func AdoptLock(_ string, _ int) (*Lock, error) {
	return nil, errors.New(
		"detached supervisor lock handoff is not supported on windows",
	)
}

func tryLockFile(file *os.File) error {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return ErrLockHeld
		}
		return fmt.Errorf("lock file: %w", err)
	}
	return nil
}

func unlockFile(file *os.File) error {
	var overlapped windows.Overlapped
	if err := windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped); err != nil {
		return fmt.Errorf("unlock file: %w", err)
	}
	return nil
}
