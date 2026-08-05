//go:build !windows

package machinedaemon

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestUnixPostStartSetupFailuresRetainSafeGroupCleanup(t *testing.T) {
	t.Parallel()
	setupErr := errors.New("injected post-start setup failure")
	unexpectedObserverErr := errors.New(
		"observer created after failed group inspection",
	)
	cases := []struct {
		name        string
		inspect     func(int) (int, error)
		newObserver func(int) (processExitObserver, error)
	}{
		{
			name: "group inspection",
			inspect: func(int) (int, error) {
				return 0, setupErr
			},
			newObserver: func(int) (processExitObserver, error) {
				return nil, unexpectedObserverErr
			},
		},
		{
			name: "exit observer",
			inspect: func(pid int) (int, error) {
				return pid, nil
			},
			newObserver: func(int) (processExitObserver, error) {
				return nil, setupErr
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			command := exec.Command("/bin/sh", "-c", "sleep 30")
			if err := setupProcessCommand(command); err != nil {
				t.Fatalf("configure command: %v", err)
			}
			if err := command.Start(); err != nil {
				t.Fatalf("start command: %v", err)
			}
			runner := &localProcessRunner{cmd: command}
			reaped := false
			t.Cleanup(func() {
				if !reaped {
					_ = command.Process.Kill()
					_ = command.Wait()
				}
			})

			err := afterStartProcessCommandWith(
				runner,
				tc.inspect,
				tc.newObserver,
			)
			if !errors.Is(err, setupErr) {
				t.Fatalf("post-start setup error = %v", err)
			}
			if errors.Is(err, unexpectedObserverErr) {
				t.Fatal(unexpectedObserverErr)
			}
			if _, ok := unixProcessGroupFor(runner); !ok {
				t.Fatal("post-start failure discarded the safe process-group identity")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			closure := terminateAndReapProcessCommand(ctx, runner)
			cancel()
			reaped = true
			if closure.ContainmentErr != nil {
				t.Fatalf("close retained process group: %v", closure.ContainmentErr)
			}
			if err := releaseProcessCommand(
				context.Background(),
				runner,
			); err != nil {
				t.Fatalf("release empty process group: %v", err)
			}
		})
	}
}

func TestUnixObservationFailureIgnoresCanceledRequestForForcedClosure(
	t *testing.T,
) {
	command := exec.Command("/bin/sh", "-c", "sleep 30")
	if err := setupProcessCommand(command); err != nil {
		t.Fatalf("configure command: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start command: %v", err)
	}
	runner := &localProcessRunner{cmd: command}
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			_ = command.Wait()
		}
	})
	if err := afterStartProcessCommand(runner); err != nil {
		t.Fatalf("establish process containment: %v", err)
	}

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	observationErr := errors.New("injected observation failure")
	resultCh := make(chan processClosureResult, 1)
	go func() {
		resultCh <- closeAndReapProcessCommand(
			requestCtx,
			runner,
			observationErr,
		)
	}()

	select {
	case closure := <-resultCh:
		reaped = true
		if !errors.Is(closure.ContainmentErr, observationErr) {
			t.Fatalf(
				"containment error = %v, want observation failure",
				closure.ContainmentErr,
			)
		}
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		t.Fatal("forced closure inherited cancellation and blocked in Cmd.Wait")
	}
	if err := releaseProcessCommand(context.Background(), runner); err != nil {
		t.Fatalf("release closed process group: %v", err)
	}
}

