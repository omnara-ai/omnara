package publicevents

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func publicID(kind publicid.Kind, id uuid.UUID) (string, error) {
	encoded, err := publicid.Encode(kind, id)
	if err != nil {
		return "", fmt.Errorf("public id encoding failed for %s %s: %w", kind, id, err)
	}
	return encoded, nil
}

func EventFromReadRecord(record executionstore.AgentEventReadRecord) (openapi.AgentEvent, error) {
	id, err := publicID(publicid.KindAgentEvent, record.ID)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	agentID, err := publicID(publicid.KindAgent, record.AgentID)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	turnID, err := publicID(publicid.KindAgentTurn, record.TurnID)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	switch record.EventKind {
	case "agent_input":
		return publicAgentInputEvent(record, id, orgID, projectID, agentID, turnID)
	case "model_output":
		return publicModelOutputEvent(record, id, orgID, projectID, agentID, turnID)
	case "tool_result":
		return publicToolResultEvent(record, id, orgID, projectID, agentID, turnID)
	case "context_checkpoint":
		return publicContextCheckpointEvent(record, id, orgID, projectID, agentID, turnID)
	default:
		return openapi.AgentEvent{}, fmt.Errorf("unsupported agent event kind %q", record.EventKind)
	}
}

func publicContextCheckpointEvent(
	record executionstore.AgentEventReadRecord,
	id, orgID, projectID, agentID, turnID string,
) (openapi.AgentEvent, error) {
	if record.IsOpeningEvent {
		return openapi.AgentEvent{}, errors.New("context checkpoint cannot open a turn")
	}
	checkpointID, err := publicID(
		publicid.KindContextCheckpoint,
		record.ContextCheckpointID,
	)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	event := openapi.ContextCheckpointEvent{
		Id:                             id,
		OrgId:                          orgID,
		ProjectId:                      projectID,
		AgentId:                        agentID,
		TurnId:                         turnID,
		TurnSequence:                   record.TurnSequence,
		IsOpeningEvent:                 openapi.ContextCheckpointEventIsOpeningEventFalse,
		Sequence:                       record.Sequence,
		ContextCheckpointId:            checkpointID,
		SummarizedThroughEventSequence: record.SummarizedThroughEventSequence,
		Summary:                        record.CheckpointSummary,
		CreatedAt:                      record.CreatedAt,
	}
	var response openapi.AgentEvent
	if err := response.FromContextCheckpointEvent(event); err != nil {
		return openapi.AgentEvent{}, err
	}
	return response, nil
}

func publicAgentInputEvent(
	record executionstore.AgentEventReadRecord,
	id, orgID, projectID, agentID, turnID string,
) (openapi.AgentEvent, error) {
	agentInputID, err := publicID(publicid.KindAgentInput, record.AgentInputID)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	inputKind := openapi.AgentInputKind(record.InputKind)
	if !inputKind.Valid() {
		return openapi.AgentEvent{}, fmt.Errorf("invalid agent input kind %q", record.InputKind)
	}
	blocks, err := AgentInputContentBlocks(record.ContentBlocks)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	event := openapi.AgentInputEvent{
		Id:             id,
		OrgId:          orgID,
		ProjectId:      projectID,
		AgentId:        agentID,
		TurnId:         turnID,
		TurnSequence:   record.TurnSequence,
		IsOpeningEvent: record.IsOpeningEvent,
		Sequence:       record.Sequence,
		AgentInputId:   agentInputID,
		InputKind:      inputKind,
		ContentBlocks:  blocks,
		CreatedAt:      record.CreatedAt,
	}
	if record.ActorID != uuid.Nil {
		actorID, err := publicID(publicid.KindActor, record.ActorID)
		if err != nil {
			return openapi.AgentEvent{}, err
		}
		event.ActorId = &actorID
	}
	if inputKind == openapi.AgentInputKindContent && record.InputIdempotencyKey != "" {
		event.InputIdempotencyKey = &record.InputIdempotencyKey
	}
	if record.ControlType != "" {
		controlType := openapi.AgentControlType(record.ControlType)
		if !controlType.Valid() {
			return openapi.AgentEvent{}, fmt.Errorf("invalid agent control type %q", record.ControlType)
		}
		event.ControlType = &controlType
	}
	if record.TargetInteractionID != uuid.Nil {
		interactionID, err := publicID(publicid.KindAgentInteraction, record.TargetInteractionID)
		if err != nil {
			return openapi.AgentEvent{}, err
		}
		event.InteractionId = &interactionID
	}
	if record.AgentConfigID != uuid.Nil {
		agentConfigID, err := publicID(publicid.KindAgentConfig, record.AgentConfigID)
		if err != nil {
			return openapi.AgentEvent{}, err
		}
		event.AgentConfigId = &agentConfigID
	}
	var response openapi.AgentEvent
	if err := response.FromAgentInputEvent(event); err != nil {
		return openapi.AgentEvent{}, err
	}
	return response, nil
}

