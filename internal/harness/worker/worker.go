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
	"github.com/omnara-ai/omnara/internal/errutil"
	"github.com/omnara-ai/omnara/internal/harness/kernel"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
)

type AgentWorkExecutor interface {
	ExecuteModelWork(context.Context, kernel.ModelWorkExecution) error
	ExecuteToolWork(context.Context, kernel.ToolWorkExecution) error
}

type modelWorkAdvancer interface {
	ExecuteModelWorkAndAdvance(
		context.Context, kernel.ModelWorkExecution, kernel.ModelWorkAdvanceOptions,
	) (executionstore.OwnedAgentWorkTransition, bool, error)
}

type Store interface {
	ClaimNextAgentWork(
		context.Context,
		executionstore.ClaimNextAgentWorkInput,
	) (executionstore.ClaimedAgentWork, bool, error)
	AdvanceOwnedAgentWork(
		context.Context,
		executionstore.AdvanceOwnedAgentWorkInput,
	) (executionstore.ClaimedAgentWork, bool, error)
	ReleaseAgentRuntimeLock(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error
	RenewAgentRuntimeLock(
		context.Context,
		uuid.UUID,
		uuid.UUID,
		uuid.UUID,
		time.Duration,
	) (executionstore.AgentRuntimeLockRenewal, error)
}

type Worker struct {
	store                    Store
	executor                 AgentWorkExecutor
	log                      *slog.Logger
	workerProcessID          uuid.UUID
	controlSubscriber        notifications.WorkerControlSubscriber
	runtimeLockLeaseDuration time.Duration
	capacity                 int
	claimConcurrency         int
	executionFailures        atomic.Int64
	asyncToolLimiter         *tools.AsyncExecutionLimiter
	activeMu                 sync.Mutex
	active                   map[uuid.UUID]*activeRuntime
	retained                 sync.WaitGroup
	continuation             ContinuationOptions
}

type Options struct {
	Log                      *slog.Logger
	RuntimeLockLeaseDuration time.Duration
	Capacity                 int
	ClaimConcurrency         int
	AsyncToolCapacity        int
	ControlSubscriber        notifications.WorkerControlSubscriber
	Continuation             *ContinuationOptions
}

type activeRuntime struct {
	agentID         uuid.UUID
	cancel          context.CancelFunc
	cancelRequested atomic.Bool
	renewalFailed   atomic.Bool
}

const (
	defaultAsyncToolCapacity = 32
	defaultClaimConcurrency  = 4
	minIdleClaimDelay        = 50 * time.Millisecond
	maxIdleClaimDelay        = time.Second
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
	if opts.ClaimConcurrency <= 0 {
		opts.ClaimConcurrency = defaultClaimConcurrency
	}
	if opts.AsyncToolCapacity <= 0 {
		opts.AsyncToolCapacity = defaultAsyncToolCapacity
	}
	return &Worker{
		continuation:             continuationOptions(opts.Continuation),
		store:                    store,
		executor:                 executor,
		log:                      log,
		workerProcessID:          uuid.New(),
		controlSubscriber:        opts.ControlSubscriber,
		runtimeLockLeaseDuration: opts.RuntimeLockLeaseDuration,
		capacity:                 opts.Capacity,
		claimConcurrency:         min(opts.ClaimConcurrency, opts.Capacity),
		asyncToolLimiter:         tools.NewAsyncExecutionLimiter(opts.AsyncToolCapacity),
		active:                   make(map[uuid.UUID]*activeRuntime),
	}
}

func (w *Worker) Run(ctx context.Context) error {
	ctx = logpkg.WithLogger(ctx, w.log)
	controlSubscription, err := w.subscribeWorkerControl(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = controlSubscription.Unsubscribe() }()
	slots := make(chan struct{}, w.capacity)
	var claimers, executions sync.WaitGroup
	for range w.claimConcurrency {
		claimers.Add(1)
		go func() {
			defer claimers.Done()
			w.claimLoop(ctx, slots, &executions)
		}()
	}
	<-ctx.Done()
	claimers.Wait()
	executions.Wait()
	w.retained.Wait()
	return ctx.Err()
}

func (w *Worker) subscribeWorkerControl(ctx context.Context) (notifications.Subscription, error) {
	if w.controlSubscriber == nil {
		return nil, errors.New("worker control subscriber is required")
	}
	return w.controlSubscriber.SubscribeWorkerControl(ctx, w.workerProcessID, w.handleWorkerControl)
}

func (w *Worker) claimLoop(ctx context.Context, slots chan struct{}, executions *sync.WaitGroup) {
	idleDelay := minIdleClaimDelay
	failures := 0
	for {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		loopCtx, event := logent.WorkerLoop(ctx, w.workerProcessID)
		now := time.Now().UTC()
		claim, handled, err := w.claimNext(loopCtx)
		if err == nil && handled && claim.Kind != executionstore.AgentWorkNone {
			failures = 0
			idleDelay = minIdleClaimDelay
			executions.Add(1)
			go func() {
				defer executions.Done()
				defer func() { <-slots }()
				err := w.executeClaimedWorkRecovering(loopCtx, claim, now)
				w.finishLoop(ctx, loopCtx, event, true, err)
				w.throttleAfterExecution(ctx, err)
			}()
			continue
		}
		<-slots
		w.finishLoop(ctx, loopCtx, event, handled, err)
		if ctx.Err() != nil {
			return
		}
		var delay time.Duration
		switch {
		case isRecoverableRunOnceError(err):
			failures = 0
			idleDelay = minIdleClaimDelay
			delay = initialRunLoopRetryDelay
		case err != nil:
			delay = runLoopRetryDelay(failures)
			failures++
		case handled:
			failures = 0
			idleDelay = minIdleClaimDelay
			continue
		default:
			failures = 0
			delay = idleDelay
			idleDelay = min(idleDelay*2, maxIdleClaimDelay)
		}
		if !waitForDelay(ctx, delay) {
			return
		}
	}
}

func (w *Worker) finishLoop(ctx, loopCtx context.Context, event *logpkg.Event, worked bool, err error) {
	logent.WorkerLoopResult(loopCtx, worked, err)
	recoverable := isRecoverableRunOnceError(err)
	if recoverable {
		logent.WorkerLoopRecoverableTurnRace(loopCtx, err)
	}
	if err != nil && !recoverable && !(ctx.Err() != nil && errutil.OnlyMatches(err, context.Canceled)) {
		logpkg.Error(loopCtx, err)
	}
	event.Done(loopCtx)
}

func (w *Worker) throttleAfterExecution(ctx context.Context, err error) {
	switch {
	case err == nil || ctx.Err() != nil:
		w.executionFailures.Store(0)
	case isRecoverableRunOnceError(err):
		w.executionFailures.Store(0)
		waitForDelay(ctx, initialRunLoopRetryDelay)
	default:
		waitForDelay(ctx, runLoopRetryDelay(int(w.executionFailures.Add(1)-1)))
	}
}

func (w *Worker) RunOnce(ctx context.Context) (worked bool, err error) {
	now := time.Now().UTC()
	claim, handled, err := w.claimNext(ctx)
	if err != nil || !handled {
		return false, err
	}
	if claim.Kind == executionstore.AgentWorkNone {
		return true, nil
	}
	return true, w.executeClaimedWorkRecovering(ctx, claim, now)
}

func (w *Worker) claimNext(ctx context.Context) (claim executionstore.ClaimedAgentWork, handled bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			claim, handled, err = executionstore.ClaimedAgentWork{}, false, panicError("worker claim", recovered)
		}
	}()
	claim, handled, err = w.store.ClaimNextAgentWork(
		ctx,
		executionstore.ClaimNextAgentWorkInput{
			WorkerProcessID: w.workerProcessID,
			LeaseDuration:   w.runtimeLockLeaseDuration,
		},
	)
	if errors.Is(err, storeerr.ErrNoClaimableAgentWakeup) {
		return executionstore.ClaimedAgentWork{}, false, nil
	}
	if err != nil {
		return executionstore.ClaimedAgentWork{}, false, err
	}
	return claim, handled, nil
}

