package worker

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type programLoopStore struct {
	mu       sync.Mutex
	claims   []executionstore.ProgramToolCallClaim
	claimIDs []uuid.UUID
	renewErr error
	renewals atomic.Int32
	reaps    atomic.Int32
}

func (s *programLoopStore) ClaimNextProgramToolCall(
	_ context.Context, claimID uuid.UUID, _ time.Duration,
) (executionstore.ProgramToolCallClaim, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.claims) == 0 {
		return executionstore.ProgramToolCallClaim{}, false, nil
	}
	claim := s.claims[0]
	s.claims = s.claims[1:]
	claim.ClaimID = claimID
	s.claimIDs = append(s.claimIDs, claimID)
	return claim, true, nil
}

func (s *programLoopStore) RenewProgramToolCallClaim(
	_ context.Context, ref executionstore.ProgramToolCallRef, lease time.Duration,
) (time.Time, error) {
	s.renewals.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if ref.ClaimID != s.claimIDs[len(s.claimIDs)-1] {
		return time.Time{}, storeerr.ErrProgramToolCallClaimLost
	}
	return time.Now().Add(lease), s.renewErr
}

func (s *programLoopStore) ReapProgramToolCalls(context.Context, int) (int, error) {
	s.reaps.Add(1)
	return 0, nil
}

// programLoopExecutor starts async work that runs until released or canceled.
type programLoopExecutor struct {
	release  chan struct{}
	canceled chan struct{}
	finished chan struct{}
}

func (e *programLoopExecutor) RunProgramToolCall(
	ctx context.Context, _ executionstore.ProgramToolCallClaim, _ time.Duration,
) error {
	reservation, err := tools.ReserveAsyncExecution(ctx)
	if err != nil {
		return err
	}
	reservation.Start()
	go func() {
		defer close(e.finished)
		defer reservation.Done(nil)
		select {
		case <-e.release:
		case <-ctx.Done():
			close(e.canceled)
		}
	}()
	return nil
}

func newProgramLoopWorker(store *programLoopStore, executor *programLoopExecutor) *Worker {
	return NewWorker(nil, nil, Options{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ProgramToolStore: store,
		ProgramToolExecutor: executor, ProgramToolLease: 30 * time.Second,
	})
}

func TestProgramToolLoopRenewsClaimUntilAsyncWorkFinishes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &programLoopStore{claims: []executionstore.ProgramToolCallClaim{{
			ProgramToolCallRecord: executionstore.ProgramToolCallRecord{
				ToolCallRecord: executionstore.ToolCallRecord{ID: uuid.New(), Name: "storage_list"},
				ProcessID:      uuid.New(),
			},
			Phase: executionstore.ProgramToolCallPhaseExecute,
		}}}
		executor := &programLoopExecutor{release: make(chan struct{}), canceled: make(chan struct{}),
			finished: make(chan struct{})}
		w := newProgramLoopWorker(store, executor)
		ctx, cancel := context.WithCancel(t.Context())
		var calls sync.WaitGroup
		done := make(chan struct{})
		go func() {
			defer close(done)
			w.runProgramTools(ctx, context.WithoutCancel(ctx), &calls)
		}()
		time.Sleep(time.Minute) //nolint:omnaralint // synctest advances virtual time to drive the renewal ticker.
		synctest.Wait()
		if renewals := store.renewals.Load(); renewals < 2 {
			t.Fatalf("renewals during async work = %d, want at least 2", renewals)
		}
		if store.reaps.Load() == 0 {
			t.Fatal("program tool reaper did not run")
		}
		close(executor.release)
		<-executor.finished
		synctest.Wait()
		renewals := store.renewals.Load()
		time.Sleep(time.Minute) //nolint:omnaralint // synctest advances virtual time to drive the renewal ticker.
		synctest.Wait()
		if store.renewals.Load() != renewals {
			t.Fatal("claim renewal continued after the call finished")
		}
		cancel()
		<-done
		calls.Wait()
	})
}

func TestProgramToolLoopCancelsCallWhenClaimIsLost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &programLoopStore{renewErr: storeerr.ErrProgramToolCallClaimLost,
			claims: []executionstore.ProgramToolCallClaim{{
				ProgramToolCallRecord: executionstore.ProgramToolCallRecord{
					ToolCallRecord: executionstore.ToolCallRecord{ID: uuid.New()}, ProcessID: uuid.New(),
				},
				Phase: executionstore.ProgramToolCallPhaseExecute,
			}}}
		executor := &programLoopExecutor{release: make(chan struct{}), canceled: make(chan struct{}),
			finished: make(chan struct{})}
		w := newProgramLoopWorker(store, executor)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var calls sync.WaitGroup
		go w.runProgramTools(ctx, context.WithoutCancel(ctx), &calls)
		select {
		case <-executor.canceled:
		case <-time.After(time.Minute):
			t.Fatal("losing the claim did not cancel the call")
		}
		cancel()
		calls.Wait()
	})
}
