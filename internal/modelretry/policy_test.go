package modelretry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/model"
)

func attempt(number int) Attempt {
	return Attempt{Number: number}
}

func TestDecideStopsDeterministicInputFailures(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.FixedZone("test", -7*60*60))
	for _, kind := range []model.ErrorKind{
		model.ErrorKindContextWindow,
		model.ErrorKindPayloadTooLarge,
	} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			err := model.ProviderError{Kind: kind}
			_, decision := Decide(err, attempt(1), "context-1", now)
			if decision.Action != ActionStop {
				t.Fatalf("action = %q, want %q", decision.Action, ActionStop)
			}
			if decision.RetryDelay != 0 {
				t.Fatalf("retry delay = %s, want zero", decision.RetryDelay)
			}
		})
	}
}

func TestDecideStopsDeterministicInputDespiteProviderRetryDirective(t *testing.T) {
	t.Parallel()

	retry := true
	err := model.ProviderError{
		Kind:      model.ErrorKindContextWindow,
		Retryable: &retry,
	}
	_, decision := Decide(
		err,
		attempt(1),
		"context-provider-no-retry-overflow",
		time.Date(2026, time.July, 13, 19, 0, 0, 0, time.UTC))

	if decision.Action != ActionStop {
		t.Fatalf("overflow action = %q, want %q", decision.Action, ActionStop)
	}
}

func TestDecideRetryBuckets(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 13, 19, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		err  error
		want Action
	}{
		{name: "transient", err: model.ProviderError{Kind: model.ErrorKindTransient}, want: ActionRetry},
		{name: "rate limit", err: model.ProviderError{Kind: model.ErrorKindRateLimit}, want: ActionRetry},
		{name: "provider unavailable", err: model.ProviderError{Kind: model.ErrorKindProviderUnavailable}, want: ActionRetry},
		{
			name: "provider replay rejected without recovery",
			err:  model.ProviderError{Kind: model.ErrorKindReplayRejected},
			want: ActionStop,
		},
		{name: "unknown classified", err: model.ProviderError{Kind: model.ErrorKindUnknown}, want: ActionRetry},
		{name: "unknown unclassified", err: errors.New("connection reset"), want: ActionRetry},
		{name: "auth", err: model.ProviderError{Kind: model.ErrorKindAuth}, want: ActionStop},
		{name: "billing", err: model.ProviderError{Kind: model.ErrorKindBillingAccount}, want: ActionStop},
		{name: "invalid request", err: model.ProviderError{Kind: model.ErrorKindInvalidRequest}, want: ActionStop},
		{name: "inner cancellation", err: context.Canceled, want: ActionRetry},
		{name: "inner deadline exceeded", err: context.DeadlineExceeded, want: ActionRetry},
		{
			name: "classified cancellation",
			err: model.ProviderError{
				Kind:  model.ErrorKindUnknown,
				Cause: context.Canceled,
			},
			want: ActionRetry,
		},
		{
			name: "ambiguous invalid request",
			err:  model.AmbiguousProviderOutcome(model.ProviderError{Kind: model.ErrorKindInvalidRequest}),
			want: ActionRetry,
		},
		{
			name: "ambiguous cancellation after send",
			err:  model.AmbiguousProviderOutcome(context.Canceled),
			want: ActionRetry,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, decision := Decide(test.err, attempt(1), "context-1", now)
			if decision.Action != test.want {
				t.Fatalf("action = %q, want %q", decision.Action, test.want)
			}
			if test.want == ActionRetry && decision.RetryDelay <= 0 {
				t.Fatalf("retry delay = %s, want positive delay", decision.RetryDelay)
			}
		})
	}
}

