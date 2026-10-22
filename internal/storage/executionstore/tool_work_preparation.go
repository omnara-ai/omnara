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
	Context         ModelCallContextRecord
	Config          AgentConfigRecord
	FirstCall       *ToolCallRecord
	databaseCalls   []ToolCallRecord
	databaseRows    []dbsqlc.ListClaimToolCallsRow
	wholeBatch      bool
	databaseBlocked bool
}

func prepareToolWorkTx(
	ctx context.Context, q *dbsqlc.Queries, projectID, agentID uuid.UUID, work ClaimedToolWork,
	databaseTools bool, known dbsqlc.GetModelCallContextRow,
) (*PreparedToolWork, error) {
	model := modelCallContextRecordFromSQLC(known)
	if model.State != ModelCallContextSucceeded {
		return nil, storeerr.ErrStateTransitionConflict
	}
	config, err := q.GetAgentConfig(ctx, dbsqlc.GetAgentConfigParams{ProjectID: projectID, ID: model.AgentConfigID})
	if err != nil {
		return nil, fmt.Errorf("load tool work config: %w", err)
	}
	result := &PreparedToolWork{Context: model, Config: agentConfigRecordFromSQLC(config)}
	if databaseTools {
		rows, err := q.ListClaimToolCalls(ctx, dbsqlc.ListClaimToolCallsParams{
			ProjectID: projectID, AgentID: agentID, ModelOutputID: work.ModelOutputID,
		})
		if err != nil {
			return nil, fmt.Errorf("load claimed tool batch: %w", err)
		}
		result.databaseRows = rows
		result.wholeBatch = len(rows) > 0 && rows[0].WholeBatch
		for _, row := range rows {
			record := ToolCallRecord{
				ID: row.ID, ProjectID: row.ProjectID, AgentID: row.AgentID, TurnID: row.TurnID,
				SourceEventID: row.SourceEventID, ModelCallContextID: row.ModelCallContextID,
				ProviderCallID: row.ProviderCallID, Name: row.Name, Input: row.Input, Type: row.Type,
				State: ToolCallState(row.State), CreatedAt: row.CreatedAt, ResultContentParts: row.ResultContentParts,
			}
			runnable := row.State == "awaiting_authorization" ||
				row.State == "ready" && (row.Type == "built_in" || row.Type == "mcp")
			if runnable && result.FirstCall == nil {
				result.FirstCall = &record
			}
			result.databaseBlocked = result.databaseBlocked || row.HasPermission || row.RuntimeLockID != nil || !runnable
			result.databaseCalls = append(result.databaseCalls, record)
		}
		return result, nil
	}
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
