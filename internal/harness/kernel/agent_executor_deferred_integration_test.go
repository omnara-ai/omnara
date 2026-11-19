//go:build integration

package kernel

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelbatch"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type deferredKernelModel struct {
	*sequenceKernelModel
	mu        sync.Mutex
	submitted []model.DeferredRequest
	collected []model.DeferredHandle
	collect   []func() (model.Response, error)
}

func (m *deferredKernelModel) SubmitDeferred(
	_ context.Context, input model.DeferredRequest,
) (model.DeferredSubmission, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.submitted = append(m.submitted, input)
	return model.DeferredSubmission{ProviderHandle: "batch_test"}, nil
}

func (m *deferredKernelModel) CollectDeferred(
	_ context.Context, handle model.DeferredHandle,
) (model.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.collected = append(m.collected, handle)
	if len(m.collect) == 0 {
		return model.Response{}, errors.New("unexpected deferred collect")
	}
	next := m.collect[0]
	m.collect = m.collect[1:]
	return next()
}

type deferredCallback struct {
	id    uuid.UUID
	token string
}

func deferredTestExecutor(
	fixture kernelFixture, client model.Client, callback *deferredCallback,
) AgentExecutor {
	return AgentExecutor{
		Store:           fixture.Store,
		ModelResolver:   liveTestModelResolver(fixture.Store, client),
		ToolExecutor:    tools.Executor{Store: fixture.Store},
		StreamPublisher: &capturingStreamPublisher{},
		Now:             func() time.Time { return fixture.Now.Add(time.Second) },
		DeferredCallbackURL: func(id uuid.UUID, token string) string {
			callback.id, callback.token = id, token
			return modelbatch.OpenRouterCallbackURL("https://omnara.test", id, token)
		},
	}
}

func assertNoClaimableAgentWork(t *testing.T, ctx context.Context, fixture kernelFixture) {
	t.Helper()
	claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, kernelTestClaimInput(time.Time{}))
	if err != nil && !errors.Is(err, storeerr.ErrNoClaimableAgentWakeup) {
		t.Fatalf("claim while runtime is parked: %v", err)
	}
	if found && claim.Kind != executionstore.AgentWorkNone {
		t.Fatalf("claimed new work %+v while the runtime lock is parked", claim)
	}
}

