package executionstore

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type TypedAgentEventRecord struct {
	Event               events.Event
	TurnID              uuid.UUID
	IsOpeningEvent      bool
	AgentInputID        uuid.UUID
	ModelOutputID       uuid.UUID
	ToolCallResultID    uuid.UUID
	ContextCheckpointID uuid.UUID
}

type CreateContentBlockInput struct {
	ProjectID               uuid.UUID
	AgentID                 uuid.UUID
	OwnerKind               ContentBlockOwnerKind
	OwnerAgentInputID       uuid.UUID
	OwnerModelOutputID      uuid.UUID
	OwnerToolCallResultID   uuid.UUID
	Ordinal                 int32
	BlockKind               ContentBlockKind
	TextContent             string
	StructuredData          json.RawMessage
	ArtifactID              uuid.UUID
	ToolCallID              uuid.UUID
	ExcludeFromModelContext bool
	Metadata                resourcemeta.Metadata
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
		AgentInputID:        storeutil.IDFromPtr(row.AgentInputID),
		ModelOutputID:       storeutil.IDFromPtr(row.ModelOutputID),
		ToolCallResultID:    storeutil.IDFromPtr(row.ToolCallResultID),
		ContextCheckpointID: storeutil.IDFromPtr(row.ContextCheckpointID),
	}, nil
}
