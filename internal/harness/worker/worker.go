package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/harness/kernel"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type AgentWorkExecutor interface {
	ExecuteModelWork(context.Context, kernel.ModelWorkExecution) error
	ExecuteToolWork(context.Context, kernel.ToolWorkExecution) error
}

type Store interface {
	ClaimNextAgentWork(
		context.Context,
		executionstore.ClaimNextAgentWorkInput,
	) (executionstore.ClaimedAgentWork, bool, error)
	ReleaseAgentRuntimeLock(context.Context, storage.ID, storage.ID, storage.ID) error
	RenewAgentRuntimeLock(
		context.Context,
		storage.ID,
		storage.ID,
		storage.ID,
		time.Duration,
	) (executionstore.AgentRuntimeLockRenewal, error)
}

type Worker struct {
	store                    Store
	executor                 AgentWorkExecutor
	log                      *slog.Logger
	workerProcessID          storage.ID
	controlSubscriber        notifications.WorkerControlSubscriber
	runtimeLockLeaseDuration time.Duration
	capacity                 int
	asyncToolLimiter         *tools.AsyncExecutionLimiter
	activeMu                 sync.Mutex
	active                   map[storage.ID]*activeRuntime
	retained                 sync.WaitGroup
}

type Options struct {
	Log                      *slog.Logger
	RuntimeLockLeaseDuration time.Duration
	Capacity                 int
	AsyncToolCapacity        int
	ControlSubscriber        notifications.WorkerControlSubscriber
}

type activeRuntime struct {
	agentID         storage.ID
	cancel          context.CancelFunc
	cancelRequested atomic.Bool
	renewalFailed   atomic.Bool
}

const (
	defaultAsyncToolCapacity = 32
	initialRunLoopRetryDelay = 250 * time.Millisecond
	maxRunLoopRetryDelay     = 30 * time.Second
)

var errRuntimeCancelRequested = errors.New("runtime cancel requested")

func NewWorker(store Store, executor AgentWorkExecutor, opts Options) *Worker {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	if opts.RuntimeLockLeaseDuration <= 0 {
		opts.RuntimeLockLeaseDuration = executionstore.AgentRuntimeLockLeaseDuration
	}
	if opts.Capacity <= 0 {
		opts.Capacity = 4
	}
	if opts.AsyncToolCapacity <= 0 {
		opts.AsyncToolCapacity = defaultAsyncToolCapacity
	}
	return &Worker{
		store:                    store,
		executor:                 executor,
		log:                      log,
		workerProcessID:          uuid.New(),
		controlSubscriber:        opts.ControlSubscriber,
		runtimeLockLeaseDuration: opts.RuntimeLockLeaseDuration,
		capacity:                 opts.Capacity,
		asyncToolLimiter:         tools.NewAsyncExecutionLimiter(opts.AsyncToolCapacity),
		active:                   make(map[storage.ID]*activeRuntime),
	}
}

func (w *Worker) Run(ctx context.Context) error {
	ctx = logpkg.WithLogger(ctx, w.log)
	controlSubscription, err := w.subscribeWorkerControl(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = controlSubscription.Unsubscribe() }()
	var loops sync.WaitGroup
	for range w.capacity {
		loops.Add(1)
		go func() {
			defer loops.Done()
			w.runLoop(ctx)
		}()
	}
	<-ctx.Done()
	loops.Wait()
	w.retained.Wait()
	return ctx.Err()
}

func (w *Worker) subscribeWorkerControl(ctx context.Context) (notifications.Subscription, error) {
	if w.controlSubscriber == nil {
		return nil, errors.New("worker control subscriber is required")
	}
	return w.controlSubscriber.SubscribeWorkerControl(ctx, w.workerProcessID, w.handleWorkerControl)
}

