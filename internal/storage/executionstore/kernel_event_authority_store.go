package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type TypedAgentEventRecord struct {
	Event               events.Event
	TurnID              ID
	IsOpeningEvent      bool
	AgentInputID        ID
	ModelOutputID       ID
	ToolCallResultID    ID
	ContextCheckpointID ID
}

type CreateContentBlockInput struct {
	ProjectID               ID
	AgentID                 ID
	OwnerKind               ContentBlockOwnerKind
	OwnerAgentInputID       ID
	OwnerModelOutputID      ID
	OwnerToolCallResultID   ID
	Ordinal                 int32
	BlockKind               ContentBlockKind
	TextContent             string
	StructuredData          json.RawMessage
	ArtifactID              ID
	ToolCallID              ID
	ExcludeFromModelContext bool
	Metadata                resourcemeta.Metadata
}

type ContentBlockRecord struct {
	ID                    ID
	ProjectID             ID
	AgentID               ID
	OwnerKind             ContentBlockOwnerKind
	OwnerAgentInputID     ID
	OwnerModelOutputID    ID
	OwnerToolCallResultID ID
	Ordinal               int32
	BlockKind             ContentBlockKind
	TextContent           string
	StructuredData        json.RawMessage
	ArtifactID            ID
	ToolCallID            ID
	CreatedAt             time.Time
}

type admittedToolCallResult struct {
	Result        ToolCallResultAuthorityRecord
	Event         TypedAgentEventRecord
	ContentBlocks json.RawMessage
	Inserted      bool
}

type AppendTypedAgentEventInput struct {
	ID                  ID
	ProjectID           ID
	AgentID             ID
	TurnID              ID
	IsOpeningEvent      bool
	Kind                events.Kind
	IdempotencyKey      string
	AgentInputID        ID
	ModelOutputID       ID
	ToolCallResultID    ID
	ContextCheckpointID ID
}

type sqlExecutor interface {
	dbsqlc.DBTX
}

func createContentBlockTx(
	ctx context.Context,
	db sqlExecutor,
	input CreateContentBlockInput,
) (ContentBlockRecord, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) ||
		input.OwnerKind == "" ||
		input.BlockKind == "" {
		return ContentBlockRecord{}, errors.New(
			"project, agent, owner kind, and block kind are required",
		)
	}
	if err := dbsafe.Text(input.TextContent); err != nil {
		return ContentBlockRecord{}, fmt.Errorf("content block text %w", err)
	}
	if len(input.StructuredData) > 0 {
		normalized, err := normalizeContentBlockJSONForStorage(input.StructuredData)
		if err != nil {
			return ContentBlockRecord{}, fmt.Errorf("content block structured data %w", err)
		}
		input.StructuredData = normalized
	}
	metadata, err := input.Metadata.JSON()
	if err != nil {
		return ContentBlockRecord{}, err
	}
	var textContent *string
	if input.BlockKind == ContentBlockKindText ||
		input.BlockKind == ContentBlockKindReasoning ||
		input.BlockKind == ContentBlockKindError ||
		input.TextContent != "" {
		textContent = &input.TextContent
	}
	row, err := dbsqlc.New(db).InsertContentBlock(ctx, dbsqlc.InsertContentBlockParams{
		ProjectID:               input.ProjectID,
		AgentID:                 input.AgentID,
		OwnerKind:               string(input.OwnerKind),
		OwnerAgentInputID:       sqlcIDFromNil(input.OwnerAgentInputID),
		OwnerModelOutputID:      sqlcIDFromNil(input.OwnerModelOutputID),
		OwnerToolCallResultID:   sqlcIDFromNil(input.OwnerToolCallResultID),
		Ordinal:                 input.Ordinal,
		BlockKind:               string(input.BlockKind),
		TextContent:             textContent,
		StructuredData:          sqlcRawMessageFromEmpty(input.StructuredData),
		ArtifactID:              sqlcIDFromNil(input.ArtifactID),
		ToolCallID:              sqlcIDFromNil(input.ToolCallID),
		ExcludeFromModelContext: input.ExcludeFromModelContext,
		Metadata:                metadata,
	})
	if err != nil {
		return ContentBlockRecord{}, fmt.Errorf("create content block: %w", err)
	}
	return contentBlockFromInsertSQLC(row), nil
}

func appendTypedAgentEventTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	input AppendTypedAgentEventInput,
) (TypedAgentEventRecord, error) {
	if isNilID(input.TurnID) {
		return TypedAgentEventRecord{}, errors.New("turn id is required")
	}
	if err := validateTypedEventPointers(input); err != nil {
		return TypedAgentEventRecord{}, err
	}
	qtx := dbsqlc.New(tx)
	allocation, err := qtx.AllocateEventSequence(ctx, dbsqlc.AllocateEventSequenceParams{ID: input.AgentID})
	if err != nil {
		return TypedAgentEventRecord{}, fmt.Errorf("allocate event sequence: %w", err)
	}
	if input.ProjectID != allocation.ProjectID {
		return TypedAgentEventRecord{}, storeerr.ErrNotFound
	}
	if input.IdempotencyKey != "" {
		existing, found, err := loadTypedEventByIdempotencyMaybeTx(
			ctx,
			tx,
			input.ProjectID,
			input.AgentID,
			input.IdempotencyKey,
		)
		if err != nil {
			return TypedAgentEventRecord{}, err
		}
		if found {
			if !sameTypedEventIntent(existing, input) {
				return TypedAgentEventRecord{}, storeerr.ErrIdempotencyConflict
			}
			return existing, err
		}
	}
	row, err := qtx.InsertTypedAgentEvent(ctx, dbsqlc.InsertTypedAgentEventParams{
		ID:                  sqlcIDFromNil(input.ID),
		ProjectID:           input.ProjectID,
		AgentID:             input.AgentID,
		TurnID:              input.TurnID,
		Sequence:            allocation.NextEventSequence,
		EventKind:           string(input.Kind),
		IdempotencyKey:      sqlcTextFromEmpty(input.IdempotencyKey),
		AgentInputID:        sqlcIDFromNil(input.AgentInputID),
		ModelOutputID:       sqlcIDFromNil(input.ModelOutputID),
		ToolCallResultID:    sqlcIDFromNil(input.ToolCallResultID),
		ContextCheckpointID: sqlcIDFromNil(input.ContextCheckpointID),
		IsOpeningEvent:      input.IsOpeningEvent,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return TypedAgentEventRecord{}, storeerr.ErrIdempotencyConflict
	}
	if err != nil {
		return TypedAgentEventRecord{}, fmt.Errorf("insert typed agent event: %w", err)
	}
	if err := qtx.AdvanceEventSequence(
		ctx,
		dbsqlc.AdvanceEventSequenceParams{ProjectID: input.ProjectID, ID: input.AgentID},
	); err != nil {
		return TypedAgentEventRecord{}, fmt.Errorf("advance event sequence: %w", err)
	}
	txNotifications.AddAgentEvent(input.AgentID)
	return typedAgentEventFromInsertSQLC(row)
}

func loadTypedEventByIdempotencyMaybeTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID ID,
	key string,
) (TypedAgentEventRecord, bool, error) {
	row, err := dbsqlc.New(tx).
		GetTypedAgentEventByIdempotency(ctx, dbsqlc.GetTypedAgentEventByIdempotencyParams{
			ProjectID:      projectID,
			AgentID:        agentID,
			IdempotencyKey: key,
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return TypedAgentEventRecord{}, false, nil
	}
	if err != nil {
		return TypedAgentEventRecord{}, false, fmt.Errorf(
			"load typed event by idempotency: %w",
			err,
		)
	}
	record, err := typedAgentEventFromIdempotencySQLC(row)
	return record, true, err
}

func loadTypedEventByModelOutputMaybeTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, modelOutputID ID,
) (TypedAgentEventRecord, bool, error) {
	row, err := dbsqlc.New(tx).GetTypedAgentEventByModelOutput(
		ctx,
		dbsqlc.GetTypedAgentEventByModelOutputParams{
			ProjectID:     projectID,
			AgentID:       agentID,
			ModelOutputID: sqlcIDFromNil(modelOutputID),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return TypedAgentEventRecord{}, false, nil
	}
	if err != nil {
		return TypedAgentEventRecord{}, false, fmt.Errorf("load typed event by model output: %w", err)
	}
	record, err := typedAgentEventFromModelOutputSQLC(row)
	return record, true, err
}

type CreateToolCallResultAuthorityInput struct {
	ProjectID          ID
	AgentID            ID
	TurnID             ID
	ToolCallID         ID
	Outcome            ToolResultOutcome
	ResultContentParts json.RawMessage
	IdempotencyKey     string
}

type ToolCallResultAuthorityRecord struct {
	ID          ID
	ProjectID   ID
	AgentID     ID
	TurnID      ID
	ToolCallID  ID
	Outcome     ToolResultOutcome
	CompletedAt time.Time
}

func createToolCallResultAuthorityTx(
	ctx context.Context,
	db sqlExecutor,
	input CreateToolCallResultAuthorityInput,
) (ToolCallResultAuthorityRecord, bool, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ToolCallID) {
		return ToolCallResultAuthorityRecord{}, false, errors.New(
			"project, agent, and tool call are required",
		)
	}
	if !input.Outcome.IsTerminal() {
		return ToolCallResultAuthorityRecord{}, false, errors.New(
			"terminal tool result outcome is required",
		)
	}
	row, err := dbsqlc.New(db).
		InsertToolCallResultAuthority(ctx, dbsqlc.InsertToolCallResultAuthorityParams{
			Outcome:    string(input.Outcome),
			ProjectID:  input.ProjectID,
			AgentID:    input.AgentID,
			ToolCallID: input.ToolCallID,
		})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, found, getErr := getToolCallResultAuthorityByToolCallTx(
			ctx,
			db,
			input.ProjectID,
			input.AgentID,
			input.ToolCallID,
		)
		if getErr != nil {
			return ToolCallResultAuthorityRecord{}, false, getErr
		}
		if !found {
			return ToolCallResultAuthorityRecord{}, false, storeerr.ErrStateTransitionConflict
		}
		if !sameToolCallResultIntent(existing, input) {
			return ToolCallResultAuthorityRecord{}, false, storeerr.ErrIdempotencyConflict
		}
		existingParts, partsErr := toolCallResultContentBlocksTx(
			ctx,
			dbsqlc.New(db),
			input.ProjectID,
			input.AgentID,
			existing.ID,
		)
		if partsErr != nil {
			return ToolCallResultAuthorityRecord{}, false, partsErr
		}
		if !sameJSON(existingParts, input.ResultContentParts) {
			return ToolCallResultAuthorityRecord{}, false, storeerr.ErrIdempotencyConflict
		}
		return existing, false, nil
	}
	if err != nil {
		return ToolCallResultAuthorityRecord{}, false, fmt.Errorf(
			"create tool call result authority: %w",
			err,
		)
	}
	return toolCallResultAuthorityFromInsertSQLC(row), true, nil
}

func admitToolCallResultTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	input CreateToolCallResultAuthorityInput,
) (admittedToolCallResult, error) {
	if len(input.ResultContentParts) == 0 {
		input.ResultContentParts = json.RawMessage(`[]`)
	}
	blocks, err := parseToolResultContentBlocks(input.ResultContentParts)
	if err != nil {
		return admittedToolCallResult{}, err
	}
	canonical, err := marshalToolResultContentBlocks(blocks)
	if err != nil {
		return admittedToolCallResult{}, err
	}
	input.ResultContentParts = canonical
	result, inserted, err := createToolCallResultAuthorityTx(ctx, tx, input)
	if err != nil {
		return admittedToolCallResult{}, err
	}
	if inserted {
		for _, block := range blocks {
			block.ProjectID = input.ProjectID
			block.AgentID = input.AgentID
			block.OwnerKind = ContentBlockOwnerToolCallResult
			block.OwnerToolCallResultID = result.ID
			if _, err := createContentBlockTx(ctx, tx, block); err != nil {
				return admittedToolCallResult{}, err
			}
		}
	}
	event, err := appendTypedAgentEventTx(
		ctx,
		txNotifications,
		tx,
		AppendTypedAgentEventInput{
			ProjectID:        input.ProjectID,
			AgentID:          input.AgentID,
			TurnID:           input.TurnID,
			Kind:             events.KindToolResult,
			IdempotencyKey:   input.IdempotencyKey,
			ToolCallResultID: result.ID,
		},
	)
	if err != nil {
		return admittedToolCallResult{}, err
	}
	return admittedToolCallResult{
		Result:        result,
		Event:         event,
		ContentBlocks: canonical,
		Inserted:      inserted,
	}, nil
}

func toolCallResultContentBlocksTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, resultID ID,
) (json.RawMessage, error) {
	rows, err := qtx.ListToolCallResultContentBlocks(
		ctx,
		dbsqlc.ListToolCallResultContentBlocksParams{
			ProjectID:        projectID,
			AgentID:          agentID,
			ToolCallResultID: sqlcIDFromNil(resultID),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list tool result content blocks: %w", err)
	}
	parts := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		decoded, err := resourcemeta.FromJSON(row.Metadata)
		if err != nil {
			return nil, fmt.Errorf("stored tool result content block metadata: %w", err)
		}
		metadata := contentBlockMetadataForOutput(decoded)
		var part map[string]any
		switch ContentBlockKind(row.BlockKind) {
		case ContentBlockKindText:
			part = map[string]any{"type": "text", "text": row.TextContent}
		case ContentBlockKindStructuredData:
			if row.StructuredData == nil {
				return nil, errors.New("stored structured_data block has no value")
			}
			part = map[string]any{
				"type":  "structured_data",
				"value": *row.StructuredData,
			}
		case ContentBlockKindArtifact:
			if row.ArtifactID == nil {
				return nil, errors.New("stored artifact block has no artifact")
			}
			part = map[string]any{
				"type":        "media_ref",
				"artifact_id": row.ArtifactID.String(),
			}
			if row.ExcludeFromModelContext {
				part["exclude_from_model_context"] = true
			}
		default:
			return nil, fmt.Errorf("stored tool result has unsupported block kind %q", row.BlockKind)
		}
		if metadata != nil {
			part["metadata"] = metadata
		}
		parts = append(parts, part)
	}
	body, err := marshalJSON(parts)
	if err != nil {
		return nil, fmt.Errorf("marshal stored tool result content parts: %w", err)
	}
	return normalizedJSON(body), nil
}

func getToolCallResultAuthorityByToolCallTx(
	ctx context.Context,
	db sqlExecutor,
	projectID, agentID, toolCallID ID,
) (ToolCallResultAuthorityRecord, bool, error) {
	row, err := dbsqlc.New(db).
		GetToolCallResultByToolCall(ctx, dbsqlc.GetToolCallResultByToolCallParams{
			ProjectID:  projectID,
			AgentID:    agentID,
			ToolCallID: toolCallID,
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolCallResultAuthorityRecord{}, false, nil
	}
	if err != nil {
		return ToolCallResultAuthorityRecord{}, false, fmt.Errorf(
			"get tool call result authority: %w",
			err,
		)
	}
	return toolCallResultAuthorityFromGetSQLC(row), true, nil
}

func validateModelOutputAuthorityInput(input CreateModelOutputAuthorityInput) error {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ModelCallContextID) ||
		input.StopReason == "" {
		return errors.New(
			"project, agent, model context, and stop reason are required",
		)
	}
	if !modelenvelope.IsDurableModelOutputStopReason(input.StopReason) {
		return fmt.Errorf("unsupported model output stop reason %q", input.StopReason)
	}
	return nil
}

func sameTypedEventIntent(existing TypedAgentEventRecord, input AppendTypedAgentEventInput) bool {
	return existing.Event.Kind == input.Kind &&
		existing.Event.IdempotencyKey == input.IdempotencyKey &&
		existing.TurnID == input.TurnID &&
		existing.IsOpeningEvent == input.IsOpeningEvent &&
		existing.AgentInputID == input.AgentInputID &&
		existing.ModelOutputID == input.ModelOutputID &&
		existing.ToolCallResultID == input.ToolCallResultID &&
		existing.ContextCheckpointID == input.ContextCheckpointID
}

func sameToolCallResultIntent(
	existing ToolCallResultAuthorityRecord,
	input CreateToolCallResultAuthorityInput,
) bool {
	return existing.ToolCallID == input.ToolCallID &&
		existing.Outcome == input.Outcome
}

