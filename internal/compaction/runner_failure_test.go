package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestCompactionRequestPolicyDerivesPreferredAndConfiguredFloor(t *testing.T) {
	supportsTools := false
	tests := []struct {
		name       string
		caps       model.Capabilities
		wantOutput int
		wantFloor  int
	}{
		{
			name: "summary cap changes only output policy",
			caps: model.Capabilities{
				MaxOutputTokens:           64_000,
				DefaultMaxOutputTokens:    2_048,
				DefaultCacheRetention:     model.CacheRetentionShort,
				SupportsTools:             &supportsTools,
				SupportsReasoning:         true,
				DefaultReasoningEffort:    "high",
				SupportedReasoningEfforts: []string{"low", "medium", "high"},
			},
			wantOutput: preferredSummaryOutputTokens,
			wantFloor:  2_048,
		},
		{
			name: "model output limit below summary cap is retained",
			caps: model.Capabilities{
				MaxOutputTokens:           8_192,
				DefaultMaxOutputTokens:    2_048,
				SupportsReasoning:         true,
				DefaultReasoningEffort:    "low",
				SupportedReasoningEfforts: []string{"low", "high"},
			},
			wantOutput: 8_192,
			wantFloor:  2_048,
		},
		{
			name: "default output limit is used when model maximum is unavailable",
			caps: model.Capabilities{
				DefaultMaxOutputTokens:    2_048,
				SupportsReasoning:         true,
				DefaultReasoningEffort:    "vendor-deep",
				SupportedReasoningEfforts: []string{"vendor-deep"},
			},
			wantOutput: 2_048,
			wantFloor:  2_048,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := openairesponses.Client{
				ProviderModelSlug: "policy-test",
				ModelCapabilities: test.caps,
			}
			got, floor, err := compactionRequestPolicy(client, "test")
			if err != nil {
				t.Fatalf("compaction request policy: %v", err)
			}
			want := model.RequestPolicyFromCapabilities(test.caps)
			want.MaxOutputTokens = test.wantOutput
			want.CacheRetention = model.CacheRetentionNone
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("compaction policy = %+v, want %+v", got, want)
			}
			if floor != test.wantFloor {
				t.Fatalf("compaction output floor = %d, want %d", floor, test.wantFloor)
			}
		})
	}
}

func TestCompactionRequestPolicyReconcilesProviderFixedReasoningBudget(t *testing.T) {
	supportsTools := false
	baseCapabilities := model.Capabilities{
		ContextWindowTokens:       200_000,
		MaxOutputTokens:           64_000,
		DefaultMaxOutputTokens:    32_768,
		DefaultCacheRetention:     model.CacheRetentionShort,
		SupportsTools:             &supportsTools,
		SupportsReasoning:         false,
		DefaultReasoningEffort:    "",
		SupportedReasoningEfforts: nil,
	}
	tests := []struct {
		name       string
		client     model.Client
		wantOutput int
		wantFloor  int
	}{
		{
			name: "Anthropic preferred total already valid",
			client: anthropicmessages.Client{
				ProviderModelSlug: "claude-sonnet-4",
				ModelCapabilities: baseCapabilities,
				APIVariantOptions: json.RawMessage(`{"thinking":{"type":"enabled","budget_tokens":8192}}`),
			},
			wantOutput: preferredSummaryOutputTokens,
			wantFloor:  preferredSummaryOutputTokens,
		},
		{
			name: "Anthropic falls back to normal allowance",
			client: anthropicmessages.Client{
				ProviderModelSlug: "claude-sonnet-4",
				ModelCapabilities: baseCapabilities,
				APIVariantOptions: json.RawMessage(`{"thinking":{"type":"enabled","budget_tokens":24576}}`),
			},
			wantOutput: 32_768,
			wantFloor:  32_768,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, floor, err := compactionRequestPolicy(test.client, "test")
			if err != nil {
				t.Fatalf("compaction request policy: %v", err)
			}
			want := model.RequestPolicyFromCapabilities(model.CapabilitiesForClient(test.client))
			want.MaxOutputTokens = test.wantOutput
			want.CacheRetention = model.CacheRetentionNone
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("compaction policy = %+v, want only output changed in %+v", got, want)
			}
			if floor != test.wantFloor {
				t.Fatalf("compaction output floor = %d, want %d", floor, test.wantFloor)
			}
		})
	}
}

func TestCompactionRequestPolicyRejectsIncompatibleNormalAllowance(t *testing.T) {
	tests := []struct {
		name         string
		normalOutput int
		options      json.RawMessage
	}{
		{
			name:         "preferred and normal allowances conflict",
			normalOutput: 16_384,
			options:      json.RawMessage(`{"thinking":{"type":"enabled","budget_tokens":24576}}`),
		},
		{
			name:         "normal allowance conflicts even though preferred fits",
			normalOutput: 8_192,
			options:      json.RawMessage(`{"thinking":{"type":"enabled","budget_tokens":9999}}`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := anthropicmessages.Client{
				ProviderModelSlug: "claude-sonnet-4",
				ModelCapabilities: model.Capabilities{
					ContextWindowTokens:    200_000,
					MaxOutputTokens:        64_000,
					DefaultMaxOutputTokens: test.normalOutput,
				},
				APIVariantOptions: test.options,
			}
			_, _, err := compactionRequestPolicy(client, "anthropic_messages")
			var providerErr model.ProviderError
			if !errors.Is(err, model.ErrOutputTokenLimitIncompatible) ||
				!errors.As(err, &providerErr) ||
				providerErr.Code != model.OutputTokenLimitIncompatibleCode {
				t.Fatalf("compaction policy error = %v, want terminal output-limit conflict", err)
			}
		})
	}
}

