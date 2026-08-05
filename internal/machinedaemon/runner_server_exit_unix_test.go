//go:build !windows

package machinedaemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localipc"
)

func TestSupervisorExitReturnsContainmentClosureFailure(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("injected process-group signal failure")
	runner := &localProcessRunner{
		cmd: &exec.Cmd{
			Process: &os.Process{Pid: 4242},
		},
		terminalResultReady: make(chan struct{}),
	}
	runner.containment.Store(&unixProcessGroup{
		pgid:       runner.cmd.Process.Pid,
		signalSafe: true,
		signalGroup: func(int, syscall.Signal) error {
			return wantErr
		},
		waitLiveMembersGone: func(context.Context, int) error {
			return context.DeadlineExceeded
		},
	})
	runner.publishTerminalResult(processRunnerExit{State: "unknown"})
	state := &runnerServerState{prepared: runner}

	err := state.stopForSupervisorExit(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("supervisor exit error = %v, want %v", err, wantErr)
	}
}

func TestUnexpectedSupervisorListenerFailureClosesProcessContainment(
	t *testing.T,
) {
	t.Parallel()

	command := exec.Command("/bin/sh", "-c", "sleep 30")
	if err := setupProcessCommand(command); err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	runner := &localProcessRunner{
		cmd:                 command,
		terminalResultReady: make(chan struct{}),
	}
	released := false
	t.Cleanup(func() {
		if released {
			return
		}
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	if err := afterStartProcessCommand(runner); err != nil {
		t.Fatalf("establish process containment: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	endpoint := filepath.Join(t.TempDir(), "failed-listener.sock")
	listener, err := localipc.Listen(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	defer localipc.Cleanup(endpoint)

	shutdown := make(chan struct{})
	var shutdownOnce sync.Once
	state := &runnerServerState{
		prepared: runner,
		shutdown: func() {
			shutdownOnce.Do(func() {
				close(shutdown)
				_ = listener.Close()
			})
		},
	}
	if err := serveCommandSupervisor(
		ctx,
		listener,
		state,
		shutdown,
	); err == nil {
		t.Fatal("closed listener did not fail the supervisor accept loop")
	}

	state.prepared.terminalMu.Lock()
	override := state.prepared.terminal
	state.prepared.terminalMu.Unlock()
	if override.State != "unknown" ||
		override.StateReasonCode != "supervisor_shutdown" {
		t.Fatalf("supervisor exit override = %+v", override)
	}
	if err := releaseProcessCommand(ctx, runner); err != nil {
		t.Fatalf("release stopped process containment: %v", err)
	}
	released = true
	if command.ProcessState == nil {
		t.Fatal("unexpected listener failure left the child unreaped")
	}
}
