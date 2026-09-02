package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestModelWindowForRequestUsesExactPolicy(t *testing.T) {
	capabilities := Capabilities{
		ContextWindowTokens:    200000,
		MaxOutputTokens:        64000,
		DefaultMaxOutputTokens: 2048,
	}
	policy := RequestPolicy{MaxOutputTokens: 32_000}
	window := modelWindowForRequest(capabilities, policy)
	if window.RequestMaxOutputTokens != 32_000 || window.SafetyMarginTokens == 0 {
		t.Fatalf("request window = %+v, want exact policy max and safety margin", window)
	}
	if usable := UsableInputTokensForRequest(capabilities, policy); usable != 159_808 {
		t.Fatalf("usable input tokens = %d, want 159808", usable)
	}
}

func TestPrepareForSendIgnoresProviderNeutralBundleSize(t *testing.T) {
	body := json.RawMessage(`{"request":true}`)
	bundle := modelcontext.Bundle{
		AvailableMachinePools: []modelcontext.MachinePoolRef{{
			MachinePoolName: "provider-omits-this-field",
			Description:     strings.Repeat("internal metadata ", 10_000),
		}},
	}
	client := prepareForSendClient{
		prepared:     PreparedRequest{Body: body},
		capabilities: Capabilities{ContextWindowTokens: 1_000},
	}

	prepared, err := PrepareForSend(
		context.Background(),
		client,
		PrepareForSendInput{
			Context:     bundle,
			Policy:      RequestPolicy{MaxOutputTokens: 100},
			ErrorSource: "test_api",
		},
	)
	if err != nil {
		t.Fatalf("prepare small provider request from large neutral bundle: %v", err)
	}
	if string(prepared.Body) != string(body) ||
		prepared.InputTokenEstimate != modelcontext.EstimatePreparedRequest(body, nil) {
		t.Fatalf("prepared request / estimate = %s/%d", prepared.Body, prepared.InputTokenEstimate)
	}
}

func TestRequestPolicyFromCapabilitiesFallsBackToOutputCeiling(t *testing.T) {
	policy := RequestPolicyFromCapabilities(Capabilities{MaxOutputTokens: 64_000})
	if policy.MaxOutputTokens != 64_000 {
		t.Fatalf("request policy max output = %d, want ceiling 64000", policy.MaxOutputTokens)
	}
}

func TestRequestPolicyAllowsProviderReplayAfterCutoff(t *testing.T) {
	policy := RequestPolicy{ProviderReplayCutoffEventSequence: 41}
	if policy.AllowsProviderReplay(41) {
		t.Fatal("replay at the rejected frontier was allowed")
	}
	if !policy.AllowsProviderReplay(42) {
		t.Fatal("replay created after the rejected frontier was suppressed")
	}
	if !(RequestPolicy{}).AllowsProviderReplay(1) {
		t.Fatal("zero cutoff did not allow replay")
	}
}

func TestPrepareForSendAssessesSerializedRequestEstimate(t *testing.T) {
	bundle := modelcontext.Bundle{}
	client := prepareForSendClient{prepared: PreparedRequest{
		Body:               json.RawMessage(`{"request":true}`),
		InputTokenEstimate: 900,
	}, capabilities: Capabilities{ContextWindowTokens: 1_000}}
	prepared, err := PrepareForSend(
		context.Background(),
		client,
		PrepareForSendInput{
			Context:     bundle,
			Policy:      RequestPolicy{MaxOutputTokens: 200},
			ErrorSource: "test_api",
		},
	)
	if err != nil {
		t.Fatalf("prepare over-budget request: %v", err)
	}
	if prepared.InputBudget.Fits() || prepared.InputBudget.EstimatedInputTokens != 900 ||
		prepared.InputBudget.UsableInputTokens != 750 {
		t.Fatalf("serialized request assessment = %+v, want 900 > 750", prepared.InputBudget)
	}
	client.prepared.InputTokenEstimate = 750
	prepared, err = PrepareForSend(
		context.Background(),
		client,
		PrepareForSendInput{
			Context:     bundle,
			Policy:      RequestPolicy{MaxOutputTokens: 200},
			ErrorSource: "test_api",
		},
	)
	if err != nil {
		t.Fatalf("prepare fitting serialized request: %v", err)
	}
	if prepared.InputTokenEstimate != 750 || !prepared.InputBudget.Fits() ||
		string(prepared.Body) != `{"request":true}` {
		t.Fatalf("exact-boundary prepared request / assessment = %s/%+v", prepared.Body, prepared.InputBudget)
	}
}