func TestRunnerReturnsSuppliedTerminalClaimBeforeCompactionPreflight(t *testing.T) {
	input := runInput(testPlan(2, 2, 2))
	claim := newCompactionClaim(compactionClaimInput(input, time.Time{}), 0, time.Time{})
	claim.Claimed = false
	claim.Context.State = executionstore.ModelCallContextFailed
	claim.Context.ErrorCode = storeerr.ManagedWorkAdmissionDeniedCode

	store := &fakeStore{}
	client := &summaryModel{}
	result, err := testRunner(store, client).RunClaimed(context.Background(), input, claim)
	if err != nil {
		t.Fatalf("return supplied terminal compaction claim: %v", err)
	}
	if result.State != RunTerminal || result.ModelCallContextID != claim.Context.ID ||
		len(store.claimInputs) != 0 || len(client.requests) != 0 {
		t.Fatalf(
			"terminal result=%+v claims=%+v requests=%d",
			result,
			store.claimInputs,
			len(client.requests),
		)
	}
}

func TestRunnerClaimsNewContextForDueRetry(t *testing.T) {
	now := time.Unix(123, 0).UTC()
	input := runInput(testPlan(1, 1, 1))
	predecessor := newCompactionClaim(compactionClaimInput(input, now.Add(-time.Minute)), 1, now.Add(-time.Minute))
	retryAt := now.Add(-time.Second)
	predecessor.Created = false
	predecessor.Claimed = false
	predecessor.Context.State = executionstore.ModelCallContextFailed
	predecessor.Context.RecoveryKind = executionstore.ModelCallRecoveryRetry
	predecessor.Context.AttemptNumber = 1
	predecessor.Context.RetryAt = &retryAt

	store := &fakeStore{
		events: []executionstore.CompactionSourceEventRecord{
			textCompactionEvent(1, strings.Repeat("closed source context ", 20)),
		},
		claimResults: []executionstore.ModelCallClaim{predecessor},
	}
	client := &summaryModel{}
	result, err := testRunner(store, client, func() time.Time { return now }).
		Run(context.Background(), input)
	if err != nil {
		t.Fatalf("run due compaction retry: %v", err)
	}
	if result.State != RunCompleted || result.ModelCallContextID == predecessor.Context.ID ||
		len(store.nextClaimInputs) != 1 ||
		store.nextClaimInputs[0].PredecessorModelCallContextID != predecessor.Context.ID {
		t.Fatalf(
			"retry result=%+v next claims=%+v predecessor=%+v",
			result,
			store.nextClaimInputs,
			predecessor.Context,
		)
	}
	if len(client.requests) != 1 || len(store.publishInputs) != 1 ||
		store.publishInputs[0].ModelCallContextID != result.ModelCallContextID ||
		result.Checkpoint == nil ||
		result.Checkpoint.ProducerModelCallContextID != result.ModelCallContextID {
		t.Fatalf(
			"retry result=%+v requests=%+v publications=%+v",
			result,
			client.requests,
			store.publishInputs,
		)
	}
	claimed := store.claims[len(store.claims)-1].Context
	if claimed.AttemptNumber != 2 {
		t.Fatalf("retry attempt = %+v", claimed)
	}
}

func TestRunnerLeavesFutureRetryScheduled(t *testing.T) {
	now := time.Unix(123, 0).UTC()
	input := runInput(testPlan(1, 1, 1))
	claim := newCompactionClaim(compactionClaimInput(input, now.Add(-time.Minute)), 1, now.Add(-time.Minute))
	retryAt := now.Add(time.Minute)
	claim.Created = false
	claim.Claimed = false
	claim.Context.State = executionstore.ModelCallContextFailed
	claim.Context.RecoveryKind = executionstore.ModelCallRecoveryRetry
	claim.Context.RetryAt = &retryAt
	store := &fakeStore{
		events:       []executionstore.CompactionSourceEventRecord{textCompactionEvent(1, "closed source")},
		claimResults: []executionstore.ModelCallClaim{claim},
	}

	client := &summaryModel{}
	result, err := testRunner(store, client, func() time.Time { return now }).
		Run(context.Background(), input)
	if err != nil {
		t.Fatalf("run future compaction retry: %v", err)
	}
	if result.State != RunRetryScheduled || result.RetryAt == nil ||
		!result.RetryAt.Equal(retryAt) || len(client.requests) != 0 ||
		len(store.publishInputs) != 0 {
		t.Fatalf(
			"future retry result=%+v requests=%+v publications=%+v",
			result,
			client.requests,
			store.publishInputs,
		)
	}
}

