package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/observability/metrics"
)

func TestWorkerRecordsSlotAndExecutionMetrics(t *testing.T) {
	const capacity = 2
	set := metrics.New()
	executor := &holdingModelExecutor{release: make(chan struct{}), started: make(chan struct{}, capacity+1)}
	worker := NewWorker(&dispatchStore{}, executor, Options{
		Metrics:                  metrics.NewWorkerRecorder(set),
		Log:                      slog.New(slog.NewTextHandler(io.Discard, nil)),
		RuntimeLockLeaseDuration: time.Minute,
		Capacity:                 capacity,
		ClaimConcurrency:         1,
		ControlSubscriber:        noopControlSubscriber{},
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- worker.Run(ctx) }()
	for range capacity {
		select {
		case <-executor.started:
		case <-time.After(2 * time.Second):
			t.Fatal("worker did not fill its slots")
		}
	}
	requireWorkerMetrics(t, set,
		"omnara_worker_capacity 2",
		"omnara_worker_slots_used 2",
		"omnara_worker_executions_active 2",
		"omnara_worker_ready_to_claim_seconds_count 2",
		`omnara_worker_ready_to_claim_seconds_bucket{le="1"} 2`,
	)

	executor.release <- struct{}{}
	select {
	case <-executor.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not refill the released slot")
	}
	cancel()
	if err := <-runDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v, want context canceled", err)
	}
	requireWorkerMetrics(t, set,
		"omnara_worker_slots_used 0",
		"omnara_worker_executions_active 0",
		`omnara_worker_executions_total{kind="model",outcome="success"} 1`,
		`omnara_worker_executions_total{kind="model",outcome="canceled"} 2`,
		`omnara_worker_execution_duration_seconds_count{kind="model",outcome="success"} 1`,
	)
}

func requireWorkerMetrics(t *testing.T, set *metrics.Set, want ...string) {
	t.Helper()
	response := httptest.NewRecorder()
	set.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, metrics.ScrapePath, nil))
	body := response.Body.String()
	for _, line := range want {
		if !strings.Contains(body, line) {
			t.Fatalf("missing %q in:\n%s", line, body)
		}
	}
}

func TestClaimLoopSamplesBacklogOncePerIntervalWhileSlotsAreBusy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		set := metrics.New()
		var samples atomic.Int32
		executor := &holdingModelExecutor{release: make(chan struct{}), started: make(chan struct{}, 1)}
		worker := NewWorker(&dispatchStore{}, executor, Options{
			Metrics:        metrics.NewWorkerRecorder(set),
			BacklogMetrics: metrics.NewAgentBacklogRecorder(set),
			SampleReadyBacklog: func(ctx context.Context) (executionstore.ReadyAgentBacklog, error) {
				if _, ok := ctx.Deadline(); !ok {
					t.Error("backlog sample requires a deadline")
				}
				samples.Add(1)
				return executionstore.ReadyAgentBacklog{Ready: 4, OldestReadyAge: 9 * time.Second}, nil
			},
			Log:                      slog.New(slog.NewTextHandler(io.Discard, nil)),
			RuntimeLockLeaseDuration: time.Hour,
			Capacity:                 1,
			ClaimConcurrency:         1,
			ControlSubscriber:        noopControlSubscriber{},
		})
		ctx, cancel := context.WithCancel(t.Context())
		runDone := make(chan error, 1)
		go func() { runDone <- worker.Run(ctx) }()
		<-executor.started
		synctest.Wait()
		if got := samples.Load(); got != 1 {
			t.Fatalf("samples at start = %d, want 1", got)
		}
		requireWorkerMetrics(t, set,
			"omnara_worker_slots_used 1",
			"omnara_agent_ready_backlog 4",
			"omnara_agent_oldest_ready_age_seconds 9",
		)
		time.Sleep(backlogSampleInterval * 85 / 100) //nolint:omnaralint // Advance synctest's virtual clock.
		synctest.Wait()
		if got := samples.Load(); got != 1 {
			t.Fatalf("samples before interval = %d, want 1", got)
		}
		time.Sleep(backlogSampleInterval * 30 / 100) //nolint:omnaralint // Advance synctest's virtual clock.
		synctest.Wait()
		if got := samples.Load(); got != 2 {
			t.Fatalf("samples after interval with every slot busy = %d, want 2", got)
		}
		cancel()
		if err := <-runDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("run error = %v, want context canceled", err)
		}
	})
}

func TestBacklogSamplerRecoversPanics(t *testing.T) {
	set := metrics.New()
	calls := 0
	sampler := newBacklogSampler(func(context.Context) (executionstore.ReadyAgentBacklog, error) {
		calls++
		panic("test failure")
	}, metrics.NewAgentBacklogRecorder(set), slog.New(slog.NewTextHandler(io.Discard, nil)))
	sampler.sampleIfDue(t.Context())
	sampler.sampleIfDue(t.Context())
	if calls != 1 {
		t.Fatalf("samples = %d, want 1 per interval", calls)
	}
	requireWorkerMetrics(t, set, "omnara_agent_ready_backlog NaN")
	var disabled *backlogSampler
	disabled.sampleIfDue(t.Context())
	if disabled.due() != nil {
		t.Fatal("disabled sampler must never be due")
	}
}
