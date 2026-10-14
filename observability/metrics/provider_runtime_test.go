package metrics

import (
	"strings"
	"testing"
	"time"
)

func TestProviderRuntimeRecorderEmitsLowCardinalityMetrics(t *testing.T) {
	set := New()
	recorder := NewProviderRuntimeRecorder(set)

	recorder.RecordPass(
		ProviderRuntimeOperationDiscovery,
		ProviderRuntimeResultSuccess,
		200*time.Millisecond,
	)
	recorder.RecordEvents(ProviderRuntimeOperationDiscovery, ProviderRuntimeEventTargets, 3)
	recorder.RecordEvents(ProviderRuntimeOperationDiscovery, ProviderRuntimeEventUnknown, 2)
	recorder.RecordEvents(
		ProviderRuntimeOperationDiscovery,
		ProviderRuntimeEventWakeAttemptsCleared,
		1,
	)
	recorder.RecordPass(
		ProviderRuntimeOperationConfirmation,
		ProviderRuntimeResultCanceled,
		time.Millisecond,
	)
	recorder.RecordEvents(
		ProviderRuntimeOperationConfirmation,
		ProviderRuntimeEventDeletionClaims,
		1,
	)

	body := scrapeMetrics(t, set)
	for _, want := range []string{
		`omnara_provider_runtime_reconciliation_passes_total{operation="discovery",result="success"} 1`,
		`omnara_provider_runtime_reconciliation_pass_duration_seconds_bucket{operation="discovery",result="success",le="0.25"} 1`,
		`omnara_provider_runtime_reconciliation_events_total{event="targets",operation="discovery"} 3`,
		`omnara_provider_runtime_reconciliation_events_total{event="unknown",operation="discovery"} 2`,
		`omnara_provider_runtime_reconciliation_events_total{event="wake_attempts_cleared",operation="discovery"} 1`,
		`omnara_provider_runtime_reconciliation_passes_total{operation="confirmation",result="canceled"} 1`,
		`omnara_provider_runtime_reconciliation_events_total{event="deletion_claims",operation="confirmation"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}
