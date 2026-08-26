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

func (s *Store) CreateIntegrationTargetContentInput(
	ctx context.Context,
	input CreateIntegrationTargetContentInput,
) (AgentInputRecord, []ID, error) {
	if isNilID(input.IntegrationInstallID) || isNilID(input.IntegrationTargetID) ||
		input.ProviderUserID == "" || input.IdempotencyKey == "" {
		return AgentInputRecord{}, nil, errors.New(
			"integration install, integration target, provider user, and idempotency key are required",
		)
	}
	contentBlocks, err := parseAgentInputContentBlocks(input.ContentBlocks)
	if err != nil {
		return AgentInputRecord{}, nil, err
	}
	input.ContentBlocks, err = marshalAgentInputContentBlocks(contentBlocks)
	if err != nil {
		return AgentInputRecord{}, nil, err
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentInputRecord{}, nil, fmt.Errorf("begin create integration target input: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	install, err := s.integrations.GetIntegrationInstallByIDTx(ctx, tx, input.IntegrationInstallID)
	if err != nil {
		return AgentInputRecord{}, nil, err
	}
	target, err := s.integrations.GetIntegrationTargetTx(
		ctx,
		tx,
		install.ProjectID,
		input.IntegrationTargetID,
	)
	if err != nil {
		return AgentInputRecord{}, nil, err
	}
	if target.IntegrationInstallID != install.ID {
		return AgentInputRecord{}, nil, storeerr.ErrConflict
	}
	if existing, found, err := integrationTargetInputByIdempotency(
		ctx,
		qtx,
		install,
		target,
		input.IdempotencyKey,
	); err != nil {
		return AgentInputRecord{}, nil, err
	} else if found {
		return existing, nil, nil
	}
	if install.State != integrationstore.IntegrationInstallStateActive {
		return AgentInputRecord{}, nil, storeerr.ErrUnauthorized
	}
	if err := integrationstore.ValidateProviderUserTenant(install, input.ProviderTenantID); err != nil {
		return AgentInputRecord{}, nil, err
	}
	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: install.ProjectID, ID: target.AgentID},
	); err != nil {
		return AgentInputRecord{}, nil, fmt.Errorf("lock agent for integration input: %w", err)
	}
	agent, err := loadAgentInProjectTx(ctx, tx, install.ProjectID, target.AgentID)
	if err != nil {
		return AgentInputRecord{}, nil, err
	}
	contentInput := CreateAgentContentInputInput{
		ProjectID: install.ProjectID,
		AgentID:   target.AgentID,
		Actor: &ActorParams{
			Provider:         install.Provider,
			ProviderTenantID: input.ProviderTenantID,
			ProviderUserID:   input.ProviderUserID,
			DisplayName:      &input.ActorDisplayName,
		},
		IntegrationTargetID:    target.ID,
		ContentBlocks:          input.ContentBlocks,
		Metadata:               input.Metadata,
		DeliveryMode:           input.DeliveryMode,
		IdempotencyScope:       integrationstore.IdempotencyScope(install),
		IdempotencyKey:         input.IdempotencyKey,
		CancelOpenInteractions: input.CancelOpenInteractions,
	}
	contentInput, err = prepareCreateAgentContentInput(contentInput)
	if err != nil {
		return AgentInputRecord{}, nil, err
	}
	result, err := createAgentContentInputTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		agent,
		contentInput,
		contentBlocks,
	)
	if err != nil {
		if errors.Is(err, storeerr.ErrIdempotencyConflict) {
			if existing, found, replayErr := s.GetIntegrationTargetInputByIdempotency(
				ctx,
				GetIntegrationTargetInputByIdempotencyInput{
					IntegrationInstallID: input.IntegrationInstallID,
					IntegrationTargetID:  input.IntegrationTargetID,
					IdempotencyKey:       input.IdempotencyKey,
				},
			); replayErr != nil {
				return AgentInputRecord{}, nil, replayErr
			} else if found {
				return existing, nil, nil
			}
		}
		return AgentInputRecord{}, nil, err
	}
	agentInput := result.agentInput
	if result.created && agent.IntegrationTargetID != target.ID {
		if _, err := qtx.SetAgentIntegrationTarget(
			ctx,
			dbsqlc.SetAgentIntegrationTargetParams{
				ProjectID:           agent.ProjectID,
				AgentID:             agent.ID,
				IntegrationTargetID: &target.ID,
			},
		); err != nil {
			return AgentInputRecord{}, nil, fmt.Errorf("set agent integration target: %w", err)
		}
	}
	if err := s.commitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		"create integration target input",
	); err != nil {
		return AgentInputRecord{}, nil, err
	}
	return agentInput, result.canceledInteractionIDs, nil
}

func (s *Store) GetIntegrationTargetInputByIdempotency(
	ctx context.Context,
	input GetIntegrationTargetInputByIdempotencyInput,
) (AgentInputRecord, bool, error) {
	if isNilID(input.IntegrationInstallID) || isNilID(input.IntegrationTargetID) ||
		input.IdempotencyKey == "" {
		return AgentInputRecord{}, false, errors.New(
			"integration install, integration target, and idempotency key are required",
		)
	}
	install, err := s.integrations.GetIntegrationInstallByID(ctx, input.IntegrationInstallID)
	if err != nil {
		return AgentInputRecord{}, false, err
	}
	target, err := s.integrations.GetIntegrationTarget(ctx, install.ProjectID, input.IntegrationTargetID)
	if err != nil {
		return AgentInputRecord{}, false, err
	}
	if target.IntegrationInstallID != install.ID {
		return AgentInputRecord{}, false, storeerr.ErrConflict
	}
	return integrationTargetInputByIdempotency(ctx, s.q, install, target, input.IdempotencyKey)
}

func integrationTargetInputByIdempotency(
	ctx context.Context,
	q *dbsqlc.Queries,
	install integrationstore.IntegrationInstallRecord,
	target integrationstore.IntegrationTargetRecord,
	idempotencyKey string,
) (AgentInputRecord, bool, error) {
	row, err := q.GetAgentInputByIdempotency(
		ctx,
		dbsqlc.GetAgentInputByIdempotencyParams{
			ProjectID:           install.ProjectID,
			AgentID:             target.AgentID,
			IdempotencyScope:    integrationstore.IdempotencyScope(install),
			InputIdempotencyKey: idempotencyKey,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentInputRecord{}, false, nil
	}
	if err != nil {
		return AgentInputRecord{}, false, fmt.Errorf("load agent input by idempotency: %w", err)
	}
	agentInput := agentInputRecordFromIdempotencySQLC(row)
	if agentInput.IntegrationTargetID != target.ID {
		return AgentInputRecord{}, false, storeerr.ErrIdempotencyConflict
	}
	return agentInput, true, nil
}
