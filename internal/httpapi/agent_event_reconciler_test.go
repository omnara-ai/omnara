package httpapi

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type recordingAgentEventFrontierReader struct {
	mu        sync.Mutex
	frontiers map[executionstore.ID]int64
	calls     [][]executionstore.ID
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
}

type tickerReadyClock struct {
	clock.Clock
	ready chan struct{}
	once  sync.Once
}

func (c *tickerReadyClock) Ticker(interval time.Duration) *clock.Ticker {
	ticker := c.Clock.Ticker(interval)
	c.once.Do(func() { close(c.ready) })
	return ticker
}

func (r *recordingAgentEventFrontierReader) ListAgentEventFrontiers(
	ctx context.Context,
	agentIDs []executionstore.ID,
) ([]executionstore.AgentEventFrontier, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]executionstore.ID(nil), agentIDs...))
	frontiers := make(map[executionstore.ID]int64, len(r.frontiers))
	for agentID, sequence := range r.frontiers {
		frontiers[agentID] = sequence
	}
	r.mu.Unlock()
	if r.started != nil {
		r.startOnce.Do(func() { close(r.started) })
	}
	if r.release != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.release:
		}
	}
	result := make([]executionstore.AgentEventFrontier, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		if sequence, ok := frontiers[agentID]; ok {
			result = append(result, executionstore.AgentEventFrontier{
				AgentID:       agentID,
				EventSequence: sequence,
			})
		}
	}
	return result, nil
}

func (r *recordingAgentEventFrontierReader) setFrontier(
	agentID executionstore.ID,
	sequence int64,
) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frontiers[agentID] = sequence
}

func (r *recordingAgentEventFrontierReader) recordedCalls() [][]executionstore.ID {
	r.mu.Lock()
	defer r.mu.Unlock()
	calls := make([][]executionstore.ID, len(r.calls))
	for index, call := range r.calls {
		calls[index] = append([]executionstore.ID(nil), call...)
	}
	return calls
}

func TestAgentEventStreamReconcilerBatchesAgentsAndSignalsLaggingStreams(t *testing.T) {
	agentA := uuid.New()
	agentB := uuid.New()
	agentC := uuid.New()
	reader := &recordingAgentEventFrontierReader{frontiers: map[executionstore.ID]int64{
		agentA: 5,
		agentB: 2,
		agentC: 0,
	}}
	reconciler := newAgentEventStreamReconciler(
		discardLogger(),
		reader,
		clock.NewMock(),
		time.Hour,
		2,
	)
	t.Cleanup(reconciler.close)

	laggingA := make(chan struct{}, 1)
	currentA := make(chan struct{}, 1)
	laggingB := make(chan struct{}, 1)
	currentC := make(chan struct{}, 1)
	registrationA, ok := reconciler.register(agentA, 4, laggingA)
	if !ok {
		t.Fatal("register lagging agent A stream")
	}
	defer registrationA.unregister()
	registrationA2, ok := reconciler.register(agentA, 5, currentA)
	if !ok {
		t.Fatal("register current agent A stream")
	}
	defer registrationA2.unregister()
	registrationB, ok := reconciler.register(agentB, 1, laggingB)
	if !ok {
		t.Fatal("register lagging agent B stream")
	}
	defer registrationB.unregister()
	registrationC, ok := reconciler.register(agentC, 0, currentC)
	if !ok {
		t.Fatal("register current agent C stream")
	}
	defer registrationC.unregister()

	if err := reconciler.reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile streams: %v", err)
	}
	assertSignal(t, laggingA, true)
	assertSignal(t, currentA, false)
	assertSignal(t, laggingB, true)
	assertSignal(t, currentC, false)

	calls := reader.recordedCalls()
	if len(calls) != 2 {
		t.Fatalf("frontier query calls=%d, want 2 batches", len(calls))
	}
	seen := make(map[executionstore.ID]int)
	for _, call := range calls {
		if len(call) > 2 {
			t.Fatalf("frontier query batch size=%d, want at most 2", len(call))
		}
		for _, agentID := range call {
			seen[agentID]++
		}
	}
	for _, agentID := range []executionstore.ID{agentA, agentB, agentC} {
		if seen[agentID] != 1 {
			t.Fatalf("agent %s queried %d times, want once", agentID, seen[agentID])
		}
	}

	registrationA.advance(5)
	registrationA.advance(3)
	if err := reconciler.reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile advanced stream: %v", err)
	}
	assertSignal(t, laggingA, false)
}

