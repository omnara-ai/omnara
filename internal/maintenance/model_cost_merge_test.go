package maintenance

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/observability/metrics"
)

func TestModelCostMergeJobReportsBatchesAndCounts(t *testing.T) {
	set := metrics.New()
	var next int64
	var nextErr error
	run := mergeDailyModelCosts(slog.New(slog.DiscardHandler), func(_ context.Context, limit int32) (int64, error) {
		require.Equal(t, int32(modelCostMergeBatch), limit)
		return next, nextErr
	}, metrics.NewJobRecorder(set, metrics.SubsystemMaintenance))
	items := func() string {
		response := httptest.NewRecorder()
		set.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		return response.Body.String()
	}
	require.Contains(t, items(), `omnara_maintenance_job_items_total{job="merge_daily_model_costs",outcome="merged"} 0`)

	next, nextErr = modelCostMergeBatch, nil
	require.Equal(t, maintenanceResult{worked: true, more: true}, run(t.Context()), "a full batch drains again")
	next = 3
	require.Equal(t, maintenanceResult{worked: true}, run(t.Context()))
	next = 0
	require.Equal(t, maintenanceResult{}, run(t.Context()))
	require.Contains(t, items(), `omnara_maintenance_job_items_total{job="merge_daily_model_costs",outcome="merged"} 1003`)

	failure := errors.New("store failed")
	next, nextErr = modelCostMergeBatch, failure
	result := run(t.Context())
	require.ErrorIs(t, result.err, failure)
	require.False(t, result.worked || result.more, "a failed batch rolled back")
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	next, nextErr = modelCostMergeBatch, context.Canceled
	require.Equal(t, maintenanceResult{}, run(canceled), "an interrupted batch is neither counted nor a failure")
	require.Contains(t, items(), `omnara_maintenance_job_items_total{job="merge_daily_model_costs",outcome="merged"} 1003`)
}
