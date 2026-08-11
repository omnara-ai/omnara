//go:build integration

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/harness/kernel"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

var (
	workerTestOrgID     = workerTestID("org_test")
	workerTestProjectID = workerTestID("project_test")
	workerTestUserID    = workerTestID("user_test")
)

func workerTestID(seed string) storage.ID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("omnara-worker-integration:"+seed))
}

func workerTestUserPrincipal(userID storage.ID) identitystore.PrincipalRecord {
	return identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: userID}
}

func workerTestActorParams(t *testing.T) *executionstore.ActorParams {
	t.Helper()
	params, err := executionstore.OmnaraActorParams(workerTestOrgID, workerTestUserPrincipal(workerTestUserID))
	if err != nil {
		t.Fatalf("omnara actor params: %v", err)
	}
	return params
}

func workerTestKeyWrapper(t *testing.T) secrets.KeyWrapper {
	t.Helper()
	wrapper, err := secrets.NewLocalKeyWrapper(
		"worker-test-key",
		map[string][]byte{"worker-test-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("create worker test key wrapper: %v", err)
	}
	return wrapper
}

func workerControlBus(t *testing.T) (*notifications.RedisBus, *notifications.RoutedPublisher) {
	t.Helper()
	redisClient := integrationredis.OpenClient(t)
	bus, err := notifications.NewRedisBus(redisClient, nil)
	if err != nil {
		t.Fatalf("create worker control bus: %v", err)
	}
	presence, err := notifications.NewRedisPresenceStore(redisClient)
	if err != nil {
		t.Fatalf("create presence store: %v", err)
	}
	publisher, err := notifications.NewRoutedPublisher(
		notifications.RoutedPublisherPorts{
			DaemonWakeups:     bus,
			AgentEventWakeups: bus,
			WorkerControls:    bus,
		},
		presence,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("create routed publisher: %v", err)
	}
	t.Cleanup(publisher.Close)
	return bus, publisher
}

type captureTurnExecutor struct {
	modelWorkOnlyExecutor
	started chan kernel.ModelWorkExecution
	block   bool
	done    chan error
}

type cancelOnceTurnExecutor struct {
	modelWorkOnlyExecutor
	started chan kernel.ModelWorkExecution
	calls   int
}

type completedTurnExecution struct {
	execution kernel.ModelWorkExecution
	err       error
}

type concurrentTurnExecutor struct {
	modelWorkOnlyExecutor
	started   chan kernel.ModelWorkExecution
	completed chan completedTurnExecution
}

func newCaptureTurnExecutor(block bool) *captureTurnExecutor {
	return &captureTurnExecutor{started: make(chan kernel.ModelWorkExecution, 1), block: block, done: make(chan error, 1)}
}

func newConcurrentTurnExecutor(capacity int) *concurrentTurnExecutor {
	return &concurrentTurnExecutor{
		started:   make(chan kernel.ModelWorkExecution, capacity),
		completed: make(chan completedTurnExecution, capacity),
	}
}

func (e *captureTurnExecutor) ExecuteModelWork(ctx context.Context, input kernel.ModelWorkExecution) error {
	e.started <- input
	if !e.block {
		e.done <- nil
		return nil
	}
	<-ctx.Done()
	err := ctx.Err()
	e.done <- err
	return err
}

func (e *cancelOnceTurnExecutor) ExecuteModelWork(ctx context.Context, input kernel.ModelWorkExecution) error {
	e.started <- input
	e.calls++
	if e.calls == 1 {
		return context.Canceled
	}
	<-ctx.Done()
	return ctx.Err()
}

func (e *concurrentTurnExecutor) ExecuteModelWork(ctx context.Context, input kernel.ModelWorkExecution) error {
	e.started <- input
	<-ctx.Done()
	err := ctx.Err()
	e.completed <- completedTurnExecution{execution: input, err: err}
	return err
}

func TestWorkerRunOnceAdmitsOneQueuedInputAndLeavesBacklogRunnable(t *testing.T) {
	ctx := context.Background()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	now := time.Date(2026, 5, 16, 19, 0, 0, 0, time.UTC)
	agentID, userID := createWorkerAgentWithUser(t, ctx, store, now)
	firstID := createWorkerInput(t, ctx, store, agentID, userID, "first", now.Add(time.Second))
	secondID := createWorkerInput(t, ctx, store, agentID, userID, "second", now.Add(2*time.Second))

	executor := newCaptureTurnExecutor(false)
	worker := NewWorker(store.Execution(), executor, Options{RuntimeLockLeaseDuration: 15 * time.Second, Capacity: 1})
	worked, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("worker run once: %v", err)
	}
	if !worked {
		t.Fatalf("worker should claim queued agent work")
	}
	execution := <-executor.started
	if execution.AgentID != agentID || execution.TurnID == storage.NilID {
		t.Fatalf("worker execution seed mismatch: %+v", execution)
	}
	if len(execution.InputIDs) != 1 || execution.InputIDs[0] != firstID {
		t.Fatalf("worker should admit one ordinary queued input, got %+v", execution.InputIDs)
	}
	assertAgentInputAdmitted(t, ctx, pool, agentID, firstID)
	assertAgentInputQueued(t, ctx, pool, agentID, secondID)
	assertTurnInputs(t, ctx, pool, agentID, execution.TurnID, []storage.ID{firstID})
	waitNoRuntimeLock(t, ctx, pool, agentID)
	assertWorkerWakeup(t, ctx, pool, agentID)
}

func TestWorkerRunOnceBatchesSteeringInputsAndReleasesRuntime(t *testing.T) {
	ctx := context.Background()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	now := time.Date(2026, 5, 16, 19, 30, 0, 0, time.UTC)
	agentID, userID := createWorkerAgentWithUser(t, ctx, store, now)
	firstID := createWorkerInputWithDeliveryMode(
		t,
		ctx,
		store,
		agentID,
		userID,
		"first",
		executionstore.DeliveryModeSteering,
		now.Add(time.Second),
	)
	secondID := createWorkerInputWithDeliveryMode(
		t,
		ctx,
		store,
		agentID,
		userID,
		"second",
		executionstore.DeliveryModeSteering,
		now.Add(2*time.Second),
	)

	executor := newCaptureTurnExecutor(false)
	worker := NewWorker(store.Execution(), executor, Options{RuntimeLockLeaseDuration: 15 * time.Second, Capacity: 1})
	worked, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("worker run once: %v", err)
	}
	if !worked {
		t.Fatalf("worker should claim steering agent work")
	}
	execution := <-executor.started
	if execution.AgentID != agentID || execution.TurnID == storage.NilID {
		t.Fatalf("worker execution seed mismatch: %+v", execution)
	}
	if len(execution.InputIDs) != 2 || execution.InputIDs[0] != firstID || execution.InputIDs[1] != secondID {
		t.Fatalf("worker should batch steering inputs in order, got %+v", execution.InputIDs)
	}
	assertAgentInputAdmitted(t, ctx, pool, agentID, firstID)
	assertAgentInputAdmitted(t, ctx, pool, agentID, secondID)
	assertTurnInputs(t, ctx, pool, agentID, execution.TurnID, []storage.ID{firstID, secondID})
	waitNoRuntimeLock(t, ctx, pool, agentID)
	assertWorkerWakeup(t, ctx, pool, agentID)
}

func TestWorkerRunOnceClaimsWakeupFromAnyProject(t *testing.T) {
	ctx := context.Background()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	now := time.Date(2026, 5, 16, 19, 45, 0, 0, time.UTC)
	otherProjectID := workerTestID("project_other")
	createWorkerProject(t, ctx, pool, otherProjectID, "Other Project", "idem-worker-other-project", now)
	agentID, userID := createWorkerAgentWithProject(t, ctx, store, otherProjectID, now)
	inputID := createWorkerInputWithProject(
		t,
		ctx,
		store,
		otherProjectID,
		agentID,
		userID,
		"other project input",
		now.Add(time.Second),
	)

	executor := newCaptureTurnExecutor(false)
	worker := NewWorker(store.Execution(), executor, Options{RuntimeLockLeaseDuration: 15 * time.Second, Capacity: 1})
	worked, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("worker run once: %v", err)
	}
	if !worked {
		t.Fatalf("global worker should claim queued agent work from any project")
	}
	execution := <-executor.started
	if execution.OrgID != workerTestOrgID || execution.ProjectID != otherProjectID || execution.AgentID != agentID {
		t.Fatalf("worker execution project/agent mismatch: %+v", execution)
	}
	if len(execution.InputIDs) != 1 || execution.InputIDs[0] != inputID {
		t.Fatalf("worker should admit other-project input, got %+v", execution.InputIDs)
	}
}

func TestWorkerRunOnceDoesNotDoubleClaimActiveAgent(t *testing.T) {
	ctx := context.Background()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	now := time.Date(2026, 5, 16, 19, 45, 0, 0, time.UTC)
	agentID, userID := createWorkerAgentWithUser(t, ctx, store, now)
	createWorkerInput(t, ctx, store, agentID, userID, "single active worker", now.Add(time.Second))

	firstExecutor := newCaptureTurnExecutor(true)
	firstWorker := NewWorker(store.Execution(), firstExecutor, Options{RuntimeLockLeaseDuration: 30 * time.Second, Capacity: 1})
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstDone := make(chan error, 1)
	go func() {
		worked, err := firstWorker.RunOnce(firstCtx)
		if !worked && err == nil {
			err = errors.New("first worker did not claim work")
		}
		firstDone <- err
	}()

	select {
	case <-firstExecutor.started:
	case err := <-firstDone:
		t.Fatalf("first worker returned before starting active turn: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatalf("first worker did not start active turn")
	}
	if count := runtimeLockCount(t, ctx, pool, agentID); count != 1 {
		t.Fatalf("runtime lock count while first worker is active = %d, want 1", count)
	}

	secondExecutor := newCaptureTurnExecutor(false)
	secondWorker := NewWorker(store.Execution(), secondExecutor, Options{RuntimeLockLeaseDuration: 30 * time.Second, Capacity: 1})
	worked, err := secondWorker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("second worker run once while runtime is active: %v", err)
	}
	if worked {
		t.Fatalf("second worker should not claim an agent with an active runtime lock")
	}
	select {
	case execution := <-secondExecutor.started:
		t.Fatalf("second worker executed turn while first runtime lock was active: %+v", execution)
	default:
	}
	if count := runtimeLockCount(t, ctx, pool, agentID); count != 1 {
		t.Fatalf("runtime lock count after blocked second worker = %d, want 1", count)
	}

	cancelFirst()
	select {
	case err := <-firstExecutor.done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first executor should observe cancellation, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("first executor did not stop after cancellation")
	}
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first worker should return context cancellation, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("first worker did not return after cancellation")
	}
	waitNoRuntimeLock(t, ctx, pool, agentID)
	assertWorkerWakeup(t, ctx, pool, agentID)
}

