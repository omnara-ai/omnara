package omnarad

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

func TestSupervisorStopsAfterCleanExit(t *testing.T) {
	home := t.TempDir()
	args := filepath.Join(t.TempDir(), "args")
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestExecutable(t, canonicalDaemonPath(home), "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$SUPERVISOR_ARGS\"\nexit 0\n")
	t.Setenv("SUPERVISOR_ARGS", args)
	err := runSupervisorLoop(
		context.Background(), home, time.Millisecond, make(chan os.Signal), io.Discard, io.Discard, discardLogger(),
	)
	if err != nil {
		t.Fatalf("run supervisor loop: %v", err)
	}
	if got := readTestFile(t, args); got != "run-service --supervised\n" {
		t.Fatalf("child args = %q", got)
	}
}

func TestSupervisorRestartsCrash(t *testing.T) {
	home := t.TempDir()
	count := filepath.Join(t.TempDir(), "count")
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestExecutable(t, canonicalDaemonPath(home), `#!/bin/sh
printf x >> "$SUPERVISOR_COUNT"
[ "$(wc -c < "$SUPERVISOR_COUNT")" -gt 1 ] && exit 0
exit 7
`)
	t.Setenv("SUPERVISOR_COUNT", count)
	err := runSupervisorLoop(
		context.Background(), home, 10*time.Millisecond, make(chan os.Signal), io.Discard, io.Discard, discardLogger(),
	)
	if err != nil {
		t.Fatalf("run supervisor loop: %v", err)
	}
	if got := readTestFile(t, count); got != "xx" {
		t.Fatalf("child starts = %q", got)
	}
}

