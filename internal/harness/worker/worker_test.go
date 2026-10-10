package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/harness/kernel"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
)

type renewalDeadlineStore struct {
	leaseExpiresAt  time.Time
	deadlines       chan time.Time
	initialFailures int32
	calls           atomic.Int32
}

type retainedRuntimeStore struct {
	claim                          executionstore.ClaimedAgentWork
	released                       chan uuid.UUID
	renewed                        chan struct{}
	firstLocalLeaseBudgetStartedAt time.Time
	renewalCalls                   atomic.Int32
	claimErr                       error
	beforeClaimReturn              func()
}

type modelWorkOnlyExecutor struct{}

func (modelWorkOnlyExecutor) ExecuteToolWork(
	context.Context,
	kernel.ToolWorkExecution,
) error {
	panic("unexpected tool work")
}

type toolWorkOnlyExecutor struct{}

func (toolWorkOnlyExecutor) ExecuteModelWork(
	context.Context,
	kernel.ModelWorkExecution,
) error {
	panic("unexpected model work")
}

type fixedModelErrorExecutor struct {
	modelWorkOnlyExecutor
	err error
}

func (e fixedModelErrorExecutor) ExecuteModelWork(
	context.Context,
	kernel.ModelWorkExecution,
) error {
	return e.err
}

type recordingModelWorkExecutor struct {
	modelWorkOnlyExecutor
	got kernel.ModelWorkExecution
}

func (e *recordingModelWorkExecutor) ExecuteModelWork(
	_ context.Context,
	input kernel.ModelWorkExecution,
) error {
	e.got = input
	return nil
}

type failingThenBlockTurnExecutor struct {
	modelWorkOnlyExecutor
	calls         atomic.Int32
	firstPanic    bool
	secondStarted chan struct{}
}

func (e *failingThenBlockTurnExecutor) ExecuteModelWork(
	ctx context.Context,
	_ kernel.ModelWorkExecution,
) error {
	switch e.calls.Add(1) {
	case 1:
		if e.firstPanic {
			panic("agent-local panic")
		}
		return errors.New("agent-local failure")
	case 2:
		close(e.secondStarted)
		<-ctx.Done()
		return ctx.Err()
	default:
		return errors.New("unexpected extra execution")
	}
}

func TestExecuteWorkPreservesContinuationSourceLineage(t *testing.T) {
	now := time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC)
	orgID := uuid.UUID{9}
	projectID := uuid.UUID{1}
	agentID := uuid.UUID{2}
	sourceContextID := uuid.UUID{3}
	sourceOutputID := uuid.UUID{4}
	turnID := uuid.UUID{5}
	firstInputID := uuid.UUID{6}
	secondInputID := uuid.UUID{7}
	runtimeLockID := uuid.UUID{8}
	executor := &recordingModelWorkExecutor{}
	worker := &Worker{executor: executor}

	err := worker.executeWork(
		context.Background(),
		executionstore.ClaimedAgentWork{
			OrgID:     orgID,
			ProjectID: projectID,
			AgentID:   agentID,
			Kind:      executionstore.AgentWorkModel,
			RuntimeLock: executionstore.AgentRuntimeLockRecord{
				ID: runtimeLockID,
			},
			Model: executionstore.ClaimedModelWork{
				Kind:                     executionstore.ModelWorkContinue,
				SourceModelCallContextID: sourceContextID,
				SourceModelOutputID:      sourceOutputID,
				TurnID:                   turnID,
				InputIDs:                 []uuid.UUID{firstInputID, secondInputID},
				OpeningEventSequence:     42,
			},
		},
		now,
	)
	if err != nil {
		t.Fatalf("execute continuation work: %v", err)
	}
	got := executor.got
	if got.Kind != executionstore.ModelWorkContinue ||
		got.OrgID != orgID ||
		got.ProjectID != projectID ||
		got.AgentID != agentID ||
		got.SourceModelCallContextID != sourceContextID ||
		got.SourceModelOutputID != sourceOutputID ||
		got.TurnID != turnID ||
		len(got.InputIDs) != 2 ||
		got.InputIDs[0] != firstInputID ||
		got.InputIDs[1] != secondInputID ||
		got.OpeningEventSequence != 42 ||
		got.RuntimeLockID != runtimeLockID ||
		!got.Now.Equal(now) {
		t.Fatalf("continued model work lineage = %+v", got)
	}
}

