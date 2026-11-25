package tools

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows uses native tools with validated arguments and stdin-only content.
// It does not provide the Linux worker's kernel sandbox. GNU sed's own sandbox
// flag is still mandatory: scripts may not execute commands or open other files.
func newFileSearchCommand(
	ctx context.Context, binary string, args []string, roots []*os.File, storeCount int,
) (*exec.Cmd, error) {
	if len(roots) != 0 || storeCount != 0 {
		return nil, errors.New("memory searches on Windows must use confined file reads and stdin")
	}
	command := exec.CommandContext(ctx, binary, args...)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	return command, nil
}

func newScriptEditCommand(ctx context.Context, script string) (*exec.Cmd, func(), error) {
	binary, err := exec.LookPath("sed")
	if err != nil {
		return nil, nil, err
	}
	// Scripts can be 64 KiB, exceeding Windows' command-line limit. Keep the
	// script in the service account's private temporary directory instead.
	file, err := os.CreateTemp("", "omnara-edit-*.sed")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.Remove(file.Name()) }
	_, err = file.WriteString(script)
	err = errors.Join(err, file.Close())
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	attributes := windows.SecurityAttributes{InheritHandle: 1}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	job, err := windows.CreateJobObject(&attributes, nil)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	closeJob := func() { _ = windows.CloseHandle(job); cleanup() }
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{ProcessMemoryLimit: 128 * 1024 * 1024}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE |
		windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		closeJob()
		return nil, nil, err
	}
	command := exec.CommandContext(ctx, "omnara-sandbox", strconv.FormatUint(uint64(job), 10),
		binary, "--sandbox", "--binary", "-E", "-f", file.Name(), "--", "-")
	command.Env = []string{"LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP, AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(job)},
	}
	command.Cancel = func() error {
		// Kill the launcher first: it may still be about to join the job.
		err := command.Process.Kill()
		return errors.Join(err, windows.TerminateJobObject(job, 1))
	}
	return command, closeJob, nil
}

func scriptEditDiagnostic(command *exec.Cmd, diagnostic string) string {
	for i, arg := range command.Args {
		if arg == "-f" && i+1 < len(command.Args) {
			return strings.ReplaceAll(diagnostic, command.Args[i+1], "script")
		}
	}
	return diagnostic
}
