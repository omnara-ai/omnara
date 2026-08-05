//go:build !windows

package machinedaemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const terminateGracePeriod = 2 * time.Second
const containmentPollInterval = 25 * time.Millisecond
const containmentEmptyObservations = 2
const containmentQuiescenceProbeTimeout = time.Duration(
	containmentEmptyObservations+1,
) * containmentPollInterval
const processContainmentKind = "process_group"

type processContainment = unixProcessGroup

// The unreaped leader keeps the group ID trustworthy while signalSafe is true.
// Recovery never signals a recorded ID because Unix may have recycled it.
type unixProcessGroup struct {
	mu sync.Mutex

	pgid       int
	observer   processExitObserver
	signalSafe bool
	closed     bool
	reaped     bool
	waitErr    error

	signalGroup         func(int, syscall.Signal) error
	waitLiveMembersGone func(context.Context, int) error
}

func setupProcessCommand(command *exec.Cmd) error {
	if command == nil {
		return errors.New("process command is required")
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func validateProcessCommandPlatform() error {
	if !supportsSafeProcessExitObservation() {
		return errors.New(
			"safe process-tree exit observation is not supported on this platform",
		)
	}
	return nil
}

func afterStartProcessCommand(runner *localProcessRunner) error {
	return afterStartProcessCommandWith(
		runner,
		startedProcessGroupID,
		newProcessExitObserver,
	)
}

func afterStartProcessCommandWith(
	runner *localProcessRunner,
	inspectGroup func(int) (int, error),
	newObserver func(int) (processExitObserver, error),
) error {
	command := runner.cmd
	if command == nil || command.Process == nil {
		return errors.New("process has not started")
	}
	// Publish ownership before fallible inspection so every post-Start failure
	// can still stop the group.
	group := &unixProcessGroup{
		pgid:       command.Process.Pid,
		signalSafe: true,
	}
	if !runner.containment.CompareAndSwap(nil, group) {
		group = runner.containment.Load()
	}
	if group.pgid != command.Process.Pid {
		return errors.New("process command has conflicting containment identity")
	}
	actualGroup, err := inspectGroup(command.Process.Pid)
	if err != nil {
		return fmt.Errorf("inspect started process group: %w", err)
	}
	if actualGroup != group.pgid {
		return fmt.Errorf(
			"started process group is %d, want %d",
			actualGroup,
			group.pgid,
		)
	}
	observer, err := newObserver(command.Process.Pid)
	if err != nil {
		return fmt.Errorf("establish safe process exit observation: %w", err)
	}
	group.mu.Lock()
	if group.observer != nil {
		group.mu.Unlock()
		_ = observer.Close()
		return errors.New("process command already has an exit observer")
	}
	group.observer = observer
	group.mu.Unlock()
	return nil
}

func releaseProcessCommand(ctx context.Context, runner *localProcessRunner) error {
	command := runner.cmd
	group, ok := unixProcessGroupFor(runner)
	if !ok {
		if command != nil && command.Process != nil {
			return errors.New("process-group ownership is missing")
		}
		return nil
	}
	group.mu.Lock()
	defer group.mu.Unlock()
	if !group.closed {
		if !group.signalSafe {
			return errors.New(
				"process containment was reaped before live-member closure was proved",
			)
		}
		probeCtx, cancel := context.WithTimeout(
			ctx,
			containmentQuiescenceProbeTimeout,
		)
		err := group.waitForLiveMembersGone(probeCtx)
		cancel()
		if err != nil {
			if !errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return fmt.Errorf("process group %d is still active", group.pgid)
		}
		group.closed = true
	}
	// Earlier failures retain the leader to prevent PGID reuse. Once the group is
	// empty, reap the leader; a normal ExitError still counts as reaped.
	_ = group.reapLocked(command)
	if !group.reaped {
		return errors.New("closed process containment could not be reaped")
	}
	return nil
}

func interruptProcessCommand(runner *localProcessRunner) error {
	if group, ok := unixProcessGroupFor(runner); ok {
		return group.signal(syscall.SIGINT)
	}
	command := runner.cmd
	if command == nil || command.Process == nil {
		return errors.New("process has not started")
	}
	return command.Process.Signal(os.Interrupt)
}

func terminateAndReapProcessCommand(
	ctx context.Context,
	runner *localProcessRunner,
) processClosureResult {
	command := runner.cmd
	if command == nil || command.Process == nil {
		err := errors.New("process has not started")
		return processClosureResult{WaitErr: err, ContainmentErr: err}
	}
	group, hasGroup := unixProcessGroupFor(runner)
	if !hasGroup {
		containmentErr := errors.Join(
			errors.New("process-group ownership is missing"),
			command.Process.Kill(),
		)
		return processClosureResult{
			WaitErr:        command.Wait(),
			ContainmentErr: containmentErr,
		}
	}

	containmentErr := group.beginForcedClosure()
	closureCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	finishErr := group.finishClosure(closureCtx)
	containmentErr = errors.Join(containmentErr, finishErr)
	cancel()
	var waitErr error
	if finishErr == nil {
		waitErr = group.reap(command)
	}
	containmentErr = errors.Join(
		containmentErr,
		group.closeObserver(),
	)
	return processClosureResult{
		WaitErr:        waitErr,
		ContainmentErr: containmentErr,
	}
}

func waitProcessCommandLeaderExit(runner *localProcessRunner) error {
	command := runner.cmd
	if command == nil || command.Process == nil {
		return errors.New("process has not started")
	}
	group, ok := unixProcessGroupFor(runner)
	if !ok {
		return errors.New("process-group ownership is missing")
	}
	return group.waitLeaderExit()
}

func closeAndReapProcessCommand(
	ctx context.Context,
	runner *localProcessRunner,
	observationErr error,
) processClosureResult {
	command := runner.cmd
	if command == nil || command.Process == nil {
		err := errors.New("process has not started")
		return processClosureResult{WaitErr: err, ContainmentErr: err}
	}
	group, ok := unixProcessGroupFor(runner)
	if !ok {
		waitErr := command.Wait()
		return processClosureResult{
			WaitErr: waitErr,
			ContainmentErr: errors.Join(
				observationErr,
				errors.New("process-group ownership is missing"),
			),
		}
	}
	if observationErr != nil {
		// The unreaped leader makes an unconditional group kill safe even after
		// observation or request cancellation fails.
		forcedErr := group.beginForcedClosure()
		if forcedErr != nil {
			// Bound reaping by also killing the direct process handle.
			forcedErr = errors.Join(forcedErr, command.Process.Kill())
		}
		closureCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			10*time.Second,
		)
		containmentErr := errors.Join(
			fmt.Errorf("observe process leader exit: %w", observationErr),
			forcedErr,
		)
		finishErr := group.finishClosure(closureCtx)
		containmentErr = errors.Join(containmentErr, finishErr)
		cancel()
		var waitErr error
		if finishErr == nil {
			waitErr = group.reap(command)
		}
		return processClosureResult{
			WaitErr: waitErr,
			ContainmentErr: errors.Join(
				containmentErr,
				group.closeObserver(),
			),
		}
	}
	containmentErr := group.close(ctx, terminateGracePeriod)
	var waitErr error
	if containmentErr == nil {
		waitErr = group.reap(command)
	}
	return processClosureResult{
		WaitErr: waitErr,
		ContainmentErr: errors.Join(
			containmentErr,
			group.closeObserver(),
		),
	}
}

func requestTerminateProcessCommand(
	ctx context.Context,
	runner *localProcessRunner,
) error {
	command := runner.cmd
	if command == nil || command.Process == nil {
		return errors.New("process has not started")
	}
	if group, ok := unixProcessGroupFor(runner); ok {
		return group.close(ctx, terminateGracePeriod)
	}
	return errors.Join(
		errors.New("process-group ownership is missing"),
		command.Process.Kill(),
	)
}

func closeProcessCommandContainment(
	ctx context.Context,
	runner *localProcessRunner,
) error {
	group, ok := unixProcessGroupFor(runner)
	if !ok {
		return errors.New("process-group ownership is missing")
	}
	return group.close(ctx, terminateGracePeriod)
}

func processCommandContainmentID(runner *localProcessRunner) int {
	if group, ok := unixProcessGroupFor(runner); ok {
		return group.pgid
	}
	command := runner.cmd
	if command == nil || command.Process == nil {
		return 0
	}
	return command.Process.Pid
}

func unixProcessGroupFor(
	runner *localProcessRunner,
) (*unixProcessGroup, bool) {
	group := runner.containment.Load()
	if group == nil {
		return nil, false
	}
	return group, true
}

func (g *unixProcessGroup) signal(signal syscall.Signal) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return errors.New("process containment is closed")
	}
	if !g.signalSafe {
		return errors.New(
			"process containment identity is no longer safe to signal",
		)
	}
	if err := g.sendSignal(signal); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			g.closed = true
			return errors.New("process containment is already empty")
		}
		return fmt.Errorf("signal process group %d: %w", g.pgid, err)
	}
	return nil
}

