//go:build integration

package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/harness/kernel"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/stretchr/testify/require"
)

type observedContinuationExecutor struct {
	kernel.AgentExecutor
	models     []kernel.ModelWorkExecution
	tools      []kernel.ToolWorkExecution
	afterModel func(context.Context) error
	asyncGate  chan struct{}
}

func (e *observedContinuationExecutor) ExecuteModelWork(ctx context.Context, input kernel.ModelWorkExecution) error {
	e.models = append(e.models, input)
	if err := e.AgentExecutor.ExecuteModelWork(ctx, input); err != nil {
		return err
	}
	if e.afterModel != nil {
		return e.afterModel(ctx)
	}
	return nil
}

func (e *observedContinuationExecutor) ExecuteModelWorkAndAdvance(
	ctx context.Context, input kernel.ModelWorkExecution, _ kernel.ModelWorkAdvanceOptions,
) (executionstore.OwnedAgentWorkTransition, bool, error) {
	return executionstore.OwnedAgentWorkTransition{}, false, e.ExecuteModelWork(ctx, input)
}

func (e *observedContinuationExecutor) ExecuteToolWork(ctx context.Context, input kernel.ToolWorkExecution) error {
	e.tools = append(e.tools, input)
	if e.asyncGate == nil {
		return e.AgentExecutor.ExecuteToolWork(ctx, input)
	}
	reservation, err := tools.ReserveAsyncExecution(ctx)
	if err != nil {
		return err
	}
	reservation.Start()
	go func() {
		select {
		case <-e.asyncGate:
			reservation.Done(e.AgentExecutor.ExecuteToolWork(ctx, input))
		case <-ctx.Done():
			reservation.Done(ctx.Err())
		}
	}()
	return nil
}

func continuationToolResponse(index int) model.Response {
	return model.Response{
		ID: fmt.Sprintf("response_%d", index), StopReason: model.StopReasonToolUse,
		Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{{
			ID: fmt.Sprintf("call_%d", index), Name: "list_agents", Input: json.RawMessage(`{}`),
		}}),
	}
}

func continuationFinalResponse(index int) model.Response {
	return model.Response{
		ID: fmt.Sprintf("response_%d", index), StopReason: model.StopReasonEndTurn,
		Content: []model.ResponsePart{{Type: "text", Text: "done"}},
	}
}

func newObservedContinuationExecutor(store *storage.Store, responses ...model.Response) *observedContinuationExecutor {
	client := &sequenceModelClient{providerModelSlug: "worker-kernel-test", responses: responses}
	return &observedContinuationExecutor{AgentExecutor: kernel.AgentExecutor{
		Store: store,
		ContextBuilder: modelcontext.Builder{
			Store: modelcontext.NewStore(store.Execution(), store.Artifacts(), store.Integrations()),
		},
		ModelResolver: liveWorkerTestModelResolver(store, client),
	}}
}

