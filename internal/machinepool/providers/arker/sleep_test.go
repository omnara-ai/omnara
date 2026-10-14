package arker

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func sleepOptions(sleepAfterMS string) map[string]json.RawMessage {
	options := testOptions()
	options["sleep_after_ms"] = json.RawMessage(sleepAfterMS)
	return options
}

func TestArkerProvisionWithSleepStartsADetachedDaemon(t *testing.T) {
	fake := &fakeArker{}
	server := fake.start(t, testAllocationName(t))

	result, err := newTestProvider(server.URL).ProvisionMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(sleepOptions("45000")), "tok-1", nil, true,
	)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if result.SandboxURL != server.URL {
		t.Fatalf("sandbox url = %q, want %q", result.SandboxURL, server.URL)
	}
	_, _, env, command := fake.snapshot()
	if env[daemonprotocol.SleepAfterEnvVar] != "45000" ||
		env[daemonprotocol.WakeListenAddrEnvVar] != ":"+strconv.Itoa(daemonprotocol.WakeListenerPort) ||
		env[daemonprotocol.SleepPlatformEnvVar] != daemonprotocol.SleepPlatformArker {
		t.Fatalf("session env = %v", env)
	}
	if command != sleepBootCommand {
		t.Fatalf("daemon run = %q, want the sleep boot command", command)
	}
}

func TestArkerProvisionWithoutSleepSetsNoSleepEnv(t *testing.T) {
	fake := &fakeArker{}
	if _, err := newTestProvider(fake.start(t, testAllocationName(t)).URL).ProvisionMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), "tok-1", nil, true,
	); err != nil {
		t.Fatalf("provision: %v", err)
	}
	_, _, env, _ := fake.snapshot()
	for _, key := range []string{
		daemonprotocol.SleepAfterEnvVar,
		daemonprotocol.WakeListenAddrEnvVar,
		daemonprotocol.SleepPlatformEnvVar,
	} {
		if _, ok := env[key]; ok {
			t.Fatalf("session env has %s without sleep enabled", key)
		}
	}
}

func TestArkerWakeMachineRunsTheWakeCommandInTheWakeSession(t *testing.T) {
	fake := &fakeArker{}
	server := fake.start(t, testAllocationName(t))

	if err := newTestProvider(server.URL).WakeMachine(context.Background(), providers.WakeMachineInput{
		ProviderResourceID: testVMID,
		SandboxURL:         server.URL,
	}); err != nil {
		t.Fatalf("wake: %v", err)
	}
	var command, sessionID string
	var sessionIdx *int
	fake.record(func() { command, sessionID, sessionIdx = fake.runCommand, fake.runSessionID, fake.runSessionIdx })
	if command != wakeCommand || sessionID != "" || sessionIdx == nil || *sessionIdx != wakeSessionIdx {
		t.Fatalf("wake run = %q in session %q / %v", command, sessionID, sessionIdx)
	}
	if fake.sessions.Load() != 0 {
		t.Fatal("wake must not create a session")
	}
}

func TestArkerWakeMachineFailsWhenTheWakeRunIsNotRunning(t *testing.T) {
	fake := &fakeArker{runState: "failed"}
	server := fake.start(t, testAllocationName(t))

	err := newTestProvider(server.URL).WakeMachine(context.Background(), providers.WakeMachineInput{
		ProviderResourceID: testVMID,
		SandboxURL:         server.URL,
	})
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("wake with a failed run = %v, want an error", err)
	}
}

func TestArkerWakeMachineRequiresSandboxURL(t *testing.T) {
	if err := (&provider{}).WakeMachine(context.Background(), providers.WakeMachineInput{}); err == nil {
		t.Fatal("missing sandbox url must fail")
	}
}

func TestArkerSleepAfterBounds(t *testing.T) {
	for _, value := range []string{"29999", "1.5", `"30000"`} {
		if _, err := parseProviderOptions(sleepOptions(value)); err == nil {
			t.Fatalf("sleep_after_ms %s was accepted", value)
		}
	}
	if _, err := parseProviderOptions(sleepOptions("30000")); err != nil {
		t.Fatalf("minimum sleep_after_ms rejected: %v", err)
	}
}

func TestArkerSleepCommandsAreValidShell(t *testing.T) {
	for _, command := range []string{sleepBootCommand, wakeCommand} {
		if output, err := exec.Command("/bin/sh", "-n", "-c", command).CombinedOutput(); err != nil {
			t.Fatalf("%q does not parse: %v %s", command, err, output)
		}
	}
}

func TestArkerWakeCommandHoldsTheRunWhileTheDaemonIsAwake(t *testing.T) {
	var pokes atomic.Int64
	wakeListener := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { pokes.Add(1) }))
	t.Cleanup(wakeListener.Close)
	_, wakePort, _ := net.SplitHostPort(wakeListener.Listener.Addr().String())
	dir := t.TempDir()
	controlPath := filepath.Join(dir, "awake")
	pidPath := filepath.Join(dir, "pid")
	loop := strings.NewReplacer(
		daemonprotocol.ArkerAwakeControlFilePath, controlPath,
		daemonPIDPath, pidPath,
		"omnara_wake_port="+strconv.Itoa(daemonprotocol.WakeListenerPort),
		"omnara_wake_port="+wakePort,
	).Replace(wakeCommand)
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	startLoop := func() <-chan error {
		t.Helper()
		cmd := exec.Command("/bin/sh", "-c", loop)
		cmd.Env = append(
			os.Environ(),
			"http_proxy=http://127.0.0.1:9", "HTTP_PROXY=http://127.0.0.1:9", "no_proxy=", "NO_PROXY=",
		)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		return done
	}
	expectRunning := func(done <-chan error, why string) {
		t.Helper()
		select {
		case <-done:
			t.Fatalf("loop exited while %s", why)
		case <-time.After(1500 * time.Millisecond):
		}
	}
	expectExit := func(done <-chan error, why string) {
		t.Helper()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("loop failed after %s: %v", why, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("loop kept running after %s", why)
		}
	}
	daemon := exec.Command("sleep", "60")
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = daemon.Process.Kill() })

	write(controlPath, "=0")
	done := startLoop()
	expectRunning(done, "the daemon was starting")
	if pokes.Load() != 1 {
		t.Fatalf("wake listener pokes = %d, want 1", pokes.Load())
	}
	write(pidPath, strconv.Itoa(daemon.Process.Pid))
	expectRunning(done, "the daemon was awake")
	write(controlPath, "=0")
	expectExit(done, "the daemon allowed sleep")

	done = startLoop()
	_ = daemon.Process.Kill()
	_ = daemon.Wait()
	expectExit(done, "the daemon exited")
}