func TestPrepareForSendRejectsEmptyProviderRequest(t *testing.T) {
	bundle := modelcontext.Bundle{}
	_, err := PrepareForSend(
		context.Background(),
		prepareForSendClient{},
		PrepareForSendInput{Context: bundle, ErrorSource: "test_api"},
	)
	var providerErr ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorKindInvalidRequest ||
		providerErr.Code != "empty_prepared_request" {
		t.Fatalf("empty prepared request = %v, want invalid-request provider error", err)
	}
}

func TestPrepareForSendRejectsProviderOutputLimitConflictBeforePreparation(t *testing.T) {
	client := &outputLimitPrepareClient{
		prepareForSendClient: prepareForSendClient{
			prepared: PreparedRequest{
				Body:               json.RawMessage(`{"request":true}`),
				InputTokenEstimate: 10,
			},
			capabilities: Capabilities{ContextWindowTokens: 100_000},
		},
		limits: OutputTokenLimits{Minimum: 16_385},
	}
	_, err := PrepareForSend(
		context.Background(),
		client,
		PrepareForSendInput{
			Policy:      RequestPolicy{MaxOutputTokens: 16_384},
			ErrorSource: "test_api",
		},
	)
	var providerErr ProviderError
	if !errors.Is(err, ErrOutputTokenLimitIncompatible) ||
		!errors.As(err, &providerErr) ||
		providerErr.Kind != ErrorKindInvalidRequest ||
		providerErr.Code != OutputTokenLimitIncompatibleCode {
		t.Fatalf("output-limit conflict = %v, want classified incompatible error", err)
	}
	if client.prepareCalls != 0 {
		t.Fatalf("provider preparation calls = %d, want zero", client.prepareCalls)
	}

	client.limitsErr = errors.New("malformed output options")
	_, err = PrepareForSend(
		context.Background(),
		client,
		PrepareForSendInput{
			Policy:      RequestPolicy{MaxOutputTokens: 16_384},
			ErrorSource: "test_api",
		},
	)
	if !errors.As(err, &providerErr) ||
		providerErr.Kind != ErrorKindInvalidRequest ||
		providerErr.Code != InvalidOutputTokenConfigurationCode {
		t.Fatalf("invalid output configuration = %v, want classified invalid request", err)
	}
	if client.prepareCalls != 0 {
		t.Fatalf("provider preparation calls = %d, want zero", client.prepareCalls)
	}

	client.limitsErr = nil
	client.limits = OutputTokenLimits{Minimum: -1}
	_, err = PrepareForSend(
		context.Background(),
		client,
		PrepareForSendInput{
			Policy:      RequestPolicy{MaxOutputTokens: 16_384},
			ErrorSource: "test_api",
		},
	)
	if !errors.As(err, &providerErr) ||
		providerErr.Kind != ErrorKindInvalidRequest ||
		providerErr.Code != InvalidOutputTokenConfigurationCode {
		t.Fatalf("negative output limit = %v, want classified invalid request", err)
	}
	if client.prepareCalls != 0 {
		t.Fatalf("provider preparation calls = %d, want zero", client.prepareCalls)
	}

	client.limits = OutputTokenLimits{}
	if _, err := PrepareForSend(
		context.Background(),
		client,
		PrepareForSendInput{
			Policy:      RequestPolicy{MaxOutputTokens: 32_768},
			ErrorSource: "test_api",
		},
	); err != nil {
		t.Fatalf("prepare compatible output limit: %v", err)
	}
	if client.prepareCalls != 1 {
		t.Fatalf("provider preparation calls = %d, want one", client.prepareCalls)
	}
}