func publicModelOutputEvent(
	record executionstore.AgentEventReadRecord,
	id, orgID, projectID, agentID, turnID string,
) (openapi.AgentEvent, error) {
	if record.IsOpeningEvent {
		return openapi.AgentEvent{}, errors.New("model output cannot open a turn")
	}
	modelCallContextID, err := publicID(
		publicid.KindModelCallContext,
		record.ModelCallContextID,
	)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	stopReason := openapi.ModelOutputStopReason(record.ModelStopReason)
	if !stopReason.Valid() {
		return openapi.AgentEvent{}, fmt.Errorf(
			"invalid model stop reason %q",
			record.ModelStopReason,
		)
	}
	blocks, err := ModelOutputContentBlocks(record.ContentBlocks)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	event := openapi.ModelOutputEvent{
		Id:                 id,
		OrgId:              orgID,
		ProjectId:          projectID,
		AgentId:            agentID,
		TurnId:             turnID,
		TurnSequence:       record.TurnSequence,
		IsOpeningEvent:     openapi.ModelOutputEventIsOpeningEventFalse,
		Sequence:           record.Sequence,
		ModelCallContextId: modelCallContextID,
		StopReason:         stopReason,
		ContentBlocks:      blocks,
		Usage:              ModelUsage(record.ModelUsage),
		CreatedAt:          record.CreatedAt,
	}
	if record.ProviderMetadata != (modelenvelope.ProviderMetadata{}) {
		providerMetadata, err := json.Marshal(record.ProviderMetadata)
		if err != nil {
			return openapi.AgentEvent{}, err
		}
		event.ProviderMetadata = providerMetadata
	}
	var response openapi.AgentEvent
	if err := response.FromModelOutputEvent(event); err != nil {
		return openapi.AgentEvent{}, err
	}
	return response, nil
}

func publicToolResultEvent(
	record executionstore.AgentEventReadRecord,
	id, orgID, projectID, agentID, turnID string,
) (openapi.AgentEvent, error) {
	if record.IsOpeningEvent {
		return openapi.AgentEvent{}, errors.New("tool result cannot open a turn")
	}
	toolCallID, err := publicID(publicid.KindToolCall, record.ToolCallID)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	outcome, err := ToolCallOutcome(record.ToolOutcome)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	blocks, err := ToolResultContentBlocks(record.ContentBlocks)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	event := openapi.ToolResultEvent{
		Id:             id,
		OrgId:          orgID,
		ProjectId:      projectID,
		AgentId:        agentID,
		TurnId:         turnID,
		TurnSequence:   record.TurnSequence,
		IsOpeningEvent: openapi.ToolResultEventIsOpeningEventFalse,
		Sequence:       record.Sequence,
		ToolCallId:     toolCallID,
		Outcome:        outcome,
		ContentBlocks:  blocks,
		CreatedAt:      record.CreatedAt,
	}
	var response openapi.AgentEvent
	if err := response.FromToolResultEvent(event); err != nil {
		return openapi.AgentEvent{}, err
	}
	return response, nil
}

