package executionstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestValidateModelResponseEnvelopeRequiresStopReason(t *testing.T) {
	envelope := modelenvelope.ResponseEnvelope{
		RequestedProviderModelSlug: "test-model",
		APIFormat:                  "test-api",
		APIVariant:                 "default",
		Normalized: modelenvelope.ResponseNormalized{
			ID:      "resp_missing_stop",
			Content: []modelenvelope.ResponsePart{{Type: "text", Text: "hello"}},
		},
	}
	err := validateModelResponseEnvelope(envelope)
	if err == nil || !strings.Contains(err.Error(), "stop reason") {
		t.Fatalf("validateModelResponseEnvelope error = %v, want missing stop reason error", err)
	}
}

func TestCreateModelOutputAuthorityRejectsUnsupportedStopReason(t *testing.T) {
	for _, reason := range []modelenvelope.StopReason{
		modelenvelope.StopReasonPause,
		modelenvelope.StopReasonContextWindow,
		modelenvelope.StopReasonUnknown,
		"not_a_stop_reason",
	} {
		t.Run(string(reason), func(t *testing.T) {
			_, err := createModelOutputAuthorityTx(context.Background(), nil, CreateModelOutputAuthorityInput{
				ProjectID:               parseUUIDText("00000000-0000-4000-8000-000000000001"),
				AgentID:                 parseUUIDText("00000000-0000-4000-8000-000000000002"),
				ModelCallContextID:      parseUUIDText("00000000-0000-4000-8000-000000000003"),
				ServedProviderModelSlug: "test-model",
				StopReason:              reason,
			})
			if err == nil || !strings.Contains(err.Error(), "unsupported model output stop reason") {
				t.Fatalf("createModelOutputAuthorityTx error = %v, want unsupported stop reason error", err)
			}
		})
	}
}

func TestBindToolCallsUsesBindingsForIdentityAndEnvelopeForContent(t *testing.T) {
	firstID := parseUUIDText("00000000-0000-4000-8000-000000000001")
	secondID := parseUUIDText("00000000-0000-4000-8000-000000000002")
	envelope := validToolCallEnvelope(
		modelenvelope.ResponsePart{Type: modelenvelope.ResponsePartTypeText, Text: "before tools"},
		modelenvelope.ResponsePart{
			Type:           modelenvelope.ResponsePartTypeToolCall,
			ProviderCallID: "call_first",
			ToolName:       "read_file",
			ToolInput:      json.RawMessage(`{"path":"README.md","nested":{"b":2,"a":1}}`),
		},
		modelenvelope.ResponsePart{
			Type:           modelenvelope.ResponsePartTypeToolCall,
			ProviderCallID: "call_second",
			ToolName:       "docs__lookup",
			ToolInput:      json.RawMessage(`{"query":"durable agents"}`),
		},
	)

	bound, err := bindValidatedToolCalls(
		envelope,
		[]ToolCallBindingInput{
			{
				ID:             secondID,
				ProviderCallID: "call_second",
				Type:           toolcatalog.ToolTypeMCP,
			},
			{
				ID:             firstID,
				ProviderCallID: "call_first",
				Type:           toolcatalog.ToolTypeBuiltIn,
			},
		},
	)
	if err != nil {
		t.Fatalf("bind tool calls: %v", err)
	}
	if len(bound) != 2 {
		t.Fatalf("bound tool calls = %+v, want two", bound)
	}
	if bound[0].ID != firstID ||
		bound[0].ProviderCallID != "call_first" ||
		bound[0].Type != toolcatalog.ToolTypeBuiltIn ||
		bound[0].Name != "read_file" ||
		!sameJSON(bound[0].Input, json.RawMessage(`{"nested":{"a":1,"b":2},"path":"README.md"}`)) {
		t.Fatalf("first bound tool call = %+v", bound[0])
	}
	if bound[1].ID != secondID ||
		bound[1].ProviderCallID != "call_second" ||
		bound[1].Type != toolcatalog.ToolTypeMCP ||
		bound[1].Name != "docs__lookup" ||
		!sameJSON(bound[1].Input, json.RawMessage(`{"query":"durable agents"}`)) {
		t.Fatalf("second bound tool call = %+v", bound[1])
	}
}