func (w *Worker) executeClaimedWorkRecovering(
	ctx context.Context,
	claim executionstore.ClaimedAgentWork,
	now time.Time,
) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError("worker iteration", recovered)
		}
	}()
	return w.executeClaimedWork(ctx, claim, now)
}

func (w *Worker) executeClaimedWork(
	ctx context.Context,
	claim executionstore.ClaimedAgentWork,
	now time.Time,
) error {
	runtime := claim.RuntimeLock
	projectID, agentID := claim.ProjectID, claim.AgentID
	logent.AgentWorkScope(ctx, claim.OrgID, claim.ProjectID, claim.AgentID)
	logent.RuntimeLock(ctx, runtime)
	leaseBudgetStartedAt := claim.LocalLeaseBudgetStartedAt
	if _, fenced := w.executor.(modelWorkAdvancer); !fenced {
		leaseBudgetStartedAt = time.Time{}
	}
	turnCtx, active, stopRenewal, err := w.startRuntimeRenewal(
		ctx,
		projectID,
		agentID,
		runtime,
		leaseBudgetStartedAt,
	)
	if err != nil {
		finalizeCtx, cancelFinalize := w.finalizeContext(ctx)
		defer cancelFinalize()
		if releaseErr := w.store.ReleaseAgentRuntimeLock(
			finalizeCtx,
			projectID,
			agentID,
			runtime.ID,
		); releaseErr != nil && !errors.Is(releaseErr, storeerr.ErrRuntimeLockInactive) {
			return releaseErr
		}
		if errors.Is(err, errRuntimeCancelRequested) {
			return nil
		}
		return err
	}
	startedAt := time.Now()
	additionalModelStarts := 0
	for {
		if claim.Kind == executionstore.AgentWorkModel && claim.Model.AdmittedInputTurn.Turn.ID != uuid.Nil {
			logent.AdmittedAgentInputTurn(ctx, claim.Model.AdmittedInputTurn)
		}
		asyncScope := tools.NewAsyncExecutionScope(w.asyncToolLimiter)
		var advance *kernel.ModelWorkAdvanceOptions
		if w.continuation.canAdvance(time.Since(startedAt)) {
			advance = &kernel.ModelWorkAdvanceOptions{
				Deadline:       startedAt.Add(w.continuation.MaxDuration),
				AllowModelWork: additionalModelStarts < w.continuation.MaxModelStarts,
			}
		}
		transition, advanced, stepErr := w.executeWorkWithAdvance(
			tools.WithAsyncExecutionScope(turnCtx, asyncScope), claim, now, advance,
		)
		err = stepErr
		asyncScope.Seal()
		if asyncScope.Started() {
			w.retainRuntimeUntilAsyncCompletion(
				ctx, projectID, agentID, runtime.ID, asyncScope, stopRenewal,
			)
			return runtimeExecutionError(err, active)
		}
		if err == nil && advanced && !transition.Continued {
			stopRenewal()
			return nil
		}
		if err == nil {
			err = turnCtx.Err()
		}
		if err != nil {
			break
		}
		if advanced {
			claim = transition.Work
		} else {
			if !w.continuation.canAdvance(time.Since(startedAt)) {
				break
			}
			var continued bool
			_, prepare := w.executor.(modelWorkAdvancer)
			claim, continued, err = w.advanceOwnedWork(turnCtx, executionstore.AdvanceOwnedAgentWorkInput{
				ProjectID: projectID, AgentID: agentID, RuntimeLockID: runtime.ID,
				AllowModelWork: additionalModelStarts < w.continuation.MaxModelStarts,
				PrepareModel:   prepare,
			})
			if err != nil {
				break
			}
			if !continued {
				stopRenewal()
				return nil
			}
		}
		err = turnCtx.Err()
		if err != nil || !w.continuation.canAdvance(time.Since(startedAt)) {
			break
		}
		if claim.Kind == executionstore.AgentWorkModel {
			additionalModelStarts++
		}
		now = time.Now().UTC()
	}
	stopRenewal()
	finalizeCtx, cancelFinalize := w.finalizeContext(ctx)
	defer cancelFinalize()
	err = runtimeExecutionError(err, active)
	if releaseErr := w.store.ReleaseAgentRuntimeLock(
		finalizeCtx,
		projectID,
		agentID,
		runtime.ID,
	); releaseErr != nil &&
		err == nil {
		err = releaseErr
	}
	return err
}