func (g *unixProcessGroup) close(
	ctx context.Context,
	grace time.Duration,
) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	if !g.signalSafe {
		return errors.New(
			"process containment identity is no longer safe to signal",
		)
	}
	probeCtx, cancel := context.WithTimeout(ctx, containmentQuiescenceProbeTimeout)
	probeErr := g.waitForLiveMembersGone(probeCtx)
	cancel()
	if probeErr == nil {
		g.closed = true
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !errors.Is(probeErr, context.DeadlineExceeded) {
		return probeErr
	}

	var resultErr error
	if err := g.sendSignal(syscall.SIGTERM); err != nil &&
		!errors.Is(err, syscall.ESRCH) {
		resultErr = errors.Join(
			resultErr,
			fmt.Errorf("terminate process group %d: %w", g.pgid, err),
		)
	}
	if grace > 0 {
		graceCtx, cancel := context.WithTimeout(ctx, grace)
		err := g.waitForLiveMembersGone(graceCtx)
		cancel()
		if err == nil {
			g.closed = true
			return nil
		}
		if ctx.Err() != nil {
			return errors.Join(resultErr, ctx.Err())
		}
	}
	if err := g.sendSignal(syscall.SIGKILL); err != nil &&
		!errors.Is(err, syscall.ESRCH) {
		resultErr = errors.Join(
			resultErr,
			fmt.Errorf("kill process group %d: %w", g.pgid, err),
		)
	}
	if err := g.waitForLiveMembersGone(ctx); err != nil {
		return errors.Join(resultErr, err)
	}
	g.closed = true
	return nil
}