func TestAgentEventStreamReconcilerUsesCursorAfterFrontierRead(t *testing.T) {
	agentID := uuid.New()
	reader := &recordingAgentEventFrontierReader{
		frontiers: map[executionstore.ID]int64{agentID: 5},
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	reconciler := newAgentEventStreamReconciler(
		discardLogger(),
		reader,
		clock.NewMock(),
		time.Hour,
		defaultAgentEventReconciliationBatchSize,
	)
	t.Cleanup(reconciler.close)
	notify := make(chan struct{}, 1)
	registration, ok := reconciler.register(agentID, 1, notify)
	if !ok {
		t.Fatal("register stream")
	}
	defer registration.unregister()

	reconciled := make(chan error, 1)
	go func() { reconciled <- reconciler.reconcile(context.Background()) }()
	<-reader.started
	registration.advance(5)
	close(reader.release)
	if err := <-reconciled; err != nil {
		t.Fatalf("reconcile streams: %v", err)
	}
	assertSignal(t, notify, false)

	reader.setFrontier(agentID, 6)
	if err := reconciler.reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile newer frontier: %v", err)
	}
	assertSignal(t, notify, true)

	registration.unregister()
	reader.mu.Lock()
	reader.calls = nil
	reader.mu.Unlock()
	if err := reconciler.reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile after unregister: %v", err)
	}
	if calls := reader.recordedCalls(); len(calls) != 0 {
		t.Fatalf("frontier queries after unregister=%d, want 0", len(calls))
	}

	reconciler.close()
	if _, ok := reconciler.register(agentID, 0, notify); ok {
		t.Fatal("registered stream after reconciler close")
	}
}

func TestAgentEventStreamReconcilerRunsOnItsProcessTicker(t *testing.T) {
	agentID := uuid.New()
	reader := &recordingAgentEventFrontierReader{
		frontiers: map[executionstore.ID]int64{agentID: 2},
	}
	timer := clock.NewMock()
	readyTimer := &tickerReadyClock{Clock: timer, ready: make(chan struct{})}
	reconciler := newAgentEventStreamReconciler(
		discardLogger(),
		reader,
		readyTimer,
		time.Second,
		defaultAgentEventReconciliationBatchSize,
	)
	t.Cleanup(reconciler.close)
	notify := make(chan struct{}, 1)
	registration, ok := reconciler.register(agentID, 1, notify)
	if !ok {
		t.Fatal("register stream")
	}
	defer registration.unregister()

	<-readyTimer.ready
	timer.Add(time.Second)
	select {
	case <-notify:
		return
	case <-time.After(time.Second):
		t.Fatal("process reconciliation ticker did not signal lagging stream")
	}
}

func TestAgentEventStreamReconcilerHonorsCallerCancellation(t *testing.T) {
	agentID := uuid.New()
	reader := &recordingAgentEventFrontierReader{
		frontiers: map[executionstore.ID]int64{agentID: 2},
		release:   make(chan struct{}),
	}
	reconciler := newAgentEventStreamReconciler(
		discardLogger(),
		reader,
		clock.NewMock(),
		time.Hour,
		defaultAgentEventReconciliationBatchSize,
	)
	t.Cleanup(reconciler.close)
	registration, ok := reconciler.register(agentID, 1, make(chan struct{}, 1))
	if !ok {
		t.Fatal("register stream")
	}
	defer registration.unregister()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := reconciler.reconcile(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked reconciliation error=%v, want deadline exceeded", err)
	}
}

func TestAgentEventStreamReconcilerCloseCancelsBlockedSweep(t *testing.T) {
	agentID := uuid.New()
	reader := &recordingAgentEventFrontierReader{
		frontiers: map[executionstore.ID]int64{agentID: 2},
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	timer := clock.NewMock()
	readyTimer := &tickerReadyClock{Clock: timer, ready: make(chan struct{})}
	reconciler := newAgentEventStreamReconciler(
		discardLogger(),
		reader,
		readyTimer,
		time.Second,
		defaultAgentEventReconciliationBatchSize,
	)
	t.Cleanup(reconciler.close)
	registration, ok := reconciler.register(agentID, 1, make(chan struct{}, 1))
	if !ok {
		t.Fatal("register stream")
	}
	defer registration.unregister()

	<-readyTimer.ready
	timer.Add(time.Second)
	<-reader.started
	closed := make(chan struct{})
	go func() {
		reconciler.close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("reconciler close did not cancel blocked sweep")
	}
}

func assertSignal(t *testing.T, signal <-chan struct{}, want bool) {
	t.Helper()
	select {
	case <-signal:
		if !want {
			t.Fatal("received unexpected reconciliation signal")
		}
	default:
		if want {
			t.Fatal("missing reconciliation signal")
		}
	}
}