func TestRunnerRetriesDatabaseUnsafeSummaryAsMalformedProviderResponse(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("closed source context ", 20)),
	}}
	client := &summaryModel{results: []summaryResult{{response: model.Response{
		ID:                      "resp_malformed",
		ProviderReportedCostUSD: "0.0000015",
		StopReason:              model.StopReasonEndTurn,
		Content: []model.ResponsePart{{
			Type: model.ResponsePartTypeText,
			Text: "unsafe\x00summary",
		}},
	}}}}
	result, err := testRunner(store, client).Run(
		context.Background(),
		runInput(testPlan(1, 1, 1)),
	)
	if err != nil {
		t.Fatalf("run malformed compaction response: %v", err)
	}
	if result.State != RunRetryScheduled || len(store.retryFailures) != 1 ||
		store.retryFailures[0].ErrorCode != "malformed_success_response" ||
		!strings.Contains(string(store.retryFailures[0].ErrorDetails), `"outcome_ambiguous":true`) ||
		len(store.publishInputs) != 0 {
		t.Fatalf(
			"malformed compaction outcome result=%+v retries=%+v publications=%+v",
			result,
			store.retryFailures,
			store.publishInputs,
		)
	}
	retryFailure := store.retryFailures[0]
	if retryFailure.ProviderResponseID != "resp_malformed" ||
		retryFailure.ProviderReportedCostUSD != "0.0000015" ||
		strings.Contains(retryFailure.ErrorMessage, "\x00") {
		t.Fatalf("malformed response evidence was not safely retained: %+v", retryFailure)
	}
}

func TestRunnerClearsDatabaseUnsafeResponseEvidenceWhenRetriesAreExhausted(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("closed source context ", 20)),
	}}
	client := &summaryModel{results: []summaryResult{{response: model.Response{
		ID:                      "resp\x00malformed",
		ServedProviderModelSlug: "served\x00malformed",
		ProviderReportedCostUSD: "0.0000015",
		StopReason:              model.StopReasonEndTurn,
		Content: []model.ResponsePart{{
			Type: model.ResponsePartTypeText,
			Text: "complete summary",
		}},
	}}}}
	firstResult, err := testRunner(store, client).Run(
		context.Background(),
		runInput(testPlan(1, 1, 1)),
	)
	if err != nil || firstResult.State != RunRetryScheduled {
		t.Fatalf("run first malformed compaction response: result=%+v err=%v", firstResult, err)
	}

	exhaustedClaim := store.claims[0]
	exhaustedClaim.Created = false
	exhaustedClaim.Context.ID = testIDN(299)
	exhaustedClaim.Context.AttemptNumber = executionstore.MaxModelCallRetriesPerOperation + 1
	exhaustedClaim.Context.State = executionstore.ModelCallContextStarted
	store.claimResults = []executionstore.ModelCallClaim{exhaustedClaim}
	client.results = []summaryResult{{response: model.Response{
		ID:                      "resp\x00malformed",
		ServedProviderModelSlug: "served\x00malformed",
		ProviderReportedCostUSD: "0.0000015",
		StopReason:              model.StopReasonEndTurn,
		Content: []model.ResponsePart{{
			Type: model.ResponsePartTypeText,
			Text: "complete summary",
		}},
	}}}

	terminalResult, err := testRunner(store, client).Run(
		context.Background(),
		runInput(testPlan(1, 1, 1)),
	)
	if err != nil {
		t.Fatalf("run exhausted malformed compaction response: %v", err)
	}
	if terminalResult.State != RunTerminal || len(store.terminalFailures) != 1 {
		t.Fatalf("terminal result=%+v failures=%+v", terminalResult, store.terminalFailures)
	}
	terminalFailure := store.terminalFailures[0]
	if terminalFailure.ServedProviderModelSlug != "" || terminalFailure.ProviderResponseID != "" ||
		terminalFailure.ProviderReportedCostUSD != "" ||
		terminalFailure.ErrorCode != "malformed_success_response" ||
		strings.Contains(terminalFailure.ErrorMessage, "\x00") {
		t.Fatalf("terminal malformed response evidence was not cleared: %+v", terminalFailure)
	}
}

func TestRunnerRetriesToolUsingCompactionResponseWithDatabaseSafeEvidence(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("closed source context ", 20)),
	}}
	client := &summaryModel{results: []summaryResult{{response: model.Response{
		ID:                      "resp_tool_use",
		ServedProviderModelSlug: "served-summary-model",
		StopReason:              model.StopReasonToolUse,
		Usage:                   model.Usage{InputTokens: 31, OutputTokens: 7},
		Content: []model.ResponsePart{{
			Type:           model.ResponsePartTypeToolCall,
			ProviderCallID: "call_1",
			ToolName:       "unexpected_tool",
			ToolInput:      json.RawMessage(`{}`),
		}},
	}}}}

	result, err := testRunner(store, client).Run(
		context.Background(),
		runInput(testPlan(1, 1, 1)),
	)
	if err != nil {
		t.Fatalf("run semantic compaction failure: %v", err)
	}
	if result.State != RunRetryScheduled || len(store.retryFailures) != 1 {
		t.Fatalf("semantic result=%+v failures=%+v", result, store.retryFailures)
	}
	failure := store.retryFailures[0]
	if failure.ErrorCode != "tool_use" || failure.ProviderResponseID != "resp_tool_use" ||
		failure.Usage.InputTokens != 31 || failure.Usage.OutputTokens != 7 {
		t.Fatalf("semantic response evidence was not retained: %+v", failure)
	}
}

func TestRunnerRetriesEmptyCompactionSummary(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("closed source context ", 20)),
	}}
	client := &summaryModel{results: []summaryResult{{response: model.Response{
		ID:         "resp_empty_summary",
		StopReason: model.StopReasonEndTurn,
	}}}}

	result, err := testRunner(store, client).Run(
		context.Background(),
		runInput(testPlan(1, 1, 1)),
	)
	if err != nil {
		t.Fatalf("run empty compaction summary: %v", err)
	}
	if result.State != RunRetryScheduled || len(store.retryFailures) != 1 ||
		store.retryFailures[0].ErrorCode != "empty_summary" {
		t.Fatalf("empty summary result=%+v failures=%+v", result, store.retryFailures)
	}
}