func TestWorkerCancelControlCancelsActiveTurnAndDeletesWakeup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := openWorkerIntegrationDB(t, ctx)
	bus, publisher := workerControlBus(t)
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)),
		storage.WithPostCommitPublisher(publisher),
	)
	now := time.Date(2026, 5, 16, 20, 0, 0, 0, time.UTC)
	agentID, userID := createWorkerAgentWithUser(t, ctx, store, now)
	createWorkerInput(t, ctx, store, agentID, userID, "cancel me", now.Add(time.Second))

	executor := newCaptureTurnExecutor(true)
	worker := NewWorker(store.Execution(), executor, Options{RuntimeLockLeaseDuration: 30 * time.Second, Capacity: 1, ControlSubscriber: bus})
	errs := make(chan error, 1)
	go func() {
		err := worker.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			errs <- err
			return
		}
		errs <- nil
	}()

	var execution kernel.ModelWorkExecution
	select {
	case execution = <-executor.started:
	case <-time.After(5 * time.Second):
		t.Fatalf("worker did not start active turn")
	}
	if execution.RuntimeLockID == storage.NilID {
		t.Fatalf("worker execution missing runtime lock: %+v", execution)
	}
	steeringID := createWorkerInputWithDeliveryMode(
		t,
		context.Background(),
		store,
		agentID,
		userID,
		"steer canceled run",
		"steering",
		now.Add(1500*time.Millisecond),
	)
	cancelResult, err := store.Execution().CancelAgent(
		context.Background(),
		executionstore.CancelAgentInput{
			ProjectID: workerTestProjectID,
			AgentID:   agentID,
			Actor:     workerTestActorParams(t),
		},
	)
	if err != nil {
		t.Fatalf("cancel agent: %v", err)
	}
	if string(cancelResult.Event.Kind) != "agent_input" {
		t.Fatalf("cancel should append typed control agent_input event, got %+v", cancelResult.Event)
	}
	if !cancelResult.RuntimeCancelRequested {
		t.Fatalf("cancel should request owning runtime cancellation")
	}
	if !cancelResult.Affected {
		t.Fatalf("cancel should report affected")
	}
	select {
	case err := <-executor.done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("executor should observe context cancellation, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("active turn was not canceled")
	}
	waitNoRuntimeLock(t, context.Background(), pool, agentID)
	waitNoWorkerWakeup(t, context.Background(), pool, agentID)
	assertAgentInputCanceled(t, context.Background(), pool, agentID, steeringID)
	cancel()
	if err := <-errs; err != nil {
		t.Fatalf("worker run returned error: %v", err)
	}
}

func TestWorkerCancelBeforeInitialRenewalCompletesCleanly(t *testing.T) {
	ctx := context.Background()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	now := time.Date(2026, 5, 16, 20, 1, 0, 0, time.UTC)
	agentID, userID := createWorkerAgentWithUser(t, ctx, store, now)
	createWorkerInput(t, ctx, store, agentID, userID, "cancel before initial renewal", now.Add(time.Second))

	executor := newCaptureTurnExecutor(false)
	worker := NewWorker(store.Execution(), executor, Options{RuntimeLockLeaseDuration: 30 * time.Second, Capacity: 1})
	claim, worked, err := store.Execution().ClaimNextAgentWork(
		ctx,
		executionstore.ClaimNextAgentWorkInput{
			WorkerProcessID: worker.workerProcessID,
			LeaseDuration:   worker.runtimeLockLeaseDuration,
		},
	)
	if err != nil {
		t.Fatalf("claim agent work: %v", err)
	}
	if !worked || claim.Kind != executionstore.AgentWorkModel {
		t.Fatalf("claim = %+v, worked = %t, want executable work", claim, worked)
	}

	cancelResult, err := store.Execution().CancelAgent(
		ctx,
		executionstore.CancelAgentInput{
			ProjectID: workerTestProjectID,
			AgentID:   agentID,
			Actor:     workerTestActorParams(t),
		},
	)
	if err != nil {
		t.Fatalf("cancel agent: %v", err)
	}
	if !cancelResult.RuntimeCancelRequested {
		t.Fatal("cancel should mark the claimed runtime before its initial renewal")
	}
	if !cancelResult.Affected {
		t.Fatal("cancel should report affected")
	}

	if err := worker.executeClaimedWork(ctx, claim, now.Add(2*time.Second)); err != nil {
		t.Fatalf("execute canceled claim: %v", err)
	}
	select {
	case execution := <-executor.started:
		t.Fatalf("executor started after cancellation won the initial renewal: %+v", execution)
	default:
	}
	waitNoRuntimeLock(t, ctx, pool, agentID)
	waitNoWorkerWakeup(t, ctx, pool, agentID)
}

