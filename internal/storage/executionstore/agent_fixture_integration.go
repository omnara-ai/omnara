//go:build integration

package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
)

type AgentFixtureInput struct {
	ProjectID       ID
	Name            string
	CurrentConfigID ID
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
	if _, err := qtx.GetAgentConfig(
		ctx,
		dbsqlc.GetAgentConfigParams{ProjectID: input.ProjectID, ID: input.CurrentConfigID},
	); err != nil {
		return AgentRecord{}, fmt.Errorf("load agent config: %w", err)
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