func TestRunnerRetriesAmbiguousCompactionStopReasons(t *testing.T) {
	for _, stopReason := range []model.StopReason{model.StopReasonUnknown, model.StopReasonError} {
		t.Run(string(stopReason), func(t *testing.T) {
			store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
				textCompactionEvent(1, strings.Repeat("closed source context ", 20)),
			}}
			client := &summaryModel{results: []summaryResult{{response: model.Response{
				ID:         "resp_ambiguous_stop",
				StopReason: stopReason,
			}}}}

			result, err := testRunner(store, client).Run(
				context.Background(),
				runInput(testPlan(1, 1, 1)),
			)
			if err != nil {
				t.Fatalf("run ambiguous compaction response: %v", err)
			}
			if result.State != RunRetryScheduled || len(store.retryFailures) != 1 ||
				!strings.Contains(string(store.retryFailures[0].ErrorDetails), `"outcome_ambiguous":true`) ||
				store.retryFailures[0].ErrorCode != string(stopReason) {
				t.Fatalf("ambiguous stop result=%+v failures=%+v", result, store.retryFailures)
			}
		})
	}
}

func TestRunnerDurablyRetriesUnclassifiedResolverFailureBeforeProviderSend(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, "closed source"),
	}}
	wantErr := errors.New("database connection unavailable")
	runner := Runner{
		Store:          store,
		Resolver:       errorResolver{err: wantErr},
		ContextBuilder: &fakeContextBuilder{},
	}
	result, err := runner.Run(context.Background(), runInput(testPlan(1, 1, 1)))
	if err != nil || result.State != RunRetryScheduled {
		t.Fatalf("run unclassified resolver failure: result=%+v err=%v", result, err)
	}
	if len(store.claims) != 1 || len(store.retryFailures) != 1 || len(store.terminalFailures) != 0 ||
		store.retryFailures[0].ErrorKind != model.ErrorKindTransient ||
		store.retryFailures[0].ErrorCode != "resolve_compaction_model_failed" ||
		!strings.Contains(string(store.retryFailures[0].ErrorDetails), wantErr.Error()) {
		t.Fatalf(
			"unclassified resolver evidence claims=%+v retry=%+v terminal=%+v",
			store.claims,
			store.retryFailures,
			store.terminalFailures,
		)
	}
}

func TestRunnerStopsAfterEighthRetryableResolverFailure(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, "closed source"),
	}}
	providerErr := model.ProviderError{
		Kind:    model.ErrorKindTransient,
		Source:  "test-resolver",
		Code:    "resolver_temporarily_unavailable",
		Message: "the model resolver is temporarily unavailable",
	}
	runner := Runner{
		Store:          store,
		Resolver:       errorResolver{err: providerErr},
		ContextBuilder: &fakeContextBuilder{},
	}
	input := runInput(testPlan(1, 1, 1))
	first, err := runner.Run(context.Background(), input)
	if err != nil || first.State != RunRetryScheduled || len(store.retryFailures) != 1 {
		t.Fatalf("first pre-send compaction failure: result=%+v retry=%+v err=%v", first, store.retryFailures, err)
	}

	exhaustedClaim := store.claims[0]
	exhaustedClaim.Created = false
	exhaustedClaim.Context.ID = testIDN(299)
	exhaustedClaim.Context.AttemptNumber = executionstore.MaxModelCallRetriesPerOperation + 1
	exhaustedClaim.Context.State = executionstore.ModelCallContextStarted
	exhaustedClaim.Context.RetryAt = nil
	store.claimResults = []executionstore.ModelCallClaim{exhaustedClaim}

	terminal, err := runner.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("ninth pre-send compaction failure: %v", err)
	}
	if terminal.State != RunTerminal || len(store.retryFailures) != 1 ||
		len(store.terminalFailures) != 1 {
		t.Fatalf(
			"ninth attempt result=%+v retries=%+v terminal=%+v",
			terminal,
			store.retryFailures,
			store.terminalFailures,
		)
	}
	failure := store.terminalFailures[0]
	if failure.ErrorKind != model.ErrorKindTransient ||
		failure.ErrorCode != "resolver_temporarily_unavailable" {
		t.Fatalf("ninth pre-send terminal failure = %+v", failure)
	}
}

func TestRunnerDurablyRetriesUnclassifiedPrepareFailureBeforeProviderSend(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, "closed source"),
	}}
	wantErr := errors.New("artifact store unavailable")
	client := &summaryModel{prepareErrs: []error{wantErr}}
	result, err := testRunner(store, client).Run(
		context.Background(),
		runInput(testPlan(1, 1, 1)),
	)
	if err != nil || result.State != RunRetryScheduled {
		t.Fatalf("run unclassified prepare failure: result=%+v err=%v", result, err)
	}
	if len(store.claims) != 1 || len(store.retryFailures) != 1 || len(store.terminalFailures) != 0 ||
		store.retryFailures[0].ErrorKind != model.ErrorKindTransient ||
		store.retryFailures[0].ErrorCode != "prepare_compaction_request_failed" ||
		!strings.Contains(string(store.retryFailures[0].ErrorDetails), wantErr.Error()) {
		t.Fatalf(
			"unclassified prepare evidence claims=%+v retry=%+v terminal=%+v",
			store.claims,
			store.retryFailures,
			store.terminalFailures,
		)
	}
}