func (s *retainedRuntimeStore) ClaimNextAgentWork(
	context.Context,
	executionstore.ClaimNextAgentWorkInput,
) (executionstore.ClaimedAgentWork, bool, error) {
	if s.beforeClaimReturn != nil {
		s.beforeClaimReturn()
	}
	return s.claim, true, s.claimErr
}

func (*retainedRuntimeStore) DeleteAgentWakeupIfNoWork(
	context.Context,
	uuid.UUID,
	uuid.UUID,
) error {
	return nil
}

func (s *retainedRuntimeStore) ReleaseAgentRuntimeLock(
	_ context.Context,
	_, _, runtimeID uuid.UUID,
) error {
	s.released <- runtimeID
	return nil
}

func (s *retainedRuntimeStore) RenewAgentRuntimeLock(
	context.Context,
	uuid.UUID,
	uuid.UUID,
	uuid.UUID,
	time.Duration,
) (executionstore.AgentRuntimeLockRenewal, error) {
	call := s.renewalCalls.Add(1)
	if call == 2 && s.renewed != nil {
		close(s.renewed)
	}
	localLeaseBudgetStartedAt := time.Now()
	if call == 1 && !s.firstLocalLeaseBudgetStartedAt.IsZero() {
		localLeaseBudgetStartedAt = s.firstLocalLeaseBudgetStartedAt
	}
	return executionstore.AgentRuntimeLockRenewal{
		RuntimeLock:               s.claim.RuntimeLock,
		LocalLeaseBudgetStartedAt: localLeaseBudgetStartedAt,
	}, nil
}

type erroredAsyncExecutor struct {
	toolWorkOnlyExecutor
	started chan struct{}
	release chan struct{}
	err     error
}

func (e *erroredAsyncExecutor) ExecuteToolWork(ctx context.Context, _ kernel.ToolWorkExecution) error {
	reservation, err := tools.ReserveAsyncExecution(ctx)
	if err != nil {
		return err
	}
	reservation.Start()
	go func() {
		close(e.started)
		select {
		case <-e.release:
			reservation.Done(nil)
		case <-ctx.Done():
			reservation.Done(ctx.Err())
		}
	}()
	return e.err
}

type retainedAsyncExecutor struct {
	toolWorkOnlyExecutor
	started       chan struct{}
	firstDone     chan struct{}
	releaseFirst  chan struct{}
	releaseSecond chan struct{}
}

func (e *retainedAsyncExecutor) ExecuteToolWork(ctx context.Context, _ kernel.ToolWorkExecution) error {
	reservations := make([]*tools.AsyncExecutionReservation, 0, 2)
	for range 2 {
		reservation, err := tools.ReserveAsyncExecution(ctx)
		if err != nil {
			for _, reserved := range reservations {
				reserved.Done(err)
			}
			return err
		}
		reservation.Start()
		reservations = append(reservations, reservation)
	}
	gates := []chan struct{}{e.releaseFirst, e.releaseSecond}
	for i, reservation := range reservations {
		go func() {
			select {
			case <-gates[i]:
				reservation.Done(nil)
				if i == 0 {
					close(e.firstDone)
				}
			case <-ctx.Done():
				reservation.Done(ctx.Err())
			}
		}()
	}
	go func() {
		close(e.started)
	}()
	return nil
}

