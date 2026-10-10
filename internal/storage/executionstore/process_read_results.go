package executionstore

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/processresult"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func inspectPublishedProcessReadObservationTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID uuid.UUID,
	agentID uuid.UUID,
	toolCallID uuid.UUID,
	raw json.RawMessage,
) (toolCallResultPublication, error) {
	tool, err := qtx.GetToolCall(
		ctx,
		dbsqlc.GetToolCallParams{ProjectID: projectID, AgentID: agentID, ID: toolCallID},
	)
	contentParts := tool.ResultContentParts
	if err != nil {
		return toolCallResultPublication{}, err
	}
	publication, err := inspectPublishedToolCallResultTx(
		ctx,
		qtx,
		projectID,
		agentID,
		toolCallID,
		ToolResultOutcomeSucceeded,
		contentParts,
	)
	if err != nil || !publication.Matches {
		return publication, err
	}
	committed, ok := structuredToolResultValue(contentParts)
	if !ok {
		publication.Matches = false
		return publication, nil
	}
	publication.Matches, err = processresult.ReadMatches(
		raw,
		committed,
	)
	return publication, err
}

func structuredToolResultValue(parts json.RawMessage) (json.RawMessage, bool) {
	var decoded []struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(parts, &decoded) != nil {
		return nil, false
	}
	for _, part := range decoded {
		if part.Type == "structured_data" && len(part.Value) != 0 {
			return part.Value, true
		}
	}
	return nil, false
}