func (w *Worker) runLoop(ctx context.Context) {
	idleDelay := 50 * time.Millisecond
	failures := 0
	for {
		loopCtx, event := logent.WorkerLoop(ctx, w.workerProcessID)
		worked, err := w.RunOnce(loopCtx)
		logent.WorkerLoopResult(loopCtx, worked, err)
		recoverable := isRecoverableRunOnceError(err)
		if recoverable {
			logent.WorkerLoopRecoverableTurnRace(loopCtx, err)
		}
		switch {
		case err != nil && !recoverable &&
			!(errors.Is(err, context.Canceled) && ctx.Err() != nil):
			logpkg.Error(loopCtx, err)
		}
		event.Done(loopCtx)
		if ctx.Err() != nil {
			return
		}
		if recoverable {
			failures = 0
			idleDelay = 50 * time.Millisecond
			if !waitForDelay(ctx, initialRunLoopRetryDelay) {
				return
			}
			continue
		}
		if err != nil {
			delay := runLoopRetryDelay(failures)
			failures++
			if !waitForDelay(ctx, delay) {
				return
			}
			continue
		}
		failures = 0
		if worked {
			idleDelay = 50 * time.Millisecond
			continue
		}
		if !waitForDelay(ctx, idleDelay) {
			return
		}
		if idleDelay < time.Second {
			idleDelay *= 2
			if idleDelay > time.Second {
				idleDelay = time.Second
			}
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) (worked bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError("worker iteration", recovered)
		}
	}()
	now := time.Now().UTC()
	claim, handled, err := w.store.ClaimNextAgentWork(
		ctx,
		executionstore.ClaimNextAgentWorkInput{
			WorkerProcessID: w.workerProcessID,
			LeaseDuration:   w.runtimeLockLeaseDuration,
		},
	)
	if errors.Is(err, storeerr.ErrNoClaimableAgentWakeup) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !handled {
		return false, nil
	}
	if claim.Kind == executionstore.AgentWorkNone {
		return true, nil
	}
	return true, w.executeClaimedWork(ctx, claim, now)
}

func (w *Worker) executeClaimedWork(
	ctx context.Context,
	claim executionstore.ClaimedAgentWork,
	now time.Time,
) error {
	runtime := claim.RuntimeLock
	logent.AgentWorkScope(ctx, claim.OrgID, claim.ProjectID, claim.AgentID)
	logent.RuntimeLock(ctx, runtime)
	if claim.Kind == executionstore.AgentWorkModel &&
		claim.Model.AdmittedInputTurn.Turn.ID != storage.NilID {
		logent.AdmittedAgentInputTurn(ctx, claim.Model.AdmittedInputTurn)
	}
	turnCtx, active, stopRenewal, err := w.startRuntimeRenewal(
		ctx,
		claim.ProjectID,
		claim.AgentID,
		runtime,
	)
	if err != nil {
		finalizeCtx, cancelFinalize := w.finalizeContext(ctx)
		defer cancelFinalize()
		if releaseErr := w.store.ReleaseAgentRuntimeLock(
			finalizeCtx,
			claim.ProjectID,
			claim.AgentID,
			runtime.ID,
		); releaseErr != nil && !errors.Is(releaseErr, storeerr.ErrRuntimeLockInactive) {
			return releaseErr
		}
		if errors.Is(err, errRuntimeCancelRequested) {
			return nil
		}
		return err
	}
	asyncScope := tools.NewAsyncExecutionScope(w.asyncToolLimiter)
	turnCtx = tools.WithAsyncExecutionScope(turnCtx, asyncScope)
	err = w.executeWork(turnCtx, claim, now)
	asyncScope.Seal()
	if asyncScope.Started() {
		if errors.Is(err, context.Canceled) && active.cancelRequested.Load() {
			err = nil
		} else if errors.Is(err, context.Canceled) && active.renewalFailed.Load() {
			err = storeerr.ErrRuntimeLockInactive
		}
		w.retainRuntimeUntilAsyncCompletion(
			ctx,
			claim.ProjectID,
			claim.AgentID,
			runtime.ID,
			asyncScope,
			stopRenewal,
		)
		return err
	}
	stopRenewal()
	finalizeCtx, cancelFinalize := w.finalizeContext(ctx)
	defer cancelFinalize()
	if errors.Is(err, context.Canceled) && active.cancelRequested.Load() {
		err = nil
	} else if errors.Is(err, context.Canceled) && active.renewalFailed.Load() {
		err = storeerr.ErrRuntimeLockInactive
	}
	if releaseErr := w.store.ReleaseAgentRuntimeLock(
		finalizeCtx,
		claim.ProjectID,
		claim.AgentID,
		runtime.ID,
	); releaseErr != nil &&
		err == nil {
		err = releaseErr
	}
	return err
}