func TestWorkerConcurrentRuntimesShareRouteAndCancelIndependently(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := openWorkerIntegrationDB(t, ctx)
	bus, publisher := workerControlBus(t)
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)),
		storage.WithPostCommitPublisher(publisher),
	)
	now := time.Date(2026, 5, 16, 20, 2, 0, 0, time.UTC)
	secondProjectID := workerTestID("project_concurrent_runtime")
	createWorkerProject(
		t,
		ctx,
		pool,
		secondProjectID,
		"Concurrent Runtime Project",
		"idem-worker-concurrent-runtime-project",
		now,
	)
	firstAgentID, firstUserID := createWorkerAgentWithUser(t, ctx, store, now)
	secondAgentID, secondUserID := createWorkerAgentWithProject(
		t,
		ctx,
		store,
		secondProjectID,
		now.Add(time.Millisecond),
	)
	createWorkerInput(t, ctx, store, firstAgentID, firstUserID, "first concurrent turn", now.Add(time.Second))
	createWorkerInputWithProject(
		t,
		ctx,
		store,
		secondProjectID,
		secondAgentID,
		secondUserID,
		"second concurrent turn",
		now.Add(2*time.Second),
	)

	executor := newConcurrentTurnExecutor(2)
	worker := NewWorker(store.Execution(), executor, Options{
		RuntimeLockLeaseDuration: 15 * time.Second,
		Capacity:                 2,
		ControlSubscriber:        bus,
	})
	runDone := make(chan error, 1)
	go func() {
		err := worker.Run(ctx)
		if errors.Is(err, context.Canceled) {
			err = nil
		}
		runDone <- err
	}()

	executions := make(map[storage.ID]kernel.ModelWorkExecution, 2)
	for len(executions) < 2 {
		select {
		case execution := <-executor.started:
			executions[execution.AgentID] = execution
		case err := <-runDone:
			t.Fatalf("worker stopped before both turns started: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatalf("worker started %d concurrent turns, want 2", len(executions))
		}
	}
	firstExecution, firstStarted := executions[firstAgentID]
	secondExecution, secondStarted := executions[secondAgentID]
	if !firstStarted || !secondStarted {
		t.Fatalf("worker executions = %+v, want agents %s and %s", executions, firstAgentID, secondAgentID)
	}
	if firstExecution.RuntimeLockID == secondExecution.RuntimeLockID {
		t.Fatalf("concurrent executions share runtime lock %s", firstExecution.RuntimeLockID)
	}
	var firstRouteID, secondRouteID storage.ID
	if err := pool.QueryRow(ctx, `
SELECT runtime_lock.worker_process_id
FROM agent_runtime_locks runtime_lock
JOIN agents agent ON agent.id = runtime_lock.agent_id
WHERE agent.project_id = $1 AND runtime_lock.agent_id = $2 AND runtime_lock.id = $3
`, workerTestProjectID, firstAgentID, firstExecution.RuntimeLockID).Scan(&firstRouteID); err != nil {
		t.Fatalf("query first runtime route: %v", err)
	}
	if err := pool.QueryRow(ctx, `
SELECT runtime_lock.worker_process_id
FROM agent_runtime_locks runtime_lock
JOIN agents agent ON agent.id = runtime_lock.agent_id
WHERE agent.project_id = $1 AND runtime_lock.agent_id = $2 AND runtime_lock.id = $3
`, secondProjectID, secondAgentID, secondExecution.RuntimeLockID).Scan(&secondRouteID); err != nil {
		t.Fatalf("query second runtime route: %v", err)
	}
	if firstRouteID != worker.workerProcessID || secondRouteID != worker.workerProcessID {
		t.Fatalf(
			"runtime routes = %s and %s, want process route %s",
			firstRouteID,
			secondRouteID,
			worker.workerProcessID,
		)
	}

	cancelResult, err := store.Execution().CancelAgent(
		context.Background(),
		executionstore.CancelAgentInput{
			ProjectID: workerTestProjectID,
			AgentID:   firstAgentID,
			Actor:     workerTestActorParams(t),
		},
	)
	if err != nil {
		t.Fatalf("cancel first concurrent agent: %v", err)
	}
	if !cancelResult.RuntimeCancelRequested {
		t.Fatal("cancel did not target the first active runtime")
	}
	select {
	case completed := <-executor.completed:
		if completed.execution.AgentID != firstAgentID || !errors.Is(completed.err, context.Canceled) {
			t.Fatalf("canceled execution = %+v, want first agent context cancellation", completed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first concurrent execution was not canceled")
	}
	waitNoRuntimeLock(t, context.Background(), pool, firstAgentID)
	renewalAt := runtimeLockRenewedAt(t, context.Background(), pool, secondProjectID, secondAgentID)
	waitRuntimeLockRenewalAfter(
		t,
		context.Background(),
		pool,
		secondProjectID,
		secondAgentID,
		renewalAt,
	)
	select {
	case completed := <-executor.completed:
		t.Fatalf("second concurrent execution stopped after first cancellation: %+v", completed)
	default:
	}

	cancel()
	select {
	case completed := <-executor.completed:
		if completed.execution.AgentID != secondAgentID || !errors.Is(completed.err, context.Canceled) {
			t.Fatalf("execution stopped during worker shutdown = %+v, want second agent", completed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second concurrent execution did not stop during worker shutdown")
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("worker run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestWorkerRunContinuesAfterUnexpectedTurnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := openWorkerIntegrationDB(t, ctx)
	bus, _ := workerControlBus(t)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	now := time.Date(2026, 5, 16, 20, 3, 0, 0, time.UTC)
	agentID, userID := createWorkerAgentWithUser(t, ctx, store, now)
	createWorkerInput(t, ctx, store, agentID, userID, "cancel unexpectedly", now.Add(time.Second))

	executor := &cancelOnceTurnExecutor{started: make(chan kernel.ModelWorkExecution, 2)}
	worker := NewWorker(store.Execution(), executor, Options{RuntimeLockLeaseDuration: 30 * time.Second, Capacity: 1, ControlSubscriber: bus})
	runDone := make(chan error, 1)
	go func() { runDone <- worker.Run(ctx) }()

	for attempt := 1; attempt <= 2; attempt++ {
		select {
		case execution := <-executor.started:
			if execution.AgentID != agentID {
				t.Fatalf("attempt %d agent = %s, want %s", attempt, execution.AgentID, agentID)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("worker did not start attempt %d", attempt)
		}
	}
	select {
	case err := <-runDone:
		t.Fatalf("worker stopped after agent-local cancellation: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-runDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("worker shutdown error = %v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
	waitNoRuntimeLock(t, context.Background(), pool, agentID)
}

func TestWorkerCancelFallsBackToRenewalWithoutControlDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	now := time.Date(2026, 5, 16, 20, 5, 0, 0, time.UTC)
	agentID, userID := createWorkerAgentWithUser(t, ctx, store, now)
	createWorkerInput(t, ctx, store, agentID, userID, "cancel by renewal", now.Add(time.Second))

	executor := newCaptureTurnExecutor(true)
	worker := NewWorker(store.Execution(), executor, Options{RuntimeLockLeaseDuration: 15 * time.Second, Capacity: 1})
	runDone := make(chan error, 1)
	go func() {
		worked, err := worker.RunOnce(ctx)
		if err == nil && !worked {
			err = errors.New("worker did not claim active turn")
		}
		runDone <- err
	}()

	var execution kernel.ModelWorkExecution
	select {
	case execution = <-executor.started:
	case <-time.After(5 * time.Second):
		t.Fatalf("worker did not start active turn")
	}
	if execution.RuntimeLockID == storage.NilID {
		t.Fatalf("worker execution missing runtime lock: %+v", execution)
	}

	cancelResult, err := store.Execution().CancelAgent(
		context.Background(),
		executionstore.CancelAgentInput{
			ProjectID: workerTestProjectID,
			AgentID:   agentID,
			Actor:     workerTestActorParams(t),
		},
	)
	if err != nil {
		t.Fatalf("cancel agent: %v", err)
	}
	if string(cancelResult.Event.Kind) != "agent_input" {
		t.Fatalf("cancel should append typed control agent_input event, got %+v", cancelResult.Event)
	}
	if !cancelResult.RuntimeCancelRequested {
		t.Fatalf("cancel should mark active runtime cancel_requested_at")
	}
	if !cancelResult.Affected {
		t.Fatalf("cancel should report affected")
	}

	select {
	case err := <-executor.done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("executor should observe renewal-driven cancellation, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("active turn was not canceled by renewal fallback")
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("worker run once returned error after renewal cancel fallback: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("worker run once did not return after renewal cancel fallback")
	}
	waitNoRuntimeLock(t, context.Background(), pool, agentID)
	waitNoWorkerWakeup(t, context.Background(), pool, agentID)
}

func TestWorkerNewInputAfterCancelStartsNewTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := openWorkerIntegrationDB(t, ctx)
	bus, publisher := workerControlBus(t)
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)),
		storage.WithPostCommitPublisher(publisher),
	)
	now := time.Date(2026, 5, 16, 20, 15, 0, 0, time.UTC)
	agentID, userID := createWorkerAgentWithUser(t, ctx, store, now)
	createWorkerInput(t, ctx, store, agentID, userID, "cancel me", now.Add(time.Second))

	blockingExecutor := newCaptureTurnExecutor(true)
	runningWorker := NewWorker(store.Execution(), blockingExecutor, Options{RuntimeLockLeaseDuration: 30 * time.Second, Capacity: 1, ControlSubscriber: bus})
	errs := make(chan error, 1)
	go func() {
		err := runningWorker.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			errs <- err
			return
		}
		errs <- nil
	}()
	firstExecution := <-blockingExecutor.started
	if firstExecution.TurnID == storage.NilID {
		t.Fatalf("first execution missing turn: %+v", firstExecution)
	}
	if _, err := store.Execution().CancelAgent(
		context.Background(),
		executionstore.CancelAgentInput{
			ProjectID: workerTestProjectID,
			AgentID:   agentID,
			Actor:     workerTestActorParams(t),
		},
	); err != nil {
		t.Fatalf("cancel agent: %v", err)
	}
	select {
	case err := <-blockingExecutor.done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("executor should observe context cancellation, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("active turn was not canceled")
	}
	waitNoRuntimeLock(t, context.Background(), pool, agentID)
	waitNoWorkerWakeup(t, context.Background(), pool, agentID)
	cancel()
	if err := <-errs; err != nil {
		t.Fatalf("worker run returned error: %v", err)
	}

	nextID := createWorkerInput(
		t,
		context.Background(),
		store,
		agentID,
		userID,
		"continue after cancel",
		now.Add(3*time.Second),
	)
	nextExecutor := newCaptureTurnExecutor(false)
	nextWorker := NewWorker(store.Execution(), nextExecutor, Options{RuntimeLockLeaseDuration: 15 * time.Second, Capacity: 1})
	worked, err := nextWorker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("worker run after cancel: %v", err)
	}
	if !worked {
		t.Fatalf("worker should claim new input after cancel")
	}
	nextExecution := <-nextExecutor.started
	if nextExecution.AgentID != agentID || nextExecution.TurnID == storage.NilID ||
		nextExecution.TurnID == firstExecution.TurnID {
		t.Fatalf("new input should open a new turn after cancel, got first=%s next=%+v", firstExecution.TurnID, nextExecution)
	}
	if len(nextExecution.InputIDs) != 1 || nextExecution.InputIDs[0] != nextID {
		t.Fatalf("new turn should admit only post-cancel input, got %+v want %s", nextExecution.InputIDs, nextID)
	}
	assertAgentInputAdmitted(t, context.Background(), pool, agentID, nextID)
	assertTurnInputs(t, context.Background(), pool, agentID, nextExecution.TurnID, []storage.ID{nextID})
}

func TestWorkerRunOnceUsesRealKernelExecutor(t *testing.T) {
	ctx := context.Background()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	now := time.Date(2026, 5, 16, 21, 0, 0, 0, time.UTC)
	agentID, userID := createWorkerAgentWithUser(t, ctx, store, now)
	inputID := createWorkerInput(t, ctx, store, agentID, userID, "use the real kernel path", now.Add(time.Second))

	modelClient := fixedModelClient{
		providerModelSlug: "worker-kernel-test",
		text:              "kernel path complete",
		usage: model.Usage{
			InputTokens:         11,
			UncachedInputTokens: 4,
			OutputTokens:        7,
			CacheReadTokens:     3,
			CacheWriteTokens:    4,
			ReasoningTokens:     2,
		},
	}
	executor := kernel.AgentExecutor{
		Store:          store,
		ContextBuilder: modelcontext.Builder{Store: modelcontext.NewStore(store.Execution(), store.Artifacts(), store.Integrations())},
		ModelResolver:  liveWorkerTestModelResolver(store, modelClient),
		Now:            func() time.Time { return now.Add(2 * time.Second) },
	}
	worker := NewWorker(store.Execution(), executor, Options{RuntimeLockLeaseDuration: 15 * time.Second, Capacity: 1})
	worked, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("worker real kernel run once: %v", err)
	}
	if !worked {
		t.Fatalf("worker should claim queued kernel work")
	}
	assertAgentInputAdmitted(t, ctx, pool, agentID, inputID)
	waitNoRuntimeLock(t, ctx, pool, agentID)
	waitNoWorkerWakeup(t, ctx, pool, agentID)
	outputID := assertAssistantEventRecorded(t, ctx, pool, agentID, "kernel path complete")
	assertCompletedModelContext(t, ctx, pool, agentID, outputID)
	assertModelOutputUsage(t, ctx, pool, agentID, outputID, 11, 4, 3, 4, 7, 2, 18)
}

func TestWorkerKernelStructuredQuestionBlocksResolvesAndContinues(t *testing.T) {
	ctx := context.Background()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	now := time.Date(2026, 5, 16, 22, 0, 0, 0, time.UTC)
	agentID, userID := createWorkerAgentWithTools(t, ctx, store, now, "ask_question")
	inputID := createWorkerInput(t, ctx, store, agentID, userID, "ask then continue", now.Add(time.Second))

	modelClient := &sequenceModelClient{providerModelSlug: "worker-kernel-test", responses: []model.Response{
		{
			ID:         "resp_worker_question_tool",
			StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
				{
					ID:   "call_question",
					Name: "ask_question",
					Input: workerQuestionInput(
						interactionform.Question{
							Prompt:  "Ship?",
							Options: []interactionform.Option{{Label: "Yes"}, {Label: "No"}},
						},
					),
				},
			}),
		},
		{
			ID:         "resp_worker_question_final",
			Content:    []model.ResponsePart{{Type: "text", Text: "question answered and continued"}},
			StopReason: model.StopReasonEndTurn,
		},
	}}
	executor := kernel.AgentExecutor{
		Store:          store,
		ContextBuilder: modelcontext.Builder{Store: modelcontext.NewStore(store.Execution(), store.Artifacts(), store.Integrations())},
		ModelResolver:  liveWorkerTestModelResolver(store, modelClient),
		Now:            func() time.Time { return now.Add(2 * time.Second) },
	}
	worker := NewWorker(store.Execution(), executor, Options{RuntimeLockLeaseDuration: 15 * time.Second, Capacity: 1})
	requireWorkerClaim(t, ctx, worker, "persist question tool output")
	assertAgentInputAdmitted(t, ctx, pool, agentID, inputID)
	assertNoOpenInteractions(t, ctx, store, agentID)
	requireWorkerClaim(t, ctx, worker, "dispatch question tool")
	interaction := assertOpenStructuredQuestion(t, ctx, store, agentID)
	waitNoRuntimeLock(t, ctx, pool, agentID)
	if _, err := store.Execution().ResolveAgentInteraction(
		ctx,
		executionstore.ResolveAgentInteractionInput{
			ProjectID: workerTestProjectID,
			AgentID:   agentID,
			ID:        interaction.ID,
			Resolution: workerQuestionResolution(
				interactionform.Answer{OptionIndices: []int{0}},
			),
			Actor: workerTestActorParams(t),
		},
	); err != nil {
		t.Fatalf("resolve structured question: %v", err)
	}
	requireWorkerClaim(t, ctx, worker, "continue after question result")
	toolCalls, err := storagetest.ListCompletedToolCallsForTurn(ctx, store, workerTestProjectID, agentID, interaction.TurnID)
	if err != nil {
		t.Fatalf("list completed tool calls: %v", err)
	}
	if len(toolCalls) != 1 || toolCalls[0].Name != "ask_question" || toolCalls[0].State != "completed" {
		t.Fatalf("resolved question should complete ask_question tool call, got %+v", toolCalls)
	}
	outputID := assertAssistantEventRecorded(t, ctx, pool, agentID, "question answered and continued")
	assertCompletedModelContext(t, ctx, pool, agentID, outputID)
	waitNoRuntimeLock(t, ctx, pool, agentID)
	waitNoWorkerWakeup(t, ctx, pool, agentID)
}

func TestWorkerKernelStructuredQuestionUsesCatalogAllowPolicy(t *testing.T) {
	ctx := context.Background()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	now := time.Date(2026, 5, 16, 23, 0, 0, 0, time.UTC)
	agentID, userID := createWorkerAgentWithCatalogDefaultTools(t, ctx, store, now, "ask_question")
	inputID := createWorkerInput(
		t,
		ctx,
		store,
		agentID,
		userID,
		"ask with catalog allow then continue",
		now.Add(time.Second),
	)

	modelClient := &sequenceModelClient{providerModelSlug: "worker-kernel-test", responses: []model.Response{
		{
			ID:         "resp_worker_catalog_allowed_question_tool",
			StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
				{
					ID:   "call_catalog_allowed_question",
					Name: "ask_question",
					Input: workerQuestionInput(
						interactionform.Question{
							Prompt:  "Ship?",
							Options: []interactionform.Option{{Label: "Yes"}, {Label: "No"}},
						},
					),
				},
			}),
		},
		{
			ID:         "resp_worker_catalog_allowed_question_final",
			Content:    []model.ResponsePart{{Type: "text", Text: "catalog-allowed question answered and continued"}},
			StopReason: model.StopReasonEndTurn,
		},
	}}
	executor := kernel.AgentExecutor{
		Store:          store,
		ContextBuilder: modelcontext.Builder{Store: modelcontext.NewStore(store.Execution(), store.Artifacts(), store.Integrations())},
		ModelResolver:  liveWorkerTestModelResolver(store, modelClient),
		Now:            func() time.Time { return now.Add(2 * time.Second) },
	}
	worker := NewWorker(store.Execution(), executor, Options{RuntimeLockLeaseDuration: 15 * time.Second, Capacity: 1})
	requireWorkerClaim(t, ctx, worker, "persist catalog-allowed question output")
	assertAgentInputAdmitted(t, ctx, pool, agentID, inputID)
	requireWorkerClaim(t, ctx, worker, "dispatch catalog-allowed question")
	question := assertOpenInteraction(t, ctx, store, agentID, "question")
	waitNoRuntimeLock(t, ctx, pool, agentID)
	if _, err := store.Execution().ResolveAgentInteraction(
		ctx,
		executionstore.ResolveAgentInteractionInput{
			ProjectID: workerTestProjectID,
			AgentID:   agentID,
			ID:        question.ID,
			Resolution: workerQuestionResolution(
				interactionform.Answer{OptionIndices: []int{0}},
			),
			Actor: workerTestActorParams(t),
		},
	); err != nil {
		t.Fatalf("answer structured question: %v", err)
	}

	requireWorkerClaim(t, ctx, worker, "continue after catalog-allowed question")
	toolCalls, err := storagetest.ListCompletedToolCallsForTurn(ctx, store, workerTestProjectID, agentID, question.TurnID)
	if err != nil {
		t.Fatalf("list completed question tool calls: %v", err)
	}
	if len(toolCalls) != 1 || toolCalls[0].Name != "ask_question" || toolCalls[0].State != "completed" {
		t.Fatalf("answered question should complete ask_question tool call, got %+v", toolCalls)
	}
	outputID := assertAssistantEventRecorded(t, ctx, pool, agentID, "catalog-allowed question answered and continued")
	assertCompletedModelContext(t, ctx, pool, agentID, outputID)
	waitNoRuntimeLock(t, ctx, pool, agentID)
	waitNoWorkerWakeup(t, ctx, pool, agentID)
}

