package worker

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/harness/kernel"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type continuationStore struct {
	retainedRuntimeStore
	next         []executionstore.ClaimedAgentWork
	advances     []executionstore.AdvanceOwnedAgentWorkInput
	advanceError error
	advancePanic bool
	advanceDelay time.Duration
}

func (s *continuationStore) AdvanceOwnedAgentWork(
	ctx context.Context,
	input executionstore.AdvanceOwnedAgentWorkInput,
) (executionstore.ClaimedAgentWork, bool, error) {
	s.advances = append(s.advances, input)
	if s.advanceDelay > 0 {
		time.Sleep(s.advanceDelay) //nolint:omnaralint // Exercise elapsed time; no concurrent state is being awaited.
	}
	if s.advancePanic {
		panic("advance failed")
	}
	if s.advanceError != nil {
		return executionstore.ClaimedAgentWork{}, false, s.advanceError
	}
	if len(s.next) == 0 || (s.next[0].Kind == executionstore.AgentWorkModel && !input.AllowModelWork) {
		err := s.ReleaseAgentRuntimeLock(ctx, input.ProjectID, input.AgentID, input.RuntimeLockID)
		return executionstore.ClaimedAgentWork{}, false, err
	}
	claim := s.next[0]
	s.next = s.next[1:]
	return claim, true, nil
}

type continuationExecutor struct {
	kinds     []executionstore.AgentWorkKind
	runtimes  []uuid.UUID
	afterStep func(context.Context) error
}

func (e *continuationExecutor) execute(
	ctx context.Context,
	kind executionstore.AgentWorkKind,
	runtime uuid.UUID,
) error {
	e.kinds = append(e.kinds, kind)
	e.runtimes = append(e.runtimes, runtime)
	if e.afterStep != nil {
		return e.afterStep(ctx)
	}
	return nil
}

func (e *continuationExecutor) ExecuteModelWork(
	ctx context.Context,
	input kernel.ModelWorkExecution,
) error {
	return e.execute(ctx, executionstore.AgentWorkModel, input.RuntimeLockID)
}

func (e *continuationExecutor) ExecuteToolWork(
	ctx context.Context,
	input kernel.ToolWorkExecution,
) error {
	return e.execute(ctx, executionstore.AgentWorkTool, input.RuntimeLockID)
}

func newContinuationStore(kinds ...executionstore.AgentWorkKind) *continuationStore {
	claim := executionstore.ClaimedAgentWork{
		OrgID: uuid.New(), ProjectID: uuid.New(), AgentID: uuid.New(),
		Kind:        executionstore.AgentWorkModel,
		RuntimeLock: executionstore.AgentRuntimeLockRecord{ID: uuid.New()},
	}
	s := &continuationStore{retainedRuntimeStore: retainedRuntimeStore{claim: claim, released: make(chan uuid.UUID, 8)}}
	for _, kind := range kinds {
		next := claim
		next.Kind = kind
		s.next = append(s.next, next)
	}
	return s
}

func TestWorkerOwnedContinuationBudget(t *testing.T) {
	model, tool := executionstore.AgentWorkModel, executionstore.AgentWorkTool
	for _, test := range []struct {
		name    string
		options *ContinuationOptions
		want    []executionstore.AgentWorkKind
	}{
		{"default", nil, []executionstore.AgentWorkKind{model, tool, model, tool, model, tool}},
		{
			"one additional model", &ContinuationOptions{MaxModelStarts: 1},
			[]executionstore.AgentWorkKind{model, tool, model, tool},
		},
		{"disabled", &ContinuationOptions{}, []executionstore.AgentWorkKind{model}},
		{
			"elapsed", &ContinuationOptions{MaxModelStarts: 5, MaxDuration: time.Nanosecond},
			[]executionstore.AgentWorkKind{model},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newContinuationStore(tool, model, tool, model, tool, model)
			executor := &continuationExecutor{}
			worker := NewWorker(store, executor, Options{Continuation: test.options})
			worked, err := worker.RunOnce(t.Context())
			if err != nil || !worked {
				t.Fatalf("run: worked=%v err=%v", worked, err)
			}
			if !reflect.DeepEqual(executor.kinds, test.want) {
				t.Fatalf("steps=%v want=%v", executor.kinds, test.want)
			}
			for _, runtime := range executor.runtimes {
				if runtime != store.claim.RuntimeLock.ID {
					t.Fatalf("runtime changed: %s", runtime)
				}
			}
			if store.renewalCalls.Load() != 1 {
				t.Fatalf("initial renewals=%d want=1", store.renewalCalls.Load())
			}
			if len(store.released) != 1 {
				t.Fatalf("releases=%d want=1", len(store.released))
			}
			if runtime := <-store.released; runtime != store.claim.RuntimeLock.ID {
				t.Fatalf("released %s", runtime)
			}
		})
	}
}

