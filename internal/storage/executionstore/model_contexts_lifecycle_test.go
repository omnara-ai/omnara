package executionstore

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestValidateModelCallFailureEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                    string
		servedProviderModelSlug string
		providerRequestID       string
		providerResponseID      string
		hasUsage                bool
		providerReportedCostUSD modelenvelope.ProviderReportedCostUSD
	}{
		{name: "served model", servedProviderModelSlug: "model"},
		{name: "request ID", providerRequestID: "req"},
		{name: "response ID", providerResponseID: "resp"},
		{name: "usage", hasUsage: true},
		{name: "provider-reported cost", providerReportedCostUSD: "0.01"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateModelCallFailureEvidence(
				"",
				"",
				test.servedProviderModelSlug,
				test.providerRequestID,
				test.providerResponseID,
				test.hasUsage,
				test.providerReportedCostUSD,
			)
			if err == nil {
				t.Fatal("provider evidence without API identity was accepted")
			}
		})
	}

	if err := validateModelCallFailureEvidence(
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
		"model",
		"req",
		"resp",
		true,
		"",
	); err != nil {
		t.Fatalf("provider evidence with API identity was rejected: %v", err)
	}

	if err := validateModelCallFailureEvidence(
		modelprotocol.APIFormatOpenAIResponses,
		"",
		"",
		"",
		"",
		false,
		"",
	); err == nil {
		t.Fatal("partial API identity was accepted")
	}

	if err := validateModelCallFailureEvidence(
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
		"",
		"",
		"",
		false,
		"not-a-decimal",
	); err == nil {
		t.Fatal("invalid provider-reported cost was accepted")
	}
}
