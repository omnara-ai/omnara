//go:build integration && servicee2e && windows

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func prepareServiceProcess(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	// OpenSSH can disable Ctrl+C in its descendants, including new consoles.
	// Like WinSW, enable it in the launching parent before the child inherits it.
	setHandler := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCtrlHandler")
	if result, _, err := setHandler.Call(0, 0); result == 0 {
		t.Fatalf("enable service Ctrl+C handling: %v", err)
	}
	// A private console allows a targeted Ctrl+C even when tests run over SSH
	// without a console. HideWindow keeps it out of interactive desktop sessions.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_CONSOLE,
		HideWindow:    true,
	}
}

func signalServiceProcess(cmd *exec.Cmd, signal os.Signal) error {
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&windows.CREATE_NEW_CONSOLE == 0 {
		return cmd.Process.Signal(signal)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	helper := exec.CommandContext(ctx, executable, "-test.run=^TestServiceE2EWindowsStopHelper$")
	helper.Env = serviceProcessEnv("OMNARA_E2E_STOP_PID=" + strconv.Itoa(cmd.Process.Pid))
	if output, err := helper.CombinedOutput(); err != nil {
		return fmt.Errorf("send service Ctrl+C: %w\n%s", err, output)
	}
	return nil
}

func TestServiceE2EWindowsStopHelper(t *testing.T) {
	pidText := os.Getenv("OMNARA_E2E_STOP_PID")
	if pidText == "" {
		return
	}
	pid, err := strconv.ParseUint(pidText, 10, 32)
	if err != nil || pid == 0 {
		t.Fatalf("invalid service process ID %q", pidText)
	}
	// Attaching resets console handlers. Do this in a short-lived helper rather
	// than resetting the test runner's handlers. Only the service and its children
	// share this console, and the helper ignores its own Ctrl+C broadcast.
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	for _, call := range []struct {
		name string
		args []uintptr
	}{
		{"FreeConsole", nil},
		{"AttachConsole", []uintptr{uintptr(pid)}},
		{"SetConsoleCtrlHandler", []uintptr{0, 1}},
	} {
		if result, _, err := kernel32.NewProc(call.name).Call(call.args...); result == 0 {
			t.Fatalf("%s: %v", call.name, err)
		}
	}
	// Go delivers Ctrl+C as os.Interrupt, which the services handle alongside
	// SIGTERM. This is also the graceful stop event used by WinSW.
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_C_EVENT, 0); err != nil {
		t.Fatalf("GenerateConsoleCtrlEvent: %v", err)
	}
}
