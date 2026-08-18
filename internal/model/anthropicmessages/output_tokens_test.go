package anthropicmessages

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
)

func TestAnthropicOutputTokenLimitValidation(t *testing.T) {
	tests := []struct {
		name         string
		options      json.RawMessage
		maxOutput    int
		wantConflict bool
		wantInvalid  bool
	}{
		{name: "absent thinking", maxOutput: 16_384},
		{name: "disabled thinking", options: json.RawMessage(`{"thinking":{"type":"disabled"}}`), maxOutput: 16_384},
		{name: "adaptive thinking", options: json.RawMessage(`{"thinking":{"type":"adaptive"}}`), maxOutput: 16_384},
		{name: "unknown future mode remains provider owned", options: json.RawMessage(`{"thinking":{"type":"future","budget_tokens":32000}}`), maxOutput: 16_384},
		{name: "manual budget below output", options: json.RawMessage(`{"thinking":{"type":"enabled","budget_tokens":16384}}`), maxOutput: 16_385},
		{name: "manual budget equals output", options: json.RawMessage(`{"thinking":{"type":"enabled","budget_tokens":16384}}`), maxOutput: 16_384, wantConflict: true},
		{name: "manual budget exceeds output", options: json.RawMessage(`{"thinking":{"type":"enabled","budget_tokens":24000}}`), maxOutput: 16_384, wantConflict: true},
		{name: "missing manual budget", options: json.RawMessage(`{"thinking":{"type":"enabled"}}`), maxOutput: 16_384, wantInvalid: true},
		{name: "noninteger manual budget", options: json.RawMessage(`{"thinking":{"type":"enabled","budget_tokens":1024.5}}`), maxOutput: 16_384, wantInvalid: true},
		{name: "manual budget below provider minimum", options: json.RawMessage(`{"thinking":{"type":"enabled","budget_tokens":1023}}`), maxOutput: 16_384, wantInvalid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := Client{APIVariantOptions: test.options}
			err := model.ValidateOutputTokenLimit(
				client,
				model.RequestPolicy{MaxOutputTokens: test.maxOutput},
				"anthropic_messages",
			)
			if test.wantConflict {
				if !errors.Is(err, model.ErrOutputTokenLimitIncompatible) {
					t.Fatalf("validation error = %v, want output-limit conflict", err)
				}
				return
			}
			if test.wantInvalid {
				var providerErr model.ProviderError
				if err == nil || errors.Is(err, model.ErrOutputTokenLimitIncompatible) ||
					!errors.As(err, &providerErr) || providerErr.Kind != model.ErrorKindInvalidRequest ||
					providerErr.Code != model.InvalidOutputTokenConfigurationCode {
					t.Fatalf("validation error = %v, want deterministic invalid request", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("valid output limit: %v", err)
			}
		})
	}
}
