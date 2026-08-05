//go:build darwin

package localstore

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func syncFile(file *os.File) error {
	if _, err := unix.FcntlInt(file.Fd(), unix.F_FULLFSYNC, 0); err != nil {
		return fmt.Errorf("full fsync: %w", err)
	}
	return nil
}
