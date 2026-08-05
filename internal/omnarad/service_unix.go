//go:build darwin || linux

package omnarad

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func serviceUserHome() (string, error) {
	home := strings.TrimSpace(os.Getenv("HOME"))
	if home == "" {
		return "", errors.New("HOME is required for daemon service setup")
	}
	if !filepath.IsAbs(home) {
		return "", errors.New("HOME must be absolute for daemon service setup")
	}
	home = filepath.Clean(home)
	if filepath.Dir(home) == home {
		return "", errors.New("HOME cannot be a filesystem root for daemon service setup")
	}
	return home, nil
}

func ensureServiceLog(home string) (string, error) {
	if err := ensureOwnedDirectory(home, 0o700); err != nil {
		return "", err
	}
	logsDir := filepath.Join(home, "logs")
	if err := createOwnedDirectory(logsDir, 0o700); err != nil {
		return "", err
	}
	daemonLogDir := filepath.Join(logsDir, "daemon")
	if err := createOwnedDirectory(daemonLogDir, 0o700); err != nil {
		return "", err
	}
	logPath := filepath.Join(home, serviceLogRelativePath)
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		if err := ensureOwnedRegularFile(logPath, 0o600); err != nil {
			return "", err
		}
		return logPath, nil
	}
	if err != nil {
		return "", fmt.Errorf("create daemon service log: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(logPath)
		return "", fmt.Errorf("chmod daemon service log: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(logPath)
		return "", fmt.Errorf("close daemon service log: %w", err)
	}
	return logPath, nil
}

func createOwnedDirectory(path string, mode os.FileMode) error {
	if err := os.Mkdir(path, mode); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create service directory %s: %w", path, err)
	}
	return ensureOwnedDirectory(path, mode)
}

func createOwnedDirectoryAll(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return fmt.Errorf("create service directory %s: %w", path, err)
	}
	return validateOwnedDirectory(path)
}

func ensureOwnedDirectory(path string, mode os.FileMode) error {
	if err := validateOwnedDirectory(path); err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("chmod service directory %s: %w", path, err)
	}
	return nil
}

func validateOwnedDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect service directory %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("service directory must be a directory and not a symlink: %s", path)
	}
	if err := ensureCurrentUserOwner(info, path); err != nil {
		return err
	}
	return nil
}

func ensureOwnedRegularFile(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect service file %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("service file must be regular and not a symlink: %s", path)
	}
	if err := ensureCurrentUserOwner(info, path); err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("chmod service file %s: %w", path, err)
	}
	return nil
}

func ensureCurrentUserOwner(info os.FileInfo, path string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("owner information is unavailable for %s", path)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("path is not owned by the current user: %s", path)
	}
	return nil
}

func readOwnedServiceFile(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("service definition must be regular and not a symlink: %s", path)
	}
	if err := ensureCurrentUserOwner(info, path); err != nil {
		return nil, false, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	return body, true, nil
}