func TestWorkerRetainsRuntimeWithoutConsumingTurnExecution(t *testing.T) {
	projectID := uuid.UUID{1}
	agentID := uuid.UUID{2}
	runtimeID := uuid.UUID{3}
	store := &retainedRuntimeStore{
		claim: executionstore.ClaimedAgentWork{
			ProjectID:   projectID,
			AgentID:     agentID,
			Kind:        executionstore.AgentWorkTool,
			RuntimeLock: executionstore.AgentRuntimeLockRecord{ID: runtimeID},
			Tool: executionstore.ClaimedToolWork{
				TurnID:             uuid.UUID{4},
				ModelCallContextID: uuid.UUID{5},
				ModelOutputID:      uuid.UUID{6},
				SourceEventID:      uuid.UUID{7},
			},
		},
		released: make(chan uuid.UUID, 1),
	}
	executor := &retainedAsyncExecutor{
		started:       make(chan struct{}),
		firstDone:     make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		releaseSecond: make(chan struct{}),
	}
	worker := NewWorker(store, executor, Options{
		RuntimeLockLeaseDuration: 15 * time.Second,
		Capacity:                 1,
		AsyncToolCapacity:        2,
	})

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run worker once: %v", err)
	}
	if !worked {
		t.Fatal("worker did not claim async work")
	}
	<-executor.started
	select {
	case released := <-store.released:
		t.Fatalf("runtime %s released before async completion", released)
	default:
	}
	worker.activeMu.Lock()
	_, active := worker.active[runtimeID]
	worker.activeMu.Unlock()
	if !active {
		t.Fatal("runtime was not retained for async completion")
	}

	close(executor.releaseFirst)
	<-executor.firstDone
	select {
	case released := <-store.released:
		t.Fatalf("runtime %s released while a sibling async call was still running", released)
	default:
	}
	close(executor.releaseSecond)
	select {
	case released := <-store.released:
		if released != runtimeID {
			t.Fatalf("released runtime = %s, want %s", released, runtimeID)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime was not released after async completion")
	}
}

type cancelableRetainedAsyncExecutor struct {
	toolWorkOnlyExecutor
	started  chan struct{}
	canceled chan struct{}
}

func (e *cancelableRetainedAsyncExecutor) ExecuteToolWork(ctx context.Context, _ kernel.ToolWorkExecution) error {
	reservation, err := tools.ReserveAsyncExecution(ctx)
	if err != nil {
		return err
	}
	reservation.Start()
	go func() {
		close(e.started)
		<-ctx.Done()
		close(e.canceled)
		reservation.Done(nil)
	}()
	return nil
}

