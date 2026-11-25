package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestWindowsScriptParentDeath(t *testing.T) {
	requireNativeFileTools(t)
	for _, mode := range []string{"running", "running_with_search", "before_assignment"} {
		t.Run(mode, func(t *testing.T) {
			dir := privateFileToolTemp(t)
			parent := windowsFileToolHelper(t, mode)
			stdout, err := parent.StdoutPipe()
			require.NoError(t, err)
			stdin, err := parent.StdinPipe()
			require.NoError(t, err)
			defer stdin.Close()
			parent.Stderr = os.Stderr
			require.NoError(t, parent.Start())
			defer func() { _ = parent.Process.Kill(); _ = parent.Wait() }()
			var ready struct {
				Launcher uint32
				Members  []uint32
				Search   uint32
			}
			require.NoError(t, json.NewDecoder(stdout).Decode(&ready))
			var search windows.Handle
			if ready.Search != 0 {
				search, err = windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, ready.Search)
				require.NoError(t, err)
				t.Cleanup(func() {
					_ = windows.TerminateProcess(search, 1)
					_ = windows.CloseHandle(search)
				})
			}
			if mode == "running_with_search" {
				require.NotZero(t, search)
				require.NotContains(t, ready.Members, ready.Search)
			}
			require.NotZero(t, ready.Launcher)
			if mode != "before_assignment" {
				require.Contains(t, ready.Members, ready.Launcher)
				require.GreaterOrEqual(t, len(ready.Members), 2, "launcher and real sed must both be in the job")
			} else {
				require.Empty(t, ready.Members, "suspended launcher must not have joined the job")
				ready.Members = []uint32{ready.Launcher}
			}
			processes := make([]windows.Handle, 0, len(ready.Members))
			for _, pid := range ready.Members {
				handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, pid)
				require.NoError(t, err)
				t.Cleanup(func() {
					_ = windows.TerminateProcess(handle, 1)
					_ = windows.CloseHandle(handle)
				})
				processes = append(processes, handle)
			}
			var thread windows.Handle
			if mode == "before_assignment" {
				thread = suspendedFileToolThread(t, ready.Launcher)
			}
			require.NoError(t, parent.Process.Kill())
			require.Error(t, parent.Wait())
			if thread != 0 {
				// The only remaining job handle belongs to the suspended launcher.
				// Resuming actual production code must join the job and close that
				// last handle, terminating the launcher before a tool can escape.
				previous, err := windows.ResumeThread(thread)
				require.NoError(t, err)
				require.EqualValues(t, 1, previous)
			}
			for _, handle := range processes {
				status, err := windows.WaitForSingleObject(handle, 5000)
				require.NoError(t, err)
				require.EqualValues(t, windows.WAIT_OBJECT_0, status, "file-tool process survived its parent")
			}
			if search != 0 {
				status, err := windows.WaitForSingleObject(search, 0)
				require.NoError(t, err)
				require.EqualValues(t, windows.WAIT_TIMEOUT, status, "search exited before job-handle isolation was proven")
			}
			if mode == "before_assignment" {
				require.NoFileExists(t, filepath.Join(dir, "tool-started"), "tool ran after its parent died before job assignment")
			}
		})
	}
}

func TestWindowsFileToolsSurviveConsoleInterrupt(t *testing.T) {
	requireNativeFileTools(t)
	for _, tool := range []string{"rg", "sed"} {
		t.Run(tool, func(t *testing.T) {
			command := windowsFileToolHelper(t, "interrupt-"+tool)
			command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE}
			output, err := command.CombinedOutput()
			require.NoError(t, err, "%s", output)
		})
	}
}