func (w *Worker) retainRuntimeUntilAsyncCompletion(
	ctx context.Context,
	projectID, agentID, runtimeLockID storage.ID,
	scope *tools.AsyncExecutionScope,
	stopRenewal func(),
) {
	w.retained.Add(1)
	go func() {
		defer w.retained.Done()
		defer w.recoverPanic(
			"retained agent runtime cleanup panicked",
			"agent_id",
			agentID,
		)
		<-scope.Done()
		stopRenewal()
		finalizeCtx, cancelFinalize := w.finalizeContext(ctx)
		defer cancelFinalize()
		if err := scope.Err(); err != nil {
			logpkg.Error(finalizeCtx, err)
		}
		if err := w.store.ReleaseAgentRuntimeLock(
			finalizeCtx,
			projectID,
			agentID,
			runtimeLockID,
		); err != nil && !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
			logpkg.Error(finalizeCtx, err)
		}
	}()
}

func (w *Worker) executeWork(
	ctx context.Context,
	claim executionstore.ClaimedAgentWork,
	now time.Time,
) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError("agent work", recovered)
		}
	}()
	switch claim.Kind {
	case executionstore.AgentWorkModel:
		return w.executor.ExecuteModelWork(ctx, kernel.ModelWorkExecution{
			Kind:                     claim.Model.Kind,
			OrgID:                    claim.OrgID,
			ProjectID:                claim.ProjectID,
			AgentID:                  claim.AgentID,
			ModelCallContextID:       claim.Model.ModelCallContextID,
			SourceModelCallContextID: claim.Model.SourceModelCallContextID,
			SourceModelOutputID:      claim.Model.SourceModelOutputID,
			TurnID:                   claim.Model.TurnID,
			InputIDs:                 claim.Model.InputIDs,
			OpeningEventSequence:     claim.Model.OpeningEventSequence,
			RuntimeLockID:            claim.RuntimeLock.ID,
			Now:                      now,
		})
	case executionstore.AgentWorkTool:
		return w.executor.ExecuteToolWork(ctx, kernel.ToolWorkExecution{
			ProjectID:          claim.ProjectID,
			AgentID:            claim.AgentID,
			TurnID:             claim.Tool.TurnID,
			ModelCallContextID: claim.Tool.ModelCallContextID,
			ModelOutputID:      claim.Tool.ModelOutputID,
			SourceEventID:      claim.Tool.SourceEventID,
			RuntimeLockID:      claim.RuntimeLock.ID,
			Now:                now,
		})
	default:
		return fmt.Errorf("unsupported agent work kind %d", claim.Kind)
	}
}

func (w *Worker) handleWorkerControl(_ context.Context, message notifications.WorkerControl) {
	defer w.recoverPanic("worker control handler panicked")
	switch message.Kind {
	case notifications.WorkerControlKindCancel:
		if message.Cancel == nil {
			return
		}
		w.handleWorkerControlCancel(*message.Cancel)
	}
}

func (w *Worker) handleWorkerControlCancel(message notifications.WorkerControlCancel) {
	if message.RuntimeLockID == storage.NilID {
		return
	}
	w.activeMu.Lock()
	active, ok := w.active[message.RuntimeLockID]
	w.activeMu.Unlock()
	if ok && message.AgentID == active.agentID {
		active.cancelRequested.Store(true)
		active.cancel()
	}
}