func TestWorkerControlCancelsRetainedAsyncExecution(t *testing.T) {
	projectID := uuid.UUID{1}
	agentID := uuid.UUID{2}
	runtimeID := uuid.UUID{3}
	store := &retainedRuntimeStore{
		claim: executionstore.ClaimedAgentWork{
			ProjectID:   projectID,
			AgentID:     agentID,
			Kind:        executionstore.AgentWorkTool,
			RuntimeLock: executionstore.AgentRuntimeLockRecord{ID: runtimeID},
			Tool: executionstore.ClaimedToolWork{
				TurnID:             uuid.UUID{4},
				ModelCallContextID: uuid.UUID{5},
				ModelOutputID:      uuid.UUID{6},
				SourceEventID:      uuid.UUID{7},
			},
		},
		released: make(chan uuid.UUID, 1),
	}
	executor := &cancelableRetainedAsyncExecutor{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	worker := NewWorker(store, executor, Options{
		RuntimeLockLeaseDuration: 15 * time.Second,
		Capacity:                 1,
		AsyncToolCapacity:        1,
	})

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run worker once: %v", err)
	}
	if !worked {
		t.Fatal("worker did not claim async work")
	}
	<-executor.started
	worker.handleWorkerControlCancel(notifications.WorkerControlCancel{
		AgentID:       agentID,
		RuntimeLockID: runtimeID,
	})
	select {
	case <-executor.canceled:
	case <-time.After(time.Second):
		t.Fatal("retained async execution did not observe worker control cancellation")
	}
	select {
	case released := <-store.released:
		if released != runtimeID {
			t.Fatalf("released runtime = %s, want %s", released, runtimeID)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime was not released after canceled async execution stopped")
	}
}

func TestWorkerReturnsWhileRetainedAsyncExecutionDrainsAfterWorkError(t *testing.T) {
	projectID := uuid.UUID{1}
	agentID := uuid.UUID{2}
	runtimeID := uuid.UUID{3}
	store := &retainedRuntimeStore{
		claim: executionstore.ClaimedAgentWork{
			ProjectID:   projectID,
			AgentID:     agentID,
			Kind:        executionstore.AgentWorkTool,
			RuntimeLock: executionstore.AgentRuntimeLockRecord{ID: runtimeID},
			Tool: executionstore.ClaimedToolWork{
				TurnID:             uuid.UUID{4},
				ModelCallContextID: uuid.UUID{5},
				ModelOutputID:      uuid.UUID{6},
				SourceEventID:      uuid.UUID{7},
			},
		},
		released:                       make(chan uuid.UUID, 1),
		renewed:                        make(chan struct{}),
		firstLocalLeaseBudgetStartedAt: time.Now().Add(-executionstore.MinimumAgentRuntimeLockLeaseDuration),
	}
	turnErr := errors.New("turn failed after starting async execution")
	executor := &erroredAsyncExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
		err:     turnErr,
	}
	worker := NewWorker(store, executor, Options{
		RuntimeLockLeaseDuration: executionstore.MinimumAgentRuntimeLockLeaseDuration,
		Capacity:                 1,
		AsyncToolCapacity:        1,
	})
	type runResult struct {
		worked bool
		err    error
	}
	runDone := make(chan runResult, 1)
	go func() {
		worked, err := worker.RunOnce(context.Background())
		runDone <- runResult{worked: worked, err: err}
	}()

	select {
	case <-executor.started:
	case <-time.After(time.Second):
		close(executor.release)
		t.Fatal("async execution did not start")
	}
	select {
	case result := <-runDone:
		if !result.worked {
			t.Fatal("worker did not claim work")
		}
		if !errors.Is(result.err, turnErr) {
			t.Fatalf("run worker once error = %v, want %v", result.err, turnErr)
		}
	case <-time.After(time.Second):
		close(executor.release)
		t.Fatal("worker stayed occupied while async execution drained")
	}
	select {
	case <-store.renewed:
	case <-time.After(time.Second):
		close(executor.release)
		t.Fatal("runtime lease was not renewed while async execution drained")
	}
	close(executor.release)
	select {
	case released := <-store.released:
		if released != runtimeID {
			t.Fatalf("released runtime = %s, want %s", released, runtimeID)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime was not released after async execution drained")
	}
}

func TestWorkerLoopContinuesAfterTurnFailure(t *testing.T) {
	for _, test := range []struct {
		name       string
		firstPanic bool
	}{
		{name: "returned error"},
		{name: "panic", firstPanic: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtimeID := uuid.UUID{3}
			store := &retainedRuntimeStore{
				claim: executionstore.ClaimedAgentWork{
					ProjectID:   uuid.UUID{1},
					AgentID:     uuid.UUID{2},
					Kind:        executionstore.AgentWorkModel,
					RuntimeLock: executionstore.AgentRuntimeLockRecord{ID: runtimeID},
					Model: executionstore.ClaimedModelWork{
						TurnID:               uuid.UUID{4},
						InputIDs:             []uuid.UUID{{5}},
						OpeningEventSequence: 1,
					},
				},
				released: make(chan uuid.UUID, 2),
			}
			executor := &failingThenBlockTurnExecutor{
				firstPanic:    test.firstPanic,
				secondStarted: make(chan struct{}),
			}
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			worker := NewWorker(store, executor, Options{
				Log:                      log,
				RuntimeLockLeaseDuration: time.Minute,
				Capacity:                 1,
				AsyncToolCapacity:        1,
			})
			ctx, cancel := context.WithCancel(logpkg.WithLogger(context.Background(), log))
			loopDone := make(chan struct{})
			go func() {
				defer close(loopDone)
				runClaimLoop(ctx, worker)
			}()

			select {
			case <-executor.secondStarted:
			case <-time.After(2 * time.Second):
				cancel()
				t.Fatal("worker loop did not continue after the first failure")
			}
			cancel()
			select {
			case <-loopDone:
			case <-time.After(time.Second):
				t.Fatal("worker loop did not stop after cancellation")
			}

			for attempt := 1; attempt <= 2; attempt++ {
				select {
				case released := <-store.released:
					if released != runtimeID {
						t.Fatalf(
							"attempt %d released runtime = %s, want %s",
							attempt,
							released,
							runtimeID,
						)
					}
				default:
					t.Fatalf("attempt %d did not release its runtime", attempt)
				}
			}
		})
	}
}

