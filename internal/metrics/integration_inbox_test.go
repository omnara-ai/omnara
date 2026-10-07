package metrics

import (
	"errors"
	"math"
	"strings"
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

func TestIntegrationInboxIntakeBoundsLabels(t *testing.T) {
	set := New()
	m := NewIntegrationInboxIntakeRecorder(set)
	for _, provider := range []string{"slack", "discord", "github"} {
		for _, outcome := range []IntegrationInboxIntakeOutcome{
			IntegrationInboxIntakeOutcomeAccepted, IntegrationInboxIntakeOutcomeDuplicate,
			IntegrationInboxIntakeOutcomeCommitted, IntegrationInboxIntakeOutcomeFiltered, IntegrationInboxIntakeOutcomeError,
		} {
			m.Record(provider, outcome)
			m.Record(provider, outcome)
		}
	}
	for _, id := range []string{"integration-123", "repository-456", "agent-789", "", "unknown"} {
		m.Record(id, IntegrationInboxIntakeOutcome(id))
	}
	var absent *IntegrationInboxIntakeRecorder
	absent.Record("slack", IntegrationInboxIntakeOutcomeAccepted)
	families, err := set.registry.Gather()
	require.NoError(t, err)
	found := false
	for _, family := range families {
		if family.GetName() != "omnara_integration_inbox_intake_total" {
			continue
		}
		found = true
		require.Len(t, family.Metric, 16)
		for _, metric := range family.Metric {
			require.Len(t, metric.Label, 2)
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			require.Contains(t, []string{"slack", "discord", "github", "other"}, labels["provider"])
			require.Contains(t, []string{"accepted", "duplicate", "committed", "filtered", "error"}, labels["outcome"])
			want := 2.0
			if labels["provider"] == "other" {
				want = 5
				require.Equal(t, "error", labels["outcome"])
			}
			require.Equal(t, want, metric.GetCounter().GetValue())
		}
	}
	require.True(t, found)
	require.Contains(t, scrapeMetrics(t, set),
		`omnara_integration_inbox_intake_total{outcome="duplicate",provider="slack"} 2`)
}

func TestIntegrationInboxProcessingCountsAndDuration(t *testing.T) {
	set := New()
	m := NewIntegrationInboxRecorder(set)
	for _, outcome := range []IntegrationInboxProcessingOutcome{
		IntegrationInboxProcessingOutcomeCompleted, IntegrationInboxProcessingOutcomeRetryScheduled,
		IntegrationInboxProcessingOutcomeFailed, IntegrationInboxProcessingOutcomeLeaseLost,
		IntegrationInboxProcessingOutcomeRetryNotRecorded,
	} {
		m.RecordProcessing(outcome, 2*time.Second)
		m.RecordProcessing(outcome, 3*time.Second)
	}
	m.RecordProcessing("receipt-123", time.Second)
	m.RecordProcessing("receipt-456", time.Second)
	m.RecordProcessing("", time.Second)
	m.RecordProcessing("unknown", time.Second)
	m.RecordProcessing(IntegrationInboxProcessingOutcomeOther, time.Second)
	var absent *IntegrationInboxRecorder
	absent.RecordProcessing(IntegrationInboxProcessingOutcomeCompleted, time.Second)
	body := scrapeMetrics(t, set)
	require.Equal(t, 6, strings.Count(body, "\nomnara_integration_inbox_processing_total{"))
	require.Equal(t, 6, strings.Count(body, "\nomnara_integration_inbox_processing_duration_seconds_count{"))
	for _, outcome := range []string{"completed", "retry_scheduled", "failed", "lease_lost", "retry_not_recorded"} {
		require.Contains(t, body, `omnara_integration_inbox_processing_total{outcome="`+outcome+`"} 2`)
		require.Contains(t, body, `omnara_integration_inbox_processing_duration_seconds_count{outcome="`+outcome+`"} 2`)
		require.Contains(t, body, `omnara_integration_inbox_processing_duration_seconds_sum{outcome="`+outcome+`"} 5`)
	}
	require.Contains(t, body, `omnara_integration_inbox_processing_total{outcome="other"} 5`)
	require.Contains(t, body, `omnara_integration_inbox_processing_duration_seconds_count{outcome="other"} 5`)
	require.Contains(t, body, `omnara_integration_inbox_processing_duration_seconds_sum{outcome="other"} 5`)
	require.NotContains(t, body, "receipt-123")
}
