//go:build !windows

package localstore

import (
	"errors"
	"os"
	"syscall"
)

func syncDirBestEffort(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func validatePrivateFile(info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("permissions allow group or other access")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("owner information is unavailable")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return errors.New("file is not owned by the current user")
	}
	return nil
}