func TestPrepareForSendEnforcesLiveModalities(t *testing.T) {
	bundle := modelcontext.Bundle{
		RenderedMedia: []modelcontext.RenderedMedia{{
			Media: modelcontext.ResolvedMedia{Kind: modelcontext.AttachmentKindImage},
		}},
	}
	client := prepareForSendClient{
		prepared: PreparedRequest{Body: json.RawMessage(`{"request":true}`), InputTokenEstimate: 100},
		capabilities: Capabilities{
			ContextWindowTokens: 100_000,
			InputModalities:     []string{"text"},
			OutputModalities:    []string{"text"},
		},
	}
	prepareInput := PrepareForSendInput{Context: bundle, ErrorSource: "test_api"}
	_, err := PrepareForSend(context.Background(), client, prepareInput)
	var providerErr ProviderError
	if !errors.As(err, &providerErr) || providerErr.Code != "unsupported_input_modality" {
		t.Fatalf("image modality error = %v, want unsupported input modality", err)
	}

	client.capabilities.InputModalities = []string{"text", "image"}
	bundle.RenderedMedia[0].Media.Kind = modelcontext.AttachmentKindDocument
	prepareInput.Context = bundle
	_, err = PrepareForSend(context.Background(), client, prepareInput)
	if !errors.As(err, &providerErr) || providerErr.Code != "unsupported_input_modality" ||
		!strings.Contains(providerErr.Message, "file input") {
		t.Fatalf("file modality error = %v, want unsupported file input modality", err)
	}

	bundle.RenderedMedia[0].Representation = modelcontext.MediaRepresentationInlineText
	prepareInput.Context = bundle
	if _, err := PrepareForSend(context.Background(), client, prepareInput); err != nil {
		t.Fatalf("inline text document: %v", err)
	}

	bundle.RenderedMedia[0].Representation = modelcontext.MediaRepresentationInline
	bundle.RenderedMedia[0].Media.MediaType = "application/pdf"
	prepareInput.Context = bundle
	client.apiVariant = modelprotocol.APIVariantOpenRouter
	if _, err := PrepareForSend(context.Background(), client, prepareInput); err != nil {
		t.Fatalf("OpenRouter PDF: %v", err)
	}

	bundle.RenderedMedia[0].Media.MediaType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	prepareInput.Context = bundle
	_, err = PrepareForSend(context.Background(), client, prepareInput)
	if !errors.As(err, &providerErr) || providerErr.Code != "unsupported_input_modality" {
		t.Fatalf("OpenRouter Office file modality error = %v, want unsupported input modality", err)
	}

	client.capabilities.InputModalities = []string{"text", "image", "file"}
	client.capabilities.OutputModalities = []string{"audio"}
	_, err = PrepareForSend(context.Background(), client, prepareInput)
	if !errors.As(err, &providerErr) || providerErr.Code != "unsupported_output_modality" {
		t.Fatalf("output modality error = %v, want unsupported output modality", err)
	}

	client.capabilities.OutputModalities = []string{"text"}
	if _, err := PrepareForSend(context.Background(), client, prepareInput); err != nil {
		t.Fatalf("compatible modalities: %v", err)
	}
}