func (g *unixProcessGroup) beginForcedClosure() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	if !g.signalSafe {
		return errors.New(
			"process containment identity is no longer safe to signal",
		)
	}
	if err := g.sendSignal(syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			g.closed = true
			return nil
		}
		return fmt.Errorf("kill process group %d: %w", g.pgid, err)
	}
	return nil
}

func (g *unixProcessGroup) finishClosure(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	if err := g.waitForLiveMembersGone(ctx); err != nil {
		return err
	}
	g.closed = true
	return nil
}

func (g *unixProcessGroup) waitLeaderExit() error {
	g.mu.Lock()
	observer := g.observer
	g.mu.Unlock()
	if observer == nil {
		return errors.New("process exit observer is missing")
	}
	return observer.Wait()
}

// Reaping is safe only after group emptiness is proved; ProcessState, not
// Cmd.Wait's error, proves that it completed.
func (g *unixProcessGroup) reap(command *exec.Cmd) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reapLocked(command)
}

func (g *unixProcessGroup) reapLocked(command *exec.Cmd) error {
	if g.reaped {
		return g.waitErr
	}
	if !g.closed {
		return errors.New(
			"cannot reap process leader before containment closure is proved",
		)
	}
	waitErr := command.Wait()
	if command.ProcessState == nil {
		return fmt.Errorf("reap closed process leader: %w", waitErr)
	}
	g.signalSafe = false
	g.reaped = true
	g.waitErr = waitErr
	return waitErr
}

func (g *unixProcessGroup) closeObserver() error {
	g.mu.Lock()
	observer := g.observer
	g.observer = nil
	g.mu.Unlock()
	if observer == nil {
		return nil
	}
	return observer.Close()
}

func (g *unixProcessGroup) sendSignal(signal syscall.Signal) error {
	if g.signalGroup != nil {
		return g.signalGroup(g.pgid, signal)
	}
	return syscall.Kill(-g.pgid, signal)
}

func (g *unixProcessGroup) waitForLiveMembersGone(ctx context.Context) error {
	if g.waitLiveMembersGone != nil {
		return g.waitLiveMembersGone(ctx, g.pgid)
	}
	return waitProcessGroupLiveMembersGone(ctx, g.pgid)
}

func waitProcessGroupLiveMembersGone(ctx context.Context, groupID int) error {
	emptyObservations := 0
	for {
		live, err := processGroupHasLiveMembers(groupID)
		if err != nil {
			return err
		}
		if live {
			emptyObservations = 0
		} else {
			emptyObservations++
			if emptyObservations >= containmentEmptyObservations {
				return nil
			}
		}
		timer := time.NewTimer(containmentPollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func closeRecordedProcessContainment(
	_ context.Context,
	containmentID int,
	kind string,
) error {
	if kind != processContainmentKind {
		return fmt.Errorf("unsupported Unix containment kind %q", kind)
	}
	if containmentID <= 0 {
		return errors.New("recorded process group id is invalid")
	}
	empty, err := processGroupEmpty(containmentID)
	if err != nil {
		return err
	}
	if empty {
		return nil
	}
	return fmt.Errorf(
		"recorded Unix process group %d may still be active; its numeric identity is no longer safe to signal",
		containmentID,
	)
}

func processGroupEmpty(groupID int) (bool, error) {
	err := syscall.Kill(-groupID, 0)
	switch {
	case errors.Is(err, syscall.ESRCH):
		return true, nil
	case err == nil, errors.Is(err, syscall.EPERM):
		return false, nil
	default:
		return false, fmt.Errorf("inspect process group %d: %w", groupID, err)
	}
}

func exitSignalFromError(err error) string {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return ""
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return ""
	}
	return status.Signal().String()
}
