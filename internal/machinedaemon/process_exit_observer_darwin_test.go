//go:build darwin

package machinedaemon

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestDarwinFastExitBeforeObserverRegistrationRemainsTruthful(t *testing.T) {
	for _, mode := range []string{"pipe", "pty"} {
		t.Run(mode, func(t *testing.T) {
			command := exec.Command("/usr/bin/true")
			runner := &localProcessRunner{
				cmd:                 command,
				terminalResultReady: make(chan struct{}),
			}
			switch mode {
			case "pipe":
				if err := setupProcessCommand(command); err != nil {
					t.Fatalf("set up process group: %v", err)
				}
				if err := command.Start(); err != nil {
					t.Fatalf("start fast command: %v", err)
				}
			case "pty":
				if err := startPTYProcessCommand(command, runner); err != nil {
					t.Fatalf("start fast PTY command: %v", err)
				}
			}
			reaped := false
			t.Cleanup(func() {
				if reaped {
					return
				}
				_ = command.Process.Kill()
				_ = command.Wait()
			})

			waitDarwinZombie(t, command.Process.Pid)
			if err := afterStartProcessCommand(runner); err != nil {
				t.Fatalf("register observer after child exit: %v", err)
			}
			closure := closeAndReapProcessCommand(
				context.Background(),
				runner,
				waitProcessCommandLeaderExit(runner),
			)
			reaped = true
			if closure.WaitErr != nil {
				t.Fatalf("wait fast command: %v", closure.WaitErr)
			}
			if closure.ContainmentErr != nil {
				t.Fatalf(
					"close fast command containment: %v",
					closure.ContainmentErr,
				)
			}
			if err := runner.waitOutputDrain(); err != nil {
				t.Fatalf("drain fast command output: %v", err)
			}
			if err := releaseProcessCommand(context.Background(), runner); err != nil {
				t.Fatalf("release fast command containment: %v", err)
			}
		})
	}
}

func waitDarwinZombie(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		zombie, err := darwinProcessIsZombie(pid)
		if err != nil {
			t.Fatalf("inspect fast command: %v", err)
		}
		if zombie {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d did not become a zombie", pid)
		}
		time.Sleep(time.Millisecond) //nolint:omnaralint // No exit signal exists that would not reap the zombie.
	}
}
