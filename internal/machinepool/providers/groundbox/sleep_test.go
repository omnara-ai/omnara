package groundbox

import (
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

func TestGroundboxKeeperCommandsAreValidShell(t *testing.T) {
	for _, command := range []string{bootCommand, wakeCommand} {
		if output, err := exec.Command("/bin/sh", "-n", "-c", command).CombinedOutput(); err != nil {
			t.Fatalf("%q does not parse: %v %s", command, err, output)
		}
	}
	if !strings.Contains(bootCommand, "omnara_mode=boot\n") || !strings.Contains(wakeCommand, "omnara_mode=wake\n") {
		t.Fatal("keeper commands do not set their mode")
	}
}

type keeperHarness struct {
	t           *testing.T
	controlPath string
	pidPath     string
	pokes       atomic.Int64
	replacer    *strings.Replacer
	env         []string
}

func newKeeperHarness(t *testing.T) *keeperHarness {
	t.Helper()
	h := &keeperHarness{t: t}
	wakeListener := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { h.pokes.Add(1) }))
	t.Cleanup(wakeListener.Close)
	_, wakePort, err := net.SplitHostPort(wakeListener.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	h.controlPath = filepath.Join(dir, "awake")
	h.pidPath = filepath.Join(dir, "pid")
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "setsid"), []byte("#!/bin/sh\nexec \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	h.replacer = strings.NewReplacer(
		daemonprotocol.GroundboxAwakeControlFilePath, h.controlPath,
		daemonPIDPath, h.pidPath,
		daemonLogPath, filepath.Join(dir, "log"),
		"omnara_wake_port="+strconv.Itoa(daemonprotocol.WakeListenerPort), "omnara_wake_port="+wakePort,
		shellQuote(providers.ManagedDaemonLauncherArgs()[2]), "'exec sleep 60'",
	)
	h.env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return h
}

func (h *keeperHarness) start(command string) <-chan error {
	h.t.Helper()
	cmd := exec.Command("/bin/sh", "-c", h.replacer.Replace(command))
	cmd.Env = h.env
	if err := cmd.Start(); err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = cmd.Process.Kill() })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return done
}

func (h *keeperHarness) daemonPID() int {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		raw, err := os.ReadFile(h.pidPath)
		if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && parseErr == nil && pid > 0 {
			return pid
		}
		select {
		case <-deadline:
			h.t.Fatal("the keeper did not start the daemon")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (h *keeperHarness) write(value string) {
	h.t.Helper()
	if err := os.WriteFile(h.controlPath, []byte(value), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func expectKeeperRunning(t *testing.T, done <-chan error, why string) {
	t.Helper()
	select {
	case <-done:
		t.Fatalf("keeper exited while %s", why)
	case <-time.After(1500 * time.Millisecond):
	}
}

func expectKeeperExit(t *testing.T, done <-chan error, why string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("keeper failed after %s: %v", why, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("keeper kept running after %s", why)
	}
}

func TestGroundboxBootKeeperStartsOneDaemonAndHoldsTheSandboxAwake(t *testing.T) {
	h := newKeeperHarness(t)
	first := h.start(bootCommand)
	pid := h.daemonPID()
	t.Cleanup(func() {
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Kill()
		}
	})
	expectKeeperRunning(t, first, "the daemon was awake")
	if h.pokes.Load() != 0 {
		t.Fatalf("wake pokes = %d, want none on the first boot", h.pokes.Load())
	}

	second := h.start(bootCommand)
	expectKeeperRunning(t, second, "the daemon was awake")
	if again := h.daemonPID(); again != pid {
		t.Fatalf("a second boot started daemon %d next to %d", again, pid)
	}
	if h.pokes.Load() != 1 {
		t.Fatalf("wake pokes = %d, want the running daemon poked once", h.pokes.Load())
	}

	h.write("=0")
	expectKeeperExit(t, first, "the daemon allowed sleep")
	expectKeeperExit(t, second, "the daemon allowed sleep")
}

func TestGroundboxWakeKeeperHoldsTheSandboxWhileTheDaemonIsAwake(t *testing.T) {
	h := newKeeperHarness(t)
	daemon := exec.Command("sleep", "60")
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = daemon.Process.Kill() })
	if err := os.WriteFile(h.pidPath, []byte(strconv.Itoa(daemon.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	h.write("=0")

	done := h.start(wakeCommand)
	expectKeeperRunning(t, done, "the daemon was woken")
	if h.pokes.Load() != 1 {
		t.Fatalf("wake pokes = %d, want 1", h.pokes.Load())
	}
	if raw, _ := os.ReadFile(h.controlPath); string(raw) != "=1" {
		t.Fatalf("control file = %q, want =1 while awake", raw)
	}
	h.write("=0")
	expectKeeperExit(t, done, "the daemon allowed sleep")

	done = h.start(wakeCommand)
	_ = daemon.Process.Kill()
	_ = daemon.Wait()
	expectKeeperExit(t, done, "the daemon exited")
}