func validateTypedEventPointers(input AppendTypedAgentEventInput) error {
	count := 0
	if !isNilID(input.AgentInputID) {
		count++
	}
	if !isNilID(input.ModelOutputID) {
		count++
	}
	if !isNilID(input.ToolCallResultID) {
		count++
	}
	if !isNilID(input.ContextCheckpointID) {
		count++
	}
	if count != 1 {
		return errors.New("typed agent event requires exactly one typed pointer")
	}
	switch input.Kind {
	case events.KindAgentInput:
		if isNilID(input.AgentInputID) {
			return errors.New("agent_input event requires agent_input_id")
		}
	case events.KindModelOutput:
		if isNilID(input.ModelOutputID) {
			return errors.New("model_output event requires model_output_id")
		}
	case events.KindToolResult:
		if isNilID(input.ToolCallResultID) {
			return errors.New("tool_result event requires tool_call_result_id")
		}
	case events.KindContextCheckpoint:
		if isNilID(input.ContextCheckpointID) {
			return errors.New("context_checkpoint event requires context_checkpoint_id")
		}
	default:
		return fmt.Errorf("invalid typed frontier event kind: %s", input.Kind)
	}
	return nil
}

func sameModelOutputAuthorityIntent(
	existing ModelOutputAuthorityRecord,
	input CreateModelOutputAuthorityInput,
) bool {
	return existing.ModelCallContextID == input.ModelCallContextID &&
		existing.ServedProviderModelSlug == input.ServedProviderModelSlug &&
		existing.StopReason == input.StopReason &&
		((len(existing.ProviderReplay) == 0 && len(input.ProviderReplay) == 0) ||
			sameJSON(existing.ProviderReplay, input.ProviderReplay)) &&
		existing.Usage == modelUsageForStorage(input.Usage)
}

func modelOutputAuthorityFromSQLC(row dbsqlc.InsertModelOutputAuthorityRow) ModelOutputAuthorityRecord {
	return ModelOutputAuthorityRecord{
		ID:                      row.ID,
		ProjectID:               row.ProjectID,
		AgentID:                 row.AgentID,
		TurnID:                  row.TurnID,
		ModelCallContextID:      row.ModelCallContextID,
		ServedProviderModelSlug: row.ServedProviderModelSlug,
		StopReason:              modelenvelope.StopReason(row.StopReason),
		ProviderResponseID:      row.ProviderResponseID,
		ProviderReplay:          rawMessageFromSQLCPtr(row.ProviderReplay),
		Usage: modelUsageFromSQLC(
			row.InputTokensTotal,
			row.UncachedInputTokens,
			row.CacheReadInputTokens,
			row.CacheWriteInputTokens,
			row.OutputTokensTotal,
			row.ReasoningOutputTokens,
		),
		CreatedAt: row.CreatedAt,
	}
}

func modelOutputAuthorityFromGetSQLC(row dbsqlc.GetModelOutputByModelContextRow) ModelOutputAuthorityRecord {
	return ModelOutputAuthorityRecord{
		ID:                      row.ID,
		ProjectID:               row.ProjectID,
		AgentID:                 row.AgentID,
		TurnID:                  row.TurnID,
		ModelCallContextID:      row.ModelCallContextID,
		ServedProviderModelSlug: row.ServedProviderModelSlug,
		StopReason:              modelenvelope.StopReason(row.StopReason),
		ProviderResponseID:      row.ProviderResponseID,
		ProviderReplay:          rawMessageFromSQLCPtr(row.ProviderReplay),
		Usage: modelUsageFromSQLC(
			row.InputTokensTotal,
			row.UncachedInputTokens,
			row.CacheReadInputTokens,
			row.CacheWriteInputTokens,
			row.OutputTokensTotal,
			row.ReasoningOutputTokens,
		),
		CreatedAt: row.CreatedAt,
	}
}

func toolCallResultAuthorityFromInsertSQLC(row dbsqlc.InsertToolCallResultAuthorityRow) ToolCallResultAuthorityRecord {
	return ToolCallResultAuthorityRecord{
		ID:          row.ID,
		ProjectID:   row.ProjectID,
		AgentID:     row.AgentID,
		TurnID:      row.TurnID,
		ToolCallID:  row.ToolCallID,
		Outcome:     ToolResultOutcome(row.Outcome),
		CompletedAt: row.CompletedAt,
	}
}

func toolCallResultAuthorityFromGetSQLC(row dbsqlc.GetToolCallResultByToolCallRow) ToolCallResultAuthorityRecord {
	return ToolCallResultAuthorityRecord{
		ID:          row.ID,
		ProjectID:   row.ProjectID,
		AgentID:     row.AgentID,
		TurnID:      row.TurnID,
		ToolCallID:  row.ToolCallID,
		Outcome:     ToolResultOutcome(row.Outcome),
		CompletedAt: row.CompletedAt,
	}
}

