package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/harness/kernel"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type renewalDeadlineStore struct {
	leaseExpiresAt  time.Time
	deadlines       chan time.Time
	initialFailures int32
	calls           atomic.Int32
}

type retainedRuntimeStore struct {
	claim                          executionstore.ClaimedAgentWork
	released                       chan storage.ID
	renewed                        chan struct{}
	firstLocalLeaseBudgetStartedAt time.Time
	renewalCalls                   atomic.Int32
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
	orgID := storage.ID{9}
	projectID := storage.ID{1}
	agentID := storage.ID{2}
	sourceContextID := storage.ID{3}
	sourceOutputID := storage.ID{4}
	turnID := storage.ID{5}
	firstInputID := storage.ID{6}
	secondInputID := storage.ID{7}
	runtimeLockID := storage.ID{8}
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
				InputIDs:                 []storage.ID{firstInputID, secondInputID},
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
	return s.claim, true, nil
}

func (*retainedRuntimeStore) DeleteAgentWakeupIfNoWork(
	context.Context,
	storage.ID,
	storage.ID,
) error {
	return nil
}

func (s *retainedRuntimeStore) ReleaseAgentRuntimeLock(
	_ context.Context,
	_, _, runtimeID storage.ID,
) error {
	s.released <- runtimeID
	return nil
}

func (s *retainedRuntimeStore) RenewAgentRuntimeLock(
	context.Context,
	storage.ID,
	storage.ID,
	storage.ID,
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
	projectID := storage.ID{1}
	agentID := storage.ID{2}
	runtimeID := storage.ID{3}
	store := &retainedRuntimeStore{
		claim: executionstore.ClaimedAgentWork{
			ProjectID:   projectID,
			AgentID:     agentID,
			Kind:        executionstore.AgentWorkTool,
			RuntimeLock: executionstore.AgentRuntimeLockRecord{ID: runtimeID},
			Tool: executionstore.ClaimedToolWork{
				TurnID:             storage.ID{4},
				ModelCallContextID: storage.ID{5},
				ModelOutputID:      storage.ID{6},
				SourceEventID:      storage.ID{7},
			},
		},
		released: make(chan storage.ID, 1),
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
	projectID := storage.ID{1}
	agentID := storage.ID{2}
	runtimeID := storage.ID{3}
	store := &retainedRuntimeStore{
		claim: executionstore.ClaimedAgentWork{
			ProjectID:   projectID,
			AgentID:     agentID,
			Kind:        executionstore.AgentWorkTool,
			RuntimeLock: executionstore.AgentRuntimeLockRecord{ID: runtimeID},
			Tool: executionstore.ClaimedToolWork{
				TurnID:             storage.ID{4},
				ModelCallContextID: storage.ID{5},
				ModelOutputID:      storage.ID{6},
				SourceEventID:      storage.ID{7},
			},
		},
		released: make(chan storage.ID, 1),
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
	projectID := storage.ID{1}
	agentID := storage.ID{2}
	runtimeID := storage.ID{3}
	store := &retainedRuntimeStore{
		claim: executionstore.ClaimedAgentWork{
			ProjectID:   projectID,
			AgentID:     agentID,
			Kind:        executionstore.AgentWorkTool,
			RuntimeLock: executionstore.AgentRuntimeLockRecord{ID: runtimeID},
			Tool: executionstore.ClaimedToolWork{
				TurnID:             storage.ID{4},
				ModelCallContextID: storage.ID{5},
				ModelOutputID:      storage.ID{6},
				SourceEventID:      storage.ID{7},
			},
		},
		released:                       make(chan storage.ID, 1),
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
			runtimeID := storage.ID{3}
			store := &retainedRuntimeStore{
				claim: executionstore.ClaimedAgentWork{
					ProjectID:   storage.ID{1},
					AgentID:     storage.ID{2},
					Kind:        executionstore.AgentWorkModel,
					RuntimeLock: executionstore.AgentRuntimeLockRecord{ID: runtimeID},
					Model: executionstore.ClaimedModelWork{
						TurnID:               storage.ID{4},
						InputIDs:             []storage.ID{{5}},
						OpeningEventSequence: 1,
					},
				},
				released: make(chan storage.ID, 2),
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
				worker.runLoop(ctx)
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

func TestWorkerReturnsUnavailableModelGrantError(t *testing.T) {
	runtimeID := storage.ID{3}
	store := &retainedRuntimeStore{
		claim: executionstore.ClaimedAgentWork{
			ProjectID:   storage.ID{1},
			AgentID:     storage.ID{2},
			Kind:        executionstore.AgentWorkModel,
			RuntimeLock: executionstore.AgentRuntimeLockRecord{ID: runtimeID},
			Model: executionstore.ClaimedModelWork{
				Kind:                 executionstore.ModelWorkStart,
				TurnID:               storage.ID{4},
				InputIDs:             []storage.ID{{5}},
				OpeningEventSequence: 1,
			},
		},
		released: make(chan storage.ID, 1),
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
	storage.ID,
	storage.ID,
) error {
	panic("unexpected DeleteAgentWakeupIfNoWork call")
}

func (s *renewalDeadlineStore) ReleaseAgentRuntimeLock(
	context.Context,
	storage.ID,
	storage.ID,
	storage.ID,
) error {
	panic("unexpected ReleaseAgentRuntimeLock call")
}

func (s *renewalDeadlineStore) RenewAgentRuntimeLock(
	ctx context.Context,
	_, _, _ storage.ID,
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
				storage.ID{1},
				storage.ID{2},
				executionstore.AgentRuntimeLockRecord{ID: storage.ID{3}, LeaseExpiresAt: test.leaseExpiresAt},
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
		storage.ID{1},
		storage.ID{2},
		executionstore.AgentRuntimeLockRecord{ID: storage.ID{3}, LeaseExpiresAt: leaseExpiresAt},
	)
	if err != nil {
		t.Fatalf("start runtime renewal after transient failure: %v", err)
	}
	stop()
	if calls := store.calls.Load(); calls != 2 {
		t.Fatalf("renewal calls = %d, want initial failure and successful retry", calls)
	}
}
