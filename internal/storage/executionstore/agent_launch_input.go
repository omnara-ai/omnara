package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type LaunchInitialInput struct {
	ContentBlocks          json.RawMessage        `json:"content_blocks"`
	Metadata               json.RawMessage        `json:"metadata,omitempty"`
	Actor                  *ActorParams           `json:"actor,omitempty"`
	Origin                 *LaunchInputOrigin     `json:"origin,omitempty"`
	DeliveryMode           AgentInputDeliveryMode `json:"delivery_mode,omitempty"`
	CancelOpenInteractions bool                   `json:"cancel_open_interactions,omitempty"`
	SemanticEventKey       string                 `json:"semantic_event_key,omitempty"`
}

type LaunchInputOrigin struct {
	IntegrationID uuid.UUID                            `json:"integration_id"`
	Address       integrationstore.ConversationAddress `json:"address"`
	DisplayName   string                               `json:"display_name,omitempty"`
}

type launchAdmission struct {
	Scheduled     bool
	AgentID       uuid.UUID
	IntegrationID uuid.UUID
	SelectionSlot string
	Artifacts     []artifactstore.PreparedArtifact
}

func prepareLaunchInitialInput(input LaunchAgentInput) (*LaunchInitialInput, []CreateContentBlockInput, error) {
	initial := input.InitialInput
	if initial != nil && (input.Message != "" || input.MessageActor != nil) {
		return nil, nil, storeerr.InvalidRequest(
			errors.New("initial_input is mutually exclusive with message and message_actor"),
		)
	}
	if initial == nil {
		if input.Message == "" {
			return nil, nil, nil
		}
		content, err := marshalAgentInputContentBlocks(
			[]CreateContentBlockInput{{BlockKind: ContentBlockKindText, TextContent: input.Message}},
		)
		if err != nil {
			return nil, nil, err
		}
		initial = &LaunchInitialInput{ContentBlocks: content, Actor: input.MessageActor}
	}
	snapshot := *initial
	blocks, err := parseAgentInputContentBlocks(snapshot.ContentBlocks)
	if err != nil {
		return nil, nil, err
	}
	snapshot.ContentBlocks, err = marshalAgentInputContentBlocks(blocks)
	if err != nil {
		return nil, nil, err
	}
	prepared, err := prepareCreateAgentContentInput(CreateAgentContentInputInput{
		DeliveryMode: snapshot.DeliveryMode, CancelOpenInteractions: snapshot.CancelOpenInteractions,
	})
	if err != nil {
		return nil, nil, err
	}
	snapshot.DeliveryMode = prepared.DeliveryMode
	if origin := snapshot.Origin; origin != nil {
		if origin.IntegrationID == uuid.Nil || snapshot.SemanticEventKey == "" {
			return nil, nil, storeerr.InvalidRequest(
				errors.New("initial origin requires an integration and semantic event key"),
			)
		}
		if err := origin.Address.Validate(); err != nil {
			return nil, nil, err
		}
	}
	return &snapshot, blocks, nil
}

func (s *Store) insertLaunchInitialContentInputTx(
	ctx context.Context,
	tx pgx.Tx,
	txNotifications *notifications.TxNotifications,
	agent AgentRecord,
	launch LaunchAgentInput,
	initial LaunchInitialInput,
	blocks []CreateContentBlockInput,
	admission *launchAdmission,
	result *LaunchAgentResult,
) error {
	q := dbsqlc.New(tx)
	actor := initial.Actor
	var err error
	if actor == nil {
		actor, err = OmnaraActorParams(agent.OrgID, launch.LaunchedBy)
		if err != nil {
			return err
		}
	}
	content := CreateAgentContentInputInput{
		ProjectID: agent.ProjectID, AgentID: agent.ID, Actor: actor,
		ContentBlocks: initial.ContentBlocks, Metadata: initial.Metadata,
		DeliveryMode: initial.DeliveryMode, CancelOpenInteractions: initial.CancelOpenInteractions,
		IdempotencyScope: "content_input", IdempotencyKey: initial.SemanticEventKey,
	}
	if content.IdempotencyKey == "" {
		content.IdempotencyKey = launchChildIdempotencyKey(launch.IdempotencyKey, "content-input")
	}
	if origin := initial.Origin; origin != nil {
		targetInput := integrationstore.EnsureConversationTargetInput{
			ProjectID: agent.ProjectID, AgentID: agent.ID, IntegrationID: origin.IntegrationID,
			Address: origin.Address, DisplayName: origin.DisplayName,
		}
		if admission != nil {
			targetInput.IntegrationID, targetInput.SelectionSlot = admission.IntegrationID, admission.SelectionSlot
		}
		result.IntegrationTarget, err = s.integrations.EnsureConversationTargetTx(ctx, tx, targetInput)
		if err != nil {
			return err
		}
		if admission != nil {
			if err := s.integrations.AssignAgentIntegrationConversationTx(
				ctx, tx, agent.ProjectID, agent.ID, admission.IntegrationID, origin.Address,
			); err != nil {
				return err
			}
		}
		integration, err := s.integrations.GetProjectIntegrationByIDTx(ctx, tx, origin.IntegrationID)
		if err != nil {
			return err
		}
		if admission == nil || !admission.Scheduled {
			if err := validateIntegrationInputActor(integration.ID, actor); err != nil {
				return err
			}
		}
		content.IntegrationTargetID = result.IntegrationTarget.ID
		content.IdempotencyScope = integrationstore.IdempotencyScope(integration)
	}
	if admission != nil {
		result.Artifacts, err = artifactstore.InsertPreparedArtifactsTx(
			ctx,
			tx,
			agent.ProjectID,
			agent.ID,
			admission.Artifacts,
		)
		if err != nil {
			return err
		}
	}
	created, err := createAgentContentInputTx(ctx, txNotifications, tx, q, agent, content, blocks)
	if err != nil {
		return err
	}
	if created.created && content.IntegrationTargetID != uuid.Nil {
		if _, err := s.SelectInteractionDestinationForOriginTx(
			ctx,
			tx,
			agent.ProjectID,
			agent.ID,
			content.IntegrationTargetID,
		); err != nil {
			return err
		}
	}
	result.AgentInput, result.InputContentBlocks = created.agentInput, created.contentBlocks
	return nil
}

func launchChildIdempotencyKey(parent, child string) string {
	if parent == "" {
		return ""
	}
	return "launch:" + parent + ":" + child
}

func machineSourceSlotChildIdempotencyKey(agentID uuid.UUID, index, slotIndex int, child string) string {
	return agentChildIdempotencyKey(
		agentID,
		fmt.Sprintf("machine-source:%d:slot:%d:%s", index, slotIndex, child),
	)
}

func agentChildIdempotencyKey(agentID uuid.UUID, child string) string {
	return "agent:" + agentID.String() + ":" + child
}
