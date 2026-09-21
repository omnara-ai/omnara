package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// AgentInputOrigin is attribution for verified hosted inbox admission, not actor
// identity or tool authority. Ordinary public input has no integration origin.
type AgentInputOrigin struct {
	AppID       uuid.UUID                            `json:"app_id"`
	Address     integrationstore.ConversationAddress `json:"address"`
	DisplayName string                               `json:"display_name,omitempty"`
}

// InboxInputResult returns the recorded input with Created=false on replay, without cancellation or
// newly inserted artifacts. A committed receipt replay need not reload its
// target; AgentInput.IntegrationTargetID remains the durable attribution.
type InboxInputResult struct {
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
	if input.Origin == nil || input.Origin.AppID == uuid.Nil || input.IntegrationTargetID != uuid.Nil {
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
	tx pgx.Tx,
	input CreateAgentContentInputInput,
) (CreateAgentContentInputInput, integrationstore.ProjectAppRecord, error) {
	app, err := s.integrations.GetProjectAppByIDTx(ctx, tx, input.Origin.AppID)
	if err != nil {
		return input, app, err
	}
	if app.ProjectID != input.ProjectID {
		return input, app, storeerr.ErrUnauthorized
	}
	input.IdempotencyScope = integrationstore.IdempotencyScope(app)
	return input, app, nil
}

// App attribution is checked against the already loaded receipt app. This is
// identity consistency, not an approver ACL or a live actor-to-app relationship.
func validateAppInputActor(appID uuid.UUID, actor *ActorParams) error {
	if actor == nil || strings.TrimSpace(actor.ProviderUserID) == "" {
		return storeerr.ErrUnauthorized
	}
	expected, err := AppActorParams(appID, actor.ProviderUserID, nil)
	if err != nil {
		return err
	}
	if actor.Provider != expected.Provider || actor.ProviderTenantID != expected.ProviderTenantID {
		return storeerr.ErrUnauthorized
	}
	return nil
}

// Caller holds project/app/conversation gates. No earlier locks are
// acquired here; the agent row serializes input dedupe and transient effects.
func (s *Store) admitOriginContentTx(
	ctx context.Context,
	tx pgx.Tx,
	notifications *notifications.TxNotifications,
	input CreateAgentContentInputInput,
	blocks []CreateContentBlockInput,
	artifacts []artifactstore.PreparedArtifact,
) (InboxInputResult, error) {
	if err := lifecyclelock.Agents(
		ctx,
		tx,
		[]lifecyclelock.AgentRef{{ProjectID: input.ProjectID, AgentID: input.AgentID}},
	); err != nil {
		return InboxInputResult{}, err
	}
	if result, found, err := s.originContentReplayTx(ctx, tx, input); err != nil || found {
		return result, err
	}
	input, app, err := s.resolveInputOriginTx(ctx, tx, input)
	if err != nil {
		return InboxInputResult{}, err
	}
	if err := validateAppInputActor(app.ID, input.Actor); err != nil {
		return InboxInputResult{}, err
	}
	agent, err := loadAgentInProjectTx(ctx, tx, input.ProjectID, input.AgentID)
	if err != nil {
		return InboxInputResult{}, err
	}
	target, err := s.integrations.EnsureConversationTargetTx(ctx, tx, integrationstore.EnsureConversationTargetInput{
		ProjectID: input.ProjectID, AgentID: input.AgentID, AppID: app.ID,
		Address: input.Origin.Address, DisplayName: input.Origin.DisplayName,
	})
	if err != nil {
		return InboxInputResult{}, err
	}
	input.IntegrationTargetID = target.ID
	files, err := artifactstore.InsertPreparedArtifactsTx(ctx, tx, input.ProjectID, input.AgentID, artifacts)
	if err != nil {
		return InboxInputResult{}, err
	}
	created, err := createAgentContentInputTx(ctx, notifications, tx, dbsqlc.New(tx), agent, input, blocks)
	if err != nil {
		return InboxInputResult{}, err
	}
	if created.created {
		if _, err := s.SelectInteractionDestinationForOriginTx(
			ctx,
			tx,
			input.ProjectID,
			input.AgentID,
			target.ID,
		); err != nil {
			return InboxInputResult{}, err
		}
	}
	return InboxInputResult{
		AgentInput:             created.agentInput,
		ContentBlocks:          created.contentBlocks,
		IntegrationTarget:      target,
		Artifacts:              files,
		Created:                created.created,
		CanceledInteractionIDs: created.canceledInteractionIDs,
	}, nil
}

func (s *Store) originContentReplayTx(
	ctx context.Context,
	tx pgx.Tx,
	input CreateAgentContentInputInput,
) (InboxInputResult, bool, error) {
	q := dbsqlc.New(tx)
	existing, found, err := loadAgentInputByIdempotencyMaybeTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		input.IdempotencyScope,
		input.IdempotencyKey,
	)
	if err != nil || !found {
		return InboxInputResult{}, false, err
	}
	target, err := s.integrations.GetIntegrationTargetTx(ctx, tx, input.ProjectID, existing.IntegrationTargetID)
	if err != nil {
		return InboxInputResult{}, false, err
	}
	if target.AgentID != input.AgentID || target.AppID != input.Origin.AppID ||
		target.ProviderRefKind != input.Origin.Address.Kind || target.ProviderRef != input.Origin.Address.Ref {
		return InboxInputResult{}, false, storeerr.ErrIdempotencyConflict
	}
	content, err := agentInputContentBlocks(ctx, q, input.ProjectID, input.AgentID, []uuid.UUID{existing.ID})
	return InboxInputResult{
		AgentInput:        existing,
		ContentBlocks:     content[existing.ID],
		IntegrationTarget: target,
	}, err == nil, err
}