func TestWorkerLoopRetainsMixedShutdownError(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(logpkg.NewJSONHandler(&logs, nil))
	ctx, cancel := context.WithCancel(logpkg.WithLogger(context.Background(), logger))
	failure := errors.New("persist worker result")
	worker := NewWorker(
		&retainedRuntimeStore{
			claimErr:          errors.Join(context.Canceled, failure),
			beforeClaimReturn: cancel,
		},
		nil,
		Options{Log: logger, Capacity: 1},
	)

	runClaimLoop(ctx, worker)

	record := logs.String()
	if !strings.Contains(record, `"level":"error"`) {
		t.Fatalf("worker loop log level is not error: %s", record)
	}
	if !strings.Contains(record, failure.Error()) {
		t.Fatalf("worker loop log does not contain genuine failure: %s", record)
	}
}

func TestWorkerReturnsUnavailableModelGrantError(t *testing.T) {
	runtimeID := uuid.UUID{3}
	store := &retainedRuntimeStore{
		claim: executionstore.ClaimedAgentWork{
			ProjectID:   uuid.UUID{1},
			AgentID:     uuid.UUID{2},
			Kind:        executionstore.AgentWorkModel,
			RuntimeLock: executionstore.AgentRuntimeLockRecord{ID: runtimeID},
			Model: executionstore.ClaimedModelWork{
				Kind:                 executionstore.ModelWorkStart,
				TurnID:               uuid.UUID{4},
				InputIDs:             []uuid.UUID{{5}},
				OpeningEventSequence: 1,
			},
		},
		released: make(chan uuid.UUID, 1),
	}
	worker := NewWorker(
		store,
		fixedModelErrorExecutor{err: storeerr.ErrModelGrantUnavailable},
		Options{
			RuntimeLockLeaseDuration: time.Minute,
			Capacity:                 1,
			AsyncToolCapacity:        1,
		},
	)
	worked, err := worker.RunOnce(context.Background())
	if !worked {
		t.Fatal("worker did not claim model work")
	}
	if !errors.Is(err, storeerr.ErrModelGrantUnavailable) {
		t.Fatalf("worker error = %v, want ErrModelGrantUnavailable", err)
	}
	select {
	case released := <-store.released:
		if released != runtimeID {
			t.Fatalf("released runtime = %s, want %s", released, runtimeID)
		}
	default:
		t.Fatal("worker did not release runtime after model grant failure")
	}
}

func TestNewWorkerUsesFixedRuntimeLockLeaseDuration(t *testing.T) {
	worker := NewWorker(nil, nil, Options{Capacity: 1})
	if worker.runtimeLockLeaseDuration != executionstore.AgentRuntimeLockLeaseDuration {
		t.Fatalf(
			"runtime-lock lease duration = %s, want %s",
			worker.runtimeLockLeaseDuration,
			executionstore.AgentRuntimeLockLeaseDuration,
		)
	}
}