func TestWorkerKernelPermissionApprovalUnlocksStructuredQuestionTool(t *testing.T) {
	ctx := context.Background()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	now := time.Date(2026, 5, 16, 23, 0, 0, 0, time.UTC)
	agentID, userID := createWorkerAgentWithToolPermissions(
		t,
		ctx,
		store,
		now,
		map[string]string{"ask_question": toolpermission.ModeAlwaysAsk},
	)
	inputID := createWorkerInput(t, ctx, store, agentID, userID, "ask with approval then continue", now.Add(time.Second))

	modelClient := &sequenceModelClient{providerModelSlug: "worker-kernel-test", responses: []model.Response{
		{
			ID:         "resp_worker_permission_question_tool",
			StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
				{
					ID:   "call_permission_question",
					Name: "ask_question",
					Input: workerQuestionInput(
						interactionform.Question{
							Prompt:  "Ship?",
							Options: []interactionform.Option{{Label: "Yes"}, {Label: "No"}},
						},
					),
				},
			}),
		},
		{
			ID:         "resp_worker_permission_question_final",
			Content:    []model.ResponsePart{{Type: "text", Text: "allowed question answered and continued"}},
			StopReason: model.StopReasonEndTurn,
		},
	}}
	executor := kernel.AgentExecutor{
		Store:          store,
		ContextBuilder: modelcontext.Builder{Store: modelcontext.NewStore(store.Execution(), store.Artifacts(), store.Integrations())},
		ModelResolver:  liveWorkerTestModelResolver(store, modelClient),
		Now:            func() time.Time { return now.Add(2 * time.Second) },
	}
	worker := NewWorker(store.Execution(), executor, Options{RuntimeLockLeaseDuration: 15 * time.Second, Capacity: 1})
	requireWorkerClaim(t, ctx, worker, "persist permission-gated question output")
	assertAgentInputAdmitted(t, ctx, pool, agentID, inputID)
	requireWorkerClaim(t, ctx, worker, "prepare question permission")
	approval := assertOpenInteraction(t, ctx, store, agentID, "permission")
	waitNoRuntimeLock(t, ctx, pool, agentID)
	if _, err := store.Execution().ResolveAgentInteraction(
		ctx,
		executionstore.ResolveAgentInteractionInput{
			ProjectID:  workerTestProjectID,
			AgentID:    agentID,
			ID:         approval.ID,
			Resolution: workerPermissionResolution(toolpermission.AllowOptionIndex, ""),
			Actor:      workerTestActorParams(t),
		},
	); err != nil {
		t.Fatalf("allow permission interaction: %v", err)
	}

	requireWorkerClaim(t, ctx, worker, "dispatch authorized question")
	question := assertOpenInteraction(t, ctx, store, agentID, "question")
	waitNoRuntimeLock(t, ctx, pool, agentID)
	if _, err := store.Execution().ResolveAgentInteraction(
		ctx,
		executionstore.ResolveAgentInteractionInput{
			ProjectID: workerTestProjectID,
			AgentID:   agentID,
			ID:        question.ID,
			Resolution: workerQuestionResolution(
				interactionform.Answer{OptionIndices: []int{0}},
			),
			Actor: workerTestActorParams(t),
		},
	); err != nil {
		t.Fatalf("answer structured question: %v", err)
	}

	requireWorkerClaim(t, ctx, worker, "continue after authorized question result")
	toolCalls, err := storagetest.ListCompletedToolCallsForTurn(ctx, store, workerTestProjectID, agentID, approval.TurnID)
	if err != nil {
		t.Fatalf("list completed permission tool calls: %v", err)
	}
	if len(toolCalls) != 1 || toolCalls[0].Name != "ask_question" || toolCalls[0].State != "completed" {
		t.Fatalf("allowed and answered question should complete ask_question tool call, got %+v", toolCalls)
	}
	outputID := assertAssistantEventRecorded(t, ctx, pool, agentID, "allowed question answered and continued")
	assertCompletedModelContext(t, ctx, pool, agentID, outputID)
	waitNoRuntimeLock(t, ctx, pool, agentID)
	waitNoWorkerWakeup(t, ctx, pool, agentID)
}

func TestWorkerKernelPermissionDenialCompletesToolResult(t *testing.T) {
	ctx := context.Background()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	now := time.Date(2026, 5, 16, 23, 15, 0, 0, time.UTC)
	agentID, userID := createWorkerAgentWithToolPermissions(
		t,
		ctx,
		store,
		now,
		map[string]string{"ask_question": toolpermission.ModeAlwaysAsk},
	)
	createWorkerInput(t, ctx, store, agentID, userID, "ask with permission denial", now.Add(time.Second))

	modelClient := &sequenceModelClient{providerModelSlug: "worker-kernel-test", responses: []model.Response{
		{
			ID:         "resp_worker_permission_denied_tool",
			StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
				{
					ID:   "call_permission_denied_question",
					Name: "ask_question",
					Input: workerQuestionInput(
						interactionform.Question{
							Prompt:  "Ship?",
							Options: []interactionform.Option{{Label: "Yes"}, {Label: "No"}},
						},
					),
				},
			}),
		},
		{
			ID:         "resp_worker_permission_denied_final",
			Content:    []model.ResponsePart{{Type: "text", Text: "permission denied and continued"}},
			StopReason: model.StopReasonEndTurn,
		},
	}}
	executor := kernel.AgentExecutor{
		Store:          store,
		ContextBuilder: modelcontext.Builder{Store: modelcontext.NewStore(store.Execution(), store.Artifacts(), store.Integrations())},
		ModelResolver:  liveWorkerTestModelResolver(store, modelClient),
		Now:            func() time.Time { return now.Add(2 * time.Second) },
	}
	worker := NewWorker(store.Execution(), executor, Options{RuntimeLockLeaseDuration: 15 * time.Second, Capacity: 1})
	requireWorkerClaim(t, ctx, worker, "persist permission-denied question output")
	requireWorkerClaim(t, ctx, worker, "prepare denied question permission")
	approval := assertOpenInteraction(t, ctx, store, agentID, "permission")
	waitNoRuntimeLock(t, ctx, pool, agentID)
	if _, err := store.Execution().ResolveAgentInteraction(
		ctx,
		executionstore.ResolveAgentInteractionInput{
			ProjectID: workerTestProjectID,
			AgentID:   agentID,
			ID:        approval.ID,
			Resolution: workerPermissionResolution(
				toolpermission.DenyOptionIndex,
				"not allowed",
			),
			Actor: workerTestActorParams(t),
		},
	); err != nil {
		t.Fatalf("deny permission interaction: %v", err)
	}

	requireWorkerClaim(t, ctx, worker, "continue after permission denial")
	assertNoOpenInteractions(t, ctx, store, agentID)
	assertCompletedToolCall(t, ctx, pool, agentID, "ask_question", "denied")
	assertDeniedToolResultShape(t, ctx, pool, agentID, "ask_question", "not allowed", approval.ID)
	outputID := assertAssistantEventRecorded(t, ctx, pool, agentID, "permission denied and continued")
	assertCompletedModelContext(t, ctx, pool, agentID, outputID)
	waitNoRuntimeLock(t, ctx, pool, agentID)
	waitNoWorkerWakeup(t, ctx, pool, agentID)
}

func TestWorkerKernelMachineToolWithoutBindingFailsBeforeApproval(t *testing.T) {
	ctx := context.Background()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	now := time.Date(2026, 5, 17, 0, 30, 0, 0, time.UTC)
	agentID, userID := createWorkerAgentWithTools(t, ctx, store, now, "run_command")
	createWorkerInput(t, ctx, store, agentID, userID, "run command without machine", now.Add(time.Second))

	modelClient := &sequenceModelClient{providerModelSlug: "worker-kernel-test", responses: []model.Response{
		{
			ID:         "resp_worker_no_machine_command",
			StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
				{ID: "call_no_machine_command", Name: "run_command", Input: json.RawMessage(`{"command":"echo hi"}`)},
			}),
		},
		{
			ID:         "resp_worker_no_machine_final",
			Content:    []model.ResponsePart{{Type: "text", Text: "saw missing machine"}},
			StopReason: model.StopReasonEndTurn,
		},
	}}
	executor := kernel.AgentExecutor{
		Store:          store,
		ContextBuilder: modelcontext.Builder{Store: modelcontext.NewStore(store.Execution(), store.Artifacts(), store.Integrations())},
		ModelResolver:  liveWorkerTestModelResolver(store, modelClient),
		Now:            func() time.Time { return now.Add(2 * time.Second) },
	}
	worker := NewWorker(store.Execution(), executor, Options{RuntimeLockLeaseDuration: 15 * time.Second, Capacity: 1})
	requireWorkerClaim(t, ctx, worker, "persist no-machine command output")
	requireWorkerClaim(t, ctx, worker, "fail no-machine command")
	requireWorkerClaim(t, ctx, worker, "continue after no-machine failure")
	assertNoOpenInteractions(t, ctx, store, agentID)
	assertNoMachineToolCallFailed(t, ctx, pool, agentID)
	assertAssistantEventRecorded(t, ctx, pool, agentID, "saw missing machine")
	waitNoRuntimeLock(t, ctx, pool, agentID)
	waitNoWorkerWakeup(t, ctx, pool, agentID)
}

func TestWorkerKernelDeniedMachineToolDoesNotRequireBinding(t *testing.T) {
	ctx := context.Background()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	now := time.Date(2026, 5, 17, 0, 35, 0, 0, time.UTC)
	agentID, userID := createWorkerAgentWithToolPermissions(
		t,
		ctx,
		store,
		now,
		map[string]string{"run_command": toolpermission.ModeAlwaysDeny},
	)
	createWorkerInput(t, ctx, store, agentID, userID, "try denied command without machine", now.Add(time.Second))

	modelClient := &sequenceModelClient{providerModelSlug: "worker-kernel-test", responses: []model.Response{
		{
			ID:         "resp_worker_denied_no_machine_command",
			StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
				{ID: "call_denied_no_machine_command", Name: "run_command", Input: json.RawMessage(`{"command":"echo hi"}`)},
			}),
		},
		{
			ID:         "resp_worker_denied_no_machine_final",
			Content:    []model.ResponsePart{{Type: "text", Text: "policy denied"}},
			StopReason: model.StopReasonEndTurn,
		},
	}}
	executor := kernel.AgentExecutor{
		Store:          store,
		ContextBuilder: modelcontext.Builder{Store: modelcontext.NewStore(store.Execution(), store.Artifacts(), store.Integrations())},
		ModelResolver:  liveWorkerTestModelResolver(store, modelClient),
		Now:            func() time.Time { return now.Add(2 * time.Second) },
	}
	worker := NewWorker(store.Execution(), executor, Options{RuntimeLockLeaseDuration: 15 * time.Second, Capacity: 1})
	requireWorkerClaim(t, ctx, worker, "persist denied command output")
	requireWorkerClaim(t, ctx, worker, "deny command")
	requireWorkerClaim(t, ctx, worker, "continue after command denial")
	assertNoOpenInteractions(t, ctx, store, agentID)
	assertCompletedToolCall(t, ctx, pool, agentID, "run_command", "denied")
	assertDeniedToolResultShape(
		t,
		ctx,
		pool,
		agentID,
		"run_command",
		"agent config permission mode denied this tool call",
		storage.NilID,
	)
	assertAssistantEventRecorded(t, ctx, pool, agentID, "policy denied")
	waitNoRuntimeLock(t, ctx, pool, agentID)
	waitNoWorkerWakeup(t, ctx, pool, agentID)
}

