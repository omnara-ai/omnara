package model

import (
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

func TestResponseEvidenceForStorageKeepsOnlySafeAuditFields(t *testing.T) {
	response := Response{
		ID:                      "resp_1",
		ProviderRequestID:       "req_1",
		ServedProviderModelSlug: "served-model",
		ProviderReportedCostUSD: "0.0000125",
		Usage:                   Usage{InputTokens: 12, OutputTokens: 4},
		Content: []ResponsePart{{
			Type:     ResponsePartTypeToolCall,
			ToolName: "",
		}},
	}
	evidence := ResponseEvidenceForStorage(response)
	if evidence.ID != response.ID ||
		evidence.ProviderRequestID != response.ProviderRequestID ||
		evidence.ServedProviderModelSlug != response.ServedProviderModelSlug ||
		evidence.ProviderReportedCostUSD != response.ProviderReportedCostUSD ||
		evidence.Usage != response.Usage || len(evidence.Content) != 0 {
		t.Fatalf("response evidence = %+v", evidence)
	}
}

func TestResponseEvidenceForStorageDiscardsUnsafeIdentity(t *testing.T) {
	response := Response{
		ID:                      "resp\x00unsafe",
		ServedProviderModelSlug: "served-model",
		Usage:                   Usage{InputTokens: 12, OutputTokens: 4},
	}
	if evidence := ResponseEvidenceForStorage(response); evidence.ID != "" ||
		evidence.ServedProviderModelSlug != "" || evidence.ProviderReportedCostUSD != "" ||
		evidence.Usage != (Usage{}) ||
		len(evidence.Content) != 0 {
		t.Fatalf("unsafe response evidence = %+v", evidence)
	}
}

func TestValidateProviderResponseRejectsOversizedIdentities(t *testing.T) {
	oversized := strings.Repeat("x", MaxProviderIdentityBytes+1)
	tests := []struct {
		name      string
		fieldName string
		mutate    func(*Response)
	}{
		{name: "response id", fieldName: "response id", mutate: func(response *Response) { response.ID = oversized }},
		{
			name:      "served model slug",
			fieldName: "served model slug",
			mutate:    func(response *Response) { response.ServedProviderModelSlug = oversized },
		},
		{
			name:      "provider call id",
			fieldName: "provider call id",
			mutate:    func(response *Response) { response.Content[0].ProviderCallID = oversized },
		},
		{
			name:      "tool name",
			fieldName: "tool name",
			mutate:    func(response *Response) { response.Content[0].ToolName = oversized },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := Response{Content: []ResponsePart{{Type: ResponsePartTypeText}}}
			test.mutate(&response)
			err := ValidateProviderResponse(response)
			if err == nil || !strings.Contains(err.Error(), test.fieldName) ||
				!strings.Contains(err.Error(), "exceeds 2000 bytes") {
				t.Fatalf("oversized identity error = %v", err)
			}
		})
	}
}

func TestResponseEvidenceForStorageKeepsUsageRegardlessOfProviderMetadata(t *testing.T) {
	metadata := modelenvelope.ProviderMetadata{OpenRouter: modelenvelope.OpenRouterMetadata{Provider: "Moon\x00shot"}}
	evidence := ResponseEvidenceForStorage(Response{
		ID: "resp_1", ProviderMetadata: metadata, Usage: Usage{InputTokens: 12},
	})
	if evidence.ID != "resp_1" || evidence.Usage != (Usage{InputTokens: 12}) || evidence.ProviderMetadata != metadata {
		t.Fatalf("evidence = %+v, want kept intact", evidence)
	}
}