func contentBlockFromInsertSQLC(row dbsqlc.InsertContentBlockRow) ContentBlockRecord {
	return ContentBlockRecord{
		ID:                    row.ID,
		ProjectID:             row.ProjectID,
		AgentID:               row.AgentID,
		OwnerKind:             ContentBlockOwnerKind(row.OwnerKind),
		OwnerAgentInputID:     idFromSQLCPtr(row.OwnerAgentInputID),
		OwnerModelOutputID:    idFromSQLCPtr(row.OwnerModelOutputID),
		OwnerToolCallResultID: idFromSQLCPtr(row.OwnerToolCallResultID),
		Ordinal:               row.Ordinal,
		BlockKind:             ContentBlockKind(row.BlockKind),
		TextContent:           row.TextContent,
		StructuredData:        rawMessageFromSQLCPtr(row.StructuredData),
		ArtifactID:            idFromSQLCPtr(row.ArtifactID),
		ToolCallID:            idFromSQLCPtr(row.ToolCallID),
		CreatedAt:             row.CreatedAt,
	}
}

func typedAgentEventFromInsertSQLC(
	row dbsqlc.InsertTypedAgentEventRow,
) (TypedAgentEventRecord, error) {
	event, err := events.New(
		events.NewInput{
			ID:             row.ID,
			AgentID:        row.AgentID,
			Sequence:       row.Sequence,
			Kind:           events.Kind(row.EventKind),
			At:             row.CreatedAt,
			IdempotencyKey: row.IdempotencyKey,
		},
	)
	if err != nil {
		return TypedAgentEventRecord{}, err
	}
	return TypedAgentEventRecord{
		Event:               event,
		TurnID:              row.TurnID,
		IsOpeningEvent:      row.IsOpeningEvent,
		AgentInputID:        idFromSQLCPtr(row.AgentInputID),
		ModelOutputID:       idFromSQLCPtr(row.ModelOutputID),
		ToolCallResultID:    idFromSQLCPtr(row.ToolCallResultID),
		ContextCheckpointID: idFromSQLCPtr(row.ContextCheckpointID),
	}, nil
}

func typedAgentEventFromIdempotencySQLC(
	row dbsqlc.GetTypedAgentEventByIdempotencyRow,
) (TypedAgentEventRecord, error) {
	event, err := events.New(
		events.NewInput{
			ID:             row.ID,
			AgentID:        row.AgentID,
			Sequence:       row.Sequence,
			Kind:           events.Kind(row.EventKind),
			At:             row.CreatedAt,
			IdempotencyKey: row.IdempotencyKey,
		},
	)
	if err != nil {
		return TypedAgentEventRecord{}, err
	}
	return TypedAgentEventRecord{
		Event:               event,
		TurnID:              row.TurnID,
		IsOpeningEvent:      row.IsOpeningEvent,
		AgentInputID:        idFromSQLCPtr(row.AgentInputID),
		ModelOutputID:       idFromSQLCPtr(row.ModelOutputID),
		ToolCallResultID:    idFromSQLCPtr(row.ToolCallResultID),
		ContextCheckpointID: idFromSQLCPtr(row.ContextCheckpointID),
	}, nil
}

func typedAgentEventFromModelOutputSQLC(
	row dbsqlc.GetTypedAgentEventByModelOutputRow,
) (TypedAgentEventRecord, error) {
	event, err := events.New(
		events.NewInput{
			ID:             row.ID,
			AgentID:        row.AgentID,
			Sequence:       row.Sequence,
			Kind:           events.Kind(row.EventKind),
			At:             row.CreatedAt,
			IdempotencyKey: row.IdempotencyKey,
		},
	)
	if err != nil {
		return TypedAgentEventRecord{}, err
	}
	return TypedAgentEventRecord{
		Event:               event,
		TurnID:              row.TurnID,
		IsOpeningEvent:      row.IsOpeningEvent,
		AgentInputID:        idFromSQLCPtr(row.AgentInputID),
		ModelOutputID:       idFromSQLCPtr(row.ModelOutputID),
		ToolCallResultID:    idFromSQLCPtr(row.ToolCallResultID),
		ContextCheckpointID: idFromSQLCPtr(row.ContextCheckpointID),
	}, nil
}