func TestRunnerRecordsClassifiedPrepareFailureBeforeProviderSend(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, "closed source"),
	}}
	client := &summaryModel{prepareErrs: []error{model.ProviderError{
		Kind:    model.ErrorKindInvalidRequest,
		Source:  "test-provider",
		Code:    "unsupported_input_modality",
		Message: "the selected model does not accept this input modality",
	}}}
	result, err := testRunner(store, client).Run(
		context.Background(),
		runInput(testPlan(1, 1, 1)),
	)
	if err != nil {
		t.Fatalf("run classified prepare failure: %v", err)
	}
	if result.State != RunTerminal || len(store.terminalFailures) != 1 ||
		store.terminalFailures[0].ErrorKind != model.ErrorKindInvalidRequest ||
		store.terminalFailures[0].ErrorCode != "unsupported_input_modality" ||
		len(store.retryFailures) != 0 {
		t.Fatalf(
			"classified prepare result=%+v retry=%+v terminal=%+v",
			result,
			store.retryFailures,
			store.terminalFailures,
		)
	}
}

func TestRunnerTerminatesInvalidCompactionOutputPolicyBeforeProviderPreparation(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, "closed source"),
	}}
	providerClient := anthropicmessages.Client{
		ProviderModelSlug: "claude-sonnet-4",
		ModelCapabilities: model.Capabilities{
			ContextWindowTokens:    200_000,
			MaxOutputTokens:        64_000,
			DefaultMaxOutputTokens: 16_384,
		},
		APIVariantOptions: json.RawMessage(`{"thinking":{"type":"enabled","budget_tokens":24576}}`),
	}
	client := &trackedOutputLimitClient{
		Client:   providerClient,
		provider: providerClient,
	}
	result, err := testRunner(store, client).Run(
		context.Background(),
		runInput(testPlan(1, 1, 1)),
	)
	if err != nil {
		t.Fatalf("run invalid compaction output policy: %v", err)
	}
	if result.State != RunTerminal || len(store.terminalFailures) != 1 ||
		store.terminalFailures[0].ErrorKind != model.ErrorKindInvalidRequest ||
		store.terminalFailures[0].ErrorCode != model.OutputTokenLimitIncompatibleCode ||
		len(store.retryFailures) != 0 || len(store.replacements) != 0 ||
		len(store.publishInputs) != 0 {
		t.Fatalf(
			"invalid output policy result=%+v retry=%+v terminal=%+v replacements=%+v publications=%+v",
			result,
			store.retryFailures,
			store.terminalFailures,
			store.replacements,
			store.publishInputs,
		)
	}
	if client.prepareCalls != 0 || client.respondCalls != 0 || client.validationCalls < 1 {
		t.Fatalf(
			"provider boundary calls: validate=%d prepare=%d respond=%d, want validation and no provider work",
			client.validationCalls,
			client.prepareCalls,
			client.respondCalls,
		)
	}
}

type trackedOutputLimitClient struct {
	model.Client
	provider        model.OutputTokenLimitProvider
	validationCalls int
	prepareCalls    int
	respondCalls    int
}

func (c *trackedOutputLimitClient) OutputTokenLimits() (model.OutputTokenLimits, error) {
	c.validationCalls++
	return c.provider.OutputTokenLimits()
}

func (c *trackedOutputLimitClient) Prepare(
	ctx context.Context,
	input model.PrepareInput,
) (model.PreparedRequest, error) {
	c.prepareCalls++
	return c.Client.Prepare(ctx, input)
}

func (c *trackedOutputLimitClient) Respond(
	ctx context.Context,
	input model.Request,
) (model.Response, error) {
	c.respondCalls++
	return c.Client.Respond(ctx, input)
}

func TestRunnerClassifiesMissingLiveGrantAsAuthFailure(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, "closed source"),
	}}
	runner := Runner{
		Store:          store,
		Resolver:       errorResolver{err: storeerr.ErrModelGrantUnavailable},
		ContextBuilder: &fakeContextBuilder{},
	}
	result, err := runner.Run(context.Background(), runInput(testPlan(1, 1, 1)))
	if err != nil {
		t.Fatalf("run without live grant: %v", err)
	}
	if result.State != RunTerminal || len(store.terminalFailures) != 1 ||
		store.terminalFailures[0].ErrorKind != model.ErrorKindAuth ||
		store.terminalFailures[0].ErrorCode != "model_grant_unavailable" {
		t.Fatalf(
			"missing grant result=%+v failures=%+v",
			result,
			store.terminalFailures,
		)
	}
}

func TestRunnerSchedulesExplicitRateLimitFailureDurably(t *testing.T) {
	seconds := int64(17)
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("retry source ", 50)),
	}}
	client := &summaryModel{results: []summaryResult{{err: model.ProviderError{
		Kind: model.ErrorKindRateLimit, Source: "openai-responses", Code: "rate_limit_exceeded",
		Message: "try later", RequestID: "req_rate", RetryAfter: &model.RetryAfter{DeltaSeconds: &seconds},
	}}}}
	now := time.Unix(1_000, 0).UTC()
	result, err := testRunner(store, client, func() time.Time { return now }).
		Run(context.Background(), runInput(testPlan(1, 1, 1)))
	if err != nil {
		t.Fatalf("run rate-limited compaction: %v", err)
	}
	if result.State != RunRetryScheduled || result.RetryAt == nil ||
		!result.RetryAt.Equal(now.Add(17*time.Second)) {
		t.Fatalf("retry result = %+v", result)
	}
	if len(store.retryFailures) != 1 ||
		store.retryFailures[0].APIFormat != client.APIFormat() ||
		store.retryFailures[0].APIVariant != client.ModelAPIVariant() ||
		store.retryFailures[0].ProviderRequestID != "req_rate" {
		t.Fatalf("durable retry evidence = %+v", store.retryFailures)
	}
}