func (s *renewalDeadlineStore) ClaimNextAgentWork(
	context.Context,
	executionstore.ClaimNextAgentWorkInput,
) (executionstore.ClaimedAgentWork, bool, error) {
	panic("unexpected ClaimNextAgentWork call")
}

func (s *renewalDeadlineStore) DeleteAgentWakeupIfNoWork(
	context.Context,
	uuid.UUID,
	uuid.UUID,
) error {
	panic("unexpected DeleteAgentWakeupIfNoWork call")
}

func (s *renewalDeadlineStore) ReleaseAgentRuntimeLock(
	context.Context,
	uuid.UUID,
	uuid.UUID,
	uuid.UUID,
) error {
	panic("unexpected ReleaseAgentRuntimeLock call")
}

func (s *renewalDeadlineStore) RenewAgentRuntimeLock(
	ctx context.Context,
	_, _, _ uuid.UUID,
	_ time.Duration,
) (executionstore.AgentRuntimeLockRenewal, error) {
	call := s.calls.Add(1)
	if call <= s.initialFailures {
		return executionstore.AgentRuntimeLockRenewal{}, errors.New("transient renewal failure")
	}
	if call == s.initialFailures+1 {
		return executionstore.AgentRuntimeLockRenewal{
			RuntimeLock:               executionstore.AgentRuntimeLockRecord{LeaseExpiresAt: s.leaseExpiresAt},
			LocalLeaseBudgetStartedAt: time.Now(),
		}, nil
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		panic("renewal attempt has no deadline")
	}
	s.deadlines <- deadline
	<-ctx.Done()
	return executionstore.AgentRuntimeLockRenewal{}, ctx.Err()
}

func TestRuntimeRenewalDelayUsesBoundedJitterRange(t *testing.T) {
	leaseDuration := 90 * time.Second
	base := leaseDuration / 3
	jitter := base * 15 / 100
	maximum := base

	for range 100 {
		delay := runtimeRenewalDelay(leaseDuration, maximum)
		if delay < base-jitter || delay > maximum {
			t.Fatalf("renewal delay %s outside [%s, %s]", delay, base-jitter, maximum)
		}
	}
}

func TestRuntimeRenewalRetryDelayBacksOffAndCaps(t *testing.T) {
	for _, test := range []struct {
		attempt int
		base    time.Duration
	}{
		{attempt: 0, base: 250 * time.Millisecond},
		{attempt: 3, base: 2 * time.Second},
		{attempt: 20, base: 5 * time.Second},
	} {
		jitter := test.base * 15 / 100
		for range 100 {
			delay := runtimeRenewalRetryDelay(test.attempt)
			if delay < test.base-jitter || delay > test.base+jitter {
				t.Fatalf("attempt %d retry delay %s outside [%s, %s]", test.attempt, delay, test.base-jitter, test.base+jitter)
			}
		}
	}
}

func TestRuntimeRenewalUsesLocalMonotonicBudgetAcrossClockSkew(t *testing.T) {
	leaseDuration := 300 * time.Millisecond
	for _, test := range []struct {
		name           string
		leaseExpiresAt time.Time
	}{
		{name: "database behind worker", leaseExpiresAt: time.Now().Add(-24 * time.Hour)},
		{name: "database ahead of worker", leaseExpiresAt: time.Now().Add(24 * time.Hour)},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &renewalDeadlineStore{
				leaseExpiresAt: test.leaseExpiresAt,
				deadlines:      make(chan time.Time, 1),
			}
			worker := NewWorker(store, nil, Options{RuntimeLockLeaseDuration: leaseDuration, Capacity: 1})

			startedAt := time.Now()
			_, _, stop, err := worker.startRuntimeRenewal(
				context.Background(),
				uuid.UUID{1},
				uuid.UUID{2},
				executionstore.AgentRuntimeLockRecord{ID: uuid.UUID{3}, LeaseExpiresAt: test.leaseExpiresAt},
				time.Time{},
			)
			if err != nil {
				t.Fatalf("start runtime renewal: %v", err)
			}
			startedBy := time.Now()

			select {
			case deadline := <-store.deadlines:
				budget := leaseDuration - leaseDuration/3
				if deadline.Before(startedAt.Add(budget)) || deadline.After(startedBy.Add(budget)) {
					t.Fatalf(
						"renewal cutoff %s outside local monotonic window [%s, %s]",
						deadline,
						startedAt.Add(budget),
						startedBy.Add(budget),
					)
				}
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for renewal attempt")
			}
			stop()
		})
	}
}

