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
	if isNilID(input.IntegrationTargetBindingID) != isNilID(input.AgentID) {
		return AgentInputRecord{}, nil, errors.New(
			"integration target binding and agent must either both be set or both be omitted",
		)
	}
	if err := integrationstore.ValidateIntegrationRuntimeLeaseProof(input.RuntimeLease); err != nil {
		return AgentInputRecord{}, nil, err
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
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
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
	agentID := target.AgentID
	if !isNilID(input.IntegrationTargetBindingID) {
		agentID = input.AgentID
	}
	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: install.ProjectID, ID: agentID},
	); err != nil {
		return AgentInputRecord{}, nil, fmt.Errorf("lock agent for integration input: %w", err)
	}
	idempotencyScope := integrationstore.IdempotencyScope(install)
	var binding integrationstore.IntegrationTargetBindingRecord
	if !isNilID(input.IntegrationTargetBindingID) {
		binding, err = s.integrations.GetActiveReceiveBindingTx(
			ctx,
			tx,
			install.ProjectID,
			input.AgentID,
			install.ID,
			target.ID,
			input.IntegrationTargetBindingID,
		)
		if err != nil {
			return AgentInputRecord{}, nil, err
		}
		idempotencyScope = integrationstore.BindingIdempotencyScope(install, binding)
	}
	if existing, found, err := integrationTargetInputByIdempotency(
		ctx,
		qtx,
		install.ProjectID,
		agentID,
		target.ID,
		input.IntegrationTargetBindingID,
		idempotencyScope,
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
	agent, err := loadAgentInProjectTx(ctx, tx, install.ProjectID, agentID)
	if err != nil {
		return AgentInputRecord{}, nil, err
	}
	if err := integrationstore.LockIntegrationRuntimeLeaseForMutation(
		ctx,
		qtx,
		input.RuntimeLease,
		install.ProjectID,
		install.ID,
	); err != nil {
		return AgentInputRecord{}, nil, err
	}
	if input.RefreshTarget {
		target, err = s.integrations.GetOrCreateIntegrationTargetForBindingTx(
			ctx,
			tx,
			integrationstore.CreateIntegrationTargetInput{
				ProjectID: install.ProjectID, AgentID: agentID,
				IntegrationInstallID: install.ID, ProviderRef: target.ProviderRef,
				ProviderRefKind: target.ProviderRefKind, DisplayName: input.TargetDisplayName,
				ProviderMetadata: input.TargetProviderMetadata,
			},
		)
		if err != nil {
			return AgentInputRecord{}, nil, err
		}
	}
	contentInput := CreateAgentContentInputInput{
		ProjectID: install.ProjectID,
		AgentID:   agentID,
		Actor: &ActorParams{
			Provider:         install.Provider,
			ProviderTenantID: input.ProviderTenantID,
			ProviderUserID:   input.ProviderUserID,
			DisplayName:      &input.ActorDisplayName,
		},
		IntegrationTargetID:        target.ID,
		IntegrationTargetBindingID: input.IntegrationTargetBindingID,
		ContentBlocks:              input.ContentBlocks,
		Metadata:                   input.Metadata,
		DeliveryMode:               input.DeliveryMode,
		IdempotencyScope:           idempotencyScope,
		IdempotencyKey:             input.IdempotencyKey,
		CancelOpenInteractions:     input.CancelOpenInteractions,
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
			if existing, found, replayErr := integrationTargetInputByIdempotency(
				ctx,
				s.q,
				install.ProjectID,
				agentID,
				target.ID,
				input.IntegrationTargetBindingID,
				idempotencyScope,
				input.IdempotencyKey,
			); replayErr != nil {
				return AgentInputRecord{}, nil, replayErr
			} else if found {
				return existing, nil, nil
			}
		}
		return AgentInputRecord{}, nil, err
	}
	agentInput := result.agentInput
	if result.created && isNilID(input.IntegrationTargetBindingID) && agent.IntegrationTargetID != target.ID {
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

func (s *Store) CreateBoundIntegrationTargetContentInput(
	ctx context.Context,
	input CreateBoundIntegrationTargetContentInput,
) (CreateBoundIntegrationTargetContentResult, error) {
	targetInput := input.Target
	if isNilID(targetInput.ProjectID) || isNilID(targetInput.AgentID) ||
		isNilID(targetInput.IntegrationInstallID) || input.ProviderUserID == "" ||
		input.IdempotencyKey == "" {
		return CreateBoundIntegrationTargetContentResult{}, errors.New(
			"project, agent, integration install, provider user, and idempotency key are required",
		)
	}
	if err := integrationstore.ValidateIntegrationRuntimeLeaseProof(input.RuntimeLease); err != nil {
		return CreateBoundIntegrationTargetContentResult{}, err
	}
	contentBlocks, err := parseAgentInputContentBlocks(input.ContentBlocks)
	if err != nil {
		return CreateBoundIntegrationTargetContentResult{}, err
	}
	input.ContentBlocks, err = marshalAgentInputContentBlocks(contentBlocks)
	if err != nil {
		return CreateBoundIntegrationTargetContentResult{}, err
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CreateBoundIntegrationTargetContentResult{}, fmt.Errorf(
			"begin create bound integration target input: %w",
			err,
		)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	install, err := s.integrations.GetIntegrationInstallByIDTx(
		ctx,
		tx,
		targetInput.IntegrationInstallID,
	)
	if err != nil {
		return CreateBoundIntegrationTargetContentResult{}, err
	}
	if install.ProjectID != targetInput.ProjectID {
		return CreateBoundIntegrationTargetContentResult{}, storeerr.ErrConflict
	}
	if install.State != integrationstore.IntegrationInstallStateActive {
		return CreateBoundIntegrationTargetContentResult{}, storeerr.ErrUnauthorized
	}
	if err := integrationstore.ValidateProviderUserTenant(install, input.ProviderTenantID); err != nil {
		return CreateBoundIntegrationTargetContentResult{}, err
	}
	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{
			ProjectID: targetInput.ProjectID,
			ID:        targetInput.AgentID,
		},
	); err != nil {
		return CreateBoundIntegrationTargetContentResult{}, fmt.Errorf(
			"lock agent for bound integration input: %w",
			err,
		)
	}
	if err := integrationstore.LockIntegrationRuntimeLeaseForMutation(
		ctx,
		qtx,
		input.RuntimeLease,
		install.ProjectID,
		install.ID,
	); err != nil {
		return CreateBoundIntegrationTargetContentResult{}, err
	}
	target, err := s.integrations.GetOrCreateIntegrationTargetForBindingTx(
		ctx,
		tx,
		targetInput,
	)
	if err != nil {
		return CreateBoundIntegrationTargetContentResult{}, err
	}
	idempotencyScope := integrationstore.RouteIdempotencyScope(install, input.IntegrationRouteID)
	if existing, found, existingErr := integrationTargetInputByIdempotency(
		ctx,
		qtx,
		install.ProjectID,
		targetInput.AgentID,
		target.ID,
		NilID,
		idempotencyScope,
		input.IdempotencyKey,
	); existingErr != nil {
		return CreateBoundIntegrationTargetContentResult{}, existingErr
	} else if found {
		return CreateBoundIntegrationTargetContentResult{
			AgentInput:                 existing,
			IntegrationTargetID:        existing.IntegrationTargetID,
			IntegrationTargetBindingID: existing.IntegrationTargetBindingID,
		}, nil
	}
	agent, err := loadAgentInProjectTx(ctx, tx, install.ProjectID, targetInput.AgentID)
	if err != nil {
		return CreateBoundIntegrationTargetContentResult{}, err
	}
	if agent.State != AgentStateActive {
		return CreateBoundIntegrationTargetContentResult{}, storeerr.ErrStateTransitionConflict
	}
	binding, err := s.integrations.CreateIntegrationTargetBindingTx(
		ctx,
		tx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID:            install.ProjectID,
			AgentID:              targetInput.AgentID,
			IntegrationInstallID: install.ID,
			IntegrationTargetID:  target.ID,
			IntegrationRouteID:   input.IntegrationRouteID,
			ReceiveAllowed:       input.ReceiveAllowed,
			SendAllowed:          input.SendAllowed,
			Source:               input.BindingSource,
			Metadata:             input.BindingMetadata,
		},
	)
	if err != nil {
		return CreateBoundIntegrationTargetContentResult{}, err
	}
	contentInput, err := prepareCreateAgentContentInput(CreateAgentContentInputInput{
		ProjectID: install.ProjectID,
		AgentID:   targetInput.AgentID,
		Actor: &ActorParams{
			Provider:         install.Provider,
			ProviderTenantID: input.ProviderTenantID,
			ProviderUserID:   input.ProviderUserID,
			DisplayName:      &input.ActorDisplayName,
		},
		IntegrationTargetID:        target.ID,
		IntegrationTargetBindingID: binding.ID,
		ContentBlocks:              input.ContentBlocks,
		Metadata:                   input.Metadata,
		DeliveryMode:               input.DeliveryMode,
		IdempotencyScope:           idempotencyScope,
		IdempotencyKey:             input.IdempotencyKey,
		CancelOpenInteractions:     input.CancelOpenInteractions,
	})
	if err != nil {
		return CreateBoundIntegrationTargetContentResult{}, err
	}
	created, err := createAgentContentInputTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		agent,
		contentInput,
		contentBlocks,
	)
	if err != nil {
		return CreateBoundIntegrationTargetContentResult{}, err
	}
	if err := s.commitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		"create bound integration target input",
	); err != nil {
		return CreateBoundIntegrationTargetContentResult{}, err
	}
	return CreateBoundIntegrationTargetContentResult{
		AgentInput:                 created.agentInput,
		CanceledInteractionIDs:     created.canceledInteractionIDs,
		IntegrationTargetID:        target.ID,
		IntegrationTargetBindingID: binding.ID,
	}, nil
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
	return integrationTargetInputByIdempotency(
		ctx,
		s.q,
		install.ProjectID,
		target.AgentID,
		target.ID,
		NilID,
		integrationstore.IdempotencyScope(install),
		input.IdempotencyKey,
	)
}

func integrationTargetInputByIdempotency(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID, integrationTargetID, integrationTargetBindingID ID,
	idempotencyScope string,
	idempotencyKey string,
) (AgentInputRecord, bool, error) {
	row, err := q.GetAgentInputByIdempotency(
		ctx,
		dbsqlc.GetAgentInputByIdempotencyParams{
			ProjectID:           projectID,
			AgentID:             agentID,
			IdempotencyScope:    idempotencyScope,
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
	if agentInput.IntegrationTargetID != integrationTargetID ||
		(!isNilID(integrationTargetBindingID) &&
			agentInput.IntegrationTargetBindingID != integrationTargetBindingID) {
		return AgentInputRecord{}, false, storeerr.ErrIdempotencyConflict
	}
	return agentInput, true, nil
}
