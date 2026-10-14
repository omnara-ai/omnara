package sandbox

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const chrootDir = "/usr/local/lib/omnara/chroot-sandbox"

func RunChroot(args []string) error {
	if len(args) != 1 || os.Getuid() == 0 || os.Geteuid() != os.Getuid() {
		return errors.New("chroot-sandbox requires a non-root caller and one script argument")
	}
	os.Clearenv()
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return fmt.Errorf("prepare chroot-sandbox descriptors: %w", err)
	}
	if err := unix.Chroot(chrootDir); err != nil {
		return fmt.Errorf("chroot: %w", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return fmt.Errorf("enter chroot-sandbox root: %w", err)
	}
	if _, _, err := syscall.AllThreadsSyscall6(unix.SYS_PRCTL, unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0, 0); err != 0 {
		return fmt.Errorf("prevent new chroot-sandbox privileges: %w", err)
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := [2]unix.CapUserData{}
	if _, _, err := syscall.AllThreadsSyscall(unix.SYS_CAPSET,
		uintptr(unsafe.Pointer(&header)), uintptr(unsafe.Pointer(&data[0])), 0); err != 0 {
		return fmt.Errorf("drop chroot-sandbox capabilities: %w", err)
	}
	return run([]string{"0", "/usr/bin/sed", "--sandbox", "-E", "-e", args[0], "--", "-"}, true)
}
