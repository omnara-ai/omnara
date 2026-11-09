package metrics

import (
	"strings"
	"testing"
	"time"
)

func TestModelCallRecorderCountsAttemptsTokensAndLatency(t *testing.T) {
	set := New()
	recorder := NewModelCallRecorder(set)
	ttft := 1500 * time.Millisecond
	recorder.Observe(ModelCallSample{
		APIFormat: "anthropic-messages", APIVariant: "default", EndpointHost: "api.anthropic.com", Model: "claude-x",
		Purpose: "agent_turn", Outcome: "success",
		RequestBytes: 100 * 1024, UsageReported: true, InputTokens: 20000, CachedInputTokens: 15000, CacheReported: true,
		OutputTokens: 50,
		TTFT:         &ttft, Duration: 3 * time.Second,
	})
	recorder.Observe(ModelCallSample{
		APIFormat: "anthropic-messages", APIVariant: "default", EndpointHost: "api.anthropic.com", Model: "claude-x",
		Purpose: "agent_turn", Outcome: "error", ErrorKind: "rate_limit",
		RequestBytes: 10, Duration: 400 * time.Millisecond,
	})

	body := scrapeMetrics(t, set)
	labels := `api_format="anthropic-messages",api_variant="default",endpoint_host="api.anthropic.com"`
	model := `,model="claude-x"}`
	for _, want := range []string{
		`omnara_model_call_attempts_total{` + labels + `,error_kind="rate_limit",model="claude-x",outcome="error"` +
			`,purpose="agent_turn"} 1`,
		`omnara_model_call_ttft_seconds_sum{` + labels + model + ` 1.5`,
		`omnara_model_call_ttft_seconds_count{` + labels + model + ` 1`,
		`omnara_model_call_duration_seconds_count{` + labels + model + ` 2`,
		`omnara_model_call_request_bytes_total{` + labels + model + ` 102410`,
		`omnara_model_call_input_tokens_total{` + labels + model + ` 20000`,
		`omnara_model_call_cached_input_tokens_total{` + labels + model + ` 15000`,
		`omnara_model_call_cache_reported_input_tokens_total{` + labels + model + ` 20000`,
		`omnara_model_call_output_tokens_total{` + labels + model + ` 50`,
		`omnara_model_call_usage_missing_total{` + labels + model + ` 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

func TestModelCallRecorderNilIsNoop(t *testing.T) {
	var recorder *ModelCallRecorder
	recorder.Observe(ModelCallSample{})
}

func TestModelCallRecorderCreatesEveryCounterOnTheFirstAttempt(t *testing.T) {
	set := New()
	NewModelCallRecorder(set).Observe(ModelCallSample{Model: "m", RequestBytes: 10, Duration: time.Second})
	body := scrapeMetrics(t, set)
	labels := `{api_format="unknown",api_variant="unknown",endpoint_host="unknown",model="m"}`
	for _, want := range []string{
		"omnara_model_call_duration_seconds_count" + labels + " 1",
		"omnara_model_call_request_bytes_total" + labels + " 10",
		"omnara_model_call_usage_missing_total" + labels + " 1",
		"omnara_model_call_input_tokens_total" + labels + " 0",
		"omnara_model_call_cached_input_tokens_total" + labels + " 0",
		"omnara_model_call_cache_reported_input_tokens_total" + labels + " 0",
		"omnara_model_call_output_tokens_total" + labels + " 0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}
