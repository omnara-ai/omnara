package metrics

import (
	"strings"
	"testing"
)

func TestMemoryRecorderListingDuration(t *testing.T) {
	set := New()
	recorder := NewMemoryRecorder(set)
	recorder.StartListing().ObserveDuration()

	body := scrapeMetrics(t, set)
	for _, want := range []string{
		"# TYPE omnara_memory_listing_duration_seconds histogram",
		"omnara_memory_listing_duration_seconds_count 1\n",
		`omnara_memory_listing_duration_seconds_bucket{le="120"}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}

	var disabled *MemoryRecorder
	disabled.StartListing().ObserveDuration()
}