func TestWindowsScriptLaunchFailureCleanup(t *testing.T) {
	requireNativeFileTools(t)
	for _, mode := range []string{"launcher_missing", "job_assignment", "tool_missing"} {
		t.Run(mode, func(t *testing.T) {
			dir := privateFileToolTemp(t)
			command, cleanup, err := newScriptEditCommand(t.Context(), "s/a/b/")
			require.NoError(t, err)
			defer func() {
				if cleanup != nil {
					cleanup()
				}
			}()
			switch mode {
			case "launcher_missing":
				command.Path = filepath.Join(dir, "missing-launcher.exe")
			case "job_assignment":
				command.Args[1] = "0"
			case "tool_missing":
				command.Args[2] = filepath.Join(dir, "missing-sed.exe")
			}
			command.Stdin = strings.NewReader("a")
			output, err := command.CombinedOutput()
			require.Error(t, err)
			if mode == "launcher_missing" {
				require.Nil(t, command.Process)
			} else {
				require.True(t, sandboxSetupFailed(err, output), "expected setup failure: %s (%v)", output, err)
			}
			requireFileToolProcessesExited(t, command)
			cleanup()
			cleanup = nil
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
	t.Run("unavailable_launcher", func(t *testing.T) {
		dir := privateFileToolTemp(t)
		sed, err := exec.LookPath("sed")
		require.NoError(t, err)
		t.Setenv("PATH", filepath.Dir(sed))
		_, err = editFileText(t.Context(), []byte("a"), "s/a/b/")
		require.ErrorIs(t, err, errFileToolUnavailable)
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Empty(t, entries)
	})
}

func TestWindowsScriptOutputLimit(t *testing.T) {
	requireNativeFileTools(t)
	dir := privateFileToolTemp(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	output, err := editFileText(ctx, []byte(strings.Repeat("x", 1024)), ":again;p;b again")
	require.ErrorContains(t, err, "memory content must be at most 10 MiB")
	require.NoError(t, ctx.Err(), "output overflow must stop the tool before the deadline")
	require.Nil(t, output, "partial output must not become file content")
	output, err = editFileText(t.Context(), []byte("a"), "s/a/b/")
	require.NoError(t, err)
	require.Equal(t, "b", string(output))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestWindowsScriptRepeatedCleanup(t *testing.T) {
	requireNativeFileTools(t)
	privateFileToolTemp(t)
	// Isolate handle accounting from other tests and their background goroutines.
	output, err := windowsFileToolHelper(t, "repeat").CombinedOutput()
	require.NoError(t, err, "%s", output)
}

func TestWindowsFileToolLifetimeHelper(t *testing.T) {
	mode := os.Getenv("OMNARA_FILE_TOOL_TEST_MODE")
	if mode == "" {
		return
	}
	if mode == "sentinel" {
		require.NoError(t, os.WriteFile(os.Getenv("OMNARA_FILE_TOOL_SENTINEL"), []byte("started"), 0o600))
		return
	}
	if strings.HasPrefix(mode, "interrupt-") {
		// SSH/CI launchers may ignore Ctrl+C. Clear that inherited setting before
		// spawning tools so only their production process-group flags protect them.
		result, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCtrlHandler").Call(0, 0)
		require.NotZero(t, result, "enable console Ctrl+C: %v", err)
		var command *exec.Cmd
		want := "a"
		if mode == "interrupt-sed" {
			var cleanup func()
			command, cleanup, err = newScriptEditCommand(t.Context(), "s/a/b/g")
			require.NoError(t, err)
			defer cleanup()
			want = "b"
		} else {
			command, err = newFileSearchCommand(t.Context(), "rg", []string{"--line-buffered", "-e", "."}, nil, 0)
			require.NoError(t, err)
		}
		stdin, err := command.StdinPipe()
		require.NoError(t, err)
		defer stdin.Close()
		stdout, err := command.StdoutPipe()
		require.NoError(t, err)
		command.Stderr = os.Stderr
		require.NoError(t, command.Start())
		defer func() { _ = command.Cancel(); _ = command.Wait() }()
		written := make(chan error, 1)
		go func() {
			_, err := io.WriteString(stdin, strings.Repeat("a", 64*1024)+"\n")
			written <- err
		}()
		ready := make([]byte, 64*1024)
		_, err = io.ReadFull(stdout, ready)
		require.NoError(t, err)
		require.Equal(t, strings.Repeat(want, len(ready)), string(ready))
		require.NoError(t, <-written)
		// Register only after both tools are running: setting an inherited ignore
		// flag before spawning would conceal a missing CREATE_NEW_PROCESS_GROUP.
		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, os.Interrupt)
		defer signal.Stop(interrupt)
		require.NoError(t, windows.GenerateConsoleCtrlEvent(windows.CTRL_C_EVENT, 0))
		select {
		case <-interrupt:
		case <-time.After(5 * time.Second):
			t.Fatal("private console did not receive Ctrl+C")
		}
		_, err = io.WriteString(stdin, "a\n")
		require.NoError(t, err)
		require.NoError(t, stdin.Close())
		remaining, err := io.ReadAll(stdout)
		require.NoError(t, err)
		require.NoError(t, command.Wait())
		require.Equal(t, "\n"+want+"\n", string(remaining))
		return
	}
	if mode == "repeat" {
		run := func() {
			command, cleanup, err := newScriptEditCommand(t.Context(), "s/a/b/")
			require.NoError(t, err)
			defer cleanup()
			command.Stdin = strings.NewReader("a")
			output, err := command.Output()
			require.NoError(t, err)
			require.Equal(t, "b", string(output))
			requireFileToolProcessesExited(t, command)
		}
		for range 4 {
			run()
		}
		before := fileToolProcessHandleCount(t)
		for range 32 {
			run()
			_, err := editFileText(t.Context(), []byte("a"), "s/[//")
			require.ErrorContains(t, err, "script execution failed")
		}
		after := fileToolProcessHandleCount(t)
		// Runtime thread/event caches may grow slightly. A leaked job, pipe or
		// process handle per command would exceed this allowance after 32 pairs
		// of successful and failed edits.
		require.LessOrEqual(t, after, before+24, "handle count grew across completed edits: %d -> %d", before, after)
		entries, err := os.ReadDir(os.TempDir())
		require.NoError(t, err)
		require.Empty(t, entries)
		return
	}
	require.Contains(t, []string{"running", "running_with_search", "before_assignment"}, mode)
	command, cleanup, err := newScriptEditCommand(t.Context(), "p;:again;b again")
	require.NoError(t, err)
	defer cleanup()
	command.Stdin = strings.NewReader(strings.Repeat("x", 64*1024) + "\n")
	command.Stderr = os.Stderr
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	defer stdout.Close()
	if mode == "before_assignment" {
		command.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
		executable, err := os.Executable()
		require.NoError(t, err)
		command.Args = append(command.Args[:2], executable, "-test.run=^TestWindowsFileToolLifetimeHelper$")
		command.Env = append(command.Env, "OMNARA_FILE_TOOL_TEST_MODE=sentinel",
			"OMNARA_FILE_TOOL_SENTINEL="+filepath.Join(os.TempDir(), "tool-started"))
	}
	require.NoError(t, command.Start())
	defer func() { _ = command.Cancel(); _ = command.Wait() }()
	if mode != "before_assignment" {
		// More than sed's stdout buffer proves the real tool is running, without
		// depending on buffering flags or timing sleeps.
		_, err := io.ReadFull(stdout, make([]byte, 64*1024))
		require.NoError(t, err)
	}
	var searchPID uint32
	if mode == "running_with_search" {
		search, err := newFileSearchCommand(t.Context(), "rg", []string{"-e", "."}, nil, 0)
		require.NoError(t, err)
		// Keep a real search process alive after the parent dies. Otherwise stdin
		// EOF could make rg exit and conceal an accidentally inherited job handle.
		search.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
		require.NoError(t, search.Start())
		defer func() { _ = search.Process.Kill(); _ = search.Wait() }()
		searchPID = uint32(search.Process.Pid)
	}
	require.NoError(t, json.NewEncoder(os.Stdout).Encode(struct {
		Launcher uint32
		Members  []uint32
		Search   uint32
	}{uint32(command.Process.Pid), fileToolJobMembers(t, command), searchPID}))
	// The controlling test kills this process. EOF also releases it if that test
	// fails, so its normal defers can terminate the tool and close the job.
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func privateFileToolTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	return dir
}

func windowsFileToolHelper(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, executable, "-test.run=^TestWindowsFileToolLifetimeHelper$", "-test.timeout=25s")
	command.Env = append(os.Environ(), "OMNARA_FILE_TOOL_TEST_MODE="+mode)
	command.WaitDelay = time.Second
	return command
}

func fileToolJobMembers(t *testing.T, command *exec.Cmd) []uint32 {
	t.Helper()
	var info struct {
		Assigned, Count uint32
		PIDs            [8]uintptr
	}
	job := windows.Handle(command.SysProcAttr.AdditionalInheritedHandles[0])
	err := windows.QueryInformationJobObject(job, windows.JobObjectBasicProcessIdList,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
	require.NoError(t, err)
	require.LessOrEqual(t, info.Assigned, uint32(len(info.PIDs)))
	require.LessOrEqual(t, info.Count, uint32(len(info.PIDs)))
	pids := make([]uint32, info.Count)
	for i := range pids {
		pids[i] = uint32(info.PIDs[i])
	}
	return pids
}

func requireFileToolProcessesExited(t *testing.T, command *exec.Cmd) {
	t.Helper()
	job := windows.Handle(command.SysProcAttr.AdditionalInheritedHandles[0])
	for _, pid := range fileToolJobMembers(t, command) {
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			continue
		}
		require.NoError(t, err, "open file-tool process %d", pid)
		status, waitErr := windows.WaitForSingleObject(handle, 0)
		var inJob uint32
		result, _, queryErr := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob").Call(
			uintptr(handle), uintptr(job), uintptr(unsafe.Pointer(&inJob)))
		var name string
		if inJob != 0 && status != windows.WAIT_OBJECT_0 {
			buffer := make([]uint16, 1024)
			size := uint32(len(buffer))
			if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err == nil {
				name = windows.UTF16ToString(buffer[:size])
			}
		}
		require.NoError(t, windows.CloseHandle(handle))
		require.NoError(t, waitErr)
		require.NotZero(t, result, "IsProcessInJob: %v", queryErr)
		if inJob != 0 {
			require.EqualValues(t, windows.WAIT_OBJECT_0, status, "file-tool process %d (%s) is still running", pid, name)
		}
	}
}

func suspendedFileToolThread(t *testing.T, pid uint32) windows.Handle {
	t.Helper()
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	require.NoError(t, err)
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID == pid {
			thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			require.NoError(t, err)
			t.Cleanup(func() { _ = windows.CloseHandle(thread) })
			return thread
		}
	}
	require.ErrorIs(t, err, windows.ERROR_NO_MORE_FILES)
	t.Fatal("suspended launcher thread not found")
	return 0
}

func fileToolProcessHandleCount(t *testing.T) uint32 {
	t.Helper()
	var count uint32
	result, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount").Call(
		uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&count)))
	require.NotZero(t, result, "GetProcessHandleCount: %v", err)
	return count
}