func TestDecideRetriesReplayRejectionOnlyWhenRecoveryIsAvailable(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 13, 19, 0, 0, 0, time.UTC)
	retry := true
	doNotRetry := false
	for _, test := range []struct {
		name      string
		err       error
		available bool
		want      Action
	}{
		{
			name:      "first rejection",
			err:       model.ProviderError{Kind: model.ErrorKindReplayRejected},
			available: true,
			want:      ActionRetry,
		},
		{
			name: "provider retry false cannot block changed request",
			err: model.ProviderError{
				Kind:      model.ErrorKindReplayRejected,
				Retryable: &doNotRetry,
			},
			available: true,
			want:      ActionRetry,
		},
		{
			name: "same frontier stops despite provider retry true",
			err: model.ProviderError{
				Kind:      model.ErrorKindReplayRejected,
				Retryable: &retry,
			},
			want: ActionStop,
		},
		{
			name: "same frontier stops despite ambiguous wrapper",
			err: model.AmbiguousProviderOutcome(model.ProviderError{
				Kind: model.ErrorKindReplayRejected,
			}),
			want: ActionStop,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, decision := Decide(
				test.err,
				Attempt{Number: 1, ProviderReplayCutoffCanAdvance: test.available},
				"context-replay-recovery",
				now,
			)
			if decision.Action != test.want {
				t.Fatalf("action = %q, want %q", decision.Action, test.want)
			}
		})
	}

	_, decision := Decide(
		model.ProviderError{Kind: model.ErrorKindReplayRejected},
		Attempt{
			Number:                         MaxModelCallRetriesPerOperation + 1,
			ProviderReplayCutoffCanAdvance: true,
		},
		"context-replay-recovery-exhausted",
		now,
	)
	if decision.Action != ActionStop {
		t.Fatalf("exhausted replay recovery action = %q, want %q", decision.Action, ActionStop)
	}
}

func TestDecideStopsAfterEightRetries(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 13, 19, 0, 0, 0, time.UTC)
	delta := int64(7_200)
	httpDate := now.Add(3 * time.Hour)
	err := model.ProviderError{
		Kind: model.ErrorKindTransient,
		RetryAfter: &model.RetryAfter{
			DeltaSeconds: &delta,
			HTTPDate:     &httpDate,
		},
	}
	_, decision := Decide(
		err,
		attempt(MaxModelCallRetriesPerOperation),
		"context-1",
		now,
	)
	if got := decision.Action; got != ActionRetry {
		t.Fatalf("attempt %d action = %q, want %q", MaxModelCallRetriesPerOperation, got, ActionRetry)
	}
	_, decision = Decide(
		err,
		attempt(MaxModelCallRetriesPerOperation+1),
		"context-1",
		now,
	)
	if decision.Action != ActionStop {
		t.Fatalf("attempt %d action = %q, want %q", MaxModelCallRetriesPerOperation+1, decision.Action, ActionStop)
	}
	if decision.RetryDelay != 0 {
		t.Fatalf("stopped retry delay = %s, want zero", decision.RetryDelay)
	}
}

func TestBackoffIsDeterministicJitteredAndAttemptIndexed(t *testing.T) {
	t.Parallel()

	got := backoff(1, "context-1")
	if got != backoff(1, "context-1") {
		t.Fatal("backoff changed for the same durable context and attempt number")
	}
	if got < 800*time.Millisecond || got > 1200*time.Millisecond {
		t.Fatalf("first-attempt backoff = %s, want within 80-120%% of 1s", got)
	}

	values := make(map[time.Duration]struct{})
	for i := range 20 {
		values[backoff(1, "context-"+string(rune('a'+i)))] = struct{}{}
	}
	if len(values) == 1 {
		t.Fatal("durable context identity did not vary retry jitter")
	}

	for _, test := range []struct {
		attemptNumber int
		baseDelay     time.Duration
	}{
		{attemptNumber: 0, baseDelay: time.Second},
		{attemptNumber: 1, baseDelay: time.Second},
		{attemptNumber: 2, baseDelay: 2 * time.Second},
		{attemptNumber: 3, baseDelay: 4 * time.Second},
		{attemptNumber: 5, baseDelay: 16 * time.Second},
	} {
		delay := backoff(test.attemptNumber, "context-1")
		minimum := test.baseDelay * 80 / 100
		maximum := test.baseDelay * 120 / 100
		if delay < minimum || delay > maximum {
			t.Fatalf(
				"attempt %d backoff = %s, want %s..%s",
				test.attemptNumber,
				delay,
				minimum,
				maximum,
			)
		}
	}
	if got := backoff(6, "context-1"); got < 25_600*time.Millisecond || got > 30*time.Second {
		t.Fatalf("reachable capped backoff = %s, want 25.6s..30s", got)
	} else if got != 30*time.Second {
		t.Fatalf("deterministic capped backoff = %s, want 30s", got)
	}
}

