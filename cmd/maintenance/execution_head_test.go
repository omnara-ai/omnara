package main

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
	"github.com/stretchr/testify/require"
)

func TestExecutionHeadTickOutcomes(t *testing.T) {
	for _, outcome := range []string{"success", "error", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var logs bytes.Buffer
			set := metrics.New()
			recorder := metrics.NewJobRecorder(set, metrics.SubsystemMaintenance)
			runExecutionHeadTick(ctx, slog.New(slog.NewJSONHandler(&logs, nil)), recorder,
				func(context.Context, int32) (executionstore.ExecutionHeadSampleReport, error) {
					report := executionstore.ExecutionHeadSampleReport{
						Samples: []executionstore.ExecutionHeadSample{{AgentID: uuid.New(), ProjectID: uuid.New()}}}
					switch outcome {
					case "error":
						report.Samples[0].Err = errors.New("timeout")
					case "canceled":
						cancel()
						return report, context.Canceled
					}
					return report, nil
				})

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
	runExecutionHeadTick(t.Context(), slog.New(slog.NewJSONHandler(&logs, nil)),
		metrics.NewJobRecorder(set, metrics.SubsystemMaintenance),
		func(context.Context, int32) (executionstore.ExecutionHeadSampleReport, error) {
			return executionstore.ExecutionHeadSampleReport{
				Samples: []executionstore.ExecutionHeadSample{sample}}, nil
		})
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
