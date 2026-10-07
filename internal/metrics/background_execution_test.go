package metrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBackgroundExecutionMetrics(t *testing.T) {
	set := New()
	depth := 0
	recorder := NewBackgroundExecutionRecorder(set, 8, func() int { return depth })
	body := scrapeMetrics(t, set)
	require.Contains(t, body, "omnara_background_execution_queue_capacity 8\n")
	require.Contains(t, body, "omnara_background_execution_queue_depth 0\n")
	require.Contains(t, body, "omnara_background_execution_submissions_in_flight 0\n")
	require.Contains(t, body, "omnara_background_execution_submissions_total{result=\"full\"} 0\n")

	recorder.StartSubmission()
	depth = 8
	body = scrapeMetrics(t, set)
	require.Contains(t, body, "omnara_background_execution_queue_depth 8\n")
	require.Contains(t, body, "omnara_background_execution_submissions_in_flight 1\n")
	recorder.RecordSubmission(BackgroundSubmissionAccepted, 2*time.Second)
	recorder.StartSubmission()
	recorder.RecordSubmission(BackgroundSubmissionFull, time.Millisecond)
	recorder.StartSubmission()
	recorder.RecordSubmission(BackgroundSubmissionShutdown, time.Second)
	recorder.RecordStart(5 * time.Second)
	depth = 7
	body = scrapeMetrics(t, set)
	require.Contains(t, body, "omnara_background_execution_queue_depth 7\n")
	require.Contains(t, body, "omnara_background_execution_submissions_in_flight 0\n")
	for _, result := range []string{"accepted", "full", "shutdown"} {
		require.Contains(t, body, "omnara_background_execution_submissions_total{result=\""+result+"\"} 1\n")
	}
	require.Contains(t, body, "omnara_background_execution_submission_duration_seconds_count 3\n")
	require.Contains(t, body, "omnara_background_execution_submission_duration_seconds_sum 3.001\n")
	require.Contains(t, body, "omnara_background_execution_start_delay_seconds_count 1\n")
	require.Contains(t, body, "omnara_background_execution_start_delay_seconds_sum 5\n")
}
