package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type ContextEventRecord struct {
	ID                    uuid.UUID
	SourceEventID         uuid.UUID
	AgentInputID          uuid.UUID
	ProjectID             uuid.UUID
	AgentID               uuid.UUID
	TurnID                uuid.UUID
	ModelOutputID         uuid.UUID
	ModelCallContextID    uuid.UUID
	ModelProviderConfigID uuid.UUID
	Role                  modelprotocol.MessageRole
	Sequence              int64
	ContentParts          json.RawMessage
	RequestedModelSlug    string
	APIFormat             modelprotocol.APIFormat
	APIVariant            modelprotocol.APIVariant
	ProviderReplay        json.RawMessage
	StopReason            modelenvelope.StopReason
	CreatedAt             time.Time
}

func (s *Store) ListContextEvents(
	ctx context.Context,
	projectID, agentID uuid.UUID,
	afterSequence int64,
	watermark int64,
	limit int32,
) ([]ContextEventRecord, error) {
	if projectID == uuid.Nil {
		return nil, errors.New("project id is required")
	}
	if agentID == uuid.Nil {
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
			SourceEventID:         row.ID,
			AgentInputID:          storeutil.IDFromPtr(row.AgentInputID),
			Sequence:              row.Sequence,
			CreatedAt:             row.CreatedAt,
			ContentParts:          row.ContentParts,
			ModelOutputID:         storeutil.IDFromPtr(row.ModelOutputID),
			ModelCallContextID:    storeutil.IDFromPtr(row.ModelCallContextID),
			ModelProviderConfigID: storeutil.IDFromPtr(row.ModelProviderConfigID),
			RequestedModelSlug:    row.RequestedProviderModelSlug,
			APIFormat:             modelprotocol.APIFormat(row.ApiFormat),
			APIVariant:            modelprotocol.APIVariant(row.ApiVariant),
			ProviderReplay:        rawMessageFromSQLCPtr(row.ProviderReplay),
			StopReason:            modelenvelope.StopReason(row.StopReason),
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

func (s *Store) IsOutputLimitBoundary(ctx context.Context, projectID, agentID uuid.UUID, sequence int64) (bool, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || sequence <= 0 {
		return false, errors.New("project, agent, and positive event sequence are required")
	}
	return s.q.IsOutputLimitBoundary(ctx, dbsqlc.IsOutputLimitBoundaryParams{
		ProjectID: projectID, AgentID: agentID, EventSequence: sequence,
	})
}
