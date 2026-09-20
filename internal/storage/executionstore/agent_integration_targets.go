package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type AppAccess struct{}

func (AppAccess) ValidateAppDestination(
	ctx context.Context,
	tx pgx.Tx,
	binding integrationstore.AppDestination,
) error {
	qtx := dbsqlc.New(tx)
	project, err := loadProjectTx(ctx, qtx, binding.ProjectID)
	if err != nil {
		return err
	}
	if project.OrgID != binding.OrgID {
		return storeerr.ErrNotFound
	}
	if binding.AgentProfileID != uuid.Nil {
		_, err := lockAgentProfileTx(ctx, qtx, binding.ProjectID, binding.AgentProfileID)
		return err
	}
	if err := lifecyclelock.Agents(ctx, tx, []lifecyclelock.AgentRef{{
		ProjectID: binding.ProjectID,
		AgentID:   binding.AgentID,
	}}); err != nil {
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
		return fmt.Errorf("validate app agent: %w", err)
	}
	if AgentState(row.State) != AgentStateActive {
		return storeerr.ErrStateTransitionConflict
	}
	return nil
}

func (AppAccess) ClearAppTargetsFromAgents(
	ctx context.Context,
	tx pgx.Tx,
	projectID, appID uuid.UUID,
) error {
	err := dbsqlc.New(tx).ClearDeletedIntegrationTargetsFromAgents(
		ctx,
		dbsqlc.ClearDeletedIntegrationTargetsFromAgentsParams{
			ProjectID: projectID,
			AppID:     appID,
		},
	)
	if err != nil {
		return fmt.Errorf("clear integration targets from agents: %w", err)
	}
	return nil
}
