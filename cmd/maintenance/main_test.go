package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/metrics"
)

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

func TestCompletedMaintenanceOutcome(t *testing.T) {
	failure := errors.New("provider failed")
	tests := []struct {
		name            string
		canceled        bool
		err             error
		wantInterrupted bool
		wantErr         error
	}{
		{name: "success"},
		{name: "live cancellation is a failure", err: context.Canceled, wantErr: context.Canceled},
		{name: "shutdown without operation error", canceled: true, wantInterrupted: true},
		{name: "shutdown cancellation", canceled: true, err: context.Canceled, wantInterrupted: true},
		{
			name:            "wrapped shutdown cancellation",
			canceled:        true,
			err:             fmt.Errorf("query: %w", context.Canceled),
			wantInterrupted: true,
		},
		{name: "shutdown failure", canceled: true, err: failure, wantInterrupted: true, wantErr: failure},
		{
			name:            "shutdown mixed error",
			canceled:        true,
			err:             errors.Join(context.Canceled, failure),
			wantInterrupted: true,
			wantErr:         failure,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.canceled {
				cancel()
			}
			outcome := completedMaintenanceOutcome(ctx, test.err)
			if outcome.interrupted != test.wantInterrupted {
				t.Fatalf("interrupted = %t, want %t", outcome.interrupted, test.wantInterrupted)
			}
			if test.wantErr == nil && outcome.err != nil {
				t.Fatalf("err = %v, want nil", outcome.err)
			}
			if test.wantErr != nil && !errors.Is(outcome.err, test.wantErr) {
				t.Fatalf("err = %v, want %v", outcome.err, test.wantErr)
			}
		})
	}
}

func TestProviderRuntimeResultUsesCompletedOutcome(t *testing.T) {
	for _, test := range []struct {
		name    string
		outcome maintenanceOutcome
		want    metrics.ProviderRuntimeResult
	}{
		{name: "success", want: metrics.ProviderRuntimeResultSuccess},
		{
			name:    "failure",
			outcome: maintenanceOutcome{err: errors.New("provider failed")},
			want:    metrics.ProviderRuntimeResultError,
		},
		{
			name:    "shutdown cancellation",
			outcome: maintenanceOutcome{interrupted: true},
			want:    metrics.ProviderRuntimeResultCanceled,
		},
		{
			name: "failure during shutdown",
			outcome: maintenanceOutcome{
				interrupted: true,
				err:         errors.New("provider failed"),
			},
			want: metrics.ProviderRuntimeResultError,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := providerRuntimeResult(test.outcome); got != test.want {
				t.Fatalf("provider runtime result = %q, want %q", got, test.want)
			}
		})
	}
}
