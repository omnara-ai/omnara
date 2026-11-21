package metrics

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelCostBacklogDistinguishesEmptyFailedAndStaleSamples(t *testing.T) {
	set := New()
	m := NewModelCostBacklogRecorder(set, 100)
	value := func(name string) (float64, string) {
		t.Helper()
		families, err := set.registry.Gather()
		require.NoError(t, err)
		for _, family := range families {
			if family.GetName() == "omnara_model_cost_"+name {
				require.Len(t, family.Metric, 1)
				return family.Metric[0].GetGauge().GetValue(), family.GetHelp()
			}
		}
		t.Fatalf("missing metric %s", name)
		return 0, ""
	}
	pending, help := value("merge_backlog")
	require.True(t, math.IsNaN(pending))
	require.True(t, strings.Contains(help, "up to 100."), help)
	lastSuccess, _ := value("merge_backlog_sample_last_success_timestamp_seconds")
	require.Zero(t, lastSuccess)

	m.Record(7, nil)
	pending, _ = value("merge_backlog")
	require.Equal(t, 7.0, pending)
	lastSuccess, _ = value("merge_backlog_sample_last_success_timestamp_seconds")
	require.Positive(t, lastSuccess)

	m.Record(0, errors.New("database unavailable"))
	pending, _ = value("merge_backlog")
	require.True(t, math.IsNaN(pending))
	stale, _ := value("merge_backlog_sample_last_success_timestamp_seconds")
	require.Equal(t, lastSuccess, stale)

	m.Record(0, nil)
	pending, _ = value("merge_backlog")
	require.Zero(t, pending)
}