func (w *Worker) registerActiveRuntime(
	agentID storage.ID,
	runtime executionstore.AgentRuntimeLockRecord,
	cancel context.CancelFunc,
) (*activeRuntime, func()) {
	active := &activeRuntime{agentID: agentID, cancel: cancel}
	w.activeMu.Lock()
	w.active[runtime.ID] = active
	w.activeMu.Unlock()
	return active, func() {
		w.activeMu.Lock()
		delete(w.active, runtime.ID)
		w.activeMu.Unlock()
	}
}

func (w *Worker) finalizeContext(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := w.runtimeLockLeaseDuration / 3
	if timeout < 5*time.Second {
		timeout = 5 * time.Second
	}
	if timeout > 30*time.Second {
		timeout = 30 * time.Second
	}
	return context.WithTimeout(context.WithoutCancel(parent), timeout)
}

func (w *Worker) startRuntimeRenewal(
	ctx context.Context,
	projectID, agentID storage.ID,
	runtime executionstore.AgentRuntimeLockRecord,
) (context.Context, *activeRuntime, func(), error) {
	turnCtx, cancelTurn := context.WithCancel(ctx)
	active, unregister := w.registerActiveRuntime(
		agentID,
		runtime,
		cancelTurn,
	)
	renewalCutoff, err := w.renewRuntimeUntil(
		turnCtx,
		projectID,
		agentID,
		runtime.ID,
		runtimeRenewalCutoff(time.Now(), w.runtimeLockLeaseDuration),
	)
	if err != nil {
		if errors.Is(err, errRuntimeCancelRequested) ||
			(errors.Is(err, context.Canceled) && active.cancelRequested.Load()) {
			err = errRuntimeCancelRequested
		}
		unregister()
		cancelTurn()
		return nil, nil, nil, err
	}
	renewalCtx, cancelRenewal := context.WithCancel(turnCtx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer w.recoverRuntimeRenewalPanic(
			active,
			cancelTurn,
			agentID,
			runtime.ID,
		)
		for {
			delay := runtimeRenewalWait(renewalCutoff, w.runtimeLockLeaseDuration)
			timer := time.NewTimer(delay)
			select {
			case <-renewalCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}

			cutoff, err := w.renewRuntimeUntil(
				renewalCtx,
				projectID,
				agentID,
				runtime.ID,
				renewalCutoff,
			)
			if err == nil {
				renewalCutoff = cutoff
				continue
			}

			switch {
			case errors.Is(err, errRuntimeCancelRequested):
				active.cancelRequested.Store(true)
				cancelTurn()
				return
			case errors.Is(err, storeerr.ErrRuntimeLockInactive):
				active.renewalFailed.Store(true)
				cancelTurn()
				return
			case renewalCtx.Err() != nil:
				return
			}

			active.renewalFailed.Store(true)
			cancelTurn()
			return
		}
	}()
	return turnCtx, active, func() {
		cancelRenewal()
		cancelTurn()
		<-done
		unregister()
	}, nil
}

