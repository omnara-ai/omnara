package httpapi

import (
	"context"
	"errors"
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/stretchr/testify/require"
)

func TestAgentConfigEditFailedReportsInternalErrorsPerTarget(t *testing.T) {
	internal := errors.New("database unavailable")
	edits, generation := []openapi.AgentConfigEditResultEdits{openapi.AgentConfigEditResultEditsApplied}, 1
	result, err := agentConfigEditFailed(context.Background(),
		openapi.AgentConfigEditResult{Edits: &edits, CurrentGeneration: &generation}, internal)
	require.NoError(t, err)
	require.Equal(t,
		[]any{openapi.AgentConfigEditResultStatusFailed, openapi.ErrorCodeInternalError, "internal server error"},
		[]any{result.Status, *result.Code, *result.Error})
	require.Nil(t, result.Edits)
	require.Nil(t, result.CurrentGeneration)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = agentConfigEditFailed(canceled, openapi.AgentConfigEditResult{}, internal)
	require.ErrorIs(t, err, internal)
}