func ToolCallOutcome(
	outcome executionstore.ToolResultOutcome,
) (openapi.ToolCallOutcome, error) {
	value := openapi.ToolCallOutcome(outcome)
	if !value.Valid() {
		return "", fmt.Errorf("invalid terminal tool outcome %q", outcome)
	}
	return value, nil
}

func TurnFromReadRecord(record executionstore.AgentTurnReadRecord) (openapi.AgentTurn, error) {
	id, err := publicID(publicid.KindAgentTurn, record.ID)
	if err != nil {
		return openapi.AgentTurn{}, err
	}
	agentID, err := publicID(publicid.KindAgent, record.AgentID)
	if err != nil {
		return openapi.AgentTurn{}, err
	}
	openingEvents, err := EventsFromReadRecords(record.OpeningEvents)
	if err != nil {
		return openapi.AgentTurn{}, err
	}
	response := openapi.AgentTurn{
		Id:            id,
		AgentId:       agentID,
		TurnSequence:  record.TurnSequence,
		EventCount:    record.EventCount,
		OpeningEvents: openingEvents,
		StartedAt:     record.StartedAt,
		UpdatedAt:     record.UpdatedAt,
	}
	if record.LatestEvent.ID != uuid.Nil {
		latest, err := EventFromReadRecord(record.LatestEvent)
		if err != nil {
			return openapi.AgentTurn{}, err
		}
		response.LatestEvent = &latest
	}
	if record.LatestSemanticEvent.ID != uuid.Nil {
		latestSemantic, err := EventFromReadRecord(record.LatestSemanticEvent)
		if err != nil {
			return openapi.AgentTurn{}, err
		}
		response.LatestSemanticEvent = &latestSemantic
	}
	return response, nil
}

func TurnsFromReadRecords(records []executionstore.AgentTurnReadRecord) ([]openapi.AgentTurn, error) {
	out := make([]openapi.AgentTurn, 0, len(records))
	for _, record := range records {
		response, err := TurnFromReadRecord(record)
		if err != nil {
			return nil, err
		}
		out = append(out, response)
	}
	return out, nil
}

func EventsFromReadRecords(records []executionstore.AgentEventReadRecord) ([]openapi.AgentEvent, error) {
	out := make([]openapi.AgentEvent, 0, len(records))
	for _, record := range records {
		response, err := EventFromReadRecord(record)
		if err != nil {
			return nil, err
		}
		out = append(out, response)
	}
	return out, nil
}

func ModelUsage(usage modelenvelope.Usage) *openapi.ModelUsage {
	if usage == (modelenvelope.Usage{}) {
		return nil
	}
	return &openapi.ModelUsage{
		InputTokensTotal:      &usage.InputTokens,
		UncachedInputTokens:   &usage.UncachedInputTokens,
		CacheReadInputTokens:  modelenvelope.OptionalCount(usage.CacheReadTokens),
		CacheWriteInputTokens: modelenvelope.OptionalCount(usage.CacheWriteTokens),
		OutputTokensTotal:     &usage.OutputTokens,
		ReasoningOutputTokens: modelenvelope.OptionalCount(usage.ReasoningTokens),
	}
}

func SequenceBoundary(value *int64, name string) (int64, error) {
	if value == nil {
		return 0, nil
	}
	if *value < 0 {
		return 0, errors.New(name + " must be a non-negative integer")
	}
	return *value, nil
}

func TimelineLimit(value *int32, defaultValue, maxValue int32) (int32, error) {
	if value == nil {
		return defaultValue, nil
	}
	if *value < 1 || *value > maxValue {
		return 0, fmt.Errorf("limit must be an integer between 1 and %d", maxValue)
	}
	return *value, nil
}

