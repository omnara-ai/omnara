package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateAgentContentInput(
	ctx context.Context,
	input CreateAgentContentInputInput,
) (AgentInputRecord, json.RawMessage, bool, error) {
	if input.ProjectID == uuid.Nil {
		return AgentInputRecord{}, nil, false, errors.New("project id is required")
	}
	if input.AgentID == uuid.Nil {
		return AgentInputRecord{}, nil, false, errors.New("agent id is required")
	}
	if input.Origin != nil || input.IntegrationTargetID != uuid.Nil {
		return AgentInputRecord{}, nil, false, storeerr.InvalidRequest(
			errors.New("integration origin requires verified inbox admission"),
		)
	}
	exists, err := s.q.AgentExistsInProject(
		ctx,
		dbsqlc.AgentExistsInProjectParams{
			ProjectID: input.ProjectID,
			ID:        input.AgentID,
		},
	)
	if err != nil {
		return AgentInputRecord{}, nil, false, fmt.Errorf(
			"check agent for content input: %w",
			err,
		)
	}
	if !exists {
		return AgentInputRecord{}, nil, false, storeerr.ErrNotFound
	}
	input, err = prepareCreateAgentContentInput(input)
	if err != nil {
		return AgentInputRecord{}, nil, false, err
	}
	contentBlocks, err := parseAgentInputContentBlocks(input.ContentBlocks)
	if err != nil {
		return AgentInputRecord{}, nil, false, err
	}
	input.ContentBlocks, err = marshalAgentInputContentBlocks(contentBlocks)
	if err != nil {
		return AgentInputRecord{}, nil, false, err
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentInputRecord{}, nil, false, fmt.Errorf(
			"begin create agent content input: %w",
			err,
		)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	agent, err := loadAgentTx(ctx, tx, input.AgentID)
	if err != nil {
		return AgentInputRecord{}, nil, false, err
	}
	if agent.ProjectID != input.ProjectID {
		return AgentInputRecord{}, nil, false, storeerr.ErrNotFound
	}
	result, err := createAgentContentInputTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		agent,
		input,
		contentBlocks,
	)
	if err != nil {
		return AgentInputRecord{}, nil, false, err
	}
	if err := s.commitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		"create agent content input",
	); err != nil {
		return AgentInputRecord{}, nil, false, err
	}
	return result.agentInput, result.contentBlocks, result.created, nil
}

func prepareCreateAgentContentInput(
	input CreateAgentContentInputInput,
) (CreateAgentContentInputInput, error) {
	if input.DeliveryMode == "" {
		input.DeliveryMode = DeliveryModeQueued
	}
	if input.DeliveryMode != DeliveryModeQueued && input.DeliveryMode != DeliveryModeSteering {
		return CreateAgentContentInputInput{}, storeerr.InvalidRequest(errors.New(
			"delivery_mode must be queued or steering",
		))
	}
	if input.DeliveryMode == DeliveryModeQueued && input.CancelOpenInteractions {
		return CreateAgentContentInputInput{}, storeerr.InvalidRequest(errors.New(
			"cancel_open_interactions is allowed only for steering inputs",
		))
	}
	if input.IdempotencyScope == "" {
		input.IdempotencyScope = "content_input"
	}
	return input, nil
}

type createAgentContentInputTxResult struct {
	agentInput             AgentInputRecord
	contentBlocks          json.RawMessage
	created                bool
	canceledInteractionIDs []uuid.UUID
}

func createAgentContentInputTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	agent AgentRecord,
	input CreateAgentContentInputInput,
	contentBlocks []CreateContentBlockInput,
) (createAgentContentInputTxResult, error) {
	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: input.ProjectID, ID: input.AgentID},
	); err != nil {
		return createAgentContentInputTxResult{}, fmt.Errorf("lock agent for content input: %w", err)
	}
	if input.IdempotencyKey != "" {
		existingInput, found, err := loadAgentInputByIdempotencyMaybeTx(
			ctx,
			tx,
			input.ProjectID,
			input.AgentID,
			input.IdempotencyScope,
			input.IdempotencyKey,
		)
		if err != nil {
			return createAgentContentInputTxResult{}, err
		}
		if found {
			existingActorID, actorFound, err := lookupActorIDTx(
				ctx,
				qtx,
				input.ProjectID,
				input.Actor,
			)
			if err != nil {
				return createAgentContentInputTxResult{}, err
			}
			existingContentBlocksByInput, err := agentInputContentBlocks(
				ctx,
				qtx,
				existingInput.ProjectID,
				existingInput.AgentID,
				[]uuid.UUID{existingInput.ID},
			)
			if err != nil {
				return createAgentContentInputTxResult{}, err
			}
			existingContentBlocks := existingContentBlocksByInput[existingInput.ID]
			if !actorFound ||
				existingInput.DeliveryMode != input.DeliveryMode ||
				existingInput.ActorID != existingActorID ||
				existingInput.IntegrationTargetID != input.IntegrationTargetID ||
				!sameJSON(existingInput.Metadata, normalizedJSON(input.Metadata)) ||
				!sameJSON(existingContentBlocks, input.ContentBlocks) {
				return createAgentContentInputTxResult{}, storeerr.ErrIdempotencyConflict
			}
			return createAgentContentInputTxResult{
				agentInput:    existingInput,
				contentBlocks: existingContentBlocks,
			}, nil
		}
	}
	var err error
	agent, err = loadAgentInProjectTx(ctx, tx, input.ProjectID, input.AgentID)
	if err != nil {
		return createAgentContentInputTxResult{}, err
	}
	if agent.State == AgentStateArchived {
		return createAgentContentInputTxResult{}, storeerr.ErrStateTransitionConflict
	}
	// Origin belongs to the project/agent and an active connection independently
	// of actor attribution. Hosted ingress validates its verified actor before
	// reaching this shared input kernel.
	if input.IntegrationTargetID != uuid.Nil {
		target, err := qtx.GetInteractionDestinationTarget(ctx, dbsqlc.GetInteractionDestinationTargetParams{
			ProjectID: input.ProjectID, AgentID: input.AgentID, TargetID: input.IntegrationTargetID,
		})
		if err != nil {
			return createAgentContentInputTxResult{}, err
		}
		if target.ConnectionState != "active" {
			return createAgentContentInputTxResult{}, storeerr.ErrUnauthorized
		}
	}
	actorID, err := resolveActorTx(
		ctx,
		qtx,
		input.ProjectID,
		input.AgentID,
		input.Actor,
		uuid.Nil,
	)
	if err != nil {
		return createAgentContentInputTxResult{}, err
	}
	agentInput, err := insertAgentInputTx(ctx, tx, insertAgentInputInput{
		ProjectID:           input.ProjectID,
		AgentID:             input.AgentID,
		DeliveryMode:        input.DeliveryMode,
		ActorID:             actorID,
		IntegrationTargetID: input.IntegrationTargetID,
		IdempotencyScope:    input.IdempotencyScope,
		InputIdempotencyKey: input.IdempotencyKey,
		Metadata:            input.Metadata,
	})
	if err != nil {
		return createAgentContentInputTxResult{}, err
	}
	if err := createAgentInputContentBlocksTx(
		ctx,
		tx,
		agentInput,
		contentBlocks,
	); err != nil {
		return createAgentContentInputTxResult{}, err
	}
	result := createAgentContentInputTxResult{
		agentInput:    agentInput,
		contentBlocks: input.ContentBlocks,
		created:       true,
	}
	if input.CancelOpenInteractions {
		result.canceledInteractionIDs, err = cancelOpenInteractionsForSteeringInputTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			input.ProjectID,
			input.AgentID,
			agentInput.ID,
		)
		if err != nil {
			return createAgentContentInputTxResult{}, err
		}
	}
	if err := qtx.ReconcileAgentWakeup(
		ctx,
		dbsqlc.ReconcileAgentWakeupParams{
			ProjectID: input.ProjectID,
			AgentID:   input.AgentID,
			Metadata:  []byte(`{"reason":"agent_input"}`),
		},
	); err != nil {
		return createAgentContentInputTxResult{}, fmt.Errorf(
			"reconcile agent wakeup after input: %w",
			err,
		)
	}
	return result, nil
}

func createAgentInputContentBlocksTx(
	ctx context.Context,
	tx pgx.Tx,
	input AgentInputRecord,
	blocks []CreateContentBlockInput,
) error {
	for _, block := range blocks {
		block.ProjectID = input.ProjectID
		block.AgentID = input.AgentID
		block.OwnerKind = ContentBlockOwnerAgentInput
		block.OwnerAgentInputID = input.ID
		if _, err := createContentBlockTx(ctx, tx, block); err != nil {
			return err
		}
	}
	return nil
}

func agentInputContentBlocks(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID uuid.UUID,
	inputIDs []uuid.UUID,
) (map[uuid.UUID]json.RawMessage, error) {
	contentBlocks := make(map[uuid.UUID]json.RawMessage, len(inputIDs))
	if len(inputIDs) == 0 {
		return contentBlocks, nil
	}
	rows, err := q.ListContentBlocksForAgentInputs(ctx, dbsqlc.ListContentBlocksForAgentInputsParams{
		ProjectID:     projectID,
		AgentID:       agentID,
		AgentInputIds: inputIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("list agent input content blocks: %w", err)
	}
	blocksByInput := make(map[uuid.UUID][]CreateContentBlockInput, len(inputIDs))
	for _, row := range rows {
		metadata, err := resourcemeta.FromJSON(row.Metadata)
		if err != nil {
			return nil, fmt.Errorf("decode agent input content block metadata: %w", err)
		}
		block := CreateContentBlockInput{
			Ordinal:     row.Ordinal,
			BlockKind:   ContentBlockKind(row.BlockKind),
			TextContent: row.TextContent,
			Metadata:    metadata,
		}
		if row.ArtifactID != nil {
			block.ArtifactID = *row.ArtifactID
		}
		inputID := *row.OwnerAgentInputID
		blocksByInput[inputID] = append(blocksByInput[inputID], block)
	}
	for _, inputID := range inputIDs {
		body, err := marshalAgentInputContentBlocks(blocksByInput[inputID])
		if err != nil {
			return nil, fmt.Errorf("marshal agent input content parts: %w", err)
		}
		contentBlocks[inputID] = body
	}
	return contentBlocks, nil
}

type CreateAgentContentInputInput struct {
	ProjectID           uuid.UUID    `json:"project_id,omitempty"`
	AgentID             uuid.UUID    `json:"agent_id,omitempty"`
	Actor               *ActorParams `json:"actor,omitempty"`
	IntegrationTargetID uuid.UUID    `json:"integration_target_id,omitempty"`
	// Origin belongs to verified inbox admission. Ordinary content input rejects
	// origin and target fields; its actors and idempotency remain independent.
	Origin                 *AgentInputOrigin      `json:"origin,omitempty"`
	ContentBlocks          json.RawMessage        `json:"content_blocks"`
	Metadata               json.RawMessage        `json:"metadata,omitempty"`
	DeliveryMode           AgentInputDeliveryMode `json:"delivery_mode,omitempty"`
	IdempotencyScope       string                 `json:"idempotency_scope,omitempty"`
	IdempotencyKey         string                 `json:"idempotency_key,omitempty"`
	CancelOpenInteractions bool                   `json:"cancel_open_interactions,omitempty"`
}
