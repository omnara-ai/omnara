package integrationlock

import (
	"os"
	"runtime"

	"golang.org/x/sys/windows"
)

func lockFile(file *os.File) error {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped)
	runtime.KeepAlive(file)
	return err
}

func unlockFile(file *os.File) error {
	var overlapped windows.Overlapped
	err := windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
	runtime.KeepAlive(file)
	return err
}
