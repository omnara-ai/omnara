package metrics

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIntegrationInboxLagDistinguishesEmptyFailedAndStaleSamples(t *testing.T) {
	set := New()
	m := NewIntegrationInboxRecorder(set)
	value := func(name string) float64 {
		t.Helper()
		families, err := set.registry.Gather()
		require.NoError(t, err)
		for _, family := range families {
			if family.GetName() == "omnara_integration_inbox_"+name {
				require.Len(t, family.Metric, 1)
				require.Empty(t, family.Metric[0].Label)
				return family.Metric[0].GetGauge().GetValue()
			}
		}
		t.Fatalf("missing metric %s", name)
		return 0
	}
	require.True(t, math.IsNaN(value("oldest_ready_lag_seconds")))
	require.Zero(t, value("lag_sample_last_success_timestamp_seconds"))
	m.RecordOldestReadyLag(5*time.Second, nil)
	require.Equal(t, 5.0, value("oldest_ready_lag_seconds"))
	lastSuccess := value("lag_sample_last_success_timestamp_seconds")
	require.Positive(t, lastSuccess)
	m.RecordOldestReadyLag(0, errors.New("database unavailable"))
	require.True(t, math.IsNaN(value("oldest_ready_lag_seconds")))
	require.Equal(t, lastSuccess, value("lag_sample_last_success_timestamp_seconds"))
	m.RecordOldestReadyLag(0, nil)
	require.Zero(t, value("oldest_ready_lag_seconds"))
	body := scrapeMetrics(t, set)
	require.Contains(t, body, "omnara_integration_inbox_oldest_ready_lag_seconds 0")
	require.Contains(t, body, "omnara_integration_inbox_lag_sample_last_success_timestamp_seconds ")
	require.NotContains(t, body, "omnara_integration_inbox_oldest_ready_lag_seconds{")
}