func TestParseRetryAfterPreservesWireForm(t *testing.T) {
	delta := ParseRetryAfter("120")
	if delta == nil || delta.DeltaSeconds == nil || *delta.DeltaSeconds != 120 ||
		delta.DelayMilliseconds != nil || delta.HTTPDate != nil {
		t.Fatalf("delta Retry-After = %+v", delta)
	}

	fractional := ParseRetryAfter("0.125")
	if fractional == nil || fractional.DeltaSeconds != nil ||
		fractional.DelayMilliseconds == nil || *fractional.DelayMilliseconds != 125 ||
		fractional.HTTPDate != nil {
		t.Fatalf("fractional Retry-After = %+v", fractional)
	}

	milliseconds := RetryAfterFromHeader(http.Header{
		"Retry-After-Ms": []string{"1500.25"},
		"Retry-After":    []string{"120"},
	})
	if milliseconds == nil || milliseconds.DeltaSeconds != nil ||
		milliseconds.DelayMilliseconds == nil || *milliseconds.DelayMilliseconds != 1501 ||
		milliseconds.HTTPDate != nil {
		t.Fatalf("millisecond Retry-After = %+v", milliseconds)
	}

	const absolute = "Wed, 21 Oct 2015 07:28:00 GMT"
	date := RetryAfterFromHeader(http.Header{"Retry-After": []string{absolute}})
	wantDate := time.Date(2015, time.October, 21, 7, 28, 0, 0, time.UTC)
	if date == nil || date.DeltaSeconds != nil || date.DelayMilliseconds != nil ||
		date.HTTPDate == nil || !date.HTTPDate.Equal(wantDate) {
		t.Fatalf("date Retry-After = %+v, want %s", date, wantDate)
	}
	if got := ParseRetryAfter("not-a-retry-delay"); got != nil {
		t.Fatalf("malformed Retry-After = %+v, want nil", got)
	}
	if got := RetryAfterFromHeader(http.Header{"Retry-After-Ms": []string{"1e30"}}); got != nil {
		t.Fatalf("overflowing Retry-After-Ms = %+v, want nil", got)
	}
}

