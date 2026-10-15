package metrics

import (
	"strings"
	"testing"
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
}
