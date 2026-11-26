//go:build !windows

package integrationdb

import "syscall"

func processIsRunning(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return err == syscall.EPERM
	}
	return true
}
