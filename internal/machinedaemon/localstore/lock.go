package localstore

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Lock struct {
	file *os.File
	path string
}

var ErrLockHeld = errors.New("lock is already held")

func TryAcquireLock(path string) (*Lock, error) {
	return tryAcquireLock(path, true)
}

func TryAcquireExistingLock(path string) (*Lock, error) {
	return tryAcquireLock(path, false)
}

func tryAcquireLock(path string, create bool) (*Lock, error) {
	if path == "" {
		return nil, errors.New("lock path is required")
	}
	dir := filepath.Dir(path)
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create lock dir: %w", err)
		}
	}
	if err := ensurePrivateDir(dir); err != nil {
		return nil, err
	}
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	failed := true
	defer func() {
		if failed {
			_ = file.Close()
		}
	}()
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect lock file: %w", err)
	}
	if err := validateLockFile(path, info); err != nil {
		return nil, err
	}
	if err := validateOpenedLock(path, info, file); err != nil {
		return nil, err
	}
	if err := tryLockFile(file); err != nil {
		return nil, err
	}
	failed = false
	return &Lock{file: file, path: path}, nil
}

func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unlockFile(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return closeErr
}

func (l *Lock) WritePID(pid int) error {
	if l == nil || l.file == nil {
		return errors.New("lock is not held")
	}
	if pid <= 0 {
		return errors.New("lock PID must be positive")
	}
	if err := l.file.Truncate(0); err != nil {
		return fmt.Errorf("truncate lock file: %w", err)
	}
	if _, err := l.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek lock file: %w", err)
	}
	if _, err := fmt.Fprintf(l.file, "%d\n", pid); err != nil {
		return fmt.Errorf("write lock PID: %w", err)
	}
	return nil
}

// RelinquishInherited is valid only after a started child inherited this lock.
// It closes the parent's copy without unlocking the shared native lock.
func (l *Lock) RelinquishInherited() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// ReleaseAndRemove requires durable retirement of the lock's protected owner.
// A crash before removal leaves an unlocked file that startup may collect.
func (l *Lock) ReleaseAndRemove() error {
	return l.releaseAndRemove(os.Remove, SyncDir)
}

func (l *Lock) releaseAndRemove(
	remove func(string) error,
	syncDir func(string) error,
) error {
	if l == nil || l.path == "" {
		return nil
	}
	if remove == nil || syncDir == nil {
		return errors.New("lock cleanup operations are required")
	}
	if l.file == nil {
		if _, err := os.Lstat(l.path); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return fmt.Errorf("inspect pending lock removal: %w", err)
			}
			return errors.New("lock cleanup lost ownership before removal")
		}
		if err := syncDir(filepath.Dir(l.path)); err != nil {
			return fmt.Errorf("sync pending released lock removal: %w", err)
		}
		l.path = ""
		return nil
	}
	path := l.path
	if err := l.Release(); err != nil {
		return err
	}
	if err := remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			l.path = ""
			return nil
		}
		return fmt.Errorf("remove released lock: %w", err)
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync released lock removal: %w", err)
	}
	l.path = ""
	return nil
}

func InspectLock(path string) (int, bool, error) {
	if path == "" {
		return 0, false, errors.New("lock path is required")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("inspect lock file: %w", err)
	}
	if err := validateLockFile(path, info); err != nil {
		return 0, false, err
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return 0, false, fmt.Errorf("open lock file: %w", err)
	}
	defer func() { _ = file.Close() }()
	if err := validateOpenedLock(path, info, file); err != nil {
		return 0, false, err
	}
	if err := tryLockFile(file); err == nil {
		if unlockErr := unlockFile(file); unlockErr != nil {
			return 0, false, unlockErr
		}
		return 0, false, nil
	} else if !errors.Is(err, ErrLockHeld) {
		return 0, false, err
	}
	body, err := io.ReadAll(io.LimitReader(file, 64))
	if err != nil {
		return 0, true, fmt.Errorf("read lock PID: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil || pid <= 0 {
		return 0, true, errors.New("held lock does not contain a valid PID")
	}
	return pid, true, nil
}

func validateLockFile(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("lock file must be regular and not a symlink: %s", path)
	}
	if err := validatePrivateFile(info); err != nil {
		return fmt.Errorf("validate lock file: %w", err)
	}
	return nil
}

func validateOpenedLock(path string, info os.FileInfo, file *os.File) error {
	openedInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat lock file: %w", err)
	}
	if !os.SameFile(info, openedInfo) {
		return fmt.Errorf("lock file changed while opening: %s", path)
	}
	return nil
}
