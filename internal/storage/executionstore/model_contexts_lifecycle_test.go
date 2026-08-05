package executionstore

import (
	"testing"

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
	}{
		{name: "served model", servedProviderModelSlug: "model"},
		{name: "request ID", providerRequestID: "req"},
		{name: "response ID", providerResponseID: "resp"},
		{name: "usage", hasUsage: true},
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
	); err == nil {
		t.Fatal("partial API identity was accepted")
	}
}