func TestShouldRetryFromHeaderAcceptsOnlyExplicitBooleans(t *testing.T) {
	for _, test := range []struct {
		value string
		want  *bool
	}{
		{value: "true", want: boolPointer(true)},
		{value: " FALSE ", want: boolPointer(false)},
		{value: "1"},
		{value: ""},
	} {
		got := ShouldRetryFromHeader(http.Header{"X-Should-Retry": []string{test.value}})
		if got == nil || test.want == nil {
			if got != nil || test.want != nil {
				t.Fatalf("X-Should-Retry %q = %v, want %v", test.value, got, test.want)
			}
			continue
		}
		if *got != *test.want {
			t.Fatalf("X-Should-Retry %q = %v, want %v", test.value, *got, *test.want)
		}
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func TestAmbiguousProviderOutcomePreservesCauseAndClassification(t *testing.T) {
	cause := ProviderError{Kind: ErrorKindTransient, RequestID: "req_1"}
	err := AmbiguousProviderOutcome(cause)
	if !IsAmbiguousProviderOutcome(err) {
		t.Fatalf("error = %T %v, want ambiguous provider outcome", err, err)
	}
	var providerErr ProviderError
	if !errors.As(err, &providerErr) || providerErr.RequestID != "req_1" {
		t.Fatalf("wrapped provider error = %+v", providerErr)
	}
	var wantWrapper *AmbiguousProviderOutcomeError
	if !errors.As(err, &wantWrapper) {
		t.Fatalf("error = %T %v, want ambiguous wrapper", err, err)
	}
	var gotWrapper *AmbiguousProviderOutcomeError
	if !errors.As(AmbiguousProviderOutcome(err), &gotWrapper) || gotWrapper != wantWrapper {
		t.Fatal("wrapping an ambiguous outcome should be idempotent")
	}
}

type prepareForSendClient struct {
	prepared     PreparedRequest
	err          error
	capabilities Capabilities
	apiVariant   modelprotocol.APIVariant
}

func (c prepareForSendClient) RequestedProviderModelSlug() string { return "prepare-test" }
func (prepareForSendClient) APIFormat() modelprotocol.APIFormat   { return "test" }
func (c prepareForSendClient) ModelAPIVariant() modelprotocol.APIVariant {
	return c.apiVariant
}

func (c prepareForSendClient) Prepare(context.Context, PrepareInput) (PreparedRequest, error) {
	return c.prepared, c.err
}

func (prepareForSendClient) Respond(context.Context, Request) (Response, error) {
	return Response{}, nil
}

func (c prepareForSendClient) Capabilities() Capabilities { return c.capabilities }

type outputLimitPrepareClient struct {
	prepareForSendClient
	limits       OutputTokenLimits
	limitsErr    error
	prepareCalls int
}

func (c *outputLimitPrepareClient) OutputTokenLimits() (OutputTokenLimits, error) {
	return c.limits, c.limitsErr
}

func (c *outputLimitPrepareClient) Prepare(
	ctx context.Context,
	input PrepareInput,
) (PreparedRequest, error) {
	c.prepareCalls++
	return c.prepareForSendClient.Prepare(ctx, input)
}

func TestToolCallsFromEnvelopePreservesContentOrder(t *testing.T) {
	envelope := modelenvelope.ResponseEnvelope{
		Normalized: modelenvelope.ResponseNormalized{
			Content: []modelenvelope.ResponsePart{
				{Type: "text", Text: "before"},
				{
					Type:           "tool_call",
					ProviderCallID: "call_first",
					ToolName:       "run_command",
					ToolInput:      json.RawMessage(`{}`),
				},
				{Type: "text", Text: "between"},
				{
					Type:           "tool_call",
					ProviderCallID: "call_second",
					ToolName:       "read_file",
					ToolInput:      json.RawMessage(`{}`),
				},
			},
		},
	}
	calls := ToolCallsFromEnvelope(envelope)
	if len(calls) != 2 {
		t.Fatalf("tool calls = %+v, want 2", calls)
	}
	if calls[0].ID != "call_first" {
		t.Fatalf("first tool call = %+v, want call_first in content order", calls[0])
	}
	if calls[1].ID != "call_second" {
		t.Fatalf("second tool call = %+v, want call_second in content order", calls[1])
	}
}

func TestResponseEnvelopeRejectsInvalidToolInput(t *testing.T) {
	_, err := NewResponseEnvelopeForStorage("test-model", "test", "default", Response{
		ID:         "resp_1",
		StopReason: modelenvelope.StopReasonToolUse,
		Content: []ResponsePart{{
			Type:           "tool_call",
			ProviderCallID: "call_bad",
			ToolName:       "run_command",
			ToolInput:      json.RawMessage(`{"command":`),
		}},
	})
	if err == nil {
		t.Fatal("malformed tool input must be rejected before durable storage")
	}
}

func TestResponseEnvelopePreservesOrderedContentParts(t *testing.T) {
	envelope, err := NewResponseEnvelopeForStorage("test-model", "test", "default", Response{
		ID: "resp_1",
		Content: []ResponsePart{
			{Type: "text", Text: "before"},
			{Type: "reasoning", Text: "middle"},
			{Type: "text", Text: "after"},
		},
		StopReason: modelenvelope.StopReasonEndTurn,
	})
	if err != nil {
		t.Fatalf("response envelope: %v", err)
	}
	if len(envelope.Normalized.Content) != 3 {
		t.Fatalf("content parts = %+v, want 3", envelope.Normalized.Content)
	}
	if envelope.Normalized.Content[0].Text != "before" ||
		envelope.Normalized.Content[1].Text != "middle" ||
		envelope.Normalized.Content[2].Text != "after" {
		t.Fatalf("ordered content parts not preserved: %+v", envelope.Normalized.Content)
	}
}

func TestResponseEnvelopeAcceptsEmptySuccessfulContent(t *testing.T) {
	envelope, err := NewResponseEnvelopeForStorage("test-model", "test", "default", Response{
		ID:         "resp_empty",
		StopReason: modelenvelope.StopReasonEndTurn,
	})
	if err != nil {
		t.Fatalf("response envelope: %v", err)
	}
	if len(envelope.Normalized.Content) != 0 ||
		envelope.Normalized.StopReason != modelenvelope.StopReasonEndTurn {
		t.Fatalf("empty successful response changed: %+v", envelope.Normalized)
	}
}

func TestResponseEnvelopeAcceptsReasoningParts(t *testing.T) {
	envelope, err := NewResponseEnvelopeForStorage("test-model", "test", "default", Response{
		ID: "resp_reasoning",
		Content: []ResponsePart{
			{Type: "reasoning", Text: "visible summary"},
			{Type: "text", Text: "final answer"},
		},
		StopReason: modelenvelope.StopReasonEndTurn,
	})
	if err != nil {
		t.Fatalf("response envelope: %v", err)
	}
	if len(envelope.Normalized.Content) != 2 ||
		envelope.Normalized.Content[0].Type != "reasoning" ||
		envelope.Normalized.Content[0].Text != "visible summary" ||
		envelope.Normalized.Content[1].Type != "text" {
		t.Fatalf("reasoning content part not preserved: %+v", envelope.Normalized.Content)
	}
}

func TestResponseEnvelopeAcceptsWholeOutputReplayAndRejectsNull(t *testing.T) {
	envelope, err := NewResponseEnvelopeForStorage("test-model", "test", "default", Response{
		ID:             "resp_replay",
		ProviderReplay: json.RawMessage(`[{"type":"reasoning","encrypted_content":"opaque"}]`),
		Content:        []ResponsePart{{Type: "reasoning", Text: "summary"}},
		StopReason:     modelenvelope.StopReasonEndTurn,
	})
	if err != nil || len(envelope.ProviderReplay) == 0 {
		t.Fatalf("whole-output replay was not preserved: envelope=%+v err=%v", envelope, err)
	}
	_, err = NewResponseEnvelopeForStorage("test-model", "test", "default", Response{
		ID:             "resp_bad",
		ProviderReplay: json.RawMessage(`null`),
		Content:        []ResponsePart{{Type: "text", Text: "answer"}},
		StopReason:     modelenvelope.StopReasonEndTurn,
	})
	if err != nil {
		t.Fatalf("null replay should be treated as absent: %v", err)
	}
}

func TestValidateProviderJSONRejectsDatabaseUnsafeStrings(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "escaped NUL", body: []byte(`{"text":"\u0000"}`)},
		{name: "invalid UTF-8", body: []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateProviderJSON(test.body); err == nil {
				t.Fatal("expected database-unsafe provider JSON to be rejected")
			}
		})
	}
}

