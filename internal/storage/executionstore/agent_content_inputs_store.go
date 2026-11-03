package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
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
	type transactionResult struct {
		input         AgentInputRecord
		contentBlocks json.RawMessage
		created       bool
	}
	retried, retryErr := agentexecution.Retry(ctx,
		"create_agent_content_input",
		func(ctx context.Context) (transactionResult, error) {
			unit, err := s.cell.Begin(ctx)
			if err != nil {
				return transactionResult{}, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			tx := unit.DB()

			qtx := dbsqlc.New(tx)
			result, err := admitAgentContentInputTx(
				ctx,
				unit,
				qtx,
				input,
				contentBlocks,
			)
			if err != nil {
				return transactionResult{}, err
			}
			if err := unit.Commit(ctx, "create agent content input"); err != nil {
				return transactionResult{}, err
			}
			return transactionResult{result.agentInput, result.contentBlocks, result.created}, nil
		})
	return retried.input, retried.contentBlocks, retried.created, retryErr
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
	if input.IdempotencyKey != "" && input.IdempotencyScope == "" {
		input.IdempotencyScope = "content_input"
	}
	return input, nil
}

type createAgentContentInputTxResult struct {
	agentInput             AgentInputRecord
	contentBlocks          json.RawMessage
	created                bool
	canceledInteractionIDs []uuid.UUID
	artifacts              []artifactstore.ArtifactRecord
}

func admitAgentContentInputTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	qtx *dbsqlc.Queries,
	input CreateAgentContentInputInput,
	contentBlocks []CreateContentBlockInput,
) (createAgentContentInputTxResult, error) {
	var err error
	input, err = prepareCreateAgentContentInput(input)
	if err != nil {
		return createAgentContentInputTxResult{}, err
	}
	tx := unit.DB()
	h, err := lockExecution(ctx, unit, input.ProjectID, input.AgentID, false, uuid.Nil)
	if err != nil {
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
			return createAgentContentInputTxResult{
				agentInput:    existingInput,
				contentBlocks: existingContentBlocksByInput[existingInput.ID],
			}, nil
		}
	}
	snapshot, err := h.LoadExecution(ctx)
	if err != nil {
		return createAgentContentInputTxResult{}, err
	}
	if snapshot.View.State == agentexecution.AgentArchived {
		return createAgentContentInputTxResult{}, storeerr.ErrStateTransitionConflict
	}
	return createAgentContentInputTx(ctx, unit, qtx, input, contentBlocks)
}

func createAgentContentInputTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	qtx *dbsqlc.Queries,
	input CreateAgentContentInputInput,
	contentBlocks []CreateContentBlockInput,
) (createAgentContentInputTxResult, error) {
	if input.IntegrationTargetID != uuid.Nil {
		target, err := qtx.GetInteractionDestinationTarget(ctx, dbsqlc.GetInteractionDestinationTargetParams{
			ProjectID: input.ProjectID, AgentID: input.AgentID, TargetID: input.IntegrationTargetID,
		})
		if err != nil {
			return createAgentContentInputTxResult{}, err
		}
		if target.IntegrationState != string(integrationstore.IntegrationStateActive) {
			return createAgentContentInputTxResult{}, storeerr.ErrUnauthorized
		}
	}
	actorID, err := resolveActorTx(
		ctx,
		qtx,
		input.ProjectID,
		input.Actor,
	)
	if err != nil {
		return createAgentContentInputTxResult{}, err
	}
	h, err := unit.Handle(input.ProjectID, input.AgentID)
	if err != nil {
		return createAgentContentInputTxResult{}, err
	}
	artifacts, err := artifactstore.InsertPreparedArtifactsTx(ctx,
		unit.DB(),
		input.ProjectID,
		input.AgentID,
		input.PreparedArtifacts)
	if err != nil {
		return createAgentContentInputTxResult{}, err
	}
	parts, err := executionContent(contentBlocks)
	if err != nil {
		return createAgentContentInputTxResult{}, err
	}
	received, err := h.ReceiveContent(ctx, agentexecution.ReceiveContentInput{
		ActorID: actorID, IntegrationTargetID: input.IntegrationTargetID,
		DeliveryMode: string(input.DeliveryMode), IdempotencyScope: input.IdempotencyScope,
		IdempotencyKey: input.IdempotencyKey, Metadata: input.Metadata, Content: parts,
	})
	if err != nil {
		return createAgentContentInputTxResult{}, err
	}
	row, err := qtx.GetAgentInput(
		ctx,
		dbsqlc.GetAgentInputParams{ProjectID: input.ProjectID, AgentID: input.AgentID, ID: received.ID},
	)
	if err != nil {
		return createAgentContentInputTxResult{}, err
	}
	result := createAgentContentInputTxResult{agentInput: agentInputRecordFromGetSQLC(row),
		contentBlocks: input.ContentBlocks, created: true, artifacts: artifacts}
	if input.CancelOpenInteractions {
		result.canceledInteractionIDs, err = h.SupersedeInteractions(ctx, received.ID)
	}
	return result, err
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
	PreparedArtifacts      []artifactstore.PreparedArtifact `json:"-"`
	ProjectID              uuid.UUID                        `json:"project_id,omitempty"`
	AgentID                uuid.UUID                        `json:"agent_id,omitempty"`
	Actor                  *ActorParams                     `json:"actor,omitempty"`
	IntegrationTargetID    uuid.UUID                        `json:"integration_target_id,omitempty"`
	Origin                 *AgentInputOrigin                `json:"origin,omitempty"`
	ContentBlocks          json.RawMessage                  `json:"content_blocks"`
	Metadata               json.RawMessage                  `json:"metadata,omitempty"`
	DeliveryMode           AgentInputDeliveryMode           `json:"delivery_mode,omitempty"`
	IdempotencyScope       string                           `json:"idempotency_scope,omitempty"`
	IdempotencyKey         string                           `json:"idempotency_key,omitempty"`
	CancelOpenInteractions bool                             `json:"cancel_open_interactions,omitempty"`
}
