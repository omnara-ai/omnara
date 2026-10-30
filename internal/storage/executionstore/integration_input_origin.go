package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type AgentInputOrigin struct {
	IntegrationID uuid.UUID                            `json:"integration_id"`
	Address       integrationstore.ConversationAddress `json:"address"`
	DisplayName   string                               `json:"display_name,omitempty"`
}

type InboxInputResult struct {
	Skipped                InboxInputSkipReason
	AgentInput             AgentInputRecord
	ContentBlocks          json.RawMessage
	IntegrationTarget      integrationstore.IntegrationTargetRecord
	Artifacts              []artifactstore.ArtifactRecord
	CanceledInteractionIDs []uuid.UUID
	Created                bool
}

func prepareOriginContentInput(
	input CreateAgentContentInputInput,
) (CreateAgentContentInputInput, []CreateContentBlockInput, error) {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.IdempotencyKey == "" {
		return input, nil, storeerr.InvalidRequest(
			errors.New("origin input requires project, agent and semantic idempotency key"),
		)
	}
	if input.Origin == nil || input.Origin.IntegrationID == uuid.Nil || input.IntegrationTargetID != uuid.Nil {
		return input, nil, storeerr.InvalidRequest(errors.New("inbox input requires an origin, not a target ID"))
	}
	if err := input.Origin.Address.Validate(); err != nil {
		return input, nil, err
	}
	input, err := prepareCreateAgentContentInput(input)
	if err != nil {
		return input, nil, err
	}
	blocks, err := parseAgentInputContentBlocks(input.ContentBlocks)
	if err != nil {
		return input, nil, err
	}
	input.ContentBlocks, err = marshalAgentInputContentBlocks(blocks)
	return input, blocks, err
}

func (s *Store) resolveInputOriginTx(
	ctx context.Context,
	tx dbsqlc.DBTX,
	input CreateAgentContentInputInput,
) (CreateAgentContentInputInput, integrationstore.IntegrationRecord, error) {
	integration, err := s.integrations.GetIntegrationByIDTx(ctx, tx, input.Origin.IntegrationID)
	if err != nil {
		return input, integration, err
	}
	if integration.ProjectID != input.ProjectID {
		return input, integration, storeerr.ErrUnauthorized
	}
	input.IdempotencyScope = integrationstore.IdempotencyScope(integration)
	return input, integration, nil
}

func validateIntegrationInputActor(integration integrationstore.IntegrationRecord, actor *ActorParams) error {
	if actor == nil || strings.TrimSpace(actor.ProviderUserID) == "" {
		return storeerr.ErrUnauthorized
	}
	expected, err := IntegrationActorParams(integration, actor.ProviderUserID, nil)
	if err != nil {
		return err
	}
	if actor.Provider != expected.Provider || actor.ProviderTenantID != expected.ProviderTenantID {
		return storeerr.ErrUnauthorized
	}
	return nil
}

func (s *Store) admitOriginContentTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	input CreateAgentContentInputInput,
	blocks []CreateContentBlockInput,
	artifacts []artifactstore.PreparedArtifact,
) (InboxInputResult, error) {
	tx := unit.DB()
	input, integration, err := s.resolveInputOriginTx(ctx, tx, input)
	if err != nil {
		return InboxInputResult{}, err
	}
	if err := validateIntegrationInputActor(integration, input.Actor); err != nil {
		return InboxInputResult{}, err
	}
	target, err := s.integrations.EnsureConversationTargetTx(ctx, tx, integrationstore.EnsureConversationTargetInput{
		ProjectID: input.ProjectID, AgentID: input.AgentID, IntegrationID: integration.ID,
		Address: input.Origin.Address, DisplayName: input.Origin.DisplayName,
	})
	if err != nil {
		return InboxInputResult{}, err
	}
	input.IntegrationTargetID = target.ID
	input.Source = AgentInputSourceIntegration
	input.PreparedArtifacts = artifacts
	created, err := createAgentContentInputTx(ctx, unit, dbsqlc.New(tx), input, blocks)
	if err != nil {
		return InboxInputResult{}, err
	}
	return InboxInputResult{
		AgentInput:             created.agentInput,
		ContentBlocks:          created.contentBlocks,
		IntegrationTarget:      target,
		Artifacts:              created.artifacts,
		Created:                created.created,
		CanceledInteractionIDs: created.canceledInteractionIDs,
	}, nil
}
