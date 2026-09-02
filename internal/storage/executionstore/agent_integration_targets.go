package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type IntegrationInstallAccess struct{}

func (IntegrationInstallAccess) ValidateInstallBinding(
	ctx context.Context,
	tx pgx.Tx,
	binding integrationstore.InstallBinding,
) error {
	qtx := dbsqlc.New(tx)
	project, err := loadProjectTx(ctx, qtx, binding.ProjectID)
	if err != nil {
		return err
	}
	if project.OrgID != binding.OrgID {
		return storeerr.ErrNotFound
	}
	if binding.AgentProfileID != integrationstore.NilID {
		_, err := lockAgentProfileTx(ctx, qtx, binding.ProjectID, binding.AgentProfileID)
		return err
	}
	row, err := qtx.GetAgentInProject(
		ctx,
		dbsqlc.GetAgentInProjectParams{ProjectID: binding.ProjectID, ID: binding.AgentID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("validate integration install agent: %w", err)
	}
	if AgentState(row.State) != AgentStateActive {
		return storeerr.ErrStateTransitionConflict
	}
	return nil
}

func (IntegrationInstallAccess) ClearInstallTargetsFromAgents(
	ctx context.Context,
	tx pgx.Tx,
	projectID, integrationInstallID integrationstore.ID,
) error {
	err := dbsqlc.New(tx).ClearDeletedIntegrationTargetsFromAgents(
		ctx,
		dbsqlc.ClearDeletedIntegrationTargetsFromAgentsParams{
			ProjectID:            projectID,
			IntegrationInstallID: integrationInstallID,
		},
	)
	if err != nil {
		return fmt.Errorf("clear integration targets from agents: %w", err)
	}
	return nil
}

func (r *ToolCallReader) ListIntegrationTargets(
	ctx context.Context,
) ([]integrationstore.IntegrationTargetSummary, error) {
	t := r.transaction
	return t.store.integrations.ListIntegrationTargetsTx(
		ctx,
		t.tx,
		t.input.ProjectID,
		t.input.AgentID,
	)
}

func (t *toolCallTransaction) setAgentIntegrationTarget(
	ctx context.Context,
	integrationTargetID ID,
) (AgentRecord, error) {
	if err := t.lockForMutation(ctx); err != nil {
		return AgentRecord{}, err
	}
	return setAgentIntegrationTarget(
		ctx,
		t.q,
		t.input.ProjectID,
		t.input.AgentID,
		integrationTargetID,
	)
}

func setAgentIntegrationTarget(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID, integrationTargetID ID,
) (AgentRecord, error) {
	row, err := q.SetAgentIntegrationTarget(ctx, dbsqlc.SetAgentIntegrationTargetParams{
		ProjectID:           projectID,
		AgentID:             agentID,
		IntegrationTargetID: sqlcIDFromNil(integrationTargetID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if !isNilID(integrationTargetID) {
				if _, agentErr := q.GetAgentInProject(
					ctx,
					dbsqlc.GetAgentInProjectParams{
						ProjectID: projectID,
						ID:        agentID,
					},
				); agentErr == nil {
					return AgentRecord{}, storeerr.ErrConflict
				} else if !errors.Is(agentErr, pgx.ErrNoRows) {
					return AgentRecord{}, fmt.Errorf(
						"load agent for integration target validation: %w",
						agentErr,
					)
				}
			}
			return AgentRecord{}, storeerr.ErrNotFound
		}
		return AgentRecord{}, fmt.Errorf("set agent integration target: %w", err)
	}
	return agentRecordFromSetIntegrationTargetSQLC(row), nil
}

func agentRecordFromSetIntegrationTargetSQLC(row dbsqlc.SetAgentIntegrationTargetRow) AgentRecord {
	return agentRecordFromSQLC(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.State,
		row.Name,
		row.AgentProfileID,
		row.CurrentConfigID,
		row.IntegrationTargetID,
		row.IdempotencyKey,
		row.NextEventSequence,
		row.CreatedAt,
		row.UpdatedAt,
		row.ArchivedAt,
		row.ParentAgentID,
		row.SubagentHandle,
	)
}
