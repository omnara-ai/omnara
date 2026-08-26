package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/storage/dbconn"
)

func TestHandleSchemaVersionMismatchQuiesces(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	healthErr := make(chan error, 1)
	done := make(chan int, 1)
	go func() {
		done <- handleSchemaVersionMismatch(
			ctx,
			healthErr,
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			&dbconn.SchemaVersionMismatchError{Expected: 26, Actual: 25},
		)
	}()
	select {
	case code := <-done:
		t.Fatalf("quiesced maintenance returned early with %d", code)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	healthErr <- nil
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	case <-time.After(time.Second):
		t.Fatal("quiesced maintenance did not stop after cancellation")
	}
}

func TestJitteredMaintenanceDelayStaysWithinTenPercent(t *testing.T) {
	interval := 10 * time.Second
	var first time.Duration
	var varied bool
	for range 1000 {
		delay := jitteredMaintenanceDelay(interval)
		if delay < 9*time.Second || delay > 11*time.Second {
			t.Fatalf("jittered delay = %s, want between 9s and 11s", delay)
		}
		if first == 0 {
			first = delay
		} else if delay != first {
			varied = true
		}
	}
	if !varied {
		t.Fatalf("1000 jitter samples were all %s", first)
	}
}

func TestProviderRuntimeMaintenanceLoopRunsImmediatelyAndDoesNotOverlap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var calls atomic.Int32
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	loopDone := make(chan struct{})

	go func() {
		defer close(loopDone)
		runProviderRuntimeMaintenanceLoop(
			ctx,
			logger,
			nil,
			time.Millisecond,
			metrics.ProviderRuntimeOperationDiscovery,
			func(context.Context) (machinepool.RuntimeReconciliationStats, error) {
				call := calls.Add(1)
				current := inFlight.Add(1)
				maxInFlight.Store(max(maxInFlight.Load(), current))
				defer inFlight.Add(-1)
				if call == 1 {
					close(firstStarted)
					<-releaseFirst
				} else {
					cancel()
				}
				return machinepool.RuntimeReconciliationStats{}, nil
			},
		)
	}()

	<-firstStarted
	timer := time.NewTimer(10 * time.Millisecond)
	<-timer.C
	if calls.Load() != 1 {
		t.Fatalf("maintenance overlapped a blocked tick: calls = %d", calls.Load())
	}
	close(releaseFirst)
	select {
	case <-loopDone:
	case <-time.After(time.Second):
		t.Fatal("maintenance loop did not stop")
	}

	if calls.Load() != 2 || maxInFlight.Load() != 1 {
		t.Fatalf("maintenance calls = %d max in flight = %d, want 2 and 1", calls.Load(), maxInFlight.Load())
	}
}

func TestProviderRuntimeMaintenanceLoopRecoversAndContinuesAfterPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var calls atomic.Int32

	runProviderRuntimeMaintenanceLoop(
		ctx,
		logger,
		nil,
		time.Millisecond,
		metrics.ProviderRuntimeOperationDiscovery,
		func(context.Context) (machinepool.RuntimeReconciliationStats, error) {
			if calls.Add(1) == 1 {
				panic("test panic")
			}
			cancel()
			return machinepool.RuntimeReconciliationStats{}, nil
		},
	)

	if calls.Load() != 2 {
		t.Fatalf("maintenance calls after panic = %d, want 2", calls.Load())
	}
}

func TestIdleMachineDeletionMaintenanceTickRecoversAndAllowsNextTick(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	calls := 0
	reconcile := func(context.Context, int32) (int, error) {
		calls++
		if calls == 1 {
			panic("test panic")
		}
		return 1, nil
	}

	if candidateCount, err := runIdleMachineDeletionMaintenanceTick(
		context.Background(),
		logger,
		reconcile,
	); candidateCount != 0 || err != nil {
		t.Fatalf("result after panic = (%d, %v), want (0, nil)", candidateCount, err)
	}
	if candidateCount, err := runIdleMachineDeletionMaintenanceTick(
		context.Background(),
		logger,
		reconcile,
	); candidateCount != 1 || err != nil {
		t.Fatalf("result after recovery = (%d, %v), want (1, nil)", candidateCount, err)
	}
}

func TestProviderRuntimeResultDistinguishesShutdownCancellation(t *testing.T) {
	for _, test := range []struct {
		name        string
		err         error
		shutdownErr error
		want        metrics.ProviderRuntimeResult
	}{
		{name: "success", want: metrics.ProviderRuntimeResultSuccess},
		{name: "failure", err: errors.New("provider failed"), want: metrics.ProviderRuntimeResultError},
		{
			name:        "shutdown cancellation",
			err:         context.Canceled,
			shutdownErr: context.Canceled,
			want:        metrics.ProviderRuntimeResultCanceled,
		},
		{name: "unrelated cancellation", err: context.Canceled, want: metrics.ProviderRuntimeResultError},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := providerRuntimeResult(test.err, test.shutdownErr); got != test.want {
				t.Fatalf("provider runtime result = %q, want %q", got, test.want)
			}
		})
	}
}
