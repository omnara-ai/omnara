package metrics

import (
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiscordRuntimeCapacityAndDemand(t *testing.T) {
	set := New()
	worker := NewDiscordRuntimeRecorder(set)
	demand := NewDiscordRuntimeDemandRecorder(set)
	value := func(name string) float64 {
		t.Helper()
		families, err := set.registry.Gather()
		require.NoError(t, err)
		for _, family := range families {
			if family.GetName() == "omnara_discord_runtime_"+name {
				require.Len(t, family.Metric, 1)
				require.Empty(t, family.Metric[0].Label)
				return family.Metric[0].GetGauge().GetValue()
			}
		}
		t.Fatalf("missing metric %s", name)
		return 0
	}
	worker.RecordCapacity(1024)
	worker.RecordClaims(100)
	require.Equal(t, 1024.0, value("capacity"))
	require.Equal(t, 100.0, value("claims"))
	require.True(t, math.IsNaN(value("unclaimed_integrations")))
	require.Zero(t, value("demand_sample_last_success_timestamp_seconds"))
	demand.RecordUnclaimed(7, nil)
	require.Equal(t, 7.0, value("unclaimed_integrations"))
	lastSuccess := value("demand_sample_last_success_timestamp_seconds")
	require.Positive(t, lastSuccess)
	demand.RecordUnclaimed(0, errors.New("database unavailable"))
	require.True(t, math.IsNaN(value("unclaimed_integrations")))
	require.Equal(t, lastSuccess, value("demand_sample_last_success_timestamp_seconds"))
	demand.RecordUnclaimed(0, nil)
	worker.RecordClaims(0)
	body := scrapeMetrics(t, set)
	require.Contains(t, body, "omnara_discord_runtime_claims 0")
	require.Contains(t, body, "omnara_discord_runtime_unclaimed_integrations 0")
}
