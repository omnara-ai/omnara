package executionstore

import (
	"time"

	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func eventFromProjectIdempotencySQLC(row dbsqlc.GetEventByProjectAgentIdempotencyKeyRow) (events.Event, error) {
	return eventFromSQLC(
		row.ID,
		row.AgentID,
		row.Sequence,
		row.EventKind,
		row.CreatedAt,
		row.IdempotencyKey,
	)
}

func eventFromSQLC(
	id ID,
	agentID ID,
	sequence int64,
	kind string,
	createdAt time.Time,
	idempotencyKey string,
) (events.Event, error) {
	event := events.Event{
		ID:             id,
		AgentID:        agentID,
		Sequence:       sequence,
		Kind:           events.Kind(kind),
		At:             createdAt,
		IdempotencyKey: idempotencyKey,
	}
	return event, event.Validate()
}