func TrimEventsBeforePage(
	events []executionstore.AgentEventReadRecord,
	limit int32,
) ([]executionstore.AgentEventReadRecord, *int64) {
	if len(events) <= int(limit) {
		return events, nil
	}
	nextBeforeSequence := events[1].Sequence
	return events[1:], &nextBeforeSequence
}

type storedContentBlockType struct {
	Type string `json:"type"`
}

type storedTextContentBlock struct {
	Type     string                `json:"type"`
	Text     *string               `json:"text"`
	Metadata resourcemeta.Metadata `json:"metadata,omitempty"`
}

type storedMediaRefContentBlock struct {
	Type                    string                `json:"type"`
	ArtifactID              string                `json:"artifact_id"`
	ExcludeFromModelContext *bool                 `json:"exclude_from_model_context,omitempty"`
	Metadata                resourcemeta.Metadata `json:"metadata,omitempty"`
}

type storedToolCallContentBlock struct {
	Type       string                `json:"type"`
	ToolCallID string                `json:"tool_call_id"`
	ToolType   string                `json:"tool_type"`
	Name       string                `json:"name"`
	Input      json.RawMessage       `json:"input"`
	Metadata   resourcemeta.Metadata `json:"metadata,omitempty"`
}

type storedStructuredDataContentBlock struct {
	Type     string                `json:"type"`
	Value    json.RawMessage       `json:"value"`
	Metadata resourcemeta.Metadata `json:"metadata,omitempty"`
}

func decodeStoredContentBlocks(raw json.RawMessage) ([]json.RawMessage, error) {
	var blocks []json.RawMessage
	if len(raw) == 0 {
		return nil, errors.New("content blocks are required")
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("decode content blocks: %w", err)
	}
	if blocks == nil {
		return nil, errors.New("content blocks must be an array")
	}
	return blocks, nil
}

func storedContentBlockTypeFor(
	raw json.RawMessage,
	index int,
) (string, error) {
	var discriminator storedContentBlockType
	if err := json.Unmarshal(raw, &discriminator); err != nil {
		return "", fmt.Errorf("decode content block %d type: %w", index, err)
	}
	if discriminator.Type == "" {
		return "", fmt.Errorf("content block %d is missing type", index)
	}
	return discriminator.Type, nil
}

func decodeStoredContentBlock(
	raw json.RawMessage,
	index int,
	out any,
) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode content block %d: %w", index, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return fmt.Errorf("decode content block %d: %w", index, err)
	}
	return nil
}

func AgentInputContentBlocks(
	raw json.RawMessage,
) ([]openapi.AgentInputContentBlock, error) {
	blocks, err := decodeStoredContentBlocks(raw)
	if err != nil {
		return nil, err
	}
	out := make([]openapi.AgentInputContentBlock, 0, len(blocks))
	for index, block := range blocks {
		blockType, err := storedContentBlockTypeFor(block, index)
		if err != nil {
			return nil, err
		}
		var public openapi.AgentInputContentBlock
		switch blockType {
		case "text":
			text, err := publicTextContentBlock(block, index)
			if err != nil {
				return nil, err
			}
			if err := public.FromTextContentBlock(text); err != nil {
				return nil, err
			}
		case "media_ref":
			media, err := publicMediaRefContentBlock(block, index)
			if err != nil {
				return nil, err
			}
			if err := public.FromMediaRefContentBlock(media); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf(
				"agent input content block %d has unsupported type %q",
				index,
				blockType,
			)
		}
		out = append(out, public)
	}
	return out, nil
}

