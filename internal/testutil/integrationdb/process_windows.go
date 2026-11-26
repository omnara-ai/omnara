package integrationdb

import (
	"errors"

	"golang.org/x/sys/windows"
)

func processIsRunning(pid int) bool {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// Only a missing PID proves the process is gone. In particular, an
		// access-denied result must not let cleanup drop a live test database.
		return !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	state, err := windows.WaitForSingleObject(handle, 0)
	return err != nil || state != windows.WAIT_OBJECT_0
}
