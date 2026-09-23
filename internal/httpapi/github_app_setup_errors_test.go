package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/apps/github"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/stretchr/testify/require"
)

func TestGitHubSetupUnsupportedAccountIsNonRetryable(t *testing.T) {
	t.Parallel()
	providerErr := fmt.Errorf("inspect GitHub installations: %w", &github.APIError{Code: github.UnsupportedAccount})
	var response apierror.ResponseError
	require.ErrorAs(t, appSetupInputError(providerErr), &response)
	require.Equal(t, http.StatusBadRequest, response.Status)
	require.Equal(t, openapi.ErrorCodeInvalidRequest, response.Code)
	require.Contains(t, response.Message, "Guided GitHub setup supports user or organization Apps and installations")
	require.Contains(t, response.Message, "enterprise-owned Apps and enterprise-level installations are not supported")
	require.NotContains(t, response.Message, "retry later")
	require.NotContains(t, response.Message, "manual")
}
