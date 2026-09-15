package executionstore

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type toolCallContentBlockArgs struct {
	ToolCallID uuid.UUID
}

func createModelOutputContentBlocksTx(
	ctx context.Context,
	tx pgx.Tx,
	contextRow ModelCallContextRecord,
	envelope modelenvelope.ResponseEnvelope,
	modelOutputID uuid.UUID,
	toolCalls map[string]toolCallContentBlockArgs,
) (map[string]uuid.UUID, error) {
	contentBlockByProviderCallID := make(map[string]uuid.UUID, len(toolCalls))
	if len(envelope.Normalized.Content) == 0 {
		return contentBlockByProviderCallID, nil
	}
	for index, part := range envelope.Normalized.Content {
		ordinal := int32(index)
		switch part.Type {
		case modelenvelope.ResponsePartTypeText,
			modelenvelope.ResponsePartTypeReasoning,
			modelenvelope.ResponsePartTypeError:
			if part.Text == "" {
				continue
			}
			blockKind := ContentBlockKindText
			if part.Type == modelenvelope.ResponsePartTypeReasoning {
				blockKind = ContentBlockKindReasoning
			} else if part.Type == modelenvelope.ResponsePartTypeError {
				blockKind = ContentBlockKindError
			}
			if _, err := createContentBlockTx(ctx, tx, CreateContentBlockInput{
				ProjectID:          contextRow.ProjectID,
				AgentID:            contextRow.AgentID,
				OwnerKind:          ContentBlockOwnerModelOutput,
				OwnerModelOutputID: modelOutputID,
				Ordinal:            ordinal,
				BlockKind:          blockKind,
				TextContent:        part.Text,
			}); err != nil {
				return nil, err
			}
		case modelenvelope.ResponsePartTypeToolCall:
			args, ok := toolCalls[part.ProviderCallID]
			if !ok || args.ToolCallID == uuid.Nil {
				return nil, fmt.Errorf(
					"model output content tool_call part %d (provider_call_id=%q) has no recorded tool call",
					index,
					part.ProviderCallID,
				)
			}
			block, err := createContentBlockTx(ctx, tx, CreateContentBlockInput{
				ProjectID:          contextRow.ProjectID,
				AgentID:            contextRow.AgentID,
				OwnerKind:          ContentBlockOwnerModelOutput,
				OwnerModelOutputID: modelOutputID,
				Ordinal:            ordinal,
				BlockKind:          ContentBlockKindToolCall,
				ToolCallID:         args.ToolCallID,
			})
			if err != nil {
				return nil, err
			}
			contentBlockByProviderCallID[part.ProviderCallID] = block.ID
		default:
			return nil, fmt.Errorf("model output content part %d has unsupported type %q", index, part.Type)
		}
	}
	return contentBlockByProviderCallID, nil
}

func modelOutputHasContentBlocksTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, modelOutputID uuid.UUID,
) (bool, error) {
	rows, err := dbsqlc.New(tx).
		ListContentBlocksForModelOutput(ctx, dbsqlc.ListContentBlocksForModelOutputParams{
			ProjectID:     projectID,
			AgentID:       agentID,
			ModelOutputID: storeutil.IDFromNil(modelOutputID),
		})
	if err != nil {
		return false, fmt.Errorf("list model output content blocks: %w", err)
	}
	return len(rows) > 0, nil
}

func validateModelOutputContentReplayTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, modelOutputID uuid.UUID,
	envelope modelenvelope.ResponseEnvelope,
) error {
	rows, err := dbsqlc.New(tx).
		ListContentBlocksForModelOutput(ctx, dbsqlc.ListContentBlocksForModelOutputParams{
			ProjectID:     projectID,
			AgentID:       agentID,
			ModelOutputID: storeutil.IDFromNil(modelOutputID),
		})
	if err != nil {
		return fmt.Errorf("list model output content blocks for replay: %w", err)
	}
	expected, err := modelOutputReplayParts(envelope)
	if err != nil {
		return err
	}
	if len(rows) != len(expected) {
		return storeerr.ErrIdempotencyConflict
	}
	for index, part := range expected {
		row := rows[index]
		providerCallID := ""
		if part.Kind == ContentBlockKindToolCall {
			providerCallID, err = dbsqlc.New(tx).GetToolCallProviderCallID(
				ctx,
				dbsqlc.GetToolCallProviderCallIDParams{
					ProjectID: projectID,
					AgentID:   agentID,
					ID:        storeutil.IDFromPtr(row.ToolCallID),
				},
			)
			if err != nil {
				return fmt.Errorf("load replayed tool call provider id: %w", err)
			}
		}
		if row.Ordinal != part.Ordinal || ContentBlockKind(row.BlockKind) != part.Kind ||
			row.TextContent != part.Text || storeutil.IDFromPtr(row.ArtifactID) != uuid.Nil ||
			providerCallID != part.ProviderCallID {
			return storeerr.ErrIdempotencyConflict
		}
	}
	return nil
}

type modelOutputReplayPart struct {
	Ordinal        int32
	Kind           ContentBlockKind
	Text           string
	ProviderCallID string
}

func modelOutputReplayParts(envelope modelenvelope.ResponseEnvelope) ([]modelOutputReplayPart, error) {
	parts := make([]modelOutputReplayPart, 0, len(envelope.Normalized.Content))
	for index, part := range envelope.Normalized.Content {
		replayPart := modelOutputReplayPart{
			Ordinal: int32(index),
		}
		switch part.Type {
		case modelenvelope.ResponsePartTypeText,
			modelenvelope.ResponsePartTypeReasoning,
			modelenvelope.ResponsePartTypeError:
			if part.Text == "" {
				continue
			}
			replayPart.Kind = ContentBlockKindText
			if part.Type == modelenvelope.ResponsePartTypeReasoning {
				replayPart.Kind = ContentBlockKindReasoning
			} else if part.Type == modelenvelope.ResponsePartTypeError {
				replayPart.Kind = ContentBlockKindError
			}
			replayPart.Text = part.Text
		case modelenvelope.ResponsePartTypeToolCall:
			replayPart.Kind = ContentBlockKindToolCall
			replayPart.ProviderCallID = part.ProviderCallID
		default:
			return nil, fmt.Errorf("model output content part %d has unsupported type %q", index, part.Type)
		}
		parts = append(parts, replayPart)
	}
	return parts, nil
}

func modelOutputAuthorityInputFromContext(
	input RecordModelOutputAndCompleteContextInput,
) CreateModelOutputAuthorityInput {
	servedProviderModelSlug := ""
	envelope := input.ProviderResponse
	if envelope.ServedProviderModelSlug != "" {
		servedProviderModelSlug = envelope.ServedProviderModelSlug
	}
	return CreateModelOutputAuthorityInput{
		ProjectID:               input.ProjectID,
		AgentID:                 input.AgentID,
		ModelCallContextID:      input.ModelCallContextID,
		ServedProviderModelSlug: servedProviderModelSlug,
		StopReason:              envelope.Normalized.StopReason,
		ProviderReplay:          envelope.ProviderReplay,
		Usage:                   envelope.Normalized.Usage,
	}
}

func modelOutputAuthorityInputFromToolSourceContext(
	input RecordToolCallSourceAndCompleteContextInput,
) CreateModelOutputAuthorityInput {
	return modelOutputAuthorityInputFromContext(
		RecordModelOutputAndCompleteContextInput{
			ProjectID:          input.ProjectID,
			AgentID:            input.AgentID,
			RuntimeLockID:      input.RuntimeLockID,
			ModelCallContextID: input.ModelCallContextID,
			ProviderResponse:   input.ProviderResponse,
		},
	)
}
