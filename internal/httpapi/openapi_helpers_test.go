package httpapi

import (
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestAgentInputCommandAPIErrorIsProjectScoped(t *testing.T) {
	got := agentInputCommandAPIError(storeerr.ErrStateTransitionConflict)
	if got.Status != http.StatusConflict || got.Code != openapi.ErrorCodeStateTransitionConflict {
		t.Fatalf("agentInputCommandAPIError = %+v, want state transition conflict", got)
	}
}