func TestRunnerRecordsAmbiguousPostSendFailureInEvidence(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("ambiguous source ", 50)),
	}}
	client := &summaryModel{results: []summaryResult{{err: model.AmbiguousProviderOutcome(
		model.ProviderError{Kind: model.ErrorKindTransient, Code: "connection_reset", Message: "connection reset"},
	)}}}
	result, err := testRunner(store, client).
		Run(context.Background(), runInput(testPlan(1, 1, 1)))
	if err != nil {
		t.Fatalf("run ambiguous compaction: %v", err)
	}
	if result.State != RunRetryScheduled || len(store.retryFailures) != 1 ||
		!strings.Contains(string(store.retryFailures[0].ErrorDetails), `"outcome_ambiguous":true`) {
		t.Fatalf("ambiguous failure result=%+v evidence=%+v", result, store.retryFailures)
	}
}

func TestRunnerStopsAuthFailureWithoutRetry(t *testing.T) {
	seconds := int64(19)
	retryAt := time.Unix(5_000, 0).UTC()
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("auth source ", 50)),
	}}
	client := &summaryModel{results: []summaryResult{{err: model.ProviderError{
		Kind: model.ErrorKindAuth, Code: "invalid_api_key", Message: "invalid key",
		RetryAfter: &model.RetryAfter{DeltaSeconds: &seconds, HTTPDate: &retryAt},
	}}}}
	result, err := testRunner(store, client).
		Run(context.Background(), runInput(testPlan(1, 1, 1)))
	if err != nil {
		t.Fatalf("run auth failure: %v", err)
	}
	if result.State != RunTerminal || len(store.retryFailures) != 0 ||
		len(store.terminalFailures) != 1 || store.terminalFailures[0].ErrorKind != model.ErrorKindAuth ||
		!strings.Contains(string(store.terminalFailures[0].ErrorDetails), "retry_after") {
		t.Fatalf("auth failure result=%+v retries=%+v terminal=%+v", result, store.retryFailures, store.terminalFailures)
	}
}

func TestRunnerStopsReplayRejectionWithoutReplayRecovery(t *testing.T) {
	retry := true
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("canonical source ", 50)),
	}}
	client := &summaryModel{results: []summaryResult{{err: model.ProviderError{
		Kind:      model.ErrorKindReplayRejected,
		Code:      "invalid_encrypted_content",
		Message:   "provider labeled the canonical summary request as rejected replay",
		Retryable: &retry,
	}}}}
	result, err := testRunner(store, client).
		Run(context.Background(), runInput(testPlan(1, 1, 1)))
	if err != nil {
		t.Fatalf("run replay-rejected compaction: %v", err)
	}
	if result.State != RunTerminal || len(store.retryFailures) != 0 ||
		len(store.terminalFailures) != 1 ||
		store.terminalFailures[0].ErrorKind != model.ErrorKindReplayRejected {
		t.Fatalf(
			"replay-rejected compaction result=%+v retries=%+v terminal=%+v",
			result,
			store.retryFailures,
			store.terminalFailures,
		)
	}
}

func TestRunnerReplacesTruncatedSummaryWithStrictlySmallerSafeSource(t *testing.T) {
	store := &fakeStore{
		events: []executionstore.CompactionSourceEventRecord{
			textCompactionEvent(1, strings.Repeat("first ", 100)),
			textCompactionEvent(2, strings.Repeat("tool call ", 100)),
			textCompactionEvent(3, strings.Repeat("tool result ", 100)),
			textCompactionEvent(4, strings.Repeat("fourth ", 100)),
		},
		atomicGroups: []executionstore.CompactionAtomicGroupRecord{{
			Kind: "tool_call_result", StartSequence: 2, EndSequence: 3,
		}},
	}
	client := &summaryModel{results: []summaryResult{
		{response: model.Response{
			ID:                      "resp_truncated",
			ServedProviderModelSlug: "served-summary-model",
			StopReason:              model.StopReasonMaxTokens,
			Usage:                   model.Usage{InputTokens: 41, OutputTokens: 9},
			Content:                 []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "partial"}},
		}},
		{response: completeSummaryResponse("## Goal\nPreserve the first completed unit.\n\n## Next Steps\nContinue.")},
	}}
	result, err := testRunner(store, client).
		Run(context.Background(), runInput(testPlan(1, 4, 4)))
	if err != nil {
		t.Fatalf("run truncated compaction: %v", err)
	}
	if result.State != RunCompleted || len(store.replacements) != 1 {
		t.Fatalf("truncated result=%+v replacements=%+v", result, store.replacements)
	}
	nextEnd := store.replacements[0].NextSourceEventSequenceEnd
	if nextEnd >= 4 || nextEnd == 2 {
		t.Fatalf("replacement source end = %d, want a strictly smaller safe boundary", nextEnd)
	}
	if store.replacements[0].ErrorCode != "summary_truncated" ||
		store.replacements[0].ProviderResponseID != "resp_truncated" ||
		store.replacements[0].APIFormat != client.APIFormat() ||
		store.replacements[0].APIVariant != client.ModelAPIVariant() ||
		store.replacements[0].Usage.InputTokens != 41 ||
		store.replacements[0].Usage.OutputTokens != 9 {
		t.Fatalf("replacement evidence = %+v", store.replacements[0])
	}
	if len(store.claimInputs) != 1 || store.claims[1].Context.SourceEventSequenceEnd == nil ||
		*store.claims[1].Context.SourceEventSequenceEnd != nextEnd ||
		len(store.publishInputs) != 1 || store.publishInputs[0].ProviderRequestID != "req_complete" {
		t.Fatalf("structural retry claims=%+v publishes=%+v", store.claimInputs, store.publishInputs)
	}
}