func TestWorkerKernelPermissionApprovalStartsMachineRunCommand(t *testing.T) {
	ctx := context.Background()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	now := time.Date(2026, 5, 17, 0, 40, 0, 0, time.UTC)
	machine := createWorkerExecutableMachine(t, ctx, store, now)
	agentID, userID := createWorkerAgentWithToolPermissionsAndMachine(
		t,
		ctx,
		store,
		now,
		map[string]string{"run_command": toolpermission.ModeAlwaysAsk},
		machine.DisplayName,
	)
	createWorkerInput(t, ctx, store, agentID, userID, "run approved command", now.Add(time.Second))

	modelClient := &sequenceModelClient{providerModelSlug: "worker-kernel-test", responses: []model.Response{
		{
			ID:         "resp_worker_permission_machine_command",
			StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
				{ID: "call_permission_machine_command", Name: "run_command", Input: json.RawMessage(`{"command":"echo hi"}`)},
			}),
		},
	}}
	executor := kernel.AgentExecutor{
		Store:          store,
		ContextBuilder: modelcontext.Builder{Store: modelcontext.NewStore(store.Execution(), store.Artifacts(), store.Integrations())},
		ModelResolver:  liveWorkerTestModelResolver(store, modelClient),
		Now:            func() time.Time { return now.Add(2 * time.Second) },
	}
	worker := NewWorker(store.Execution(), executor, Options{RuntimeLockLeaseDuration: 15 * time.Second, Capacity: 1})
	requireWorkerClaim(t, ctx, worker, "persist permission-gated command output")
	requireWorkerClaim(t, ctx, worker, "prepare command permission")
	approval := assertOpenInteraction(t, ctx, store, agentID, "permission")
	waitNoRuntimeLock(t, ctx, pool, agentID)
	if _, err := store.Execution().ResolveAgentInteraction(
		ctx,
		executionstore.ResolveAgentInteractionInput{
			ProjectID:  workerTestProjectID,
			AgentID:    agentID,
			ID:         approval.ID,
			Resolution: workerPermissionResolution(toolpermission.AllowOptionIndex, ""),
			Actor:      workerTestActorParams(t),
		},
	); err != nil {
		t.Fatalf("allow run_command permission interaction: %v", err)
	}

	requireWorkerClaim(t, ctx, worker, "dispatch authorized command")
	assertNoOpenInteractions(t, ctx, store, agentID)
	assertStartingProcessForTool(t, ctx, pool, agentID, "run_command")
	assertWaitingToolCall(t, ctx, pool, agentID, "run_command")
	waitNoRuntimeLock(t, ctx, pool, agentID)
	waitNoWorkerWakeup(t, ctx, pool, agentID)
}

func TestWorkerKernelStructuredInteractionFormResolvesAtomically(t *testing.T) {
	ctx := context.Background()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	now := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	agentID, userID := createWorkerAgentWithTools(t, ctx, store, now, "ask_question")
	createWorkerInput(t, ctx, store, agentID, userID, "ask two questions then continue", now.Add(time.Second))

	modelClient := &sequenceModelClient{providerModelSlug: "worker-kernel-test", responses: []model.Response{
		{
			ID:         "resp_worker_multi_question_tool",
			StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
				{
					ID:   "call_multi_question",
					Name: "ask_question",
					Input: workerQuestionInput(
						interactionform.Question{
							Prompt:  "First?",
							Options: []interactionform.Option{{Label: "One"}},
						},
						interactionform.Question{
							Prompt:  "Second?",
							Options: []interactionform.Option{{Label: "Two"}},
						},
					),
				},
			}),
		},
		{
			ID:         "resp_worker_multi_question_final",
			Content:    []model.ResponsePart{{Type: "text", Text: "both answers continued the turn"}},
			StopReason: model.StopReasonEndTurn,
		},
	}}
	executor := kernel.AgentExecutor{
		Store:          store,
		ContextBuilder: modelcontext.Builder{Store: modelcontext.NewStore(store.Execution(), store.Artifacts(), store.Integrations())},
		ModelResolver:  liveWorkerTestModelResolver(store, modelClient),
		Now:            func() time.Time { return now.Add(2 * time.Second) },
	}
	worker := NewWorker(store.Execution(), executor, Options{RuntimeLockLeaseDuration: 15 * time.Second, Capacity: 1})
	requireWorkerClaim(t, ctx, worker, "persist multi-question output")
	requireWorkerClaim(t, ctx, worker, "dispatch multi-question tool")
	question := assertOpenInteraction(t, ctx, store, agentID, "question")
	waitNoRuntimeLock(t, ctx, pool, agentID)
	if _, err := store.Execution().ResolveAgentInteraction(
		ctx,
		executionstore.ResolveAgentInteractionInput{
			ProjectID: workerTestProjectID,
			AgentID:   agentID,
			ID:        question.ID,
			Resolution: workerQuestionResolution(
				interactionform.Answer{OptionIndices: []int{0}},
				interactionform.Answer{OptionIndices: []int{0}},
			),
			Actor: workerTestActorParams(t),
		},
	); err != nil {
		t.Fatalf("answer structured interaction form: %v", err)
	}

	requireWorkerClaim(t, ctx, worker, "continue after interaction form result")
	outputID := assertAssistantEventRecorded(t, ctx, pool, agentID, "both answers continued the turn")
	assertCompletedModelContext(t, ctx, pool, agentID, outputID)
	assertStructuredQuestionResultAnswers(t, ctx, pool, agentID, "call_multi_question", []string{"One", "Two"})
	waitNoRuntimeLock(t, ctx, pool, agentID)
	waitNoWorkerWakeup(t, ctx, pool, agentID)
}

func TestWorkerKernelMultipleToolCallsWaitForAllInteractions(t *testing.T) {
	ctx := context.Background()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	now := time.Date(2026, 5, 17, 0, 30, 0, 0, time.UTC)
	agentID, userID := createWorkerAgentWithTools(t, ctx, store, now, "ask_question")
	createWorkerInput(t, ctx, store, agentID, userID, "ask two tool calls then continue", now.Add(time.Second))

	modelClient := &sequenceModelClient{providerModelSlug: "worker-kernel-test", responses: []model.Response{
		{
			ID:         "resp_worker_two_question_tools",
			StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
				{
					ID:   "call_first_question",
					Name: "ask_question",
					Input: workerQuestionInput(
						interactionform.Question{
							Prompt:  "First?",
							Options: []interactionform.Option{{Label: "One"}},
						},
					),
				},
				{
					ID:   "call_second_question",
					Name: "ask_question",
					Input: workerQuestionInput(
						interactionform.Question{
							Prompt:  "Second?",
							Options: []interactionform.Option{{Label: "Two"}},
						},
					),
				},
			}),
		},
		{
			ID:         "resp_worker_two_question_final",
			Content:    []model.ResponsePart{{Type: "text", Text: "two tool calls answered and continued"}},
			StopReason: model.StopReasonEndTurn,
		},
	}}
	executor := kernel.AgentExecutor{
		Store:          store,
		ContextBuilder: modelcontext.Builder{Store: modelcontext.NewStore(store.Execution(), store.Artifacts(), store.Integrations())},
		ModelResolver:  liveWorkerTestModelResolver(store, modelClient),
		Now:            func() time.Time { return now.Add(2 * time.Second) },
	}
	worker := NewWorker(store.Execution(), executor, Options{RuntimeLockLeaseDuration: 15 * time.Second, Capacity: 1})
	requireWorkerClaim(t, ctx, worker, "persist sibling question outputs")
	requireWorkerClaim(t, ctx, worker, "dispatch sibling question tools")
	questions := assertOpenInteractions(t, ctx, store, agentID, "question", 2)
	waitNoRuntimeLock(t, ctx, pool, agentID)
	if _, err := store.Execution().ResolveAgentInteraction(
		ctx,
		executionstore.ResolveAgentInteractionInput{
			ProjectID: workerTestProjectID,
			AgentID:   agentID,
			ID:        questions[0].ID,
			Resolution: workerQuestionResolution(
				interactionform.Answer{OptionIndices: []int{0}},
			),
			Actor: workerTestActorParams(t),
		},
	); err != nil {
		t.Fatalf("answer first tool question: %v", err)
	}

	if _, err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("check work while sibling question remains: %v", err)
	}
	if modelClient.calls != 1 {
		t.Fatalf("model calls after one tool call answer = %d, want 1", modelClient.calls)
	}
	assertOpenInteractions(t, ctx, store, agentID, "question", 1)
	if _, err := store.Execution().ResolveAgentInteraction(
		ctx,
		executionstore.ResolveAgentInteractionInput{
			ProjectID: workerTestProjectID,
			AgentID:   agentID,
			ID:        questions[1].ID,
			Resolution: workerQuestionResolution(
				interactionform.Answer{OptionIndices: []int{0}},
			),
			Actor: workerTestActorParams(t),
		},
	); err != nil {
		t.Fatalf("answer second tool question: %v", err)
	}

	requireWorkerClaim(t, ctx, worker, "continue after sibling question results")
	assertCompletedToolCallCount(t, ctx, pool, agentID, "ask_question", "completed", 2)
	outputID := assertAssistantEventRecorded(t, ctx, pool, agentID, "two tool calls answered and continued")
	assertCompletedModelContext(t, ctx, pool, agentID, outputID)
	waitNoRuntimeLock(t, ctx, pool, agentID)
	waitNoWorkerWakeup(t, ctx, pool, agentID)
}

