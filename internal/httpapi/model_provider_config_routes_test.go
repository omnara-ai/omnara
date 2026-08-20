package httpapi

import (
	"testing"

	openapigen "github.com/omnara-ai/omnara/internal/httpapi/openapi"
)

func TestValidateCreateModelProviderConfigRequestRejectsInvalidName(t *testing.T) {
	err := validateCreateModelProviderConfigRequest(openapigen.CreateModelProviderConfigRequest{
		Name: " Model provider",
	})
	want := "model provider config name must not start or end with whitespace"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}