func TestWorkerOwnedContinuationRunsKernelAndYieldsAtModelBudget(t *testing.T) {
	ctx := t.Context()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	agentID, userID := createWorkerAgentWithTools(t, ctx, store, time.Now(), "list_agents")
	inputID := createWorkerInput(t, ctx, store, agentID, userID, "keep going", time.Now())
	executor := newObservedContinuationExecutor(store,
		continuationToolResponse(1), continuationToolResponse(2), continuationToolResponse(3), continuationFinalResponse(4))
	worker := NewWorker(store.Execution(), executor, Options{})
	requireWorkerClaim(t, ctx, worker, "run owned chain")
	require.Len(t, executor.models, 3)
	require.Len(t, executor.tools, 3)
	runtimeID := executor.models[0].RuntimeLockID
	for _, step := range executor.models {
		require.Equal(t, runtimeID, step.RuntimeLockID)
		require.Equal(t, []uuid.UUID{inputID}, step.InputIDs)
	}
	for _, step := range executor.tools {
		require.Equal(t, runtimeID, step.RuntimeLockID)
	}
	waitNoRuntimeLock(t, ctx, pool, agentID)
	var wakeups int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_wakeups WHERE agent_id=$1 AND ready_at <= now()`, agentID,
	).Scan(&wakeups))
	require.Equal(t, 1, wakeups)
	requireWorkerClaim(t, ctx, worker, "claim yielded continuation")
	require.Len(t, executor.models, 4)
	require.NotEqual(t, runtimeID, executor.models[3].RuntimeLockID)
	waitNoRuntimeLock(t, ctx, pool, agentID)
	waitNoWorkerWakeup(t, ctx, pool, agentID)
	assertCompletedToolCallCount(t, ctx, pool, agentID, "list_agents", "completed", 3)
}

func TestWorkerOwnedContinuationAdmitsInputsBetweenSteps(t *testing.T) {
	ctx := t.Context()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	agentID, userID := createWorkerAgentWithTools(t, ctx, store, time.Now(), "list_agents")
	initial := createWorkerInput(t, ctx, store, agentID, userID, "initial", time.Now())
	executor := newObservedContinuationExecutor(store,
		continuationToolResponse(1), continuationFinalResponse(2), continuationFinalResponse(3))
	var steering, queued uuid.UUID
	executor.afterModel = func(ctx context.Context) error {
		if len(executor.models) == 1 {
			queued = createWorkerInput(t, ctx, store, agentID, userID, "queued", time.Now())
			steering = createWorkerInputWithDeliveryMode(
				t, ctx, store, agentID, userID, "steering", executionstore.DeliveryModeSteering, time.Now(),
			)
		}
		return nil
	}
	worker := NewWorker(store.Execution(), executor, Options{})
	requireWorkerClaim(t, ctx, worker, "admit arriving inputs")
	require.Len(t, executor.models, 3)
	require.Len(t, executor.tools, 1)
	for i, inputID := range []uuid.UUID{initial, steering, queued} {
		require.Equal(t, []uuid.UUID{inputID}, executor.models[i].InputIDs)
		require.Equal(t, executor.models[0].RuntimeLockID, executor.models[i].RuntimeLockID)
	}
	require.Equal(t, executor.models[0].TurnID, executor.tools[0].TurnID)
	waitNoRuntimeLock(t, ctx, pool, agentID)
	waitNoWorkerWakeup(t, ctx, pool, agentID)
}

func TestWorkerOwnedContinuationHonorsDurableCancellationBetweenSteps(t *testing.T) {
	ctx := t.Context()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	agentID, userID := createWorkerAgentWithTools(t, ctx, store, time.Now(), "list_agents")
	createWorkerInput(t, ctx, store, agentID, userID, "cancel", time.Now())
	executor := newObservedContinuationExecutor(store, continuationToolResponse(1))
	executor.afterModel = func(ctx context.Context) error {
		_, err := store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
			ProjectID: workerTestProjectID, AgentID: agentID, Actor: workerTestActorParams(t),
		})
		return err
	}
	worker := NewWorker(store.Execution(), executor, Options{})
	worked, err := worker.RunOnce(ctx)
	require.True(t, worked)
	require.ErrorIs(t, err, storeerr.ErrRuntimeLockInactive)
	require.Len(t, executor.models, 1)
	require.Empty(t, executor.tools)
	waitNoRuntimeLock(t, ctx, pool, agentID)
	waitNoWorkerWakeup(t, ctx, pool, agentID)
}

func TestWorkerOwnedContinuationTransfersLeaseToAsyncRetention(t *testing.T) {
	ctx := t.Context()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	agentID, userID := createWorkerAgentWithTools(t, ctx, store, time.Now(), "list_agents")
	createWorkerInput(t, ctx, store, agentID, userID, "async", time.Now())
	executor := newObservedContinuationExecutor(store, continuationToolResponse(1), continuationFinalResponse(2))
	executor.asyncGate = make(chan struct{})
	worker := NewWorker(store.Execution(), executor, Options{AsyncToolCapacity: 1})
	requireWorkerClaim(t, ctx, worker, "retain async continuation")
	require.Len(t, executor.models, 1)
	require.Len(t, executor.tools, 1)
	var runtimeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM agent_runtime_locks WHERE agent_id=$1`, agentID,
	).Scan(&runtimeID))
	require.Equal(t, executor.models[0].RuntimeLockID, runtimeID)
	require.Equal(t, runtimeID, executor.tools[0].RuntimeLockID)
	close(executor.asyncGate)
	worker.retained.Wait()
	waitNoRuntimeLock(t, ctx, pool, agentID)
	requireWorkerClaim(t, ctx, worker, "resume after async completion")
	require.Len(t, executor.models, 2)
	require.NotEqual(t, runtimeID, executor.models[1].RuntimeLockID)
	waitNoWorkerWakeup(t, ctx, pool, agentID)
}
