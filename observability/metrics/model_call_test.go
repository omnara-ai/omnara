package metrics

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestModelCallRecorderBucketsTTFTBySizeAndSkipsUnreachedPhases(t *testing.T) {
	set := New()
	recorder := NewModelCallRecorder(set)
	ttft := 1500 * time.Millisecond
	headers := 400 * time.Millisecond
	recorder.Observe(ModelCallSample{
		APIFormat: "anthropic-messages", APIVariant: "default", Model: "claude-x",
		Purpose: "agent_turn", Outcome: "success", Cache: "hit",
		RequestBytes: 100 * 1024, UsageReported: true, InputTokens: 20000, CachedInputTokens: 15000, OutputTokens: 50,
		Headers: &headers, FirstContent: &ttft, Duration: 3 * time.Second,
	})
	recorder.Observe(ModelCallSample{
		APIFormat: "anthropic-messages", APIVariant: "default", Model: "claude-x",
		Purpose: "agent_turn", Outcome: "error", ErrorKind: "rate_limit", Cache: "unknown",
		RequestBytes: 10, Headers: &headers, Duration: headers,
	})

	body := scrapeMetrics(t, set)
	model := `api_format="anthropic-messages",api_variant="default"`
	for _, want := range []string{
		`omnara_model_call_attempts_total{` + model + `,error_kind="rate_limit",model="claude-x",outcome="error",purpose="agent_turn"} 1`,
		`omnara_model_call_ttft_by_input_tokens_seconds_count{` + model + `,cache="hit",input_tokens_le="0032k",model="claude-x"} 1`,
		`omnara_model_call_ttft_by_request_bytes_seconds_count{` + model + `,cache="hit",model="claude-x",request_bytes_le="00256KiB"} 1`,
		`omnara_model_call_phase_seconds_count{` + model + `,cache="unknown",model="claude-x",phase="headers"} 1`,
		`omnara_model_call_tokens_total{` + model + `,kind="cached_input",model="claude-x"} 15000`,
		`omnara_model_call_usage_missing_total{` + model + `,model="claude-x",outcome="error"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
	for _, unwanted := range []string{
		`cache="unknown",model="claude-x",phase="first_content"`,
		`phase="upload_complete"`,
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("unreached phase observed: %q", unwanted)
		}
	}
}

func TestModelCallRecorderNilIsNoop(t *testing.T) {
	var recorder *ModelCallRecorder
	recorder.Observe(ModelCallSample{})
}

func TestModelCallSizeBucketsSortAsStrings(t *testing.T) {
	var labels []string
	for _, value := range []int{1, 4096, 4097, 20000, 40000, 100000, 131072, 131073} {
		labels = append(labels, sizeBucket(value, modelCallInputTokenBounds))
	}
	want := []string{"0004k", "0004k", "0016k", "0032k", "0064k", "0128k", "0128k", "inf"}
	if strings.Join(labels, ",") != strings.Join(want, ",") {
		t.Fatalf("labels = %v, want %v", labels, want)
	}
	if !slices.IsSorted(labels) {
		t.Fatalf("labels do not sort in size order: %v", labels)
	}
}