func (w *Worker) renewRuntimeUntil(
	ctx context.Context,
	projectID, agentID, runtimeID storage.ID,
	cutoff time.Time,
) (time.Time, error) {
	for attempt := 0; ; attempt++ {
		attemptCtx, cancelAttempt := context.WithDeadline(ctx, cutoff)
		renewal, err := w.renewRuntime(attemptCtx, projectID, agentID, runtimeID)
		cancelAttempt()
		if err == nil {
			return runtimeRenewalCutoff(renewal.LocalLeaseBudgetStartedAt, w.runtimeLockLeaseDuration), nil
		}
		if errors.Is(err, errRuntimeCancelRequested) ||
			errors.Is(err, storeerr.ErrRuntimeLockInactive) ||
			ctx.Err() != nil {
			return time.Time{}, err
		}

		logent.RuntimeRenewalFailed(ctx, err)
		remaining := time.Until(cutoff)
		if remaining <= 0 {
			return time.Time{}, err
		}
		delay := runtimeRenewalRetryDelay(attempt)
		if delay > remaining {
			delay = remaining
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return time.Time{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func runtimeRenewalCutoff(grantedAt time.Time, leaseDuration time.Duration) time.Time {
	return grantedAt.Add(leaseDuration - leaseDuration/3)
}

func runtimeRenewalWait(cutoff time.Time, leaseDuration time.Duration) time.Duration {
	remaining := time.Until(cutoff)
	if remaining <= 0 {
		return 0
	}
	return runtimeRenewalDelay(leaseDuration, remaining/2)
}

func (w *Worker) renewRuntime(
	ctx context.Context,
	projectID, agentID, runtimeID storage.ID,
) (executionstore.AgentRuntimeLockRenewal, error) {
	renewal, err := w.store.RenewAgentRuntimeLock(
		ctx,
		projectID,
		agentID,
		runtimeID,
		w.runtimeLockLeaseDuration,
	)
	if err != nil {
		return executionstore.AgentRuntimeLockRenewal{}, err
	}
	if renewal.RuntimeLock.CancelRequestedAt != nil {
		return renewal, errRuntimeCancelRequested
	}
	return renewal, nil
}

func runtimeRenewalDelay(leaseDuration, maximum time.Duration) time.Duration {
	base := leaseDuration / 3
	jitter := base * 15 / 100
	lower := base - jitter
	upper := min(base+jitter, maximum)
	if upper <= lower {
		return upper
	}
	return lower + time.Duration(rand.Int64N(int64(upper-lower)+1))
}

func runtimeRenewalRetryDelay(attempt int) time.Duration {
	delay := 250 * time.Millisecond
	for range min(attempt, 5) {
		delay *= 2
	}
	if delay > 5*time.Second {
		delay = 5 * time.Second
	}
	jitter := delay * 15 / 100
	return delay - jitter + time.Duration(rand.Int64N(int64(2*jitter)+1))
}

func runLoopRetryDelay(failures int) time.Duration {
	delay := initialRunLoopRetryDelay
	for range min(failures, 7) {
		delay *= 2
	}
	if delay > maxRunLoopRetryDelay {
		delay = maxRunLoopRetryDelay
	}
	jitter := delay * 15 / 100
	return delay - jitter + time.Duration(rand.Int64N(int64(2*jitter)+1))
}

func waitForDelay(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func panicError(operation string, recovered any) error {
	return fmt.Errorf("%s panicked: %v\n%s", operation, recovered, debug.Stack())
}

func (w *Worker) recoverPanic(message string, attributes ...any) {
	recovered := recover()
	if recovered == nil {
		return
	}
	attributes = append(
		attributes,
		"error",
		recovered,
		"stack",
		string(debug.Stack()),
	)
	w.log.Error(message, attributes...)
}

func (w *Worker) recoverRuntimeRenewalPanic(
	active *activeRuntime,
	cancelTurn context.CancelFunc,
	agentID, runtimeID storage.ID,
) {
	recovered := recover()
	if recovered == nil {
		return
	}
	w.log.Error(
		"runtime lock renewal panicked",
		"agent_id",
		agentID,
		"runtime_lock_id",
		runtimeID,
		"error",
		recovered,
		"stack",
		string(debug.Stack()),
	)
	active.renewalFailed.Store(true)
	cancelTurn()
}

func isRecoverableRunOnceError(err error) bool {
	return errors.Is(err, storeerr.ErrAgentNotAdvanceable) ||
		errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) ||
		errors.Is(err, storeerr.ErrRuntimeLockInactive) ||
		errors.Is(err, storeerr.ErrStateTransitionConflict)
}