func TestBindToolCallsRejectsInconsistentBatch(t *testing.T) {
	validEnvelope := validToolCallEnvelope(modelenvelope.ResponsePart{
		Type:           modelenvelope.ResponsePartTypeToolCall,
		ProviderCallID: "call_one",
		ToolName:       "read_file",
		ToolInput:      json.RawMessage(`{"path":"README.md"}`),
	})
	validBinding := ToolCallBindingInput{
		ID:             parseUUIDText("00000000-0000-4000-8000-000000000001"),
		ProviderCallID: "call_one",
		Type:           toolcatalog.ToolTypeBuiltIn,
	}

	tests := []struct {
		name     string
		envelope modelenvelope.ResponseEnvelope
		bindings []ToolCallBindingInput
		want     string
	}{
		{
			name:     "missing binding",
			envelope: validEnvelope,
			bindings: []ToolCallBindingInput{{
				ProviderCallID: "call_other",
				Type:           toolcatalog.ToolTypeBuiltIn,
			}},
			want: "has no tool call binding",
		},
		{
			name:     "extra binding",
			envelope: validEnvelope,
			bindings: []ToolCallBindingInput{
				validBinding,
				{
					ProviderCallID: "call_extra",
					Type:           toolcatalog.ToolTypeBuiltIn,
				},
			},
			want: "has no provider response tool call",
		},
		{
			name:     "duplicate binding",
			envelope: validEnvelope,
			bindings: []ToolCallBindingInput{
				validBinding,
				validBinding,
			},
			want: "duplicate tool call binding",
		},
		{
			name:     "unsupported resolved type",
			envelope: validEnvelope,
			bindings: []ToolCallBindingInput{{
				ProviderCallID: "call_one",
				Type:           "unknown",
			}},
			want: "unsupported tool call type",
		},
		{
			name: "invalid envelope input",
			envelope: validToolCallEnvelope(modelenvelope.ResponsePart{
				Type:           modelenvelope.ResponsePartTypeToolCall,
				ProviderCallID: "call_one",
				ToolName:       "read_file",
				ToolInput:      json.RawMessage(`{"path":`),
			}),
			bindings: []ToolCallBindingInput{validBinding},
			want:     "tool input must be a JSON object",
		},
		{
			name: "duplicate envelope provider identity",
			envelope: validToolCallEnvelope(
				validEnvelope.Normalized.Content[0],
				validEnvelope.Normalized.Content[0],
			),
			bindings: []ToolCallBindingInput{validBinding},
			want:     "duplicate provider call id",
		},
		{
			name: "missing envelope tool name",
			envelope: validToolCallEnvelope(modelenvelope.ResponsePart{
				Type:           modelenvelope.ResponsePartTypeToolCall,
				ProviderCallID: "call_one",
				ToolInput:      json.RawMessage(`{}`),
			}),
			bindings: []ToolCallBindingInput{validBinding},
			want:     "missing provider_call_id or tool_name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := bindValidatedToolCalls(tt.envelope, tt.bindings)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("bind tool calls error = %v, want %q", err, tt.want)
			}
		})
	}
}

func validToolCallEnvelope(parts ...modelenvelope.ResponsePart) modelenvelope.ResponseEnvelope {
	return modelenvelope.ResponseEnvelope{
		RequestedProviderModelSlug: "test-model",
		APIFormat:                  "test-api",
		APIVariant:                 "default",
		Normalized: modelenvelope.ResponseNormalized{
			ID:         "resp_tool_calls",
			Content:    parts,
			StopReason: modelenvelope.StopReasonToolUse,
		},
	}
}

func bindValidatedToolCalls(
	envelope modelenvelope.ResponseEnvelope,
	bindings []ToolCallBindingInput,
) ([]boundToolCall, error) {
	if err := validateModelResponseEnvelope(envelope); err != nil {
		return nil, err
	}
	return bindToolCalls(envelope, bindings)
}