func TestRunnerBoundsErrorEvidenceBeforeReplacingCompactionSource(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("first ", 100)),
		textCompactionEvent(2, strings.Repeat("second ", 100)),
	}}
	client := &summaryModel{results: []summaryResult{
		{
			response: model.Response{
				ID:                      strings.Repeat("r", model.MaxProviderIdentityBytes+1),
				ServedProviderModelSlug: "served-summary-model",
				Usage:                   model.Usage{InputTokens: 23, OutputTokens: 7},
			},
			err: model.ProviderError{
				Kind: model.ErrorKindContextWindow, Code: "context_length_exceeded", Message: "source is too large",
			},
		},
		{response: completeSummaryResponse("## Goal\nPreserve the first unit.\n\n## Next Steps\nContinue.")},
	}}
	result, err := testRunner(store, client).
		Run(context.Background(), runInput(testPlan(1, 2, 2)))
	if err != nil {
		t.Fatalf("run compaction with unsafe error evidence: %v", err)
	}
	if result.State != RunCompleted || len(store.replacements) != 1 {
		t.Fatalf("compaction result=%+v replacements=%+v", result, store.replacements)
	}
	replacement := store.replacements[0]
	if replacement.ProviderResponseID != "" ||
		replacement.Usage != (model.Usage{}) {
		t.Fatalf("replacement retained unsafe provider evidence: %+v", replacement)
	}
}

func TestRunnerReplacesNonReducingSummaryWithSmallerSource(t *testing.T) {
	priorSummary := strings.Repeat("p", 16_000)
	checkpointEvent := mustCompactionEvent(
		2,
		string(events.KindContextCheckpoint),
		"published",
		nil,
	)
	store := &fakeStore{
		priorCheckpoint: &executionstore.ContextCheckpointRecord{
			ID:                             testIDN(800),
			SummarizedThroughEventSequence: 1,
			CheckpointEventSequence:        2,
			Summary:                        priorSummary,
		},
		events: []executionstore.CompactionSourceEventRecord{
			checkpointEvent,
			textCompactionEvent(3, strings.Repeat("first new source ", 100)),
			textCompactionEvent(4, strings.Repeat("second new source ", 100)),
		},
	}
	client := &summaryModel{results: []summaryResult{
		{response: completeSummaryResponse(priorSummary + strings.Repeat("n", 4_000))},
		{response: completeSummaryResponse(priorSummary + "\n\n## Next Steps\nContinue.")},
	}}

	result, err := testRunner(store, client).
		Run(context.Background(), runInput(testPlan(2, 4, 4)))
	if err != nil {
		t.Fatalf("run non-reducing compaction: %v", err)
	}
	if result.State != RunCompleted || len(store.retryFailures) != 0 ||
		len(store.replacements) != 1 ||
		store.replacements[0].ErrorCode != "summary_not_reduced" ||
		store.replacements[0].NextSourceEventSequenceEnd != 3 ||
		len(store.publishInputs) != 1 {
		t.Fatalf(
			"non-reducing replacement result=%+v retries=%+v replacements=%+v publishes=%+v",
			result,
			store.retryFailures,
			store.replacements,
			store.publishInputs,
		)
	}
	if len(store.claims) != 2 ||
		store.replacements[0].ModelCallContextID != store.claims[0].Context.ID {
		t.Fatalf(
			"non-reducing failure did not create a structural replacement: claims=%+v replacements=%+v",
			store.claims,
			store.replacements,
		)
	}
}

func TestRunnerReplacesContextWindowStopWithStrictlySmallerSafeSource(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("first ", 100)),
		textCompactionEvent(2, strings.Repeat("second ", 100)),
	}}
	client := &summaryModel{results: []summaryResult{
		{response: model.Response{ID: "resp_context_window", StopReason: model.StopReasonContextWindow}},
		{response: completeSummaryResponse("## Goal\nPreserve the first unit.\n\n## Next Steps\nContinue.")},
	}}
	result, err := testRunner(store, client).
		Run(context.Background(), runInput(testPlan(1, 2, 2)))
	if err != nil {
		t.Fatalf("run context-window compaction: %v", err)
	}
	if result.State != RunCompleted || len(store.replacements) != 1 ||
		store.replacements[0].NextSourceEventSequenceEnd != 1 ||
		store.replacements[0].ErrorKind != model.ErrorKindContextWindow ||
		store.replacements[0].ErrorCode != string(model.StopReasonContextWindow) {
		t.Fatalf("context-window result=%+v replacements=%+v", result, store.replacements)
	}
}

