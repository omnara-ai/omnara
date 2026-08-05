//go:build linux

package machinedaemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func processGroupHasLiveMembers(groupID int) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, fmt.Errorf("read Linux process table: %w", err)
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			pgid, groupErr := unix.Getpgid(pid)
			switch {
			case errors.Is(groupErr, unix.ESRCH):
				continue
			case groupErr != nil:
				return false, fmt.Errorf(
					"inspect process %d after stat read failed: %w",
					pid,
					errors.Join(err, groupErr),
				)
			case pgid == groupID:
				// Without stat, this member cannot be proven to be a zombie.
				return true, nil
			default:
				continue
			}
		}
		state, pgid, err := parseLinuxProcessStat(stat)
		if err != nil {
			return false, fmt.Errorf("parse /proc/%d/stat: %w", pid, err)
		}
		if pgid == groupID && state != 'Z' && state != 'X' && state != 'x' {
			return true, nil
		}
	}
	return false, nil
}

func parseLinuxProcessStat(stat []byte) (byte, int, error) {
	closeParen := strings.LastIndexByte(string(stat), ')')
	if closeParen < 0 || closeParen+2 >= len(stat) {
		return 0, 0, errors.New("missing command delimiter")
	}
	fields := strings.Fields(string(stat[closeParen+1:]))
	if len(fields) < 3 || len(fields[0]) != 1 {
		return 0, 0, errors.New("missing state or process group")
	}
	pgid, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0, 0, fmt.Errorf("parse process group: %w", err)
	}
	return fields[0][0], pgid, nil
}
