package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"golang.org/x/sys/windows"
)

// Run joins the parent's resource-limiting job before launching the native tool.
// This bounds resource use and lifetime; it is not a Windows security sandbox.
func Run(args []string) error {
	if len(args) < 2 {
		return errors.New("expected inherited job handle and executable")
	}
	value, err := strconv.ParseUint(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("parse job handle: %w", err)
	}
	job := windows.Handle(value)
	err = windows.AssignProcessToJobObject(job, windows.CurrentProcess())
	// The parent must own the only remaining handle, so its exit kills both
	// this launcher and the tool, including during startup.
	closeErr := windows.CloseHandle(job)
	if err = errors.Join(err, closeErr); err != nil {
		return fmt.Errorf("join file-tool job: %w", err)
	}
	command := exec.Command(args[1], args[2:]...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = command.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	return err
}

func RunChroot([]string) error {
	return errors.New("chroot is unavailable on Windows")
}
