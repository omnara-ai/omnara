package omnarad

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/stretchr/testify/require"
)

func TestFinishPublishedDaemonUpdateHandsOffOnlyToCapableSupervisor(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	missing := filepath.Join(t.TempDir(), "missing-omnarad")
	for _, tc := range []struct {
		name    string
		handoff bool
		want    error
	}{
		{"capable_parent", true, errDaemonUpdateHandoff},
		{"legacy_parent", false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := finishPublishedDaemonUpdate(canceled, missing, nil, tc.handoff, runServiceSubcommand)
			if tc.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.want)
			}
		})
	}
}

func TestSupervisorHandoffExitWithoutReplacementIsCrash(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "bin"), 0o700))
	writeTestExecutable(t, canonicalDaemonPath(home), "#!/bin/sh\nexit 75\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || !strings.Contains(string(body), "exit status 75") {
			t.Errorf("crash report = %q, err = %v", body, err)
		}
		w.WriteHeader(http.StatusNoContent)
		cancel()
	}))
	defer server.Close()
	setConfiguredDaemonEnvironment(t, home, server.URL, "")
	lock, err := localstore.TryAcquireLock(filepath.Join(t.TempDir(), "daemon.lock"))
	require.NoError(t, err)
	defer func() { _ = lock.Release() }()
	require.NoError(t, runSupervisorLoop(
		ctx, home, longBackoffDelay, make(chan os.Signal), io.Discard, io.Discard, discardLogger(), lock,
	))
	require.EqualValues(t, 1, calls.Load())
}

func TestSupervisorCrashAfterBinaryReplacementRestartsWithoutRefresh(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "bin"), 0o700))
	writeTestExecutable(t, filepath.Join(home, "bin", "omnarad.v2"), "#!/bin/sh\nprintf 'child v2\\n'\nexit 0\n")
	writeTestExecutable(t, canonicalDaemonPath(home), `#!/bin/sh
printf 'child v1\n'
mv "$OMNARA_HOME/bin/omnarad.v2" "$OMNARA_HOME/bin/omnarad" || exit 9
exit 7
`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	setConfiguredDaemonEnvironment(t, home, server.URL, "")
	lock, err := localstore.TryAcquireLock(filepath.Join(t.TempDir(), "daemon.lock"))
	require.NoError(t, err)
	defer func() { _ = lock.Release() }()
	output := newLineChannelWriter()
	require.NoError(t, runSupervisorLoop(
		ctx, home, 10*time.Millisecond,
		make(chan os.Signal), output, io.Discard, discardLogger(), lock,
	))
	waitForMarkerLine(t, output.lines, "child v1")
	waitForMarkerLine(t, output.lines, "child v2")
	require.EqualValues(t, 1, calls.Load())
}

func TestSupervisorHandoffHelper(t *testing.T) {
	if os.Getenv("OMNARA_SUPERVISE_TEST") != "1" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	os.Exit(Run(ctx, flag.Args(), os.Stdin, os.Stdout, os.Stderr, log))
}

func TestSupervisorHandoffJourney(t *testing.T) {
	for _, trigger := range []string{
		"update_exit", "manual_restart", "crash_then_manual_restart", "restart_during_backoff",
	} {
		t.Run(trigger, func(t *testing.T) {
			runSupervisorHandoffJourney(t, trigger)
		})
	}
}

func runSupervisorHandoffJourney(t *testing.T, trigger string) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "bin"), 0o700))
	writeTestExecutable(t, filepath.Join(home, "bin", "omnarad.v2"), `#!/bin/sh
case "$1" in
  __omnara_supervise)
    printf 'supervisor v2 pid=%s lock_fd=%s\n' "$$" "${__OMNARA_DAEMON_LOCK_FD:-}"
    exec "$OMNARA_TEST_BINARY" -test.run='^TestSupervisorHandoffHelper$' -- "$@"
    ;;
  run-service)
    [ -z "${__OMNARA_DAEMON_LOCK_FD:-}" ] || exit 9
    [ "${__OMNARA_SUPERVISOR_HANDOFF:-}" = 1 ] || exit 9
    printf 'child v2 pid=%s\n' "$$"
    trap 'exit 0' TERM USR1
    while :; do sleep 0.1; done
    ;;
esac
exit 9
`)
	childV1 := `#!/bin/sh
[ "$1" = run-service ] || exit 9
[ "${__OMNARA_SUPERVISOR_HANDOFF:-}" = 1 ] || exit 9
printf 'child v1 pid=%s\n' "$$"
`
	wantReports := int32(0)
	switch trigger {
	case "update_exit":
		childV1 += `mv "$OMNARA_HOME/bin/omnarad.v2" "$OMNARA_HOME/bin/omnarad" || exit 9
exit 75
`
	case "manual_restart":
		childV1 += `trap 'exit 0' USR1 TERM
while :; do sleep 0.1; done
`
	case "crash_then_manual_restart":
		wantReports = 1
		childV1 += `mv "$OMNARA_HOME/bin/omnarad.v2" "$OMNARA_HOME/bin/omnarad" || exit 9
exit 7
`
	case "restart_during_backoff":
		wantReports = 1
		childV1 += "exit 7\n"
	}
	writeTestExecutable(t, canonicalDaemonPath(home), childV1)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	setConfiguredDaemonEnvironment(t, home, server.URL, "")
	store, err := localstore.New(home)
	require.NoError(t, err)
	output := newLineChannelWriter()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSupervisorHandoffHelper$", "--", superviseSubcommand)
	cmd.Env = append(os.Environ(), "OMNARA_SUPERVISE_TEST=1", "OMNARA_TEST_BINARY="+os.Args[0])
	cmd.Stdout = output
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	supervisorPID := cmd.Process.Pid
	requireLockHeldBy := func() {
		t.Helper()
		_, err := localstore.TryAcquireLock(store.DaemonLockPath())
		require.ErrorIs(t, err, localstore.ErrLockHeld)
		pid, held, err := localstore.InspectLock(store.DaemonLockPath())
		require.NoError(t, err)
		require.True(t, held)
		require.Equal(t, supervisorPID, pid)
	}

	waitForLinePrefix(t, output.lines, "child v1 pid=")
	requireLockHeldBy()
	switch trigger {
	case "manual_restart", "restart_during_backoff":
		if trigger == "restart_during_backoff" {
			waitForLineContaining(t, output.lines, "supervised daemon exited")
		}
		require.NoError(t, os.Rename(filepath.Join(home, "bin", "omnarad.v2"), canonicalDaemonPath(home)))
		require.NoError(t, cmd.Process.Signal(daemonRestartSignal))
	case "crash_then_manual_restart":
		waitForLinePrefix(t, output.lines, "child v2 pid=")
		requireLockHeldBy()
		require.NoError(t, cmd.Process.Signal(daemonRestartSignal))
	}
	refreshed := waitForLinePrefix(t, output.lines, "supervisor v2 pid=")
	fields := strings.Fields(strings.TrimPrefix(refreshed, "supervisor v2 pid="))
	require.Len(t, fields, 2)
	refreshedPID, err := strconv.Atoi(fields[0])
	require.NoError(t, err)
	require.Equal(t, supervisorPID, refreshedPID)
	require.NotEqual(t, "lock_fd=", fields[1])
	requireLockHeldBy()
	waitForLinePrefix(t, output.lines, "child v2 pid=")
	requireLockHeldBy()
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("refreshed supervisor did not exit after shutdown")
	}
	require.Equal(t, wantReports, calls.Load())
	_, held, err := localstore.InspectLock(store.DaemonLockPath())
	require.NoError(t, err)
	require.False(t, held)
}

