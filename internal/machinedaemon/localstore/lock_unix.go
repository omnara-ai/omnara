//go:build !windows

package localstore

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func tryLockFile(file *os.File) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return ErrLockHeld
		}
		return fmt.Errorf("lock file: %w", err)
	}
	return nil
}

func unlockFile(file *os.File) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		return fmt.Errorf("unlock file: %w", err)
	}
	return nil
}

// PrepareChildInheritance gives command this exact native lock description.
// The returned descriptor number is valid in the child.
func (l *Lock) PrepareChildInheritance(
	command *exec.Cmd,
) (int, func(), error) {
	if l == nil || l.file == nil {
		return 0, nil, errors.New("lock is not held")
	}
	if command == nil {
		return 0, nil, errors.New("child command is required")
	}
	childFD := 3 + len(command.ExtraFiles)
	command.ExtraFiles = append(command.ExtraFiles, l.file)
	return childFD, func() {}, nil
}

func (l *Lock) PrepareForExec() (int, func(), error) {
	if l == nil || l.file == nil {
		return 0, nil, errors.New("lock is not held")
	}
	fd := int(l.file.Fd())
	syscall.ForkLock.Lock()
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		syscall.ForkLock.Unlock()
		return 0, nil, fmt.Errorf("read lock file descriptor flags: %w", err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags&^unix.FD_CLOEXEC); err != nil {
		syscall.ForkLock.Unlock()
		return 0, nil, fmt.Errorf("make lock file descriptor inheritable: %w", err)
	}
	return fd, func() {
		syscall.CloseOnExec(fd)
		syscall.ForkLock.Unlock()
	}, nil
}

func AdoptLock(path string, fd int) (*Lock, error) {
	if path == "" {
		return nil, errors.New("lock path is required")
	}
	if fd < 3 {
		return nil, errors.New("invalid inherited lock file descriptor")
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		return nil, errors.New("invalid inherited lock file descriptor")
	}
	syscall.CloseOnExec(fd)
	failed := true
	defer func() {
		if failed {
			_ = file.Close()
		}
	}()
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	expected, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect lock file: %w", err)
	}
	if err := validateLockFile(path, expected); err != nil {
		return nil, err
	}
	if err := validateOpenedLock(path, expected, file); err != nil {
		return nil, err
	}
	if err := tryLockFile(file); err != nil {
		return nil, err
	}
	failed = false
	return &Lock{file: file, path: path}, nil
}
