package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestSecretAPIErrorOnlyExposesFieldSpecificNameValidation(t *testing.T) {
	nameErr := storeerr.Tag(
		storeerr.ErrInvalidSecretName,
		storeerr.InvalidRequest(errors.New("secret name must not start or end with whitespace")),
	)
	if got := secretAPIError(context.Background(), nameErr).Message; !strings.Contains(got, "secret name") {
		t.Fatalf("secret name API error = %q, want field-specific detail", got)
	}

	generalErr := storeerr.InvalidRequest(errors.New("internal secret validation detail"))
	got := secretAPIError(context.Background(), generalErr).Message
	if strings.Contains(got, "internal secret validation detail") {
		t.Fatalf("general secret API error exposed internal detail: %q", got)
	}
	got = secretAPIError(context.Background(), storeerr.ErrInvalidSecretRequest).Message
	if got != "invalid request: invalid secret request" {
		t.Fatalf("invalid secret request API error = %q, want opaque message", got)
	}
}