func TestSupervisorSignalsRestartAndStop(t *testing.T) {
	home := t.TempDir()
	environment := filepath.Join(t.TempDir(), "environment")
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestExecutable(t, canonicalDaemonPath(home), `#!/bin/sh
printf '%s\n' "${OMNARA_RUNNER_PATH-unset}" >> "$SUPERVISOR_ENVIRONMENT"
printf 'started\n'
trap 'exit 0' USR1 TERM
while :; do sleep 1; done
`)
	t.Setenv("SUPERVISOR_ENVIRONMENT", environment)
	t.Setenv("OMNARA_API_URL", "")
	t.Setenv("OMNARA_MACHINE_TOKEN", "")
	t.Setenv("OMNARA_NO_UPDATE", "")
	t.Setenv("OMNARA_RUNNER_PATH", "/temporary/bin")
	ctx, cancel := context.WithCancel(context.Background())
	restart := make(chan os.Signal, 1)
	childOutput := newLineChannelWriter()
	done := make(chan error, 1)
	go func() {
		done <- runSupervisorLoop(ctx, home, time.Hour, restart, childOutput, io.Discard, discardLogger())
	}()
	waitForMarkerLine(t, childOutput.lines, "started")
	restart <- daemonRestartSignal
	waitForMarkerLine(t, childOutput.lines, "started")
	if got := readTestFile(t, environment); got != "/temporary/bin\nunset\n" {
		t.Fatalf("child environments = %q", got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run supervisor loop: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("supervisor did not stop")
	}
}

func TestRestartDuringCrashBackoffClearsEnvironmentOverrides(t *testing.T) {
	t.Setenv("OMNARA_API_URL", "")
	t.Setenv("OMNARA_MACHINE_TOKEN", "")
	t.Setenv("OMNARA_NO_UPDATE", "")
	t.Setenv("OMNARA_RUNNER_PATH", "/temporary/bin")
	restart := make(chan os.Signal, 1)
	restart <- daemonRestartSignal
	waitForDaemonRestart(context.Background(), time.Hour, restart)
	if _, ok := os.LookupEnv("OMNARA_RUNNER_PATH"); ok {
		t.Fatal("restart during crash backoff retained environment override")
	}
}

func TestTerminateSupervisorChildKillsAfterTimeout(t *testing.T) {
	output := newLineChannelWriter()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSupervisorChildSignalHelper$")
	cmd.Stdout = output
	cmd.Env = append(os.Environ(), "OMNARA_SUPERVISOR_CHILD_SIGNAL_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waitForMarkerLine(t, output.lines, "signal-helper-ready")
	if err := terminateSupervisorChild(
		cmd, done, syscall.SIGTERM, 10*time.Millisecond, discardLogger(),
	); err != nil {
		t.Fatalf("terminate supervisor child: %v", err)
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("supervisor child status = %v", cmd.ProcessState)
	}
}

func TestRunForegroundSupervisorOwnsExistingLock(t *testing.T) {
	home := t.TempDir()
	ready := filepath.Join(t.TempDir(), "ready")
	if err := syscall.Mkfifo(ready, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestExecutable(t, canonicalDaemonPath(home), `#!/bin/sh
: > "$SUPERVISOR_READY"
trap 'exit 0' TERM
while :; do sleep 1; done
`)
	installLock, err := localstore.TryAcquireLock(filepath.Join(home, installLockFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := installLock.Release(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUPERVISOR_READY", ready)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runForegroundSupervisor(ctx, home, discardLogger()) }()
	readyOpened := make(chan error, 1)
	go func() {
		f, err := os.Open(ready)
		if err == nil {
			err = f.Close()
		}
		readyOpened <- err
	}()
	select {
	case err := <-readyOpened:
		if err != nil {
			t.Fatalf("open ready fifo: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("supervised daemon did not start")
	}
	store, err := localstore.New(home)
	if err != nil {
		t.Fatal(err)
	}
	pid, held, err := localstore.InspectLock(store.DaemonLockPath())
	if err != nil || !held || pid != os.Getpid() {
		t.Fatalf("supervisor lock = pid %d held %t error %v", pid, held, err)
	}
	secondCtx, secondCancel := context.WithCancel(context.Background())
	secondCancel()
	err = runForegroundSupervisor(secondCtx, home, discardLogger())
	if err == nil || !strings.Contains(err.Error(), "another daemon") {
		t.Fatalf("second supervisor error = %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("supervisor stop: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("supervisor did not release lock")
	}
}

func TestRunForegroundSupervisorDoesNotRecreateMissingHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "removed-home")
	if err := runForegroundSupervisor(context.Background(), home, discardLogger()); err == nil {
		t.Fatalf("foreground start error = %v", err)
	}
	if _, err := os.Lstat(home); !os.IsNotExist(err) {
		t.Fatalf("foreground start recreated removed home: %v", err)
	}
}

func TestRunForegroundSupervisorRejectsInstallInProgress(t *testing.T) {
	home := t.TempDir()
	installLock, err := localstore.TryAcquireLock(filepath.Join(home, installLockFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = installLock.Release() }()
	if err := runForegroundSupervisor(context.Background(), home, discardLogger()); err == nil ||
		!strings.Contains(err.Error(), "being modified") {
		t.Fatalf("foreground start error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, "daemon.lock")); !os.IsNotExist(err) {
		t.Fatalf("foreground start created daemon lock: %v", err)
	}
}

func TestSupervisorChildRejectsInvalidParentLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("OMNARA_HOME", home)
	var stdout strings.Builder
	var stderr strings.Builder
	if code := Run(
		context.Background(), []string{"run-service", supervisedServiceFlag}, nil,
		&stdout, &stderr, discardLogger(),
	); code != 1 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("supervisor child without lock = code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	store, err := localstore.New(home)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := localstore.TryAcquireLock(store.DaemonLockPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	if err := lock.WritePID(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := runSupervisorChild(context.Background(), discardLogger()); err == nil {
		t.Fatal("supervisor child with wrong parent PID succeeded")
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if err := runSupervisorChild(context.Background(), discardLogger()); err == nil {
		t.Fatal("supervisor child with unlocked lock succeeded")
	}
}

func TestSupervisorChildSignalHelper(t *testing.T) {
	if os.Getenv("OMNARA_SUPERVISOR_CHILD_SIGNAL_HELPER") != "1" {
		return
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, daemonRestartSignal, syscall.SIGTERM)
	defer signal.Stop(signals)
	if _, err := os.Stdout.WriteString("signal-helper-ready\n"); err != nil {
		t.Fatal(err)
	}
	select {}
}

func TestStoppedLifecycleCommandsDoNotNeedConfiguration(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("OMNARA_HOME", home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("OMNARA_API_URL", "://invalid")
	var stdout strings.Builder
	var stderr strings.Builder
	if code := Run(context.Background(), []string{"stop"}, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("stop exit code = %d stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, daemonConfigFileName)); !os.IsNotExist(err) {
		t.Fatalf("stop wrote daemon config: %v", err)
	}
	if _, err := os.Lstat(home); !os.IsNotExist(err) {
		t.Fatalf("stop created daemon home: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"status"}, nil, &stdout, &stderr, discardLogger()); code != 1 {
		t.Fatalf("stopped status exit code = %d stderr = %q", code, stderr.String())
	}
	if stdout.String() != "omnarad is stopped\n" {
		t.Fatalf("stopped status output = %q", stdout.String())
	}
}

func TestNoServiceStatusStopAndRestart(t *testing.T) {
	home := t.TempDir()
	process, lines := startLockOwnerHelper(t, home)
	t.Setenv("OMNARA_HOME", home)
	t.Setenv("PATH", t.TempDir())

	var stdout strings.Builder
	var stderr strings.Builder
	if code := Run(context.Background(), []string{"status"}, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("status exit code = %d stderr = %q", code, stderr.String())
	}
	wantStatus := "omnarad is running (no-service, pid " + strconv.Itoa(process.Process.Pid) + ")\n"
	if stdout.String() != wantStatus {
		t.Fatalf("status output = %q, want %q", stdout.String(), wantStatus)
	}

	server := bootstrapServer(t, "token-a", "inst-a", "mch-a")
	defer server.Close()
	writeTestDaemonConfig(t, home, daemonConfig{
		SchemaVersion:  daemonConfigVersion,
		APIURL:         server.URL,
		InstallationID: "inst-a",
		MachineID:      "mch-a",
		MachineToken:   "token-a",
		RunnerPath:     "/bin",
	})
	setDaemonEnvironment(t, home, testAPIBaseURL(server.URL), "token-a")
	t.Setenv("PATH", t.TempDir())
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"restart"}, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("restart exit code = %d stderr = %q", code, stderr.String())
	}
	waitForMarkerLine(t, lines, "restart-received")
	if stdout.String() != "omnarad is restarting (no-service)\n" {
		t.Fatalf("restart output = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"stop"}, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("stop exit code = %d stderr = %q", code, stderr.String())
	}
	if stdout.String() != "omnarad is stopped\n" {
		t.Fatalf("stop output = %q", stdout.String())
	}
	if err := process.Wait(); err != nil {
		t.Fatalf("wait for lock owner: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"stop"}, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("idempotent stop exit code = %d stderr = %q", code, stderr.String())
	}
}

func TestTemporaryRestartReplacesNoServiceDaemon(t *testing.T) {
	home := t.TempDir()
	process, _ := startLockOwnerHelper(t, home)
	server := bootstrapServer(t, "token-a", "inst-a", "mch-a")
	defer server.Close()
	setConfiguredDaemonEnvironment(t, home, server.URL, "/stored/bin")
	before, err := os.ReadFile(filepath.Join(home, daemonConfigFileName))
	if err != nil {
		t.Fatalf("read daemon config before restart: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	runnerPath := filepath.Join(t.TempDir(), "runner-path")
	writeTestExecutable(
		t,
		canonicalDaemonPath(home),
		"#!/bin/sh\nprintf '%s\\n' \"$OMNARA_RUNNER_PATH\" > \"$TEMPORARY_RUNNER_PATH\"\n",
	)
	t.Setenv("OMNARA_RUNNER_PATH", "/temporary/bin")
	t.Setenv("TEMPORARY_RUNNER_PATH", runnerPath)
	t.Setenv("PATH", t.TempDir())

	var stdout strings.Builder
	var stderr strings.Builder
	if code := Run(
		context.Background(),
		[]string{"restart"},
		nil,
		&stdout,
		&stderr,
		discardLogger(),
	); code != 0 {
		t.Fatalf("temporary restart exit code = %d stderr = %q", code, stderr.String())
	}
	if stdout.Len() != 0 || stderr.String() != noServiceWarning+"\n" {
		t.Fatalf("temporary restart stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if err := process.Wait(); err != nil {
		t.Fatalf("wait for replaced daemon: %v", err)
	}
	if got := readTestFile(t, runnerPath); got != "/temporary/bin\n" {
		t.Fatalf("temporary runner path = %q", got)
	}
	after, err := os.ReadFile(filepath.Join(home, daemonConfigFileName))
	if err != nil {
		t.Fatalf("read daemon config after restart: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("restart changed daemon config")
	}
}

func TestRestartValidatesBeforeSignalingNoServiceDaemon(t *testing.T) {
	home := t.TempDir()
	process, lines := startLockOwnerHelper(t, home)
	server := bootstrapServer(t, "good-token", "inst-a", "mch-a")
	defer server.Close()
	writeTestDaemonConfig(t, home, daemonConfig{
		SchemaVersion:  daemonConfigVersion,
		APIURL:         server.URL,
		InstallationID: "inst-a",
		MachineID:      "mch-a",
		MachineToken:   "good-token",
		RunnerPath:     "/bin",
	})
	setDaemonEnvironment(t, home, testAPIBaseURL(server.URL), "bad-token")
	t.Setenv("PATH", t.TempDir())
	var stdout strings.Builder
	var stderr strings.Builder
	if code := Run(context.Background(), []string{"restart"}, nil, &stdout, &stderr, discardLogger()); code != 1 {
		t.Fatalf("invalid restart exit code = %d", code)
	}
	if err := process.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("stop lock owner: %v", err)
	}
	if err := process.Wait(); err != nil {
		t.Fatalf("wait for lock owner: %v", err)
	}
	for {
		select {
		case line := <-lines:
			if line == "restart-received" {
				t.Fatal("daemon was signaled after failed validation")
			}
		default:
			return
		}
	}
}

func TestLockOwnerHelper(t *testing.T) {
	if os.Getenv("OMNARA_LOCK_OWNER_HELPER") != "1" {
		return
	}
	store, err := localstore.New(os.Getenv("OMNARA_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := localstore.TryAcquireLock(store.DaemonLockPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	if err := lock.WritePID(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, daemonRestartSignal, syscall.SIGTERM)
	defer signal.Stop(signals)
	if _, err := os.Stdout.WriteString("lock-ready\n"); err != nil {
		t.Fatal(err)
	}
	for received := range signals {
		if received == daemonRestartSignal {
			if _, err := os.Stdout.WriteString("restart-received\n"); err != nil {
				t.Fatal(err)
			}
			continue
		}
		return
	}
}

func startLockOwnerHelper(t *testing.T, home string) (*exec.Cmd, <-chan string) {
	t.Helper()
	output := newLineChannelWriter()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockOwnerHelper$")
	cmd.Stdout = output
	cmd.Env = append(os.Environ(),
		"OMNARA_LOCK_OWNER_HELPER=1",
		"OMNARA_HOME="+home,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lock owner: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	waitForMarkerLine(t, output.lines, "lock-ready")
	return cmd, output.lines
}

type lineChannelWriter struct {
	mu      sync.Mutex
	partial []byte
	lines   chan string
}

func newLineChannelWriter() *lineChannelWriter {
	return &lineChannelWriter{lines: make(chan string, 64)}
}

func (w *lineChannelWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			return len(p), nil
		}
		w.lines <- string(w.partial[:i])
		w.partial = w.partial[i+1:]
	}
}

func waitForMarkerLine(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case line := <-lines:
			if line == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for marker %q", want)
		}
	}
}
