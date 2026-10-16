package metrics

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWorkerRecorder(t *testing.T) {
	set := New()
	recorder := NewWorkerRecorder(set)
	if !strings.Contains(scrapeMetrics(t, set), "omnara_worker_claim_deferrals_total 0") {
		t.Fatal("missing initial counter")
	}
	recorder.RecordClaimDeferral()
	if !strings.Contains(scrapeMetrics(t, set), "omnara_worker_claim_deferrals_total 1") {
		t.Fatal("missing deferral")
	}
	var absent *WorkerRecorder
	absent.RecordClaimDeferral()
	absent.RecordSlots(4, func() int { return 1 })
	absent.StartExecution()()
}

func TestWorkerRecorderSlots(t *testing.T) {
	set := New()
	recorder := NewWorkerRecorder(set)
	body := scrapeMetrics(t, set)
	require.Contains(t, body, "omnara_worker_capacity 0")
	require.Contains(t, body, "omnara_worker_slots_used 0")
	used := 3
	recorder.RecordSlots(8, func() int { return used })
	body = scrapeMetrics(t, set)
	require.Contains(t, body, "omnara_worker_capacity 8")
	require.Contains(t, body, "omnara_worker_slots_used 3")
	used = 5
	require.Contains(t, scrapeMetrics(t, set), "omnara_worker_slots_used 5")
}

func TestWorkerRecorderExecutionsActive(t *testing.T) {
	set := New()
	recorder := NewWorkerRecorder(set)
	first, second := recorder.StartExecution(), recorder.StartExecution()
	require.Contains(t, scrapeMetrics(t, set), "omnara_worker_executions_active 2")
	first()
	second()
	require.Contains(t, scrapeMetrics(t, set), "omnara_worker_executions_active 0")
}

func TestAgentBacklogDistinguishesEmptyFailedAndStaleSamples(t *testing.T) {
	set := New()
	m := NewAgentBacklogRecorder(set)
	value := func(name string) float64 {
		t.Helper()
		families, err := set.registry.Gather()
		require.NoError(t, err)
		for _, family := range families {
			if family.GetName() == "omnara_agent_"+name {
				require.Len(t, family.Metric, 1)
				require.Empty(t, family.Metric[0].Label)
				return family.Metric[0].GetGauge().GetValue()
			}
		}
		t.Fatalf("missing metric %s", name)
		return 0
	}
	require.True(t, math.IsNaN(value("ready_backlog")))
	require.True(t, math.IsNaN(value("oldest_ready_age_seconds")))
	require.Zero(t, value("backlog_sample_last_success_timestamp_seconds"))
	m.Record(AgentBacklogSample{Ready: 7, OldestReadyAge: 12 * time.Second}, nil)
	require.Equal(t, 7.0, value("ready_backlog"))
	require.Equal(t, 12.0, value("oldest_ready_age_seconds"))
	lastSuccess := value("backlog_sample_last_success_timestamp_seconds")
	require.Positive(t, lastSuccess)
	m.Record(AgentBacklogSample{}, errors.New("database unavailable"))
	require.True(t, math.IsNaN(value("ready_backlog")))
	require.True(t, math.IsNaN(value("oldest_ready_age_seconds")))
	require.Equal(t, lastSuccess, value("backlog_sample_last_success_timestamp_seconds"))
	m.Record(AgentBacklogSample{}, nil)
	require.Zero(t, value("ready_backlog"))
	require.Zero(t, value("oldest_ready_age_seconds"))
	var absent *AgentBacklogRecorder
	absent.Record(AgentBacklogSample{}, nil)
}