func TestWorkerOwnedContinuationFailureReleasesOriginalRuntime(t *testing.T) {
	for _, test := range []struct {
		name  string
		err   error
		panic bool
	}{
		{"store error", errors.New("database unavailable"), false},
		{"inactive lease", storeerr.ErrRuntimeLockInactive, false},
		{"panic", nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newContinuationStore()
			store.advanceError, store.advancePanic = test.err, test.panic
			worker := NewWorker(store, &continuationExecutor{}, Options{})
			worked, err := worker.RunOnce(t.Context())
			if !worked || err == nil {
				t.Fatalf("worked=%v err=%v", worked, err)
			}
			if test.err != nil && !errors.Is(err, test.err) {
				t.Fatalf("error=%v want=%v", err, test.err)
			}
			if len(store.released) != 1 || <-store.released != store.claim.RuntimeLock.ID {
				t.Fatal("original runtime was not released once")
			}
			if len(worker.active) != 0 {
				t.Fatal("runtime still registered")
			}
		})
	}
}

func TestWorkerOwnedContinuationCancellationBetweenSteps(t *testing.T) {
	store := newContinuationStore(executionstore.AgentWorkTool)
	executor := &continuationExecutor{}
	worker := NewWorker(store, executor, Options{})
	executor.afterStep = func(context.Context) error {
		worker.activeMu.Lock()
		active := worker.active[store.claim.RuntimeLock.ID]
		worker.activeMu.Unlock()
		active.cancelRequested.Store(true)
		active.cancel()
		return nil
	}
	worked, err := worker.RunOnce(t.Context())
	if err != nil || !worked {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	if len(store.advances) != 0 || len(store.released) != 1 {
		t.Fatalf("advances=%d releases=%d", len(store.advances), len(store.released))
	}
}

func TestWorkerOwnedContinuationKeepsRenewalRunning(t *testing.T) {
	store := newContinuationStore(executionstore.AgentWorkModel)
	store.renewed = make(chan struct{})
	executor := &continuationExecutor{}
	executor.afterStep = func(ctx context.Context) error {
		if len(executor.kinds) == 2 {
			select {
			case <-store.renewed:
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return errors.New("renewal stopped between steps")
			}
		}
		return nil
	}
	worker := NewWorker(store, executor, Options{RuntimeLockLeaseDuration: 150 * time.Millisecond})
	worked, err := worker.RunOnce(t.Context())
	if err != nil || !worked {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	if len(executor.kinds) != 2 || len(store.released) != 1 {
		t.Fatalf("steps=%v releases=%d", executor.kinds, len(store.released))
	}
}

func TestWorkerOwnedContinuationCountsTimeSpentAdvancing(t *testing.T) {
	store := newContinuationStore(executionstore.AgentWorkModel)
	store.advanceDelay = 110 * time.Millisecond
	executor := &continuationExecutor{}
	worker := NewWorker(store, executor, Options{
		Continuation: &ContinuationOptions{MaxModelStarts: 2, MaxDuration: 100 * time.Millisecond},
	})
	worked, err := worker.RunOnce(t.Context())
	if err != nil || !worked {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	if len(executor.kinds) != 1 || len(store.advances) != 1 || len(store.released) != 1 {
		t.Fatalf("steps=%v advances=%d releases=%d", executor.kinds, len(store.advances), len(store.released))
	}
}

func TestWorkerUsesRemainingClaimLeaseBudget(t *testing.T) {
	for _, test := range []struct {
		name         string
		age          time.Duration
		wantRenewals int32
	}{
		{"fresh", 0, 0}, {"delayed beyond local cutoff", time.Minute, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newContinuationStore()
			store.claim.LocalLeaseBudgetStartedAt = time.Now().Add(-test.age)
			worker := NewWorker(store, &fusedContinuationTestExecutor{}, Options{RuntimeLockLeaseDuration: time.Minute})
			worked, err := worker.RunOnce(t.Context())
			if err != nil || !worked {
				t.Fatalf("worked=%v err=%v", worked, err)
			}
			if got := store.renewalCalls.Load(); got != test.wantRenewals {
				t.Fatalf("renewals=%d want=%d", got, test.wantRenewals)
			}
		})
	}
}

type fusedContinuationTestExecutor struct{ continuationExecutor }

func (e *fusedContinuationTestExecutor) ExecuteModelWorkAndAdvance(
	ctx context.Context, input kernel.ModelWorkExecution, _ kernel.ModelWorkAdvanceOptions,
) (executionstore.OwnedAgentWorkTransition, bool, error) {
	return executionstore.OwnedAgentWorkTransition{}, false, e.ExecuteModelWork(ctx, input)
}