func requireWorkerClaim(t *testing.T, ctx context.Context, worker *Worker, label string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		worked, err := worker.RunOnce(ctx)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if worked {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: worker did not claim work", label)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s: context ended waiting for work: %v", label, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type fixedModelClient struct {
	providerModelSlug string
	text              string
	usage             model.Usage
}

func (c fixedModelClient) RequestedProviderModelSlug() string { return c.providerModelSlug }

func (c fixedModelClient) APIFormat() modelprotocol.APIFormat {
	return modelprotocol.APIFormatOpenAIResponses
}

func (c fixedModelClient) ModelAPIVariant() modelprotocol.APIVariant {
	return modelprotocol.APIVariantDefault
}

func (c fixedModelClient) Capabilities() model.Capabilities { return workerTestModelCapabilities() }

func (c fixedModelClient) Prepare(_ context.Context, input model.PrepareInput) (model.PreparedRequest, error) {
	return model.PreparedRequest{
		Body: mustWorkerJSON(map[string]any{"model": c.providerModelSlug, "messages": input.Context.Messages}),
	}, nil
}

func (c fixedModelClient) Respond(_ context.Context, _ model.Request) (model.Response, error) {
	return model.Response{
		ID:         "resp_worker_kernel",
		Content:    []model.ResponsePart{{Type: "text", Text: c.text}},
		StopReason: model.StopReasonEndTurn,
		Usage:      c.usage,
	}, nil
}

type sequenceModelClient struct {
	providerModelSlug string
	responses         []model.Response
	calls             int
}

func (c *sequenceModelClient) RequestedProviderModelSlug() string { return c.providerModelSlug }

func (c *sequenceModelClient) APIFormat() modelprotocol.APIFormat {
	return modelprotocol.APIFormatOpenAIResponses
}

func (c *sequenceModelClient) ModelAPIVariant() modelprotocol.APIVariant {
	return modelprotocol.APIVariantDefault
}

func (c *sequenceModelClient) Capabilities() model.Capabilities { return workerTestModelCapabilities() }

func (c *sequenceModelClient) Prepare(_ context.Context, input model.PrepareInput) (model.PreparedRequest, error) {
	return model.PreparedRequest{
		Body: mustWorkerJSON(
			map[string]any{
				"model":        c.providerModelSlug,
				"messages":     input.Context.Messages,
				"tools":        input.Context.ToolSpecs,
				"tool_results": input.Context.ToolResults,
			},
		),
	}, nil
}

func (c *sequenceModelClient) Respond(_ context.Context, _ model.Request) (model.Response, error) {
	if c.calls >= len(c.responses) {
		return model.Response{}, errors.New("unexpected extra model call")
	}
	response := c.responses[c.calls]
	c.calls++
	return response, nil
}

func workerTestModelCapabilities() model.Capabilities {
	supportsTools := true
	return model.Capabilities{
		ContextWindowTokens: 128000,
		MaxOutputTokens:     8192,
		SupportsTools:       &supportsTools,
	}
}

func openWorkerIntegrationDB(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../../migrations")
	seedWorkerIntegrationDB(t, ctx, pool)
	return pool
}

func seedWorkerIntegrationDB(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	now := time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO users(id, display_name, created_at, updated_at)
		VALUES ($1, 'Worker Test User', $2, $2)
		ON CONFLICT (id) DO NOTHING
	`, workerTestUserID, now); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO orgs(id, name, idempotency_key, created_at, updated_at)
		VALUES ($1, 'Test Org', 'idem-test-org', $2, $2)
		ON CONFLICT (id) DO NOTHING
	`, workerTestOrgID, now); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	workerTestMembershipID := workerTestID("org_membership_test")
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at)
		VALUES ($1, $2, 'Test Project', 'idem-test-project', $3, $3)
		ON CONFLICT (id) DO NOTHING
	`, workerTestProjectID, workerTestOrgID, now); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_emails(user_id, email, normalized_email, verified_at, is_primary, created_at, updated_at)
		VALUES ($1, 'worker-test@example.com', 'worker-test@example.com', $2, true, $2, $2)
		ON CONFLICT (user_id, normalized_email) DO NOTHING
	`, workerTestUserID, now); err != nil {
		t.Fatalf("seed user email: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO org_memberships(id, org_id, user_id, role, created_at)
		VALUES ($1, $2, $3, 'admin', $4)
		ON CONFLICT (id) DO NOTHING
	`, workerTestMembershipID, workerTestOrgID, workerTestUserID, now); err != nil {
		t.Fatalf("seed org membership: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_memberships(org_id, project_id, org_membership_id, role, created_at)
		VALUES ($1, $2, $3, 'operator', $4)
		ON CONFLICT (project_id, org_membership_id) DO NOTHING
	`, workerTestOrgID, workerTestProjectID, workerTestMembershipID, now); err != nil {
		t.Fatalf("seed project membership: %v", err)
	}
}

func createWorkerProject(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	projectID storage.ID,
	name string,
	idempotencyKey string,
	now time.Time,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5)
		ON CONFLICT (id) DO NOTHING
	`, projectID, workerTestOrgID, name, idempotencyKey, now); err != nil {
		t.Fatalf("seed project %s: %v", projectID, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_memberships(org_id, project_id, org_membership_id, role, created_at)
		VALUES ($1, $2, $3, 'operator', $4)
		ON CONFLICT (project_id, org_membership_id) DO NOTHING
	`, workerTestOrgID, projectID, workerTestID("org_membership_test"), now); err != nil {
		t.Fatalf("seed project membership %s: %v", projectID, err)
	}
}

func createWorkerAgentWithUser(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	now time.Time,
) (storage.ID, storage.ID) {
	return createWorkerAgentWithTools(t, ctx, store, now)
}

func createWorkerAgentWithTools(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	now time.Time,
	tools ...string,
) (storage.ID, storage.ID) {
	permissions := make(map[string]string, len(tools))
	for _, name := range tools {
		permissions[name] = toolpermission.ModeAlwaysAllow
	}
	return createWorkerAgentWithToolPermissions(t, ctx, store, now, permissions)
}

func createWorkerAgentWithCatalogDefaultTools(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	now time.Time,
	tools ...string,
) (storage.ID, storage.ID) {
	return createWorkerAgentWithCatalogDefaultToolsForProject(t, ctx, store, workerTestProjectID, now, tools...)
}

func createWorkerAgentWithToolPermissions(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	now time.Time,
	permissionModes map[string]string,
) (storage.ID, storage.ID) {
	return createWorkerAgentWithToolPermissionsForProject(t, ctx, store, workerTestProjectID, now, permissionModes)
}

func createWorkerAgentWithToolPermissionsAndMachine(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	now time.Time,
	permissionModes map[string]string,
	machineName string,
) (storage.ID, storage.ID) {
	t.Helper()
	sourceYAML := "name: Worker Kernel Test\ninstruction: Help the user make progress.\nmodel:\n  provider_config: openai-prod\n  name: worker-kernel-test\nmachine_sources:\n  - machine_name: " + machineName + "\n    cwd: /work\n"
	if len(permissionModes) > 0 {
		sourceYAML += "tools:\n"
		names := make([]string, 0, len(permissionModes))
		for name := range permissionModes {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			sourceYAML += "  " + name + ":\n    permission:\n      mode: " +
				permissionModes[name] + "\n      parameters: {}\n"
		}
	}
	return createWorkerAgentFromSource(t, ctx, store, workerTestProjectID, now, sourceYAML)
}

func createWorkerAgentWithProject(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	projectID storage.ID,
	now time.Time,
) (storage.ID, storage.ID) {
	return createWorkerAgentWithToolPermissionsForProject(t, ctx, store, projectID, now, nil)
}

func createWorkerAgentWithCatalogDefaultToolsForProject(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	projectID storage.ID,
	now time.Time,
	tools ...string,
) (storage.ID, storage.ID) {
	t.Helper()
	sourceYAML := "name: Worker Kernel Test\n" +
		"instruction: Help the user make progress.\n" +
		"model:\n" +
		"  provider_config: openai-prod\n" +
		"  name: worker-kernel-test\n"
	if len(tools) > 0 {
		sourceYAML += "tools:\n"
		names := append([]string(nil), tools...)
		sort.Strings(names)
		for _, name := range names {
			sourceYAML += "  " + name + ": {}\n"
		}
	}
	return createWorkerAgentFromSource(t, ctx, store, projectID, now, sourceYAML)
}

func createWorkerAgentWithToolPermissionsForProject(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	projectID storage.ID,
	now time.Time,
	permissionModes map[string]string,
) (storage.ID, storage.ID) {
	t.Helper()
	sourceYAML := "name: Worker Kernel Test\n" +
		"instruction: Help the user make progress.\n" +
		"model:\n" +
		"  provider_config: openai-prod\n" +
		"  name: worker-kernel-test\n"
	if len(permissionModes) > 0 {
		sourceYAML += "tools:\n"
		names := make([]string, 0, len(permissionModes))
		for name := range permissionModes {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			sourceYAML += "  " + name + ":\n    permission:\n      mode: " +
				permissionModes[name] + "\n      parameters: {}\n"
		}
	}
	return createWorkerAgentFromSource(t, ctx, store, projectID, now, sourceYAML)
}

func createWorkerAgentFromSource(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	projectID storage.ID,
	now time.Time,
	sourceYAML string,
) (storage.ID, storage.ID) {
	t.Helper()
	compiled := compileWorkerAgentYAMLResolved(t, ctx, store, projectID, sourceYAML, now)
	config, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               projectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       parseWorkerConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err != nil {
		t.Fatalf("create worker test config: %v", err)
	}
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID:       projectID,
		Name:            "Worker Kernel Test",
		CurrentConfigID: config.ID,
		IdempotencyKey:  "agent-" + now.Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("create worker test agent: %v", err)
	}
	launch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      projectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     workerTestUserPrincipal(workerTestUserID),
		IdempotencyKey: "launch-" + now.Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	return launch.Agent.ID, workerTestUserID
}

func compileWorkerAgentYAMLResolved(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	projectID storage.ID,
	sourceYAML string,
	now time.Time,
) agentconfig.Result {
	t.Helper()
	source, err := agentconfig.ParseSource(agentconfig.SourceFormatYAML, []byte(sourceYAML))
	if err != nil {
		t.Fatalf("parse worker test agent source: %v", err)
	}
	configuredModel := ensureWorkerModelSelection(
		t,
		ctx,
		store,
		projectID,
		source.Model.ProviderConfig,
		source.Model.Name,
		now,
	)
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(sourceYAML), agentconfig.CompileOptions{
		ResolveModelSelection: func(
			providerConfigName string,
			configuredModelName string,
		) (agentconfig.ResolvedModelSelection, error) {
			return resolvedWorkerAgentConfigModel(configuredModel), nil
		},
		ResolveMachineName: func(machineName string) (string, error) {
			machineID, err := store.Execution().ResolveAgentConfigMachineName(ctx, projectID, machineName)
			if err != nil {
				return "", err
			}
			return publicid.Encode(publicid.KindMachine, machineID)
		},
	})
	if err != nil {
		t.Fatalf("compile resolved worker test agent: %v", err)
	}
	return compiled
}

func resolvedWorkerAgentConfigModel(configuredModel modelstore.ConfiguredModelRecord) agentconfig.ResolvedModelSelection {
	supportsTools := configuredModel.SupportsTools
	return agentconfig.ResolvedModelSelection{
		ConfiguredModelID: configuredModel.ID.String(),
		SupportsTools:     &supportsTools,
	}
}

func ensureWorkerModelSelection(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	projectID storage.ID,
	providerConfigName, configuredModelName string,
	now time.Time,
) modelstore.ConfiguredModelRecord {
	t.Helper()
	providerConfig, err := store.Models().GetModelProviderConfigByName(ctx, workerTestOrgID, providerConfigName)
	if err != nil {
		if !storeerr.IsNotFound(err) {
			t.Fatalf("load model provider config %q: %v", providerConfigName, err)
		}
		secret, err := ensureWorkerProviderCredential(t, ctx, store, providerConfigName)
		if err != nil {
			t.Fatalf("ensure provider credential: %v", err)
		}
		providerConfig, err = store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
			OrgID:              workerTestOrgID,
			Name:               providerConfigName,
			APIFormat:          modelprotocol.APIFormatOpenAIResponses,
			APIVariant:         "default",
			BaseURL:            "https://api.openai.com/v1",
			CredentialSecretID: secret.ID,
		})
		if err != nil {
			t.Fatalf("create model provider config %q: %v", providerConfigName, err)
		}
	}
	configuredModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 workerTestOrgID,
		ModelProviderConfigID: providerConfig.ID,
		Name:                  configuredModelName,
		ProviderModelSlug:     configuredModelName,
		ContextWindowTokens:   128000,
		MaxOutputTokens:       8192,
	})
	if err != nil {
		t.Fatalf("create configured model %s/%s: %v", providerConfigName, configuredModelName, err)
	}
	if _, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:             workerTestOrgID,
		ProjectID:         projectID,
		ConfiguredModelID: configuredModel.ID,
	}); err != nil {
		t.Fatalf("grant configured model %s/%s: %v", providerConfigName, configuredModelName, err)
	}
	return configuredModel
}

