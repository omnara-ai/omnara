//go:build !windows

package memoryops

import (
	"errors"
	"io/fs"
	"os"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

func checkFilesystem(_ *os.Root) error { return nil }

func checkNativePath(_ *os.Root, _ string, _ fs.FileInfo) error { return nil }

func syncFile(file *os.File) error { return file.Sync() }

func tryLock(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}

func renameFile(source *os.File, sourceName string, destination *os.File, destinationName string) error {
	err := unix.Renameat(int(source.Fd()), sourceName, int(destination.Fd()), destinationName)
	runtime.KeepAlive(source)
	runtime.KeepAlive(destination)
	return err
}