func TestDecideUsesOneAttemptBudgetAcrossFailures(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 13, 19, 0, 0, 0, time.UTC)
	err := model.ProviderError{Kind: model.ErrorKindTransient}
	lastRetryable := Attempt{Number: MaxModelCallRetriesPerOperation}
	_, decision := Decide(err, lastRetryable, "context-1", now)
	if decision.Action != ActionRetry {
		t.Fatalf("eighth pre-send failure action = %q, want %q", decision.Action, ActionRetry)
	}

	exhausted := lastRetryable
	exhausted.Number++
	_, decision = Decide(err, exhausted, "context-1", now)
	if decision.Action != ActionStop || decision.RetryDelay != 0 {
		t.Fatalf("ninth pre-send failure decision = %+v, want stop without retry", decision)
	}

}

func TestDecideHonorsRetryAfterHints(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 13, 19, 0, 0, 0, time.UTC)
	delta := int64(120)
	httpDate := now.Add(5 * time.Minute)
	err := model.ProviderError{
		Kind: model.ErrorKindRateLimit,
		RetryAfter: &model.RetryAfter{
			DeltaSeconds: &delta,
			HTTPDate:     &httpDate,
		},
	}

	_, decision := Decide(err, attempt(1), "context-1", now)
	if decision.Action != ActionRetry {
		t.Fatalf("action = %q, want %q", decision.Action, ActionRetry)
	}
	if decision.RetryDelay != httpDate.Sub(now) {
		t.Fatalf("retry delay = %s, want later provider hint %s", decision.RetryDelay, httpDate.Sub(now))
	}
}

func TestDecideHonorsMillisecondRetryDelayAndExplicitRetryOverride(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 13, 19, 0, 0, 0, time.UTC)
	delayMilliseconds := int64(30_001)
	retry := true
	evidence, decision := Decide(
		model.ProviderError{
			Kind:       model.ErrorKindInvalidRequest,
			RetryAfter: &model.RetryAfter{DelayMilliseconds: &delayMilliseconds},
			Retryable:  &retry,
		},
		attempt(1),
		"context-provider-override",
		now)

	if decision.Action != ActionRetry ||
		decision.RetryDelay != 30_001*time.Millisecond {
		t.Fatalf("explicit provider retry decision = %+v", decision)
	}
	var details map[string]any
	if err := json.Unmarshal(evidence.Details, &details); err != nil {
		t.Fatalf("decode explicit provider retry evidence: %v", err)
	}
	if details["provider_retryable"] != true || details["retry_after"] == nil {
		t.Fatalf("explicit provider retry evidence = %s", evidence.Details)
	}

	retry = false
	_, decision = Decide(
		model.ProviderError{
			Kind:      model.ErrorKindRateLimit,
			Retryable: &retry,
		},
		attempt(1),
		"context-provider-no-retry",
		now)

	if decision.Action != ActionStop {
		t.Fatalf("explicit provider no-retry decision = %+v", decision)
	}

	_, decision = Decide(
		model.AmbiguousProviderOutcome(model.ProviderError{
			Kind:      model.ErrorKindInvalidRequest,
			Retryable: &retry,
		}),
		attempt(1),
		"context-ambiguous-provider-no-retry",
		now)

	if decision.Action != ActionRetry {
		t.Fatalf("ambiguous provider no-retry decision = %+v, want durable ambiguous retry", decision)
	}

	retry = true
	_, decision = Decide(
		model.ProviderError{
			Kind:      model.ErrorKindTransient,
			Retryable: &retry,
			Cause:     context.Canceled,
		},
		attempt(1),
		"context-canceled-provider-retry",
		now)

	if decision.Action != ActionRetry {
		t.Fatalf("inner cancellation provider retry decision = %+v, want provider advice to apply", decision)
	}
}

func TestDecideHonorsNormalizedRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 13, 19, 0, 0, 0, time.UTC)
	delta := int64(3_600)
	attempt := Attempt{Number: 1}
	err := model.ProviderError{
		Kind:       model.ErrorKindRateLimit,
		RetryAfter: &model.RetryAfter{DeltaSeconds: &delta},
	}

	_, decision := Decide(err, attempt, "context-pre-send", now)
	want := time.Hour
	if decision.Action != ActionRetry || decision.RetryDelay != want {
		t.Fatalf("retry decision = %+v, want provider retry delay %s", decision, want)
	}
}

func TestDecideStopsUnrecognizedFutureErrorKind(t *testing.T) {
	t.Parallel()

	_, decision := Decide(
		model.ProviderError{Kind: model.ErrorKind("future_provider_error")},
		attempt(1),
		"context-future-kind",
		time.Date(2026, time.July, 13, 19, 0, 0, 0, time.UTC))

	if decision.Action != ActionStop || decision.RetryDelay != 0 {
		t.Fatalf("future error kind decision = %+v, want stop without retry", decision)
	}
}

