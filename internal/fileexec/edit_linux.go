package fileexec

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const editRoot = "/usr/local/lib/omnara/file-edit"

func RunEdit(args []string) error {
	if len(args) != 1 || os.Getuid() == 0 || os.Geteuid() != os.Getuid() {
		return errors.New("file-edit requires a non-root caller and one script argument")
	}
	os.Clearenv()
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return fmt.Errorf("prepare file-edit descriptors: %w", err)
	}
	if err := unix.Chroot(editRoot); err != nil {
		return fmt.Errorf("chroot: %w", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return fmt.Errorf("enter file-edit root: %w", err)
	}
	if _, _, err := syscall.AllThreadsSyscall6(unix.SYS_PRCTL, unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0, 0); err != 0 {
		return fmt.Errorf("prevent new file-edit privileges: %w", err)
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := [2]unix.CapUserData{}
	if _, _, err := syscall.AllThreadsSyscall(unix.SYS_CAPSET,
		uintptr(unsafe.Pointer(&header)), uintptr(unsafe.Pointer(&data[0])), 0); err != 0 {
		return fmt.Errorf("drop file-edit capabilities: %w", err)
	}
	return Run([]string{"0", "/usr/bin/sed", "--sandbox", "-E", "-e", args[0], "--", "-"})
}