func TestInitialRuntimeRenewalRetriesTransientFailure(t *testing.T) {
	leaseDuration := 90 * time.Second
	leaseExpiresAt := time.Now().Add(leaseDuration)
	store := &renewalDeadlineStore{
		leaseExpiresAt:  leaseExpiresAt,
		deadlines:       make(chan time.Time, 1),
		initialFailures: 1,
	}
	worker := NewWorker(store, nil, Options{RuntimeLockLeaseDuration: leaseDuration, Capacity: 1})

	_, _, stop, err := worker.startRuntimeRenewal(
		context.Background(),
		uuid.UUID{1},
		uuid.UUID{2},
		executionstore.AgentRuntimeLockRecord{ID: uuid.UUID{3}, LeaseExpiresAt: leaseExpiresAt},
		time.Time{},
	)
	if err != nil {
		t.Fatalf("start runtime renewal after transient failure: %v", err)
	}
	stop()
	if calls := store.calls.Load(); calls != 2 {
		t.Fatalf("renewal calls = %d, want initial failure and successful retry", calls)
	}
}

func runClaimLoop(ctx context.Context, worker *Worker) {
	var executions sync.WaitGroup
	worker.claimLoop(ctx, make(chan struct{}, worker.capacity), &executions)
	executions.Wait()
}

type noopControlSubscriber struct{}

func (noopControlSubscriber) SubscribeWorkerControl(
	context.Context,
	uuid.UUID,
	func(context.Context, notifications.WorkerControl),
) (notifications.Subscription, error) {
	return noopSubscription{}, nil
}

type noopSubscription struct{}

func (noopSubscription) Unsubscribe() error { return nil }

type dispatchStore struct {
	claimDelay   time.Duration
	activeClaims atomic.Int32
	maxClaims    atomic.Int32
	claims       atomic.Int32
}

func (s *dispatchStore) ClaimNextAgentWork(
	ctx context.Context,
	_ executionstore.ClaimNextAgentWorkInput,
) (executionstore.ClaimedAgentWork, bool, error) {
	active := s.activeClaims.Add(1)
	defer s.activeClaims.Add(-1)
	raiseMax(&s.maxClaims, active)
	select {
	case <-time.After(s.claimDelay):
	case <-ctx.Done():
		return executionstore.ClaimedAgentWork{}, false, ctx.Err()
	}
	s.claims.Add(1)
	return executionstore.ClaimedAgentWork{
		ProjectID:   uuid.New(),
		AgentID:     uuid.New(),
		Kind:        executionstore.AgentWorkModel,
		RuntimeLock: executionstore.AgentRuntimeLockRecord{ID: uuid.New()},
		Model: executionstore.ClaimedModelWork{
			TurnID:               uuid.New(),
			InputIDs:             []uuid.UUID{uuid.New()},
			OpeningEventSequence: 1,
		},
	}, true, nil
}

func (*dispatchStore) ReleaseAgentRuntimeLock(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return nil
}

func (*dispatchStore) AdvanceOwnedAgentWork(
	context.Context,
	executionstore.AdvanceOwnedAgentWorkInput,
) (executionstore.ClaimedAgentWork, bool, error) {
	return executionstore.ClaimedAgentWork{}, false, nil
}

