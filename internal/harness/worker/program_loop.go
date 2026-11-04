package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// Program tool calls are tools run_command processes call through
// `omnarad call`. Workers claim them independently of agent work: they never
// hold the agent runtime lock, only a renewed per-call claim.

type ProgramToolStore interface {
	ClaimNextProgramToolCall(
		context.Context, uuid.UUID, time.Duration,
	) (executionstore.ProgramToolCallClaim, bool, error)
	RenewProgramToolCallClaim(context.Context, executionstore.ProgramToolCallRef, time.Duration) (time.Time, error)
	ReapProgramToolCalls(context.Context, int) (int, error)
}

type ProgramToolExecutor interface {
	RunProgramToolCall(context.Context, executionstore.ProgramToolCallClaim, time.Duration) error
}

const (
	defaultProgramToolCapacity = 16
	defaultProgramToolLease    = time.Minute
	programToolReapInterval    = 5 * time.Second
	programToolReapBatch       = 100
)

type programToolRunner struct {
	store    ProgramToolStore
	executor ProgramToolExecutor
	capacity int
	lease    time.Duration
}

func newProgramToolRunner(opts Options) *programToolRunner {
	if opts.ProgramToolStore == nil || opts.ProgramToolExecutor == nil {
		return nil
	}
	runner := &programToolRunner{store: opts.ProgramToolStore, executor: opts.ProgramToolExecutor,
		capacity: opts.ProgramToolCapacity, lease: opts.ProgramToolLease}
	if runner.capacity <= 0 {
		runner.capacity = defaultProgramToolCapacity
	}
	if runner.lease <= 0 {
		runner.lease = defaultProgramToolLease
	}
	return runner
}

// runProgramTools claims program tool calls until claimCtx ends, running each
// with executionCtx, and reaps calls whose process or claim is gone.
func (w *Worker) runProgramTools(claimCtx, executionCtx context.Context, calls *sync.WaitGroup) {
	runner := w.programTools
	var loops sync.WaitGroup
	loops.Add(2)
	go func() {
		defer loops.Done()
		w.programReapLoop(claimCtx, runner)
	}()
	go func() {
		defer loops.Done()
		w.programClaimLoop(claimCtx, executionCtx, runner, calls)
	}()
	loops.Wait()
}

func (w *Worker) programClaimLoop(
	claimCtx, executionCtx context.Context, runner *programToolRunner, calls *sync.WaitGroup,
) {
	slots := make(chan struct{}, runner.capacity)
	idleDelay := minIdleClaimDelay
	failures := 0
	for {
		select {
		case slots <- struct{}{}:
		case <-claimCtx.Done():
			return
		}
		claim, found, err := w.claimProgramToolCall(claimCtx, runner)
		if err == nil && found {
			failures, idleDelay = 0, minIdleClaimDelay
			calls.Add(1)
			go func() {
				defer calls.Done()
				defer func() { <-slots }()
				w.runProgramToolCall(executionCtx, runner, claim)
			}()
			continue
		}
		<-slots
		var delay time.Duration
		switch {
		case claimCtx.Err() != nil:
			return
		case err != nil:
			w.log.ErrorContext(claimCtx, "claim program tool call", "error", err)
			delay = runLoopRetryDelay(failures)
			failures++
		default:
			failures = 0
			delay = idleDelay
			idleDelay = min(idleDelay*2, maxIdleClaimDelay)
		}
		if !waitForDelay(claimCtx, delay) {
			return
		}
	}
}

func (w *Worker) claimProgramToolCall(
	ctx context.Context, runner *programToolRunner,
) (claim executionstore.ProgramToolCallClaim, found bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			claim, found, err = executionstore.ProgramToolCallClaim{}, false, panicError("program tool claim", recovered)
		}
	}()
	return runner.store.ClaimNextProgramToolCall(ctx, uuid.New(), runner.lease)
}

// runProgramToolCall runs one claimed phase and renews the claim until the
// call's async work, if any, has finished.
func (w *Worker) runProgramToolCall(
	ctx context.Context, runner *programToolRunner, claim executionstore.ProgramToolCallClaim,
) {
	defer w.recoverPanic("program tool call panicked", "tool_call_id", claim.ID)
	ref := executionstore.ProgramToolCallRef{ProjectID: claim.ProjectID, AgentID: claim.AgentID,
		ProcessID: claim.ProcessID, ToolCallID: claim.ID, ClaimID: claim.ClaimID}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopRenewal := w.startProgramRenewal(callCtx, cancel, runner, ref)
	scope := tools.NewAsyncExecutionScope(w.asyncToolLimiter)
	err := runner.executor.RunProgramToolCall(tools.WithAsyncExecutionScope(callCtx, scope), claim, runner.lease)
	scope.Seal()
	<-scope.Done()
	stopRenewal()
	for _, err := range []error{err, scope.Err()} {
		if err != nil && !programClaimGone(err) && ctx.Err() == nil {
			w.log.ErrorContext(ctx, "program tool call failed", "agent_id", claim.AgentID,
				"tool_call_id", claim.ID, "tool", claim.Name, "error", err)
		}
	}
}

// startProgramRenewal renews the claim every third of its lease and cancels
// the call once the claim is lost.
func (w *Worker) startProgramRenewal(
	ctx context.Context, cancel context.CancelFunc, runner *programToolRunner, ref executionstore.ProgramToolCallRef,
) func() {
	renewalCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer w.recoverPanic("program tool claim renewal panicked", "tool_call_id", ref.ToolCallID)
		ticker := time.NewTicker(runner.lease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-renewalCtx.Done():
				return
			case <-ticker.C:
			}
			attemptCtx, cancelAttempt := context.WithTimeout(renewalCtx, runner.lease/3)
			_, err := runner.store.RenewProgramToolCallClaim(attemptCtx, ref, runner.lease)
			cancelAttempt()
			switch {
			case err == nil, renewalCtx.Err() != nil:
			case programClaimGone(err):
				cancel()
				return
			default:
				w.log.WarnContext(renewalCtx, "renew program tool call claim", "tool_call_id", ref.ToolCallID,
					"error", err)
			}
		}
	}()
	return func() {
		stop()
		<-done
	}
}

func (w *Worker) programReapLoop(ctx context.Context, runner *programToolRunner) {
	for {
		reaped, err := w.reapProgramToolCalls(ctx, runner)
		if err != nil && ctx.Err() == nil {
			w.log.ErrorContext(ctx, "reap program tool calls", "error", err)
		}
		if err == nil && reaped >= programToolReapBatch {
			continue
		}
		if !waitForDelay(ctx, programToolReapInterval) {
			return
		}
	}
}

func (w *Worker) reapProgramToolCalls(ctx context.Context, runner *programToolRunner) (reaped int, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			reaped, err = 0, panicError("program tool reap", recovered)
		}
	}()
	return runner.store.ReapProgramToolCalls(ctx, programToolReapBatch)
}

func programClaimGone(err error) bool {
	return errors.Is(err, storeerr.ErrProgramToolCallClaimLost) || errors.Is(err, storeerr.ErrProcessTerminal) ||
		errors.Is(err, storeerr.ErrRuntimeLockInactive) || errors.Is(err, context.Canceled)
}