func TestRunnerReplacesPayloadTooLargeFailureWithStrictlySmallerSafeSource(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("first ", 100)),
		textCompactionEvent(2, strings.Repeat("second ", 100)),
	}}
	client := &summaryModel{results: []summaryResult{
		{err: model.ProviderError{
			Kind: model.ErrorKindPayloadTooLarge,
			Code: "request_too_large",
		}},
		{response: completeSummaryResponse("## Goal\nPreserve the first unit.\n\n## Next Steps\nContinue.")},
	}}
	result, err := testRunner(store, client).
		Run(context.Background(), runInput(testPlan(1, 2, 2)))
	if err != nil {
		t.Fatalf("run payload-too-large compaction: %v", err)
	}
	if result.State != RunCompleted || len(store.replacements) != 1 ||
		store.replacements[0].NextSourceEventSequenceEnd != 1 ||
		store.replacements[0].ErrorKind != model.ErrorKindPayloadTooLarge ||
		store.replacements[0].ErrorCode != "request_too_large" {
		t.Fatalf("payload-too-large result=%+v replacements=%+v", result, store.replacements)
	}
}

func TestRunnerStopsSmallestTruncatedSummaryWithoutRepeatingRequest(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("only closed unit ", 100)),
	}}
	client := &summaryModel{results: []summaryResult{
		{response: model.Response{
			ID:                      "resp_truncated",
			ServedProviderModelSlug: "served-summary-model",
			StopReason:              model.StopReasonMaxTokens,
			Usage:                   model.Usage{InputTokens: 51, OutputTokens: 11},
			Content:                 []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "partial"}},
		}},
	}}
	result, err := testRunner(store, client).Run(context.Background(), runInput(testPlan(1, 1, 1)))
	if err != nil {
		t.Fatalf("run truncated compaction: %v", err)
	}
	if result.State != RunTerminal || len(store.publishInputs) != 0 || len(store.retryFailures) != 0 ||
		len(store.terminalFailures) != 1 ||
		store.terminalFailures[0].ErrorCode != "compaction_source_irreducible" ||
		!strings.Contains(store.terminalFailures[0].ErrorMessage, "truncated summary") ||
		len(client.requests) != 1 {
		t.Fatalf(
			"truncated result=%+v retries=%+v terminal=%+v publishes=%+v requests=%d",
			result,
			store.retryFailures,
			store.terminalFailures,
			store.publishInputs,
			len(client.requests),
		)
	}
	failure := store.terminalFailures[0]
	if failure.ProviderResponseID != "resp_truncated" ||
		failure.ServedProviderModelSlug != "served-summary-model" ||
		failure.Usage.InputTokens != 51 || failure.Usage.OutputTokens != 11 {
		t.Fatalf("terminal response evidence was not retained: %+v", failure)
	}
}

func TestRunnerStopsSmallestNonReducingSummaryWithoutRepeatingRequest(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, "short closed unit"),
	}}
	client := &summaryModel{results: []summaryResult{{
		response: completeSummaryResponse(strings.Repeat("larger summary ", 20)),
	}}}
	result, err := testRunner(store, client).Run(context.Background(), runInput(testPlan(1, 1, 1)))
	if err != nil {
		t.Fatalf("run non-reducing compaction: %v", err)
	}
	if result.State != RunTerminal || len(store.retryFailures) != 0 || len(store.publishInputs) != 0 ||
		len(store.terminalFailures) != 1 ||
		store.terminalFailures[0].ErrorCode != "compaction_source_irreducible" ||
		!strings.Contains(store.terminalFailures[0].ErrorMessage, "smaller summary") ||
		len(client.requests) != 1 {
		t.Fatalf(
			"non-reducing result=%+v retries=%+v terminal=%+v publishes=%+v requests=%d",
			result,
			store.retryFailures,
			store.terminalFailures,
			store.publishInputs,
			len(client.requests),
		)
	}
}

func TestValidateSummaryReductionRejectsNonShrinkingOutput(t *testing.T) {
	err := validateSummaryReduction("", "short source", strings.Repeat("larger summary ", 20))
	var providerErr model.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Code != "summary_not_reduced" {
		t.Fatalf("non-reduction error = %v", err)
	}
}

func TestCompactionControlFlowDoesNotTrustProviderErrorCodes(t *testing.T) {
	providerError := model.ProviderError{
		Kind: model.ErrorKindTransient,
		Code: compactionErrorCodeSummaryTruncated,
	}
	if shrinkableCompactionFailure(providerError) {
		t.Fatal("provider error code must not trigger structural compaction retry")
	}
	providerError.Code = compactionErrorCodeSourceIrreducible
	if isIrreducibleCompactionFailure(providerError) {
		t.Fatal("provider error code must not force terminal compaction failure")
	}

	typedError := withCompactionFailureReason(
		compactionFailureSummaryTruncated,
		providerError,
	)
	if !shrinkableCompactionFailure(typedError) {
		t.Fatal("typed summary truncation should trigger structural compaction retry")
	}
	typedIrreducible := withCompactionFailureReason(
		compactionFailureSourceIrreducible,
		providerError,
	)
	if !isIrreducibleCompactionFailure(typedIrreducible) {
		t.Fatal("typed irreducible source should force terminal compaction failure")
	}
}

func TestValidateSummaryReductionDoesNotRecompressPriorSummary(t *testing.T) {
	prior := strings.Repeat("p", 4_000)
	source := strings.Repeat("s", 400)
	cumulative := prior + strings.Repeat("n", 100)
	if err := validateSummaryReduction(prior, source, cumulative); err != nil {
		t.Fatalf("valid cumulative reduction was rejected: %v", err)
	}
}