func (*dispatchStore) RenewAgentRuntimeLock(
	_ context.Context,
	_, _, runtimeID uuid.UUID,
	_ time.Duration,
) (executionstore.AgentRuntimeLockRenewal, error) {
	return executionstore.AgentRuntimeLockRenewal{
		RuntimeLock:               executionstore.AgentRuntimeLockRecord{ID: runtimeID},
		LocalLeaseBudgetStartedAt: time.Now(),
	}, nil
}

type holdingModelExecutor struct {
	modelWorkOnlyExecutor
	active    atomic.Int32
	maxActive atomic.Int32
	release   chan struct{}
	started   chan struct{}
}

func (e *holdingModelExecutor) ExecuteModelWork(ctx context.Context, _ kernel.ModelWorkExecution) error {
	raiseMax(&e.maxActive, e.active.Add(1))
	if e.started != nil {
		e.started <- struct{}{}
	}
	defer e.active.Add(-1)
	select {
	case <-e.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func raiseMax(target *atomic.Int32, value int32) {
	for {
		current := target.Load()
		if value <= current || target.CompareAndSwap(current, value) {
			return
		}
	}
}

func TestWorkerDispatcherBoundsClaimsAndNeverClaimsBeyondCapacity(t *testing.T) {
	const capacity, claimConcurrency = 6, 2
	store := &dispatchStore{claimDelay: 20 * time.Millisecond}
	executor := &holdingModelExecutor{release: make(chan struct{}), started: make(chan struct{}, capacity+1)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	worker := NewWorker(store, executor, Options{
		Log:                      log,
		RuntimeLockLeaseDuration: time.Minute,
		Capacity:                 capacity,
		ClaimConcurrency:         claimConcurrency,
		ControlSubscriber:        noopControlSubscriber{},
	})
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- worker.Run(ctx) }()

	defer cancel()
	for range capacity {
		select {
		case <-executor.started:
		case <-time.After(2 * time.Second):
			t.Fatal("dispatcher did not fill available slots")
		}
	}
	select {
	case <-executor.started:
		t.Fatal("dispatcher started work without an available slot")
	case <-time.After(100 * time.Millisecond):
	}
	if got := store.claims.Load(); got != capacity {
		t.Fatalf("claims with every slot busy = %d, want %d", got, capacity)
	}
	executor.release <- struct{}{}
	select {
	case <-executor.started:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher did not fill the released slot")
	}
	if got := store.claims.Load(); got != capacity+1 {
		cancel()
		t.Fatalf("claims after one slot freed = %d, want %d", got, capacity+1)
	}
	cancel()
	if err := <-runDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v, want context canceled", err)
	}
	if got := store.maxClaims.Load(); got > claimConcurrency {
		t.Fatalf("max concurrent claims = %d, want at most %d", got, claimConcurrency)
	}
	if got := executor.maxActive.Load(); got > capacity {
		t.Fatalf("max concurrent executions = %d, want at most %d", got, capacity)
	}
}

func (s *retainedRuntimeStore) AdvanceOwnedAgentWork(
	ctx context.Context,
	input executionstore.AdvanceOwnedAgentWorkInput,
) (executionstore.ClaimedAgentWork, bool, error) {
	err := s.ReleaseAgentRuntimeLock(ctx, input.ProjectID, input.AgentID, input.RuntimeLockID)
	return executionstore.ClaimedAgentWork{}, false, err
}

func (s *renewalDeadlineStore) AdvanceOwnedAgentWork(
	ctx context.Context,
	input executionstore.AdvanceOwnedAgentWorkInput,
) (executionstore.ClaimedAgentWork, bool, error) {
	err := s.ReleaseAgentRuntimeLock(ctx, input.ProjectID, input.AgentID, input.RuntimeLockID)
	return executionstore.ClaimedAgentWork{}, false, err
}