func TestResponseEnvelopeRejectsDatabaseUnsafeProviderStrings(t *testing.T) {
	_, err := NewResponseEnvelopeForStorage("test-model", "test", "default", Response{
		ID: "resp_1",
		Content: []ResponsePart{{
			Type: modelenvelope.ResponsePartTypeText,
			Text: "unsafe\x00text",
		}},
		StopReason: modelenvelope.StopReasonEndTurn,
	})
	if err == nil {
		t.Fatal("expected NUL provider content to be rejected")
	}
}

func TestProviderReplayIdentityIsIndependentFromSendCredentials(t *testing.T) {
	t.Parallel()

	client := replayIdentityClient{
		slug:       "anthropic/claude-sonnet-4",
		apiFormat:  "openai-chat-completions",
		apiVariant: "openrouter",
	}
	identity := ProviderReplayIdentityForClient("mpc_1", client)
	if identity != (modelenvelope.ProviderReplayIdentity{
		ModelProviderConfigID:      "mpc_1",
		RequestedProviderModelSlug: client.slug,
		APIFormat:                  client.apiFormat,
		APIVariant:                 client.apiVariant,
	}) {
		t.Fatalf("provider replay identity = %+v", identity)
	}
	if identity == ProviderReplayIdentityForClient("mpc_2", client) {
		t.Fatal("different provider configurations must not share replay identity")
	}
}

