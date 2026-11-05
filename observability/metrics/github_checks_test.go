package metrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGitHubCheckRecorderKeepsLastFullPass(t *testing.T) {
	set := New()
	recorder := NewGitHubCheckRecorder(set)
	value := func(name string) float64 {
		t.Helper()
		families, err := set.registry.Gather()
		require.NoError(t, err)
		for _, family := range families {
			if family.GetName() == "omnara_github_checks_"+name {
				require.Len(t, family.Metric, 1)
				return family.Metric[0].GetGauge().GetValue()
			}
		}
		t.Fatalf("missing metric %s", name)
		return 0
	}
	recorder.Record(1500*time.Millisecond, 3)
	require.Equal(t, 1.5, value("full_pass_duration_seconds"))
	require.Equal(t, 3.0, value("retrying"))
	recorder.Record(0, 1)
	require.Equal(t, 1.5, value("full_pass_duration_seconds"), "a partial pass keeps the last full-pass duration")
	require.Equal(t, 1.0, value("retrying"))
}
