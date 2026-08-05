package modelretry

import (
	"errors"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestNormalizePreSendFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    error
		wantKind model.ErrorKind
		wantCode string
	}{
		{
			name:     "grant unavailable",
			input:    storeerr.ErrModelGrantUnavailable,
			wantKind: model.ErrorKindAuth,
			wantCode: "model_grant_unavailable",
		},
		{
			name: "already classified",
			input: model.ProviderError{
				Kind: model.ErrorKindRateLimit,
				Code: "rate_limited",
			},
			wantKind: model.ErrorKindRateLimit,
			wantCode: "rate_limited",
		},
		{
			name:     "unclassified infrastructure",
			input:    errors.New("database unavailable"),
			wantKind: model.ErrorKindTransient,
			wantCode: "context_load_failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := NormalizePreSendFailure(
				tt.input,
				PreSendFailure{
					Code:    "context_load_failed",
					Message: "Omnara could not load the model context.",
				},
			)
			classification, ok := model.ClassifyError(got)
			if !ok {
				t.Fatalf("normalized error %v is not classified", got)
			}
			if classification.Kind != tt.wantKind || classification.Code != tt.wantCode {
				t.Fatalf(
					"classification = %s/%s, want %s/%s",
					classification.Kind,
					classification.Code,
					tt.wantKind,
					tt.wantCode,
				)
			}
			if errors.Is(tt.input, storeerr.ErrModelGrantUnavailable) {
				if !errors.Is(got, tt.input) {
					t.Fatalf("normalized error %v does not retain cause %v", got, tt.input)
				}
			}
		})
	}
}