func ModelOutputContentBlocks(
	raw json.RawMessage,
) ([]openapi.ModelOutputContentBlock, error) {
	blocks, err := decodeStoredContentBlocks(raw)
	if err != nil {
		return nil, err
	}
	out := make([]openapi.ModelOutputContentBlock, 0, len(blocks))
	for index, block := range blocks {
		blockType, err := storedContentBlockTypeFor(block, index)
		if err != nil {
			return nil, err
		}
		var public openapi.ModelOutputContentBlock
		switch blockType {
		case "text":
			text, err := publicTextContentBlock(block, index)
			if err != nil {
				return nil, err
			}
			if err := public.FromTextContentBlock(text); err != nil {
				return nil, err
			}
		case "media_ref":
			media, err := publicMediaRefContentBlock(block, index)
			if err != nil {
				return nil, err
			}
			if err := public.FromMediaRefContentBlock(media); err != nil {
				return nil, err
			}
		case "reasoning":
			reasoning, metadata, err := decodeStoredTextContentBlock(block, index, "reasoning")
			if err != nil {
				return nil, err
			}
			if err := public.FromReasoningContentBlock(openapi.ReasoningContentBlock{
				Text:     reasoning,
				Metadata: metadata,
			}); err != nil {
				return nil, err
			}
		case "tool_call":
			toolCall, err := publicModelToolCallContentBlock(block, index)
			if err != nil {
				return nil, err
			}
			if err := public.FromModelToolCallContentBlock(toolCall); err != nil {
				return nil, err
			}
		case "error":
			errorText, metadata, err := decodeStoredTextContentBlock(block, index, "error")
			if err != nil {
				return nil, err
			}
			if err := public.FromErrorContentBlock(openapi.ErrorContentBlock{
				Text:     errorText,
				Metadata: metadata,
			}); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf(
				"model output content block %d has unsupported type %q",
				index,
				blockType,
			)
		}
		out = append(out, public)
	}
	return out, nil
}

func ToolResultContentBlocks(
	raw json.RawMessage,
) ([]openapi.ToolResultContentBlock, error) {
	blocks, err := decodeStoredContentBlocks(raw)
	if err != nil {
		return nil, err
	}
	out := make([]openapi.ToolResultContentBlock, 0, len(blocks))
	for index, block := range blocks {
		blockType, err := storedContentBlockTypeFor(block, index)
		if err != nil {
			return nil, err
		}
		var public openapi.ToolResultContentBlock
		switch blockType {
		case "text":
			text, err := publicTextContentBlock(block, index)
			if err != nil {
				return nil, err
			}
			if err := public.FromTextContentBlock(text); err != nil {
				return nil, err
			}
		case "media_ref":
			media, err := publicMediaRefContentBlock(block, index)
			if err != nil {
				return nil, err
			}
			if err := public.FromMediaRefContentBlock(media); err != nil {
				return nil, err
			}
		case "structured_data":
			var stored storedStructuredDataContentBlock
			if err := decodeStoredContentBlock(block, index, &stored); err != nil {
				return nil, err
			}
			if stored.Type != "structured_data" {
				return nil, fmt.Errorf(
					"structured data content block %d has type %q",
					index,
					stored.Type,
				)
			}
			value, err := publicJSONValue(
				stored.Value,
				fmt.Sprintf("structured data content block %d value", index),
			)
			if err != nil {
				return nil, err
			}
			if err := public.FromStructuredDataContentBlock(openapi.StructuredDataContentBlock{
				Value:    value,
				Metadata: stored.Metadata,
			}); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf(
				"tool result content block %d has unsupported type %q",
				index,
				blockType,
			)
		}
		out = append(out, public)
	}
	return out, nil
}

func publicTextContentBlock(
	raw json.RawMessage,
	index int,
) (openapi.TextContentBlock, error) {
	text, metadata, err := decodeStoredTextContentBlock(raw, index, "text")
	if err != nil {
		return openapi.TextContentBlock{}, err
	}
	return openapi.TextContentBlock{Text: text, Metadata: metadata}, nil
}

