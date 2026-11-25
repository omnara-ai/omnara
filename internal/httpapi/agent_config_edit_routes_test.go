package httpapi

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
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

func TestEditAgentConfigTargetReportsPanicsPerTarget(t *testing.T) {
	id := uuid.New()
	result, err := strictOpenAPIServer{}.editAgentConfigTarget(context.Background(), &agentConfigEditRequest{},
		agentConfigEditTarget{id: id}, nil)
	require.NoError(t, err)
	target, err := result.Target.AsAgentConfigEditAgentTarget()
	require.NoError(t, err)
	agentID, err := publicID(publicid.KindAgent, id)
	require.NoError(t, err)
	require.Equal(t,
		[]any{agentID, openapi.AgentConfigEditResultStatusFailed, openapi.ErrorCodeInternalError},
		[]any{target.AgentId, result.Status, *result.Code})
}
