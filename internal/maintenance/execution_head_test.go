package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/observability/metrics"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
	"github.com/stretchr/testify/require"
)

func TestExecutionHeadTickOutcomes(t *testing.T) {
	for _, outcome := range []string{"success", "error", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(logpkg.WithLogger(t.Context(), slog.New(slog.DiscardHandler)))
			defer cancel()
			var logs bytes.Buffer
			set := metrics.New()
			recorder := metrics.NewJobRecorder(set, metrics.SubsystemMaintenance)
			run := sampleExecutionHeads(slog.New(slog.NewJSONHandler(&logs, nil)), recorder,
				func(context.Context, uuid.UUID, int32) (executionstore.ExecutionHeadSampleReport, error) {
					report := executionstore.ExecutionHeadSampleReport{
						Samples: []executionstore.ExecutionHeadSample{{AgentID: uuid.New(), ProjectID: uuid.New()}}}
					switch outcome {
					case "error":
						report.Samples[0].Err = errors.New("timeout")
					case "canceled":
						report.Samples[0].Err = context.Canceled
						cancel()
						return report, context.Canceled
					}
					return report, nil
				})
			runMaintenanceJobOnce(ctx, recorder, maintenanceJob{executionHeadJob, executionHeadInterval, run})
			if outcome == "canceled" {
				require.Empty(t, logs.String(), "shutdown is not a sampling failure")
			}

			response := httptest.NewRecorder()
			set.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			require.Contains(t, response.Body.String(),
				`omnara_maintenance_job_runs_total{job="execution_head_drift",result="`+outcome+`"} 1`)
		})
	}
}

func TestExecutionHeadTickMismatch(t *testing.T) {
	var sample executionstore.ExecutionHeadSample
	require.NoError(t, json.Unmarshal(
		[]byte(`{"Differences":[{"field":"stop_sequence","stored":8,"rebuilt":3}]}`), &sample))
	sample.AgentID = uuid.New()
	sample.ProjectID = uuid.New()
	set := metrics.New()
	var logs bytes.Buffer
	_, err := runExecutionHeadTick(t.Context(), slog.New(slog.NewJSONHandler(&logs, nil)),
		metrics.NewJobRecorder(set, metrics.SubsystemMaintenance), uuid.Nil,
		func(context.Context, uuid.UUID, int32) (executionstore.ExecutionHeadSampleReport, error) {
			return executionstore.ExecutionHeadSampleReport{
				Samples: []executionstore.ExecutionHeadSample{sample}}, nil
		})
	require.NoError(t, err, "drift is a finding, not a failed run")
	require.Contains(t, logs.String(), `"level":"ERROR"`)
	require.Contains(t, logs.String(), sample.AgentID.String())
	require.Contains(t, logs.String(), sample.ProjectID.String())
	require.Contains(t, logs.String(), `"fields":["stop_sequence"]`)
	require.Contains(t, logs.String(), `"stored":8,"rebuilt":3`)
	response := httptest.NewRecorder()
	set.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, outcome := range []string{"checked", "mismatch"} {
		require.Contains(t, response.Body.String(),
			`omnara_maintenance_job_items_total{job="execution_head_drift",outcome="`+outcome+`"} 1`)
	}
}

func TestExecutionHeadTickCursor(t *testing.T) {
	var logs bytes.Buffer
	var cursor uuid.UUID
	for range 3 {
		previous, next := cursor, uuid.New()
		var err error
		cursor, err = runExecutionHeadTick(t.Context(), slog.New(slog.NewJSONHandler(&logs, nil)), nil, cursor,
			func(_ context.Context, after uuid.UUID, n int32) (executionstore.ExecutionHeadSampleReport, error) {
				require.Equal(t, previous, after)
				require.EqualValues(t, 10, n)
				return executionstore.ExecutionHeadSampleReport{Cursor: next}, nil
			})
		require.NoError(t, err)
		require.Equal(t, next, cursor)
	}
}

func TestExecutionHeadJobInitializesItemsAndCarriesCursor(t *testing.T) {
	set := metrics.New()
	var logs bytes.Buffer
	next := uuid.New()
	var calls []uuid.UUID
	run := sampleExecutionHeads(slog.New(slog.NewJSONHandler(&logs, nil)),
		metrics.NewJobRecorder(set, metrics.SubsystemMaintenance),
		func(_ context.Context, after uuid.UUID, _ int32) (executionstore.ExecutionHeadSampleReport, error) {
			calls = append(calls, after)
			response := httptest.NewRecorder()
			set.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			for _, outcome := range []string{"checked", "mismatch", "error"} {
				require.Contains(t, response.Body.String(),
					`omnara_maintenance_job_items_total{job="execution_head_drift",outcome="`+outcome+`"} 0`)
			}
			return executionstore.ExecutionHeadSampleReport{Cursor: next}, nil
		})
	require.Zero(t, run(t.Context()))
	require.Zero(t, run(t.Context()))
	require.Equal(t, []uuid.UUID{uuid.Nil, next}, calls)
}