func TestUnixCanceledClosureRetainsLeaderAndSignalAuthorityForRetry(
	t *testing.T,
) {
	command := exec.Command("/bin/sh", "-c", "sleep 30 & exit 0")
	if err := setupProcessCommand(command); err != nil {
		t.Fatalf("configure command: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start command: %v", err)
	}
	runner := &localProcessRunner{cmd: command}
	t.Cleanup(func() { cleanupRetainedUnixProcessCommand(runner) })
	if err := afterStartProcessCommand(runner); err != nil {
		t.Fatalf("establish process containment: %v", err)
	}
	if err := waitProcessCommandLeaderExit(runner); err != nil {
		t.Fatalf("observe process leader exit: %v", err)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	closure := closeAndReapProcessCommand(canceledCtx, runner, nil)
	if !errors.Is(closure.ContainmentErr, context.Canceled) {
		t.Fatalf(
			"containment error = %v, want canceled closure",
			closure.ContainmentErr,
		)
	}
	assertUnixClosureRetained(t, runner)

	retryCtx, cancelRetry := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancelRetry()
	if err := closeProcessCommandContainment(retryCtx, runner); err != nil {
		t.Fatalf("retry process-group closure: %v", err)
	}
	if err := releaseProcessCommand(retryCtx, runner); err != nil {
		t.Fatalf("release process group after retry: %v", err)
	}
	if command.ProcessState == nil {
		t.Fatal("successful closure retry did not reap the retained leader")
	}
}

func TestUnixAmbiguousStartCleanupFailureRetainsAuthorityForRetry(
	t *testing.T,
) {
	command := exec.Command("/bin/sh", "-c", "sleep 30")
	if err := setupProcessCommand(command); err != nil {
		t.Fatalf("configure command: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start command: %v", err)
	}
	runner := &localProcessRunner{cmd: command}
	t.Cleanup(func() { cleanupRetainedUnixProcessCommand(runner) })
	if err := afterStartProcessCommand(runner); err != nil {
		t.Fatalf("establish process containment: %v", err)
	}
	group, ok := unixProcessGroupFor(runner)
	if !ok {
		t.Fatal("process-group ownership is missing")
	}
	probeErr := errors.New("injected process-table probe failure")
	group.mu.Lock()
	group.waitLiveMembersGone = func(context.Context, int) error {
		return probeErr
	}
	group.mu.Unlock()

	closure := terminateAndReapProcessCommand(
		context.Background(),
		runner,
	)
	if !errors.Is(closure.ContainmentErr, probeErr) {
		t.Fatalf(
			"containment error = %v, want closure-proof failure",
			closure.ContainmentErr,
		)
	}
	assertUnixClosureRetained(t, runner)

	group.mu.Lock()
	group.waitLiveMembersGone = nil
	group.mu.Unlock()
	retryCtx, cancelRetry := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancelRetry()
	if err := closeProcessCommandContainment(retryCtx, runner); err != nil {
		t.Fatalf("retry process-group closure: %v", err)
	}
	if err := releaseProcessCommand(retryCtx, runner); err != nil {
		t.Fatalf("release process group after retry: %v", err)
	}
}

func TestUnixSignalFailureRetainsLeaderAndSignalAuthorityForRetry(
	t *testing.T,
) {
	command := exec.Command("/bin/sh", "-c", "sleep 30 & wait")
	if err := setupProcessCommand(command); err != nil {
		t.Fatalf("configure command: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start command: %v", err)
	}
	runner := &localProcessRunner{cmd: command}
	t.Cleanup(func() { cleanupRetainedUnixProcessCommand(runner) })
	if err := afterStartProcessCommand(runner); err != nil {
		t.Fatalf("establish process containment: %v", err)
	}
	group, ok := unixProcessGroupFor(runner)
	if !ok {
		t.Fatal("process-group ownership is missing")
	}
	signalErr := errors.New("injected process-group signal failure")
	probeErr := errors.New("injected closure proof failure")
	group.mu.Lock()
	group.signalGroup = func(int, syscall.Signal) error {
		return signalErr
	}
	group.waitLiveMembersGone = func(context.Context, int) error {
		return probeErr
	}
	group.mu.Unlock()

	closure := closeAndReapProcessCommand(
		context.Background(),
		runner,
		errors.New("injected leader observation failure"),
	)
	if !errors.Is(closure.ContainmentErr, signalErr) ||
		!errors.Is(closure.ContainmentErr, probeErr) {
		t.Fatalf(
			"containment error = %v, want signal and proof failures",
			closure.ContainmentErr,
		)
	}
	assertUnixClosureRetained(t, runner)

	group.mu.Lock()
	group.signalGroup = nil
	group.waitLiveMembersGone = nil
	group.mu.Unlock()
	retryCtx, cancelRetry := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancelRetry()
	if err := closeProcessCommandContainment(retryCtx, runner); err != nil {
		t.Fatalf("retry process-group closure: %v", err)
	}
	if err := releaseProcessCommand(retryCtx, runner); err != nil {
		t.Fatalf("release process group after retry: %v", err)
	}
}

func assertUnixClosureRetained(t *testing.T, runner *localProcessRunner) {
	t.Helper()
	group, ok := unixProcessGroupFor(runner)
	if !ok {
		t.Fatal("closure failure discarded process-group ownership")
	}
	group.mu.Lock()
	defer group.mu.Unlock()
	if group.closed {
		t.Fatal("closure failure marked process-group containment empty")
	}
	if group.reaped || runner.cmd.ProcessState != nil {
		t.Fatal("closure failure reaped the process-group leader")
	}
	if !group.signalSafe {
		t.Fatal("closure failure discarded safe process-group signal authority")
	}
}

func cleanupRetainedUnixProcessCommand(runner *localProcessRunner) {
	if group, ok := unixProcessGroupFor(runner); ok {
		group.mu.Lock()
		group.signalGroup = nil
		group.waitLiveMembersGone = nil
		group.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = closeProcessCommandContainment(ctx, runner)
		_ = releaseProcessCommand(ctx, runner)
		cancel()
	}
	command := runner.cmd
	if command != nil && command.Process != nil && command.ProcessState == nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		_ = command.Wait()
	}
}