func decodeStoredTextContentBlock(
	raw json.RawMessage,
	index int,
	expectedType string,
) (string, resourcemeta.Metadata, error) {
	var block storedTextContentBlock
	if err := decodeStoredContentBlock(raw, index, &block); err != nil {
		return "", nil, err
	}
	if block.Type != expectedType {
		return "", nil, fmt.Errorf(
			"%s content block %d has type %q",
			expectedType,
			index,
			block.Type,
		)
	}
	if block.Text == nil {
		return "", nil, fmt.Errorf("%s content block %d is missing text", expectedType, index)
	}
	return *block.Text, block.Metadata, nil
}

func publicMediaRefContentBlock(
	raw json.RawMessage,
	index int,
) (openapi.MediaRefContentBlock, error) {
	var block storedMediaRefContentBlock
	if err := decodeStoredContentBlock(raw, index, &block); err != nil {
		return openapi.MediaRefContentBlock{}, err
	}
	if block.Type != "media_ref" {
		return openapi.MediaRefContentBlock{}, fmt.Errorf(
			"media content block %d has type %q",
			index,
			block.Type,
		)
	}
	id, err := uuid.Parse(block.ArtifactID)
	if err != nil || id == uuid.Nil {
		return openapi.MediaRefContentBlock{}, fmt.Errorf(
			"media content block %d has invalid artifact id",
			index,
		)
	}
	artifactID, err := publicID(publicid.KindArtifact, id)
	if err != nil {
		return openapi.MediaRefContentBlock{}, err
	}
	return openapi.MediaRefContentBlock{
		ArtifactId:              artifactID,
		ExcludeFromModelContext: block.ExcludeFromModelContext,
		Metadata:                block.Metadata,
	}, nil
}

func publicModelToolCallContentBlock(
	raw json.RawMessage,
	index int,
) (openapi.ModelToolCallContentBlock, error) {
	var block storedToolCallContentBlock
	if err := decodeStoredContentBlock(raw, index, &block); err != nil {
		return openapi.ModelToolCallContentBlock{}, err
	}
	if block.Type != "tool_call" {
		return openapi.ModelToolCallContentBlock{}, fmt.Errorf(
			"tool call content block %d has type %q",
			index,
			block.Type,
		)
	}
	id, err := uuid.Parse(block.ToolCallID)
	if err != nil || id == uuid.Nil {
		return openapi.ModelToolCallContentBlock{}, fmt.Errorf(
			"tool call content block %d has invalid tool call id",
			index,
		)
	}
	toolCallID, err := publicID(publicid.KindToolCall, id)
	if err != nil {
		return openapi.ModelToolCallContentBlock{}, err
	}
	toolType := openapi.ToolCallType(block.ToolType)
	if !toolType.Valid() {
		return openapi.ModelToolCallContentBlock{}, fmt.Errorf(
			"tool call content block %d has invalid tool type %q",
			index,
			block.ToolType,
		)
	}
	if block.Name == "" {
		return openapi.ModelToolCallContentBlock{}, fmt.Errorf(
			"tool call content block %d is missing name",
			index,
		)
	}
	input, err := ToolInput(
		block.Input,
		fmt.Sprintf("tool call content block %d input", index),
	)
	if err != nil {
		return openapi.ModelToolCallContentBlock{}, err
	}
	return openapi.ModelToolCallContentBlock{
		Input:      input,
		Metadata:   block.Metadata,
		Name:       block.Name,
		ToolCallId: toolCallID,
		ToolType:   toolType,
	}, nil
}

func ToolInput(
	raw json.RawMessage,
	description string,
) (openapi.ToolInput, error) {
	trimmed := bytes.TrimSpace(raw)
	if err := modelenvelope.ValidateToolInput(trimmed); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object", description)
	}
	return json.RawMessage(bytes.Clone(trimmed)), nil
}

func publicJSONValue(
	raw json.RawMessage,
	description string,
) (openapi.JSONBlob, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("%s is missing", description)
	}
	if !json.Valid(trimmed) {
		return nil, fmt.Errorf("%s is invalid JSON", description)
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, nil //nolint:nilnil // A nil blob is the JSON null representation.
	}
	return json.RawMessage(bytes.Clone(trimmed)), nil
}
