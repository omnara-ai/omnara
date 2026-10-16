package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
