package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type PreparedToolWork struct {
	Context   ModelCallContextRecord
	Config    AgentConfigRecord
	FirstCall *ToolCallRecord
}

func prepareToolWorkTx(
	ctx context.Context, q *dbsqlc.Queries, projectID, agentID uuid.UUID, work ClaimedToolWork,
) (*PreparedToolWork, error) {
	model, err := loadModelCallContextByID(ctx, q, projectID, agentID, work.ModelCallContextID)
	if err != nil {
		return nil, err
	}
	output, err := q.GetModelOutputByModelContext(ctx, dbsqlc.GetModelOutputByModelContextParams{
		ProjectID: projectID, AgentID: agentID, ModelCallContextID: work.ModelCallContextID,
	})
	if err != nil {
		return nil, fmt.Errorf("load tool work output: %w", err)
	}
	if model.State != ModelCallContextSucceeded || output.ID != work.ModelOutputID {
		return nil, storeerr.ErrStateTransitionConflict
	}
	config, err := q.GetAgentConfig(ctx, dbsqlc.GetAgentConfigParams{ProjectID: projectID, ID: model.AgentConfigID})
	if err != nil {
		return nil, fmt.Errorf("load tool work config: %w", err)
	}
	result := &PreparedToolWork{Context: model, Config: agentConfigRecordFromSQLC(config)}
	first, err := q.NextRunnableToolCallForModelOutput(ctx, dbsqlc.NextRunnableToolCallForModelOutputParams{
		ProjectID: projectID, AgentID: agentID, ModelOutputID: work.ModelOutputID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("load first runnable tool: %w", err)
	}
	if err == nil {
		record := toolCallRecordFromRunnableSQLC(first)
		result.FirstCall = &record
	}
	return result, nil
}
