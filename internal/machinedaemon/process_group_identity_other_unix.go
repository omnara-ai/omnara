//go:build !windows && !darwin

package machinedaemon

import "syscall"

func startedProcessGroupID(pid int) (int, error) {
	return syscall.Getpgid(pid)
}
