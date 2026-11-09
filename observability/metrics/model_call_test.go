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
		APIFormat: "anthropic-messages", APIVariant: "default", Provider: "api.anthropic.com", Model: "claude-x",
		Purpose: "agent_turn", Outcome: "success",
		RequestBytes: 100 * 1024, UsageReported: true, InputTokens: 20000, CachedInputTokens: 15000, OutputTokens: 50,
		TTFT: &ttft, Duration: 3 * time.Second,
	})
	recorder.Observe(ModelCallSample{
		APIFormat: "anthropic-messages", APIVariant: "default", Provider: "api.anthropic.com", Model: "claude-x",
		Purpose: "agent_turn", Outcome: "error", ErrorKind: "rate_limit",
		RequestBytes: 10, Duration: 400 * time.Millisecond,
	})

	body := scrapeMetrics(t, set)
	model := `api_format="anthropic-messages",api_variant="default"`
	provider := `,provider="api.anthropic.com"}`
	for _, want := range []string{
		`omnara_model_call_attempts_total{` + model + `,error_kind="rate_limit",model="claude-x",outcome="error"` +
			`,provider="api.anthropic.com",purpose="agent_turn"} 1`,
		`omnara_model_call_ttft_seconds_sum{` + model + `,model="claude-x"` + provider + ` 1.5`,
		`omnara_model_call_ttft_seconds_count{` + model + `,model="claude-x"` + provider + ` 1`,
		`omnara_model_call_duration_seconds_count{` + model + `,model="claude-x"` + provider + ` 2`,
		`omnara_model_call_request_bytes_total{` + model + `,model="claude-x"` + provider + ` 102410`,
		`omnara_model_call_input_tokens_total{` + model + `,model="claude-x"` + provider + ` 20000`,
		`omnara_model_call_cached_input_tokens_total{` + model + `,model="claude-x"` + provider + ` 15000`,
		`omnara_model_call_output_tokens_total{` + model + `,model="claude-x"` + provider + ` 50`,
		`omnara_model_call_usage_missing_total{` + model + `,model="claude-x"` + provider + ` 1`,
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
