package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type ContextEventRecord struct {
	ID                        ID
	SourceEventID             ID
	AgentInputID              ID
	ProjectID                 ID
	AgentID                   ID
	TurnID                    ID
	ModelOutputID             ID
	ModelCallContextID        ID
	AgentConfigID             ID
	ConfiguredModelRevisionID ID
	ModelProviderConfigID     ID
	Role                      modelprotocol.MessageRole
	Sequence                  int64
	ContentParts              json.RawMessage
	RequestedModelSlug        string
	APIFormat                 modelprotocol.APIFormat
	APIVariant                modelprotocol.APIVariant
	Usage                     modelenvelope.Usage
	ProviderReplay            json.RawMessage
	CreatedAt                 time.Time
}

func (s *Store) ListContextEvents(
	ctx context.Context,
	projectID, agentID ID,
	afterSequence int64,
	watermark int64,
	limit int32,
) ([]ContextEventRecord, error) {
	if isNilID(projectID) {
		return nil, errors.New("project id is required")
	}
	if isNilID(agentID) {
		return nil, errors.New("agent id is required")
	}
	if watermark < afterSequence {
		return nil, errors.New("watermark must be at or after sequence cursor")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.q.ListContextEvents(ctx, dbsqlc.ListContextEventsParams{
		ProjectID:     projectID,
		AgentID:       agentID,
		AfterSequence: afterSequence,
		Watermark:     watermark,
		PageLimit:     limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list context events: %w", err)
	}
	out := make([]ContextEventRecord, 0, len(rows))
	for _, row := range rows {
		record := ContextEventRecord{
			SourceEventID:             row.ID,
			AgentInputID:              idFromSQLCPtr(row.AgentInputID),
			Sequence:                  row.Sequence,
			CreatedAt:                 row.CreatedAt,
			ContentParts:              row.ContentParts,
			ModelOutputID:             idFromSQLCPtr(row.ModelOutputID),
			ModelCallContextID:        idFromSQLCPtr(row.ModelCallContextID),
			AgentConfigID:             idFromSQLCPtr(row.AgentConfigID),
			ConfiguredModelRevisionID: idFromSQLCPtr(row.ConfiguredModelRevisionID),
			ModelProviderConfigID:     idFromSQLCPtr(row.ModelProviderConfigID),
			RequestedModelSlug:        row.RequestedProviderModelSlug,
			APIFormat:                 modelprotocol.APIFormat(row.ApiFormat),
			APIVariant:                modelprotocol.APIVariant(row.ApiVariant),
			Usage: modelUsageFromSQLC(
				row.InputTokensTotal,
				row.UncachedInputTokens,
				row.CacheReadInputTokens,
				row.CacheWriteInputTokens,
				row.OutputTokensTotal,
				row.ReasoningOutputTokens,
			),
			ProviderReplay: rawMessageFromSQLCPtr(row.ProviderReplay),
		}
		record.ID = record.SourceEventID
		record.ProjectID = projectID
		record.AgentID = agentID
		if row.EventKind == string(events.KindModelOutput) {
			record.Role = modelprotocol.RoleAssistant
		} else {
			record.Role = modelprotocol.RoleUser
		}
		out = append(out, record)
	}
	return out, nil
}