func TestAgentExecutorParksRuntimeOnDeferredModelCallAndResumesOnCallback(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/deferred-batch-model", fixture.Now)
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "hello", fixture.Now.Add(time.Millisecond))
	client := &deferredKernelModel{
		sequenceKernelModel: &sequenceKernelModel{providerModelSlug: "deferred-batch-model"},
		collect: []func() (model.Response, error){
			func() (model.Response, error) { return model.Response{}, model.ErrDeferredPending },
			func() (model.Response, error) {
				return model.Response{
					ID:         "resp-deferred",
					Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "done in a batch"}},
					StopReason: model.StopReasonEndTurn,
				}, nil
			},
		},
	}
	var callback deferredCallback
	executor := deferredTestExecutor(fixture, client, &callback)
	store := fixture.Store.Execution()

	if err := executor.ExecuteModelWork(ctx, turn); !errors.Is(err, ErrModelWorkParked) {
		t.Fatalf("execute deferred model work: %v, want parked", err)
	}
	if client.respondedCount() != 0 {
		t.Fatal("deferred model call also made a synchronous provider call")
	}
	if len(client.submitted) != 1 || client.submitted[0].CallbackURL == "" || callback.token == "" {
		t.Fatalf("deferred submission = %+v callback=%+v", client.submitted, callback)
	}

	// The lock is held with nobody renewing it: no new work, no reaping, and
	// nothing to resume until the provider calls back.
	assertNoClaimableAgentWork(t, ctx, fixture)
	if err := store.EnsureRuntimeLockActive(ctx, kernelTestProjectID, agentID, turn.RuntimeLockID); err != nil {
		t.Fatalf("parked runtime lock is not held: %v", err)
	}
	if reaped, err := store.ReapExpiredAgentRuntimeLocks(ctx, 10); err != nil || reaped != 0 {
		t.Fatalf("reap parked runtime: reaped=%d err=%v", reaped, err)
	}
	if _, found, err := store.ClaimNextParkedModelBatch(ctx, kernelTestClaimInput(time.Time{})); err != nil || found {
		t.Fatalf("claim parked batch before callback: found=%v err=%v", found, err)
	}

	woken, err := store.WakeModelBatchRequest(ctx, callback.id, modelbatch.HashCallbackToken("wrong"))
	if err != nil || woken {
		t.Fatalf("wake with a wrong token: woken=%v err=%v", woken, err)
	}
	wake := func() {
		t.Helper()
		woken, err := store.WakeModelBatchRequest(ctx, callback.id, modelbatch.HashCallbackToken(callback.token))
		if err != nil || !woken {
			t.Fatalf("wake parked batch: woken=%v err=%v", woken, err)
		}
	}
	claimParked := func() ModelWorkExecution {
		t.Helper()
		claimInput := kernelTestClaimInput(time.Time{})
		claimInput.WorkerProcessID = uuid.New()
		claim, found, err := store.ClaimNextParkedModelBatch(ctx, claimInput)
		if err != nil || !found {
			t.Fatalf("claim parked batch after callback: found=%v err=%v", found, err)
		}
		if claim.Kind != executionstore.AgentWorkModel || claim.Model.Kind != executionstore.ModelWorkCollect ||
			claim.RuntimeLock.ID != turn.RuntimeLockID || claim.RuntimeLock.WorkerProcessID != claimInput.WorkerProcessID ||
			claim.Model.BatchRequestID != callback.id || claim.AgentID != agentID {
			t.Fatalf("resumed claim = %+v", claim)
		}
		work := modelWorkExecutionFromClaimForKernelTest(claim, fixture.Now.Add(2*time.Second))
		work.BatchRequestID = claim.Model.BatchRequestID
		return work
	}

	// A callback can arrive before the result is readable: park again.
	wake()
	if err := executor.ExecuteModelWork(ctx, claimParked()); !errors.Is(err, ErrModelWorkParked) {
		t.Fatalf("collect pending batch: %v, want parked", err)
	}
	assertNoClaimableAgentWork(t, ctx, fixture)
	if _, found, err := store.ClaimNextParkedModelBatch(ctx, kernelTestClaimInput(time.Time{})); err != nil || found {
		t.Fatalf("claim re-parked batch before its poll: found=%v err=%v", found, err)
	}

	wake()
	resumed := claimParked()
	if err := executor.ExecuteModelWork(ctx, resumed); err != nil {
		t.Fatalf("collect finished batch: %v", err)
	}
	if len(client.collected) != 2 || client.collected[1].ProviderHandle != "batch_test" ||
		client.collected[1].CustomID != client.submitted[0].CustomID {
		t.Fatalf("collected handles = %+v", client.collected)
	}
	contextID, err := uuid.Parse(client.submitted[0].CustomID)
	if err != nil {
		t.Fatalf("custom id is not the model call context: %v", err)
	}
	contextRow, found, err := store.GetModelCallContext(ctx, kernelTestProjectID, agentID, contextID)
	if err != nil || !found || contextRow.State != executionstore.ModelCallContextSucceeded {
		t.Fatalf("model call context after collect = %+v found=%v err=%v", contextRow, found, err)
	}
	// The request is forgotten once the call has an outcome: a late or repeated
	// callback has nothing left to wake.
	woken, err = store.WakeModelBatchRequest(ctx, callback.id, modelbatch.HashCallbackToken(callback.token))
	if err != nil || woken {
		t.Fatalf("wake after completion: woken=%v err=%v", woken, err)
	}
	fixture.releaseModelRuntimeLock(t, ctx, resumed)
	if _, found, err := store.ClaimNextParkedModelBatch(ctx, kernelTestClaimInput(time.Time{})); err != nil || found {
		t.Fatalf("claim parked batch after completion: found=%v err=%v", found, err)
	}
}

func TestAgentExecutorCancelWakesParkedDeferredModelCall(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/deferred-cancel-model", fixture.Now)
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "hello", fixture.Now.Add(time.Millisecond))
	client := &deferredKernelModel{
		sequenceKernelModel: &sequenceKernelModel{providerModelSlug: "deferred-cancel-model"},
	}
	var callback deferredCallback
	executor := deferredTestExecutor(fixture, client, &callback)
	store := fixture.Store.Execution()

	if err := executor.ExecuteModelWork(ctx, turn); !errors.Is(err, ErrModelWorkParked) {
		t.Fatalf("execute deferred model work: %v, want parked", err)
	}
	if _, err := store.CancelAgent(ctx, executionstore.CancelAgentInput{
		ProjectID: kernelTestProjectID, AgentID: agentID, Actor: kernelTestOmnaraActorParams(t, userID),
	}); err != nil {
		t.Fatalf("cancel agent with a parked runtime: %v", err)
	}

	// No worker is listening for the cancel, so the batch itself becomes
	// resumable and whoever claims it releases the lock.
	claim, found, err := store.ClaimNextParkedModelBatch(ctx, kernelTestClaimInput(time.Time{}))
	if err != nil || !found {
		t.Fatalf("claim canceled parked batch: found=%v err=%v", found, err)
	}
	if claim.RuntimeLock.CancelRequestedAt == nil {
		t.Fatal("resumed runtime lock does not carry the cancel request")
	}
	work := modelWorkExecutionFromClaimForKernelTest(claim, fixture.Now.Add(2*time.Second))
	work.BatchRequestID = claim.Model.BatchRequestID
	if err := executor.ExecuteModelWork(ctx, work); err != nil {
		t.Fatalf("resume canceled batch: %v", err)
	}
	if len(client.collected) != 0 {
		t.Fatal("canceled model call still collected its batch")
	}
	fixture.releaseModelRuntimeLock(t, ctx, work)
	if err := store.EnsureRuntimeLockActive(ctx, kernelTestProjectID, agentID, turn.RuntimeLockID); !errors.Is(
		err, storeerr.ErrRuntimeLockInactive,
	) {
		t.Fatalf("runtime lock after canceled resume: %v, want inactive", err)
	}
}