func ensureWorkerProviderCredential(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	providerConfigName string,
) (secretstore.SecretRecord, error) {
	t.Helper()
	name := "worker-provider-" + providerConfigName
	secret, err := store.Secrets().GetSecretByOwnerName(
		ctx,
		workerTestOrgID,
		secretstore.SecretOwnerOrg,
		storage.NilID,
		storage.NilID,
		name,
	)
	if err == nil {
		return secret, nil
	}
	if !storeerr.IsNotFound(err) {
		return secretstore.SecretRecord{}, err
	}
	secret, _, err = store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     workerTestOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      name,
		Material:  secrets.GenericMaterial{Value: "test-key"},
		Actor:     workerTestUserPrincipal(workerTestUserID),
	})
	return secret, err
}

func intPtrForWorkerTest(value int) *int {
	return &value
}

func parseWorkerConfiguredModelID(t *testing.T, compiled agentconfig.Result) storage.ID {
	t.Helper()
	id, err := storage.ParseID(compiled.Compiled.Model.ConfiguredModelID)
	if err != nil {
		t.Fatalf("parse compiled configured model id: %v", err)
	}
	return id
}

func createWorkerInput(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	agentID, userID storage.ID,
	text string,
	now time.Time,
) storage.ID {
	t.Helper()
	return createWorkerInputWithDeliveryMode(t, ctx, store, agentID, userID, text, executionstore.DeliveryModeQueued, now)
}

func createWorkerInputWithProject(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	projectID, agentID, userID storage.ID,
	text string,
	now time.Time,
) storage.ID {
	t.Helper()
	return createWorkerInputWithProjectAndDeliveryMode(
		t,
		ctx,
		store,
		projectID,
		agentID,
		userID,
		text,
		executionstore.DeliveryModeQueued,
		now,
	)
}

func createWorkerInputWithDeliveryMode(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	agentID, userID storage.ID,
	text string,
	deliveryMode executionstore.AgentInputDeliveryMode,
	now time.Time,
) storage.ID {
	t.Helper()
	return createWorkerInputWithProjectAndDeliveryMode(
		t,
		ctx,
		store,
		workerTestProjectID,
		agentID,
		userID,
		text,
		deliveryMode,
		now,
	)
}

func createWorkerInputWithProjectAndDeliveryMode(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	projectID, agentID, userID storage.ID,
	text string,
	deliveryMode executionstore.AgentInputDeliveryMode,
	now time.Time,
) storage.ID {
	t.Helper()
	producer, err := executionstore.OmnaraActorParams(workerTestOrgID, workerTestUserPrincipal(userID))
	if err != nil {
		t.Fatalf("omnara actor params: %v", err)
	}
	input, _, _, err := store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      projectID,
			AgentID:        agentID,
			Actor:          producer,
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"` + text + `"}]`),
			DeliveryMode:   deliveryMode,
			IdempotencyKey: "input-" + agentID.String() + "-" + text,
		},
	)
	if err != nil {
		t.Fatalf("create input %s: %v", text, err)
	}
	return input.ID
}

func createWorkerExecutableMachine(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	now time.Time,
) executionstore.MachineRecord {
	t.Helper()
	machine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          workerTestOrgID,
			DisplayName:    "worker-test-machine",
			IdempotencyKey: "worker-test-machine-" + now.Format(time.RFC3339Nano),
		},
	)
	if err != nil {
		t.Fatalf("create worker machine: %v", err)
	}
	if _, _, err := store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          workerTestOrgID,
			ProjectID:      workerTestProjectID,
			MachineID:      machine.ID,
			IdempotencyKey: "worker-test-grant-" + now.Format(time.RFC3339Nano),
		},
	); err != nil {
		t.Fatalf("grant worker machine: %v", err)
	}
	token, err := store.Execution().CreateBYOMachineDaemonToken(
		ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID:     workerTestOrgID,
			MachineID: machine.ID,
			Name:      "worker test daemon",
		},
	)
	if err != nil {
		t.Fatalf("create worker daemon token: %v", err)
	}
	if _, err := store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            workerTestOrgID,
			MachineID:        machine.ID,
			DaemonTokenID:    token.Record.ID,
			DaemonInstanceID: workerTestID("daemon-worker-runtime"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Hour,
		},
	); err != nil {
		t.Fatalf("register worker daemon runtime: %v", err)
	}
	return machine
}

func assertAgentInputQueued(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agentID, inputID storage.ID) {
	t.Helper()
	var state string
	var admittedEventID *storage.ID
	if err := pool.QueryRow(ctx, `
SELECT state, admitted_event_id
FROM agent_inputs
WHERE project_id = $1 AND agent_id = $2 AND id = $3
`, workerTestProjectID, agentID, inputID).
		Scan(&state, &admittedEventID); err != nil {
		t.Fatalf("query agent input: %v", err)
	}
	if state != "received" || admittedEventID != nil {
		t.Fatalf("agent input %s state=%s admitted_event_id=%s", inputID, state, admittedEventID)
	}
}

func mustWorkerJSON(value any) json.RawMessage {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return body
}

func workerQuestionInput(questions ...interactionform.Question) json.RawMessage {
	return mustWorkerJSON(map[string]any{"questions": questions})
}

func workerQuestionResolution(answers ...interactionform.Answer) interactionform.Resolution {
	return interactionform.Resolution{Answers: answers}
}

func workerPermissionResolution(
	optionIndex int,
	text string,
) interactionform.Resolution {
	return workerQuestionResolution(interactionform.Answer{
		OptionIndices: []int{optionIndex},
		Text:          text,
	})
}

func assertAgentInputAdmitted(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agentID, inputID storage.ID) {
	t.Helper()
	var state string
	var admittedEventID *storage.ID
	if err := pool.QueryRow(ctx, `
SELECT state, admitted_event_id
FROM agent_inputs
WHERE project_id = $1 AND agent_id = $2 AND id = $3
`, workerTestProjectID, agentID, inputID).
		Scan(&state, &admittedEventID); err != nil {
		t.Fatalf("query agent input: %v", err)
	}
	if state != "resolved" || admittedEventID == nil {
		t.Fatalf("agent input %s state=%s admitted_event_id=%s", inputID, state, admittedEventID)
	}
}

func assertAgentInputCanceled(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agentID, inputID storage.ID) {
	t.Helper()
	var state string
	var canceledAt *time.Time
	if err := pool.QueryRow(
		ctx,
		`SELECT state, canceled_at FROM agent_inputs WHERE project_id = $1 AND agent_id = $2 AND id = $3`,
		workerTestProjectID,
		agentID,
		inputID,
	).
		Scan(&state, &canceledAt); err != nil {
		t.Fatalf("query agent input: %v", err)
	}
	if state != "canceled" || canceledAt == nil {
		t.Fatalf("agent input %s state=%s canceled_at=%v", inputID, state, canceledAt)
	}
}

func assertTurnInputs(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	agentID, turnID storage.ID,
	want []storage.ID,
) {
	t.Helper()
	rows, err := pool.Query(ctx, `
SELECT event.agent_input_id
FROM agent_events event
JOIN agent_inputs input ON input.agent_id = event.agent_id
  AND input.id = event.agent_input_id
  AND input.input_kind = 'content'
WHERE input.project_id = $1
  AND event.agent_id = $2
  AND event.turn_id = $3
  AND event.is_opening_event
  AND event.event_kind = 'agent_input'
  AND event.agent_input_id IS NOT NULL
ORDER BY event.sequence
`, workerTestProjectID, agentID, turnID)
	if err != nil {
		t.Fatalf("query turn inputs: %v", err)
	}
	defer rows.Close()
	got := make([]storage.ID, 0, len(want))
	for rows.Next() {
		var inputID storage.ID
		if err := rows.Scan(&inputID); err != nil {
			t.Fatalf("scan turn input: %v", err)
		}
		got = append(got, inputID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("turn inputs rows: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("turn input count got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("turn inputs got %v want %v", got, want)
		}
	}
}

func waitNoRuntimeLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agentID storage.ID) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		count := runtimeLockCount(t, ctx, pool, agentID)
		if count == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("runtime lock count for %s = %d, want 0", agentID, count)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context ended waiting for runtime lock cleanup: %v", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func runtimeLockCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agentID storage.ID) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM agent_runtime_locks runtime_lock
JOIN agents agent ON agent.id = runtime_lock.agent_id
WHERE agent.project_id = $1 AND runtime_lock.agent_id = $2
`, workerTestProjectID, agentID).
		Scan(&count); err != nil {
		t.Fatalf("count runtime locks: %v", err)
	}
	return count
}

func runtimeLockRenewedAt(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	projectID storage.ID,
	agentID storage.ID,
) time.Time {
	t.Helper()
	var renewalAt time.Time
	if err := pool.QueryRow(ctx, `
SELECT runtime_lock.renewed_at
FROM agent_runtime_locks runtime_lock
JOIN agents agent ON agent.id = runtime_lock.agent_id
WHERE agent.project_id = $1 AND runtime_lock.agent_id = $2
`, projectID, agentID).Scan(&renewalAt); err != nil {
		t.Fatalf("query runtime lock renewal: %v", err)
	}
	return renewalAt
}