func TestDecideDoesNotLetRetryAfterShortenLocalBackoff(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 13, 19, 0, 0, 0, time.UTC)
	delta := int64(1)
	attempt := Attempt{Number: 5}
	err := model.ProviderError{
		Kind:       model.ErrorKindRateLimit,
		RetryAfter: &model.RetryAfter{DeltaSeconds: &delta},
	}

	_, decision := Decide(err, attempt, "context-short-retry-after", now)
	want := backoff(attempt.Number, "context-short-retry-after")
	if decision.RetryDelay != want {
		t.Fatalf("retry delay = %s, want local backoff floor %s", decision.RetryDelay, want)
	}
}

func TestDecideIndexesBackoffByAttemptNumber(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 13, 19, 0, 0, 0, time.UTC)
	attempt := Attempt{Number: 5}

	_, decision := Decide(
		model.ProviderError{Kind: model.ErrorKindTransient},
		attempt,
		"context-attempt-index",
		now)

	want := backoff(attempt.Number, "context-attempt-index")
	if decision.RetryDelay != want {
		t.Fatalf("retry delay = %s, want attempt-%d backoff %s", decision.RetryDelay, attempt.Number, want)
	}
}

func TestDecideRejectsUnrepresentableRetryAfterWithoutSchedulingEarly(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 13, 19, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name    string
		seconds int64
	}{
		{name: "negative", seconds: -1},
		{name: "duration overflow", seconds: 9_223_372_036_854_775_807},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := model.ProviderError{
				Kind:       model.ErrorKindRateLimit,
				RetryAfter: &model.RetryAfter{DeltaSeconds: &test.seconds},
			}
			_, decision := Decide(err, attempt(1), "context-1", now)
			if test.seconds < 0 {
				if decision.Action != ActionRetry || decision.RetryDelay <= 0 {
					t.Fatalf("negative retry-after decision = %+v, want local retry", decision)
				}
			} else if decision.Action != ActionStop || decision.RetryDelay != 0 {
				t.Fatalf("unrepresentable retry-after decision = %+v, want stop", decision)
			}
		})
	}
}

func TestDecideHonorsLongHTTPDateRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 13, 19, 0, 0, 0, time.UTC)
	httpDate := now.Add(30 * 24 * time.Hour)
	err := model.ProviderError{
		Kind:       model.ErrorKindRateLimit,
		RetryAfter: &model.RetryAfter{HTTPDate: &httpDate},
	}

	_, decision := Decide(err, attempt(1), "context-1", now)
	if decision.RetryDelay != 30*24*time.Hour {
		t.Fatalf("retry delay = %s, want provider minimum %s", decision.RetryDelay, 30*24*time.Hour)
	}
}

func TestEvidenceForPreservesPersistedProviderEvidence(t *testing.T) {
	t.Parallel()

	err := model.ProviderError{
		Kind:       model.ErrorKindRateLimit,
		Source:     "https://api.example.test/v1/responses?api_key=source-secret",
		StatusCode: 429,
		Code:       "token=code-secret",
		Message:    "Authorization: Bearer message-secret",
		RequestID:  "Authorization: Bearer request-secret",
	}
	evidence := EvidenceFor(model.AmbiguousProviderOutcome(err))
	if !evidence.Ambiguous {
		t.Fatal("ambiguous provider outcome was not preserved")
	}
	if evidence.Kind != model.ErrorKindRateLimit {
		t.Fatalf("kind = %q, want %q", evidence.Kind, model.ErrorKindRateLimit)
	}
	if evidence.Code != "token=code-secret" {
		t.Fatalf("code = %q, want canonical provider code", evidence.Code)
	}
	if evidence.Message != "Authorization: Bearer message-secret" {
		t.Fatalf("message = %q, want canonical provider message", evidence.Message)
	}
	if evidence.RequestID != "Authorization: Bearer request-secret" {
		t.Fatalf("request_id = %q, want canonical provider request ID", evidence.RequestID)
	}
	if !json.Valid(evidence.Details) {
		t.Fatalf("details = %q, want valid JSON", evidence.Details)
	}
	var details map[string]any
	if err := json.Unmarshal(evidence.Details, &details); err != nil {
		t.Fatalf("decode details: %v", err)
	}
	if details["source"] != "https://api.example.test/v1/responses?api_key=source-secret" ||
		details["request_id"] != "Authorization: Bearer request-secret" ||
		details["status_code"] != float64(429) {
		t.Fatalf("details = %s, want canonical provider evidence", evidence.Details)
	}
}

