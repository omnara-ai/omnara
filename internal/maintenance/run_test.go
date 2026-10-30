package maintenance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/observability/metrics"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
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

func TestMaintenanceJobDoesNotOverlapItself(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		release := make(chan struct{})
		var calls atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)
			runMaintenanceJob(ctx, time.Second, nil, maintenanceJob{
				"test", time.Second, func(context.Context) maintenanceResult {
					if calls.Add(1) == 1 {
						<-release
					}
					return maintenanceResult{}
				},
			})
		}()
		time.Sleep(time.Hour) //nolint:omnaralint // Advance synctest's virtual clock.
		require.EqualValues(t, 1, calls.Load(), "a blocked run must not start another")
		close(release)
		time.Sleep(1500 * time.Millisecond) //nolint:omnaralint // Advance synctest's virtual clock.
		cancel()
		<-done
		require.EqualValues(t, 2, calls.Load())
	})
}

func TestMaintenanceJobRecoversAndContinuesAfterPanic(t *testing.T) {
	var output bytes.Buffer
	ctx, cancel := context.WithCancel(logpkg.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&output, nil))))
	set := metrics.New()
	var calls atomic.Int32
	runMaintenanceJob(ctx, time.Millisecond, metrics.NewJobRecorder(set, metrics.SubsystemMaintenance), maintenanceJob{
		"test", time.Millisecond, func(context.Context) maintenanceResult {
			if calls.Add(1) == 1 {
				panic("test panic")
			}
			cancel()
			return maintenanceResult{}
		},
	})
	require.EqualValues(t, 2, calls.Load())
	require.Contains(t, output.String(), `"maintenance.job.name":"test"`)
	require.Contains(t, output.String(), "maintenance job panicked: test panic")
	require.Contains(t, output.String(), `"error.stack"`)
	response := httptest.NewRecorder()
	set.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Contains(t, response.Body.String(), `omnara_maintenance_job_runs_total{job="test",result="error"} 1`)
	require.Contains(t, response.Body.String(), `omnara_maintenance_job_runs_total{job="test",result="canceled"} 1`)
}

func TestMaintenanceJobRerunsWhileBehindThenWaitsItsInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		start := time.Now()
		var runs []time.Duration
		done := make(chan struct{})
		go func() {
			defer close(done)
			runMaintenanceJob(ctx, time.Second, nil, maintenanceJob{
				"test", time.Hour, func(context.Context) maintenanceResult {
					runs = append(runs, time.Since(start))
					return maintenanceResult{more: len(runs) < 3}
				},
			})
		}()
		time.Sleep(67 * time.Minute) //nolint:omnaralint // Advance synctest's virtual clock.
		cancel()
		<-done
		require.Len(t, runs, 4)
		require.Equal(t, []time.Duration{0, time.Second, 2 * time.Second}, runs[:3])
		require.GreaterOrEqual(t, runs[3]-runs[2], 54*time.Minute)
		require.LessOrEqual(t, runs[3]-runs[2], 66*time.Minute)
	})
}

func TestMaintenanceJobBacksOffAfterFailuresThenWaitsItsInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(logpkg.WithLogger(t.Context(), slog.New(slog.DiscardHandler)))
		start := time.Now()
		var runs []time.Duration
		done := make(chan struct{})
		go func() {
			defer close(done)
			runMaintenanceJob(ctx, time.Second, nil, maintenanceJob{
				"test", time.Hour, func(context.Context) maintenanceResult {
					runs = append(runs, time.Since(start))
					if len(runs) < 5 || len(runs) == 6 {
						return maintenanceResult{err: errors.New("store failed")}
					}
					return maintenanceResult{}
				},
			})
		}()
		time.Sleep(67 * time.Minute) //nolint:omnaralint // Advance synctest's virtual clock.
		cancel()
		<-done
		require.Len(t, runs, 7)
		require.Equal(t, []time.Duration{0, time.Second, 3 * time.Second, 7 * time.Second, 15 * time.Second}, runs[:5])
		require.GreaterOrEqual(t, runs[5]-runs[4], 54*time.Minute)
		require.LessOrEqual(t, runs[5]-runs[4], 66*time.Minute)
		require.Equal(t, time.Second, runs[6]-runs[5], "a success must reset the backoff")
	})
}

func TestMaintenanceJobBehindNeverWaitsLongerThanItsInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		var calls atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)
			runMaintenanceJob(ctx, time.Hour, nil, maintenanceJob{
				"test", time.Second, func(context.Context) maintenanceResult { calls.Add(1); return maintenanceResult{more: true} },
			})
		}()
		time.Sleep(10 * time.Second) //nolint:omnaralint // Advance synctest's virtual clock.
		cancel()
		<-done
		require.GreaterOrEqual(t, calls.Load(), int32(10), "behind must never wait longer than the job's interval")
	})
}

func TestMaintenanceJobsDoNotWaitOnEachOther(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		release := make(chan struct{})
		var fastCalls atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)
			runMaintenanceJobs(ctx, time.Second, nil, []maintenanceJob{
				{"blocked", time.Second, func(context.Context) maintenanceResult { <-release; return maintenanceResult{} }},
				{"fast", time.Second, func(context.Context) maintenanceResult { fastCalls.Add(1); return maintenanceResult{} }},
			})
		}()
		time.Sleep(10 * time.Second) //nolint:omnaralint // Advance synctest's virtual clock.
		require.GreaterOrEqual(t, fastCalls.Load(), int32(9), "a blocked job must not hold up another")
		cancel()
		close(release)
		<-done
	})
}

func TestMaintenanceJobsReturnWithoutRunningWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	runMaintenanceJobs(ctx, time.Millisecond, nil, []maintenanceJob{
		{"never", time.Millisecond, func(context.Context) maintenanceResult {
			t.Error("canceled maintenance ran a job")
			return maintenanceResult{}
		}},
	})
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
