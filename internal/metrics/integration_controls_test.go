package metrics

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestIntegrationControlRecorderTimestampsAndEmptyQueue(t *testing.T) {
	set := New()
	recorder := NewIntegrationControlRecorder(set)
	oldest := time.Unix(1_700_000_000, 250_000_000)
	observedAt := oldest.Add(10 * time.Minute)
	recorder.RecordOldestUnapplied(&oldest, observedAt)
	assertControlTimestamps(t, set, 1_700_000_000.25, 1_700_000_600.25)

	// Advancing the observation must not reset a stuck receipt's creation time.
	recorder.RecordOldestUnapplied(&oldest, observedAt.Add(time.Minute))
	assertControlTimestamps(t, set, 1_700_000_000.25, 1_700_000_660.25)

	recorder.RecordOldestUnapplied(nil, observedAt.Add(2*time.Minute))
	assertControlTimestamps(t, set, 0, 1_700_000_720.25)
}

func TestIntegrationControlRecorderStartsUnobservedAndAllowsNilRecorder(t *testing.T) {
	set := New()
	NewIntegrationControlRecorder(set)
	assertControlTimestamps(t, set, 0, 0)
	var recorder *IntegrationControlRecorder
	recorder.RecordOldestUnapplied(nil, time.Now())
}

func assertControlTimestamps(t *testing.T, set *Set, oldest, observedAt float64) {
	t.Helper()
	body := scrapeMetrics(t, set)
	for name, value := range map[string]float64{
		"omnara_integration_control_oldest_unapplied_timestamp_seconds": oldest,
		"omnara_integration_control_last_observation_timestamp_seconds": observedAt,
	} {
		for _, want := range []string{
			"# TYPE " + name + " gauge\n",
			fmt.Sprintf("\n%s %g\n", name, value),
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("metrics missing %q:\n%s", want, body)
			}
		}
	}
}