func (w *Worker) retainRuntimeUntilAsyncCompletion(
	ctx context.Context,
	projectID, agentID, runtimeLockID uuid.UUID,
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
	_, _, err = w.executeWorkWithAdvance(ctx, claim, now, nil)
	return err
}

func (w *Worker) executeWorkWithAdvance(
	ctx context.Context, claim executionstore.ClaimedAgentWork, now time.Time, advance *kernel.ModelWorkAdvanceOptions,
) (transition executionstore.OwnedAgentWorkTransition, advanced bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError("agent work", recovered)
		}
	}()
	switch claim.Kind {
	case executionstore.AgentWorkModel:
		input := kernel.ModelWorkExecution{
			Prepared:                 claim.Model.Prepared,
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
		}
		if executor, ok := w.executor.(modelWorkAdvancer); ok && advance != nil {
			return executor.ExecuteModelWorkAndAdvance(ctx, input, *advance)
		}
		return transition, false, w.executor.ExecuteModelWork(ctx, input)
	case executionstore.AgentWorkTool:
		return transition, false, w.executor.ExecuteToolWork(ctx, kernel.ToolWorkExecution{
			Prepared:           claim.Tool.Prepared,
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
		return transition, false, fmt.Errorf("unsupported agent work kind %d", claim.Kind)
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
	if message.RuntimeLockID == uuid.Nil {
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
	agentID uuid.UUID,
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
	projectID, agentID uuid.UUID,
	runtime executionstore.AgentRuntimeLockRecord,
	leaseBudgetStartedAt time.Time,
) (context.Context, *activeRuntime, func(), error) {
	turnCtx, cancelTurn := context.WithCancel(ctx)
	active, unregister := w.registerActiveRuntime(
		agentID,
		runtime,
		cancelTurn,
	)
	renewalCutoff := runtimeRenewalCutoff(leaseBudgetStartedAt, w.runtimeLockLeaseDuration)
	if leaseBudgetStartedAt.IsZero() || !time.Now().Before(renewalCutoff) {
		var err error
		renewalCutoff, err = w.renewRuntimeUntil(
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
	projectID, agentID, runtimeID uuid.UUID,
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
	projectID, agentID, runtimeID uuid.UUID,
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
	agentID, runtimeID uuid.UUID,
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