func waitRuntimeLockRenewalAfter(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	projectID storage.ID,
	agentID storage.ID,
	previous time.Time,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if runtimeLockRenewedAt(t, ctx, pool, projectID, agentID).After(previous) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("runtime lock renewal for %s did not advance after %s", agentID, previous)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context ended waiting for runtime lock renewal: %v", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func assertWorkerWakeup(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agentID storage.ID) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM agent_wakeups wake
JOIN agents agent ON agent.id = wake.agent_id
WHERE agent.project_id = $1 AND wake.agent_id = $2
`, workerTestProjectID, agentID).
		Scan(&count); err != nil {
		t.Fatalf("count worker wakeups: %v", err)
	}
	if count != 1 {
		t.Fatalf("wakeup count for %s = %d, want 1", agentID, count)
	}
}

func waitNoWorkerWakeup(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	agentID storage.ID,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var count int
		if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM agent_wakeups wake
JOIN agents agent ON agent.id = wake.agent_id
WHERE agent.project_id = $1 AND wake.agent_id = $2
`, workerTestProjectID, agentID).
			Scan(&count); err != nil {
			t.Fatalf("count agent wakeups: %v", err)
		}
		if count == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("wakeup count for %s = %d, want 0", agentID, count)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context ended waiting for wakeup cleanup: %v", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func assertAssistantEventRecorded(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	agentID storage.ID,
	wantText string,
) storage.ID {
	t.Helper()
	rows, err := pool.Query(ctx, `
SELECT DISTINCT output.id
FROM agent_events event
JOIN model_outputs output
  ON output.agent_id = event.agent_id
 AND output.id = event.model_output_id
JOIN content_blocks block
  ON block.agent_id = output.agent_id
 AND block.owner_model_output_id = output.id
JOIN agents agent ON agent.id = event.agent_id
WHERE agent.project_id = $1
  AND event.agent_id = $2
  AND event.event_kind = 'model_output'
  AND block.block_kind = 'text'
  AND block.text_content = $3`, workerTestProjectID, agentID, wantText)
	if err != nil {
		t.Fatalf("query assistant message: %v", err)
	}
	defer rows.Close()
	var outputIDs []storage.ID
	for rows.Next() {
		var outputID storage.ID
		if err := rows.Scan(&outputID); err != nil {
			t.Fatalf("scan assistant message output id: %v", err)
		}
		outputIDs = append(outputIDs, outputID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate assistant message output ids: %v", err)
	}
	if len(outputIDs) != 1 {
		t.Fatalf("assistant message output count with text %q = %d, want 1", wantText, len(outputIDs))
	}
	return outputIDs[0]
}

func assertCompletedModelContext(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agentID, outputID storage.ID) {
	t.Helper()
	var state string
	var linkedModelOutputID storage.ID
	if err := pool.QueryRow(ctx, `
SELECT context.state,
       output.id
FROM model_call_contexts context
JOIN model_outputs output
  ON output.agent_id = context.agent_id
 AND output.model_call_context_id = context.id
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND output.id = $3`,
		workerTestProjectID,
		agentID,
		outputID,
	).Scan(&state, &linkedModelOutputID); err != nil {
		t.Fatalf("query completed model context: %v", err)
	}
	if state != "succeeded" {
		t.Fatalf("model context state = %s, want succeeded", state)
	}
	if linkedModelOutputID != outputID {
		t.Fatalf("completed model context output = %s, want %s", linkedModelOutputID, outputID)
	}
}

func assertModelOutputUsage(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	agentID, outputID storage.ID,
	inputTokens, uncachedInputTokens, cacheReadTokens, cacheWriteTokens, outputTokens, reasoningTokens, totalTokens int,
) {
	t.Helper()
	var gotInput, gotUncachedInput, gotCacheRead, gotCacheWrite, gotOutput, gotReasoning, gotTotal int
	if err := pool.QueryRow(ctx, `
SELECT coalesce(context.input_tokens_total, 0),
	       coalesce(context.uncached_input_tokens, 0),
	       coalesce(context.cache_read_input_tokens, 0),
	       coalesce(context.cache_write_input_tokens, 0),
	       coalesce(context.output_tokens_total, 0),
	       coalesce(context.reasoning_output_tokens, 0),
	       coalesce(context.input_tokens_total, 0) + coalesce(context.output_tokens_total, 0)
FROM model_outputs output
JOIN model_call_contexts context
  ON context.agent_id = output.agent_id
 AND context.id = output.model_call_context_id
WHERE context.project_id = $1
  AND output.agent_id = $2
  AND output.id = $3
`,
		workerTestProjectID,
		agentID,
		outputID,
	).Scan(
		&gotInput,
		&gotUncachedInput,
		&gotCacheRead,
		&gotCacheWrite,
		&gotOutput,
		&gotReasoning,
		&gotTotal,
	); err != nil {
		t.Fatalf("query model output usage: %v", err)
	}
	if gotInput != inputTokens || gotUncachedInput != uncachedInputTokens || gotCacheRead != cacheReadTokens ||
		gotCacheWrite != cacheWriteTokens ||
		gotOutput != outputTokens ||
		gotReasoning != reasoningTokens ||
		gotTotal != totalTokens {
		const usageMismatchFormat = "model output usage = input:%d uncached:%d cache_read:%d cache_write:%d " +
			"output:%d reasoning:%d total:%d, want input:%d uncached:%d cache_read:%d cache_write:%d " +
			"output:%d reasoning:%d total:%d"
		t.Fatalf(
			usageMismatchFormat,
			gotInput,
			gotUncachedInput,
			gotCacheRead,
			gotCacheWrite,
			gotOutput,
			gotReasoning,
			gotTotal,
			inputTokens,
			uncachedInputTokens,
			cacheReadTokens,
			cacheWriteTokens,
			outputTokens,
			reasoningTokens,
			totalTokens,
		)
	}
}

func assertCompletedToolCall(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	agentID storage.ID,
	name, state string,
) {
	t.Helper()
	assertCompletedToolCallCount(t, ctx, pool, agentID, name, state, 1)
}

func assertCompletedToolCallCount(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	agentID storage.ID,
	name, state string,
	want int,
) {
	t.Helper()
	outcome := "succeeded"
	if state == "denied" {
		outcome = "denied"
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*)
	FROM tool_call_read_projection tc
	JOIN tool_call_results result ON result.agent_id = tc.agent_id
  AND result.tool_call_id = tc.id
WHERE tc.project_id = $1
	  AND tc.agent_id = $2
	  AND tc.name = $3
	  AND tc.state = 'completed'
	  AND result.completed_at IS NOT NULL
	  AND result.outcome = $4
`, workerTestProjectID, agentID, name, outcome).Scan(&count); err != nil {
		t.Fatalf("query completed tool call: %v", err)
	}
	if count != want {
		t.Fatalf("completed %s tool calls in state %s = %d, want %d", name, state, count, want)
	}
}

func assertWaitingToolCall(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	agentID storage.ID,
	name string,
) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM tool_call_read_projection call
WHERE call.project_id = $1
  AND call.agent_id = $2
  AND call.name = $3
  AND call.state = 'waiting'
`, workerTestProjectID, agentID, name).Scan(&count); err != nil {
		t.Fatalf("query waiting tool call: %v", err)
	}
	if count != 1 {
		t.Fatalf("waiting %s tool calls = %d, want 1", name, count)
	}
}

func assertDeniedToolResultShape(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	agentID storage.ID,
	toolName, reason string,
	interactionID storage.ID,
) {
	t.Helper()
	var body json.RawMessage
	if err := pool.QueryRow(ctx, `
SELECT block.structured_data
FROM tool_call_read_projection tc
JOIN tool_call_results result ON result.agent_id = tc.agent_id
  AND result.tool_call_id = tc.id
JOIN content_blocks block ON block.agent_id = result.agent_id
  AND block.owner_tool_call_result_id = result.id
  AND block.block_kind = 'structured_data'
WHERE tc.project_id = $1
  AND tc.agent_id = $2
	  AND tc.name = $3
	  AND tc.state = 'completed'
	  AND result.outcome = 'denied'
	ORDER BY result.completed_at DESC, block.ordinal
LIMIT 1
`, workerTestProjectID, agentID, toolName).Scan(&body); err != nil {
		t.Fatalf("query denied tool result: %v", err)
	}
	if interactionID != storage.NilID && strings.Contains(string(body), interactionID.String()) ||
		strings.Contains(string(body), agentID.String()) ||
		strings.Contains(string(body), workerTestProjectID.String()) {
		t.Fatalf("denied tool result leaked internal ids: %s", body)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode denied tool result %s: %v", body, err)
	}
	if len(decoded) != 1 || string(decoded["reason"]) != strconv.Quote(reason) {
		t.Fatalf("denied tool result = %s, want exactly the model-visible reason", body)
	}
}

func assertStructuredQuestionResultAnswers(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	agentID storage.ID,
	providerCallID string,
	answers []string,
) {
	t.Helper()
	var raw json.RawMessage
	if err := pool.QueryRow(ctx, `
SELECT block.structured_data
FROM tool_call_read_projection tc
JOIN tool_call_results result ON result.agent_id = tc.agent_id
  AND result.tool_call_id = tc.id
JOIN content_blocks block ON block.agent_id = result.agent_id
  AND block.owner_tool_call_result_id = result.id
  AND block.block_kind = 'structured_data'
WHERE tc.project_id = $1
  AND tc.agent_id = $2
	  AND tc.provider_call_id = $3
	  AND tc.name = 'ask_question'
ORDER BY block.ordinal
LIMIT 1
	`, workerTestProjectID, agentID, providerCallID).Scan(&raw); err != nil {
		t.Fatalf("query structured question result output: %v", err)
	}
	var decoded struct {
		Answers []struct {
			QuestionIndex   int `json:"question_index"`
			SelectedOptions []struct {
				OptionIndex int    `json:"option_index"`
				Label       string `json:"label"`
			} `json:"selected_options"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode structured question result output %s: %v", raw, err)
	}
	if len(decoded.Answers) != len(answers) {
		t.Fatalf("structured question answers = %+v, want %v", decoded.Answers, answers)
	}
	for i, want := range answers {
		selected := decoded.Answers[i].SelectedOptions
		if decoded.Answers[i].QuestionIndex != i ||
			len(selected) != 1 ||
			selected[0].OptionIndex != 0 ||
			selected[0].Label != want {
			t.Fatalf(
				"structured question answer %d = %v, want %q; output=%s",
				i,
				decoded.Answers[i].SelectedOptions,
				want,
				raw,
			)
		}
	}
}

func assertNoMachineToolCallFailed(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agentID storage.ID) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*)
FROM tool_call_read_projection tc
JOIN tool_call_results result ON result.agent_id = tc.agent_id
  AND result.tool_call_id = tc.id
JOIN content_blocks block ON block.agent_id = result.agent_id
  AND block.owner_tool_call_result_id = result.id
  AND block.block_kind = 'structured_data'
WHERE tc.project_id = $1
  AND tc.agent_id = $2
	  AND tc.name = 'run_command'
	  AND tc.state = 'completed'
	  AND block.structured_data->>'error_code' = 'no_active_agent_machine_binding'
`, workerTestProjectID, agentID).Scan(&count); err != nil {
		t.Fatalf("query no-machine tool call: %v", err)
	}
	if count != 1 {
		t.Fatalf("no-machine failed run_command tool count = %d, want 1", count)
	}
}

func assertStartingProcessForTool(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	agentID storage.ID,
	toolName string,
) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM processes process
JOIN tool_calls tool_call ON tool_call.agent_id = process.agent_id
  AND tool_call.id = process.tool_call_id
WHERE process.project_id = $1
  AND process.agent_id = $2
  AND tool_call.name = $3
  AND process.state IN ('queued', 'starting')
`, workerTestProjectID, agentID, toolName).
		Scan(&count); err != nil {
		t.Fatalf("query queued/starting process: %v", err)
	}
	if count != 1 {
		t.Fatalf("queued/starting %s process count = %d, want 1", toolName, count)
	}
}

func assertOpenStructuredQuestion(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	agentID storage.ID,
) executionstore.AgentInteractionRecord {
	t.Helper()
	return assertOpenInteraction(t, ctx, store, agentID, "question")
}

func assertOpenInteraction(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	agentID storage.ID,
	kind executionstore.AgentInteractionKind,
) executionstore.AgentInteractionRecord {
	t.Helper()
	records := assertOpenInteractions(t, ctx, store, agentID, kind, 1)
	return records[0]
}

func assertOpenInteractions(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	agentID storage.ID,
	kind executionstore.AgentInteractionKind,
	count int,
) []executionstore.AgentInteractionRecord {
	t.Helper()
	page, err := store.Execution().ListAgentInteractionsForAgent(ctx, executionstore.ListAgentInteractionsForAgentInput{
		ProjectID: workerTestProjectID,
		AgentID:   agentID,
		State:     executionstore.AgentInteractionStateOpen,
		Limit:     100,
	})
	if err != nil {
		t.Fatalf("list open interactions: %v", err)
	}
	filtered := make([]executionstore.AgentInteractionRecord, 0, len(page.Interactions))
	for _, record := range page.Interactions {
		if record.InteractionKind == kind {
			filtered = append(filtered, record)
		}
	}
	if len(filtered) != count {
		t.Fatalf("expected %d open %s interactions, got all=%+v filtered=%+v", count, kind, page.Interactions, filtered)
	}
	return filtered
}

func assertNoOpenInteractions(t *testing.T, ctx context.Context, store *storage.Store, agentID storage.ID) {
	t.Helper()
	page, err := store.Execution().ListAgentInteractionsForAgent(ctx, executionstore.ListAgentInteractionsForAgentInput{
		ProjectID: workerTestProjectID,
		AgentID:   agentID,
		State:     executionstore.AgentInteractionStateOpen,
		Limit:     100,
	})
	if err != nil {
		t.Fatalf("list open interactions: %v", err)
	}
	if len(page.Interactions) != 0 {
		t.Fatalf("expected no open interactions, got %+v", page.Interactions)
	}
}
