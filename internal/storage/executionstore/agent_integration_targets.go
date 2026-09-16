package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
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
	if binding.AgentProfileID == uuid.Nil {
		return storeerr.InvalidRequest(errors.New("route profile is required"))
	}
	_, err = lockAgentProfileTx(ctx, qtx, binding.ProjectID, binding.AgentProfileID)
	return err
}

func (IntegrationInstallAccess) ClearInstallTargetsFromAgents(
	ctx context.Context,
	tx pgx.Tx,
	projectID, integrationInstallID uuid.UUID,
) error {
	if err := cancelExternalChannelRequestsForInstallationTx(ctx, tx, projectID, integrationInstallID); err != nil {
		return fmt.Errorf("cancel deleted connection requests: %w", err)
	}
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

func (r *ToolCallReader) ListAgentChannelTargets(
	ctx context.Context,
	input integrationstore.ListAgentChannelTargetsInput,
) (integrationstore.AgentChannelTargetPage, error) {
	t := r.transaction
	return t.store.integrations.ListAgentChannelTargetsTx(
		ctx,
		t.tx,
		t.input.ProjectID,
		t.input.AgentID,
		input,
	)
}

func (r *ToolCallReader) GetChannelAccess(
	ctx context.Context, channelID uuid.UUID,
) (integrationstore.ChannelAccess, error) {
	t := r.transaction
	return t.store.integrations.GetAgentChannelAccessTx(ctx, t.tx, t.input.ProjectID, t.input.AgentID, channelID)
}

func (s *Store) GetAgentCurrentChannelID(ctx context.Context, projectID, agentID uuid.UUID) (uuid.UUID, error) {
	return getAgentCurrentChannelID(ctx, s.q, projectID, agentID)
}

func (r *ToolCallReader) CurrentChannelID(ctx context.Context) (uuid.UUID, error) {
	t := r.transaction
	return getAgentCurrentChannelID(ctx, t.q, t.input.ProjectID, t.input.AgentID)
}

func getAgentCurrentChannelID(ctx context.Context, q *dbsqlc.Queries, projectID, agentID uuid.UUID) (uuid.UUID, error) {
	id, err := q.GetAgentCurrentChannelID(ctx, dbsqlc.GetAgentCurrentChannelIDParams{
		ProjectID: projectID, AgentID: agentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, storeerr.ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("get current channel: %w", err)
	}
	return storeutil.IDFromPtr(id), nil
}

func (t *toolCallTransaction) setAgentIntegrationTarget(
	ctx context.Context,
	integrationTargetID uuid.UUID,
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
	projectID, agentID, integrationTargetID uuid.UUID,
) (AgentRecord, error) {
	row, err := q.SetAgentIntegrationTarget(ctx, dbsqlc.SetAgentIntegrationTargetParams{
		ProjectID:           projectID,
		AgentID:             agentID,
		IntegrationTargetID: storeutil.IDFromNil(integrationTargetID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if integrationTargetID != uuid.Nil {
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
		row.SubagentKey,
	)
}
