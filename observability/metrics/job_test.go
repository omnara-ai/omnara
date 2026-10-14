package metrics

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestJobRecorder(t *testing.T) {
	set := New(WithNamespace("svc"))
	recorder := NewJobRecorder(set, SubsystemMaintenance)
	before := float64(time.Now().Unix())

	recorder.RecordRun("meter", JobResultSuccess, 40*time.Millisecond)
	recorder.RecordRun("meter", JobResultError, 2*time.Second)
	recorder.RecordItems("meter", "settled", 3)
	recorder.RecordItems("meter", "failed", 0)

	body := scrapeMetrics(t, set)
	for _, want := range []string{
		`svc_maintenance_job_runs_total{job="meter",result="success"} 1`,
		`svc_maintenance_job_runs_total{job="meter",result="error"} 1`,
		`svc_maintenance_job_run_duration_seconds_bucket{job="meter",result="success",le="0.05"} 1`,
		`svc_maintenance_job_items_total{job="meter",outcome="settled"} 3`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
	const lastSuccessSeries = `svc_maintenance_job_last_success_timestamp_seconds{job="meter"} `
	_, value, _ := strings.Cut(body, lastSuccessSeries)
	value, _, _ = strings.Cut(value, "\n")
	lastSuccess, err := strconv.ParseFloat(value, 64)
	if err != nil || lastSuccess < before {
		t.Fatalf("last success = %v, want at least %v", lastSuccess, before)
	}
	if strings.Contains(body, `outcome="failed"`) {
		t.Fatalf("zero item count created a series:\n%s", body)
	}
}

func TestNilJobRecorderIsNoop(t *testing.T) {
	var recorder *JobRecorder
	recorder.RecordRun("job", JobResultSuccess, time.Second)
	recorder.RecordItems("job", "outcome", 1)
}

func TestHTTPRecorderStart(t *testing.T) {
	set := New()
	recorder := NewHTTPRecorder(set, SubsystemAPI)

	recorder.Start()("POST", "POST /v1/items/{id}", 201)
	recorder.Start()("BREW", "", 418)
	var nilRecorder *HTTPRecorder
	nilRecorder.Start()("GET", "/", 200)

	body := scrapeMetrics(t, set)
	for _, want := range []string{
		`omnara_api_requests_total{code="201",method="post",route="/v1/items/{id}"} 1`,
		`omnara_api_requests_total{code="418",method="unknown",route="unmatched"} 1`,
		`omnara_api_requests_in_flight 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}
