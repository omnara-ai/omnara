package modelenvelope

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestNormalizeToolInput(t *testing.T) {
	tests := []struct {
		name    string
		input   json.RawMessage
		want    string
		wantErr bool
	}{
		{name: "missing", wantErr: true},
		{name: "whitespace", input: json.RawMessage(" \n\t"), wantErr: true},
		{name: "null", input: json.RawMessage(`null`), wantErr: true},
		{name: "object", input: json.RawMessage(` {"count":1} `), want: `{"count":1}`},
		{name: "array", input: json.RawMessage(`[]`), wantErr: true},
		{name: "string", input: json.RawMessage(`"value"`), wantErr: true},
		{name: "number", input: json.RawMessage(`1`), wantErr: true},
		{name: "boolean", input: json.RawMessage(`true`), wantErr: true},
		{name: "malformed", input: json.RawMessage(`{`), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeToolInput(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NormalizeToolInput(%s) succeeded", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeToolInput(%s): %v", tt.input, err)
			}
			if string(got) != tt.want {
				t.Fatalf("NormalizeToolInput(%s) = %s, want %s", tt.input, got, tt.want)
			}
			if err := ValidateToolInput(got); err != nil {
				t.Fatalf("normalized tool input is not canonical: %v", err)
			}
		})
	}
}

func TestDurableModelOutputStopReasons(t *testing.T) {
	tests := []struct {
		reason StopReason
		want   bool
	}{
		{reason: StopReasonEndTurn, want: true},
		{reason: StopReasonToolUse, want: true},
		{reason: StopReasonMaxTokens, want: true},
		{reason: StopReasonRefusal, want: true},
		{reason: StopReasonContentFilter, want: true},
		{reason: StopReasonError, want: true},
		{reason: StopReasonPause},
		{reason: StopReasonContextWindow},
		{reason: StopReasonUnknown},
		{reason: "future_provider_reason"},
	}
	for _, tt := range tests {
		t.Run(string(tt.reason), func(t *testing.T) {
			if got := IsDurableModelOutputStopReason(tt.reason); got != tt.want {
				t.Fatalf("IsDurableModelOutputStopReason(%q) = %v, want %v", tt.reason, got, tt.want)
			}
		})
	}
}

func TestResponseEnvelopeValidationRejectsInvalidReplayPayload(t *testing.T) {
	envelope := ResponseEnvelope{
		RequestedProviderModelSlug: "current-model",
		APIFormat:                  "openai-responses",
		APIVariant:                 "default",
		ProviderReplay:             json.RawMessage(`null`),
		Normalized: ResponseNormalized{
			StopReason: StopReasonEndTurn,
			Content: []ResponsePart{{
				Type: ResponsePartTypeText,
				Text: "hello",
			}},
		},
	}
	if err := envelope.Validate(); err == nil {
		t.Fatal("response envelope accepted a null replay payload")
	}
}

func TestReplayCompatibilityRequiresExactOrigin(t *testing.T) {
	source := ProviderReplayIdentity{
		ModelProviderConfigID:      "00000000-0000-4000-8000-000000000001",
		RequestedProviderModelSlug: "model",
		APIFormat:                  "openai-responses",
		APIVariant:                 "default",
	}
	if !source.Matches(source) {
		t.Fatal("replay source did not match itself")
	}
	for _, mismatch := range []struct {
		providerConfigID string
		apiFormat        modelprotocol.APIFormat
		variant          modelprotocol.APIVariant
		model            string
	}{
		{"00000000-0000-4000-8000-000000000002", "openai-responses", "default", "model"},
		{"00000000-0000-4000-8000-000000000001", "anthropic-messages", "default", "model"},
		{"00000000-0000-4000-8000-000000000001", "openai-responses", "other", "model"},
		{"00000000-0000-4000-8000-000000000001", "openai-responses", "default", "other-model"},
	} {
		if source.Matches(ProviderReplayIdentity{
			ModelProviderConfigID:      mismatch.providerConfigID,
			RequestedProviderModelSlug: mismatch.model,
			APIFormat:                  mismatch.apiFormat,
			APIVariant:                 mismatch.variant,
		}) {
			t.Fatalf("replay matched incompatible source: %+v", mismatch)
		}
	}
}