func TestFinishPublishedDaemonUpdateLegacyFallbackReexecs(t *testing.T) {
	if os.Getenv("OMNARA_LEGACY_FALLBACK_TEST") == "1" {
		if err := finishPublishedDaemonUpdate(
			context.Background(), os.Getenv("OMNARA_REEXEC_BINARY"), nil, false, runServiceSubcommand, supervisedServiceFlag,
		); err != nil {
			os.Exit(1)
		}
		os.Exit(2)
	}
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "argv")
	binaryPath := filepath.Join(dir, "omnarad")
	writeTestExecutable(t, binaryPath, "#!/bin/sh\nprintf '%s\\n' \"$0\" \"$@\" > \"$OMNARA_REEXEC_OUTPUT\"\n")
	cmd := exec.Command(os.Args[0], "-test.run=^TestFinishPublishedDaemonUpdateLegacyFallbackReexecs$")
	cmd.Env = append(
		os.Environ(),
		"OMNARA_LEGACY_FALLBACK_TEST=1",
		"OMNARA_REEXEC_BINARY="+binaryPath,
		"OMNARA_REEXEC_OUTPUT="+outputPath,
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Equal(t, binaryPath+"\nrun-service\n--supervised\n", readTestFile(t, outputPath))
}

func TestReexecUpdatedDaemonPreservesSupervisorCapability(t *testing.T) {
	if os.Getenv("OMNARA_RECOVERY_REEXEC_TEST") == "1" {
		handoff := os.Getenv(supervisorHandoffEnv) == "1"
		require.NoError(t, os.Unsetenv(supervisorHandoffEnv))
		require.NoError(t, reexecUpdatedDaemon(
			context.Background(), os.Getenv("OMNARA_REEXEC_BINARY"), nil, handoff,
			runServiceSubcommand, supervisedServiceFlag,
		))
		t.Fatal("reexec returned without replacing the process")
	}
	for _, tc := range []struct {
		name    string
		handoff string
		want    string
	}{
		{"capable_parent", "1", "1"},
		{"legacy_parent", "", "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			outputPath := filepath.Join(dir, "result")
			binaryPath := filepath.Join(dir, "omnarad")
			writeTestExecutable(t, binaryPath, `#!/bin/sh
printf '%s\n' "${__OMNARA_SUPERVISOR_HANDOFF:-missing}" "$@" > "$OMNARA_REEXEC_OUTPUT"
`)
			cmd := exec.Command(os.Args[0], "-test.run=^TestReexecUpdatedDaemonPreservesSupervisorCapability$")
			cmd.Env = append(os.Environ(),
				"OMNARA_RECOVERY_REEXEC_TEST=1",
				"OMNARA_REEXEC_BINARY="+binaryPath,
				"OMNARA_REEXEC_OUTPUT="+outputPath,
				supervisorHandoffEnv+"="+tc.handoff,
			)
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, string(output))
			require.Equal(t, tc.want+"\nrun-service\n--supervised\n", readTestFile(t, outputPath))
		})
	}
}

func waitForLinePrefix(t *testing.T, lines <-chan string, prefix string) string {
	t.Helper()
	return waitForLineMatching(t, lines, func(line string) bool { return strings.HasPrefix(line, prefix) }, prefix)
}

func waitForLineContaining(t *testing.T, lines <-chan string, substring string) string {
	t.Helper()
	return waitForLineMatching(t, lines, func(line string) bool { return strings.Contains(line, substring) }, substring)
}

func waitForLineMatching(t *testing.T, lines <-chan string, match func(string) bool, want string) string {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case line := <-lines:
			if match(line) {
				return line
			}
		case <-deadline:
			t.Fatalf("timed out waiting for line matching %q", want)
			return ""
		}
	}
}
