package omnarad

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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
		context.Background(), home, time.Millisecond, make(chan os.Signal), discardLogger(),
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
		context.Background(), home, 10*time.Millisecond, make(chan os.Signal), discardLogger(),
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
	count := filepath.Join(t.TempDir(), "count")
	environment := filepath.Join(t.TempDir(), "environment")
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestExecutable(t, canonicalDaemonPath(home), `#!/bin/sh
printf '%s\n' "${OMNARA_RUNNER_PATH-unset}" >> "$SUPERVISOR_ENVIRONMENT"
printf x >> "$SUPERVISOR_COUNT"
trap 'exit 0' USR1 TERM
while :; do sleep 1; done
`)
	t.Setenv("SUPERVISOR_COUNT", count)
	t.Setenv("SUPERVISOR_ENVIRONMENT", environment)
	t.Setenv("OMNARA_API_URL", "")
	t.Setenv("OMNARA_MACHINE_TOKEN", "")
	t.Setenv("OMNARA_NO_UPDATE", "")
	t.Setenv("OMNARA_RUNNER_PATH", "/temporary/bin")
	ctx, cancel := context.WithCancel(context.Background())
	restart := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() { done <- runSupervisorLoop(ctx, home, time.Hour, restart, discardLogger()) }()
	waitForFileSize(t, count, 1)
	restart <- daemonRestartSignal
	waitForFileSize(t, count, 2)
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
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSupervisorChildSignalHelper$")
	cmd.Env = append(os.Environ(),
		"OMNARA_SUPERVISOR_CHILD_SIGNAL_HELPER=1",
		"OMNARA_SUPERVISOR_CHILD_SIGNAL_READY="+ready,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waitForFileSize(t, ready, 0)
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
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestExecutable(t, canonicalDaemonPath(home), `#!/bin/sh
: > "$SUPERVISOR_READY"
trap 'exit 0' TERM
while :; do sleep 1; done
`)
	t.Setenv("SUPERVISOR_READY", ready)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runForegroundSupervisor(ctx, home, discardLogger()) }()
	waitForFileSize(t, ready, 0)
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
	if err := os.WriteFile(os.Getenv("OMNARA_SUPERVISOR_CHILD_SIGNAL_READY"), nil, 0o600); err != nil {
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
	process, ready, restarted := startLockOwnerHelper(t, home)
	t.Setenv("OMNARA_HOME", home)
	t.Setenv("PATH", t.TempDir())
	waitForFileSize(t, ready, 0)

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
	setDaemonEnvironment(t, home, server.URL, "token-a")
	t.Setenv("PATH", t.TempDir())
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"restart"}, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("restart exit code = %d stderr = %q", code, stderr.String())
	}
	waitForFileSize(t, restarted, 0)
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
	process, ready, _ := startLockOwnerHelper(t, home)
	waitForFileSize(t, ready, 0)
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
	process, ready, restarted := startLockOwnerHelper(t, home)
	t.Cleanup(func() {
		_ = process.Process.Signal(syscall.SIGTERM)
		_ = process.Wait()
	})
	waitForFileSize(t, ready, 0)
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
	setDaemonEnvironment(t, home, server.URL, "bad-token")
	t.Setenv("PATH", t.TempDir())
	var stdout strings.Builder
	var stderr strings.Builder
	if code := Run(context.Background(), []string{"restart"}, nil, &stdout, &stderr, discardLogger()); code != 1 {
		t.Fatalf("invalid restart exit code = %d", code)
	}
	if _, err := os.Stat(restarted); !os.IsNotExist(err) {
		t.Fatalf("daemon was signaled after failed validation: %v", err)
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
	if err := os.WriteFile(os.Getenv("OMNARA_LOCK_READY"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, daemonRestartSignal, syscall.SIGTERM)
	defer signal.Stop(signals)
	for received := range signals {
		if received == daemonRestartSignal {
			if err := os.WriteFile(os.Getenv("OMNARA_RESTART_RECEIVED"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		return
	}
}

func startLockOwnerHelper(t *testing.T, home string) (*exec.Cmd, string, string) {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	restarted := filepath.Join(t.TempDir(), "restarted")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockOwnerHelper$")
	cmd.Env = append(os.Environ(),
		"OMNARA_LOCK_OWNER_HELPER=1",
		"OMNARA_HOME="+home,
		"OMNARA_LOCK_READY="+ready,
		"OMNARA_RESTART_RECEIVED="+restarted,
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
	return cmd, ready, restarted
}

func waitForFileSize(t *testing.T, path string, size int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		info, err := os.Stat(path)
		if err == nil && info.Size() >= size {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not reach size %d", path, size)
}