func TestEvidenceForPreservesStructuredProviderMetadata(t *testing.T) {
	t.Parallel()

	evidence := EvidenceFor(model.ProviderError{
		Kind:     model.ErrorKindUnknown,
		Metadata: json.RawMessage(`{"event":"response.error","raw_event_bytes":9,"sequence":9007199254740993}`),
	})
	var details struct {
		ProviderMetadata json.RawMessage `json:"provider_metadata"`
	}
	if err := json.Unmarshal(evidence.Details, &details); err != nil {
		t.Fatalf("decode error details: %v", err)
	}
	if string(details.ProviderMetadata) !=
		`{"event":"response.error","raw_event_bytes":9,"sequence":9007199254740993}` {
		t.Fatalf("provider metadata = %s, want malformed-frame evidence", details.ProviderMetadata)
	}
}

func TestEvidenceForOmitsUnsafeOrOversizedProviderMetadata(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		metadata json.RawMessage
	}{
		{name: "database unsafe", metadata: json.RawMessage(`{"value":"\u0000"}`)},
		{name: "out of range number", metadata: json.RawMessage(`{"value":1e1000000}`)},
		{
			name:     "oversized",
			metadata: json.RawMessage(`{"value":"` + strings.Repeat("x", maxProviderMetadataBytes) + `"}`),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			evidence := EvidenceFor(model.ProviderError{
				Kind:     model.ErrorKindUnknown,
				Metadata: test.metadata,
			})
			var details struct {
				ProviderMetadata json.RawMessage `json:"provider_metadata"`
			}
			if err := json.Unmarshal(evidence.Details, &details); err != nil {
				t.Fatalf("decode error details: %v", err)
			}
			if len(details.ProviderMetadata) != 0 {
				t.Fatalf("provider metadata = %s, want omitted", details.ProviderMetadata)
			}
		})
	}
}

func TestEvidenceForCanonicalizesProviderMetadataForJSONB(t *testing.T) {
	t.Parallel()

	evidence := EvidenceFor(model.ProviderError{
		Kind:     model.ErrorKindUnknown,
		Metadata: json.RawMessage(`{"value":"\uD800"}`),
	})
	var details struct {
		ProviderMetadata json.RawMessage `json:"provider_metadata"`
	}
	if err := json.Unmarshal(evidence.Details, &details); err != nil {
		t.Fatalf("decode error details: %v", err)
	}
	if err := model.ValidateProviderJSON(details.ProviderMetadata); err != nil {
		t.Fatalf("canonical provider metadata is not database-safe: %v", err)
	}
	var metadata struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(details.ProviderMetadata, &metadata); err != nil {
		t.Fatalf("decode canonical provider metadata: %v", err)
	}
	if metadata.Value != "\uFFFD" {
		t.Fatalf("canonical provider metadata value = %q, want replacement rune", metadata.Value)
	}
}

func TestEvidenceForNormalizesUnclassifiedAndOversizedErrors(t *testing.T) {
	t.Parallel()

	evidence := EvidenceFor(nil)
	if evidence.Kind != model.ErrorKindUnknown ||
		evidence.Code != "request_failed" || evidence.Message != "The model provider request failed." {
		t.Fatalf("nil evidence = %+v", evidence)
	}

	longMessage := strings.Repeat("x", 2_100)
	evidence = EvidenceFor(errors.New(longMessage))
	if len(evidence.Message) != 2_000 {
		t.Fatalf("oversized evidence message length = %d", len(evidence.Message))
	}

	evidence = EvidenceFor(errors.New(strings.Repeat("x", 1_999) + "\u00e9more"))
	if !utf8.ValidString(evidence.Message) || len(evidence.Message) > 2_000 {
		t.Fatalf("UTF-8-truncated evidence is invalid: %q", evidence.Message)
	}

	evidence = EvidenceFor(errors.New("bad\x00wire\xfftext"))
	if strings.ContainsRune(evidence.Message, '\x00') || !utf8.ValidString(evidence.Message) {
		t.Fatalf("database-unsafe evidence was not normalized: %q", evidence.Message)
	}
}