func TestAPIIdentityForClientRequiresCompleteIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		client      Client
		wantFormat  modelprotocol.APIFormat
		wantVariant modelprotocol.APIVariant
		wantOK      bool
	}{
		{name: "nil"},
		{
			name:   "format only",
			client: replayIdentityClient{apiFormat: "openai-chat-completions"},
		},
		{
			name:   "variant only",
			client: replayIdentityClient{apiVariant: "openrouter"},
		},
		{
			name:        "complete",
			client:      replayIdentityClient{apiFormat: "openai-chat-completions", apiVariant: "openrouter"},
			wantFormat:  "openai-chat-completions",
			wantVariant: "openrouter",
			wantOK:      true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			apiFormat, apiVariant, ok := APIIdentityForClient(test.client)
			if apiFormat != test.wantFormat || apiVariant != test.wantVariant || ok != test.wantOK {
				t.Fatalf(
					"API identity = %q/%q/%t, want %q/%q/%t",
					apiFormat,
					apiVariant,
					ok,
					test.wantFormat,
					test.wantVariant,
					test.wantOK,
				)
			}
		})
	}
}

type replayIdentityClient struct {
	slug       string
	apiFormat  modelprotocol.APIFormat
	apiVariant modelprotocol.APIVariant
}

func (c replayIdentityClient) RequestedProviderModelSlug() string { return c.slug }
func (c replayIdentityClient) APIFormat() modelprotocol.APIFormat { return c.apiFormat }
func (c replayIdentityClient) ModelAPIVariant() modelprotocol.APIVariant {
	return c.apiVariant
}
func (replayIdentityClient) Capabilities() Capabilities { return Capabilities{} }
func (replayIdentityClient) Prepare(context.Context, PrepareInput) (PreparedRequest, error) {
	return PreparedRequest{}, nil
}
func (replayIdentityClient) Respond(context.Context, Request) (Response, error) {
	return Response{}, nil
}

func TestEffectiveCacheRetentionDefaultsAnthropicToShort(t *testing.T) {
	for _, tc := range []struct {
		name      string
		format    modelprotocol.APIFormat
		variant   modelprotocol.APIVariant
		slug      string
		retention CacheRetention
		want      CacheRetention
	}{
		{
			name:    "anthropic messages unset",
			format:  modelprotocol.APIFormatAnthropicMessages,
			variant: modelprotocol.APIVariantDefault,
			slug:    "claude-sonnet-4",
			want:    CacheRetentionShort,
		},
		{
			name:    "anthropic messages bedrock unset",
			format:  modelprotocol.APIFormatAnthropicMessages,
			variant: modelprotocol.APIVariantBedrock,
			slug:    "claude-sonnet-4",
			want:    CacheRetentionShort,
		},
		{
			name:    "openrouter claude unset",
			format:  modelprotocol.APIFormatOpenAIChatCompletions,
			variant: modelprotocol.APIVariantOpenRouter,
			slug:    "~Anthropic/Claude-Sonnet-4",
			want:    CacheRetentionShort,
		},
		{
			name:    "openrouter non-claude unset",
			format:  modelprotocol.APIFormatOpenAIChatCompletions,
			variant: modelprotocol.APIVariantOpenRouter,
			slug:    "openai/gpt-5",
			want:    CacheRetentionNone,
		},
		{
			name:    "chat completions default variant unset",
			format:  modelprotocol.APIFormatOpenAIChatCompletions,
			variant: modelprotocol.APIVariantDefault,
			slug:    "anthropic/claude-sonnet-4",
			want:    CacheRetentionNone,
		},
		{
			name:    "responses unset",
			format:  modelprotocol.APIFormatOpenAIResponses,
			variant: modelprotocol.APIVariantDefault,
			slug:    "gpt-5",
			want:    CacheRetentionNone,
		},
		{
			name:      "explicit none wins",
			format:    modelprotocol.APIFormatAnthropicMessages,
			variant:   modelprotocol.APIVariantDefault,
			slug:      "claude-sonnet-4",
			retention: CacheRetentionNone,
			want:      CacheRetentionNone,
		},
		{
			name:      "explicit long wins",
			format:    modelprotocol.APIFormatOpenAIResponses,
			variant:   modelprotocol.APIVariantDefault,
			slug:      "gpt-5",
			retention: CacheRetentionLong,
			want:      CacheRetentionLong,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveCacheRetention(tc.format, tc.variant, tc.slug, tc.retention); got != tc.want {
				t.Fatalf("effective cache retention = %q, want %q", got, tc.want)
			}
		})
	}
}
