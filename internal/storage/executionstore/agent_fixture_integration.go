//go:build integration

package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
)

type AgentFixtureInput struct {
	ProjectID       ID
	Name            string
	CurrentConfigID ID
}

func (s *Store) IntegrationLaunchAgentOnce(
	ctx context.Context,
	input LaunchAgentInput,
) (LaunchAgentResult, error) {
	return s.launchAgentOnce(ctx, input)
}

func (s *Store) IntegrationExecuteToolCallOnce(
	ctx context.Context,
	input ExecuteToolCallInput,
	command ToolCallCommand,
) (ExecuteToolCallResult, error) {
	return s.executeToolCallOnce(
		ctx,
		input,
		func(*ToolCallReader) (ToolCallCommand, error) { return command, nil },
	)
}

func (s *Store) IntegrationChangeAgentConfigOnce(
	ctx context.Context,
	input ChangeAgentConfigInput,
) (ChangeAgentConfigResult, error) {
	return s.changeAgentConfigOnce(ctx, input)
}

func (s *Store) IntegrationArchiveAgentOnce(
	ctx context.Context,
	orgID, projectID, agentID ID,
	actor *ActorParams,
) (AgentRecord, []MachineRecord, error) {
	return s.archiveAgentOnce(ctx, orgID, projectID, agentID, actor)
}

func (s *Store) IntegrationDeleteMachineOnce(
	ctx context.Context,
	input DeleteMachineInput,
) (MachineRecord, error) {
	return s.deleteMachineOnce(ctx, input)
}

func (s *Store) IntegrationDeleteMachinePoolOnce(
	ctx context.Context,
	orgID, poolID ID,
) ([]MachineRecord, error) {
	return s.deleteMachinePoolOnce(ctx, orgID, poolID)
}

func (s *Store) IntegrationDeleteProjectMachineGrantOnce(
	ctx context.Context,
	orgID, projectID, grantID ID,
) (ProjectMachineGrantRecord, error) {
	return s.deleteProjectMachineGrantOnce(ctx, orgID, projectID, grantID)
}

func (s *Store) IntegrationDeleteProjectMachinePoolGrantOnce(
	ctx context.Context,
	orgID, projectID, grantID ID,
) (DeleteProjectMachinePoolGrantResult, error) {
	return s.deleteProjectMachinePoolGrantOnce(ctx, orgID, projectID, grantID)
}

func (s *Store) CreateAgentFixture(ctx context.Context, input AgentFixtureInput) (AgentRecord, error) {
	if isNilID(input.ProjectID) {
		return AgentRecord{}, errors.New("project id is required")
	}
	if isNilID(input.CurrentConfigID) {
		return AgentRecord{}, errors.New("current config id is required")
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentRecord{}, fmt.Errorf("begin create agent fixture: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	project, err := loadProjectTx(ctx, qtx, input.ProjectID)
	if err != nil {
		return AgentRecord{}, err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, project.OrgID, input.ProjectID); err != nil {
		return AgentRecord{}, err
	}
	config, err := loadAgentConfigTx(ctx, qtx, input.ProjectID, input.CurrentConfigID)
	if err != nil {
		return AgentRecord{}, err
	}
	if err := lockAgentConfigModelForUseTx(ctx, qtx, config); err != nil {
		return AgentRecord{}, err
	}
	record, _, err := insertAdmittedAgentTx(ctx, tx, qtx, insertAgentInput{
		OrgID:           project.OrgID,
		ProjectID:       input.ProjectID,
		Name:            input.Name,
		CurrentConfigID: input.CurrentConfigID,
	})
	if err != nil {
		return AgentRecord{}, err
	}
	if _, err := activateNewAgentConfigTx(ctx, txNotifications, tx, qtx, ActivateAgentConfigInput{
		ProjectID:      input.ProjectID,
		AgentID:        record.ID,
		AgentConfigID:  input.CurrentConfigID,
		ActorType:      identitystore.PrincipalTypeSystem,
		Reason:         "fixture",
		IdempotencyKey: "fixture:" + record.ID.String(),
	}); err != nil {
		return AgentRecord{}, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "create agent fixture"); err != nil {
		return AgentRecord{}, err
	}
	return record, nil
}
