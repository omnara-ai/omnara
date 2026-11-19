package worker

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/harness/kernel"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func (*renewalDeadlineStore) ClaimNextParkedModelBatch(
	context.Context,
	executionstore.ClaimNextAgentWorkInput,
) (executionstore.ClaimedAgentWork, bool, error) {
	return executionstore.ClaimedAgentWork{}, false, nil
}

func (*retainedRuntimeStore) ClaimNextParkedModelBatch(
	context.Context,
	executionstore.ClaimNextAgentWorkInput,
) (executionstore.ClaimedAgentWork, bool, error) {
	return executionstore.ClaimedAgentWork{}, false, nil
}

func (*dispatchStore) ClaimNextParkedModelBatch(
	context.Context,
	executionstore.ClaimNextAgentWorkInput,
) (executionstore.ClaimedAgentWork, bool, error) {
	return executionstore.ClaimedAgentWork{}, false, nil
}

type parkedBatchStore struct {
	retainedRuntimeStore
	parked      *executionstore.ClaimedAgentWork
	normalCalls int
}

func (s *parkedBatchStore) ClaimNextParkedModelBatch(
	context.Context,
	executionstore.ClaimNextAgentWorkInput,
) (executionstore.ClaimedAgentWork, bool, error) {
	if s.parked == nil {
		return executionstore.ClaimedAgentWork{}, false, nil
	}
	return *s.parked, true, nil
}

func (s *parkedBatchStore) ClaimNextAgentWork(
	ctx context.Context,
	input executionstore.ClaimNextAgentWorkInput,
) (executionstore.ClaimedAgentWork, bool, error) {
	s.normalCalls++
	return s.retainedRuntimeStore.ClaimNextAgentWork(ctx, input)
}

type recordingModelExecutor struct {
	modelWorkOnlyExecutor
	got []kernel.ModelWorkExecution
	err error
}

func (e *recordingModelExecutor) ExecuteModelWork(_ context.Context, input kernel.ModelWorkExecution) error {
	e.got = append(e.got, input)
	return e.err
}

func modelClaim(runtimeID uuid.UUID, kind executionstore.ModelWorkKind) executionstore.ClaimedAgentWork {
	claim := executionstore.ClaimedAgentWork{
		LocalLeaseBudgetStartedAt: time.Now(),
		OrgID:                     uuid.UUID{9},
		ProjectID:                 uuid.UUID{1},
		AgentID:                   uuid.UUID{2},
		Kind:                      executionstore.AgentWorkModel,
		RuntimeLock:               executionstore.AgentRuntimeLockRecord{ID: runtimeID},
		Model: executionstore.ClaimedModelWork{
			Kind:                 kind,
			TurnID:               uuid.UUID{4},
			InputIDs:             []uuid.UUID{{5}},
			OpeningEventSequence: 7,
		},
	}
	if kind == executionstore.ModelWorkCollect {
		claim.Model.ModelCallContextID = uuid.UUID{6}
		claim.Model.BatchRequestID = uuid.UUID{8}
	}
	return claim
}

func TestWorkerKeepsRuntimeLockWhenModelWorkParks(t *testing.T) {
	runtimeID := uuid.UUID{3}
	store := &parkedBatchStore{retainedRuntimeStore: retainedRuntimeStore{
		claim:    modelClaim(runtimeID, executionstore.ModelWorkStart),
		released: make(chan uuid.UUID, 1),
	}}
	executor := &recordingModelExecutor{err: kernel.ErrModelWorkParked}
	worker := NewWorker(store, executor, Options{RuntimeLockLeaseDuration: 15 * time.Second, Capacity: 1})

	worked, err := worker.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("run parked model work: worked=%v err=%v", worked, err)
	}
	select {
	case released := <-store.released:
		t.Fatalf("runtime %s was released although the model work parked it", released)
	default:
	}
	worker.activeMu.Lock()
	_, active := worker.active[runtimeID]
	worker.activeMu.Unlock()
	if active {
		t.Fatal("worker kept the parked runtime attached to a goroutine")
	}
	worker.retained.Wait()
}

func TestWorkerResumesParkedBatchBeforeClaimingNewWork(t *testing.T) {
	runtimeID := uuid.UUID{3}
	parked := modelClaim(runtimeID, executionstore.ModelWorkCollect)
	store := &parkedBatchStore{
		retainedRuntimeStore: retainedRuntimeStore{released: make(chan uuid.UUID, 1)},
		parked:               &parked,
	}
	executor := &recordingModelExecutor{}
	worker := NewWorker(store, executor, Options{RuntimeLockLeaseDuration: 15 * time.Second, Capacity: 1})

	worked, err := worker.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("run resumed batch: worked=%v err=%v", worked, err)
	}
	if store.normalCalls != 0 {
		t.Fatalf("worker claimed new work %d times while a parked batch was ready", store.normalCalls)
	}
	if len(executor.got) != 1 {
		t.Fatalf("model work executions = %d, want 1", len(executor.got))
	}
	got := executor.got[0]
	if got.Kind != executionstore.ModelWorkCollect || got.BatchRequestID != (uuid.UUID{8}) ||
		got.ModelCallContextID != (uuid.UUID{6}) || got.RuntimeLockID != runtimeID {
		t.Fatalf("resumed model work = %+v", got)
	}
	select {
	case released := <-store.released:
		if released != runtimeID {
			t.Fatalf("released runtime %s, want %s", released, runtimeID)
		}
	default:
		t.Fatal("runtime was not released after the batch result was handled")
	}
}
