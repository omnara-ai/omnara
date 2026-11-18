package agentexecution

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

// exportTurn queues the turn's trace for export when the unit commits. It is
// called by the transitions after which nothing more runs in the turn: a model
// output that neither calls tools nor is continued past an output limit, a
// cancellation, and a new turn replacing one that was still pending.
func (h *Handle) exportTurn(turnID uuid.UUID) {
	if turnID == uuid.Nil {
		return
	}
	if h.unit.finishedTurns == nil {
		h.unit.finishedTurns = make(map[uuid.UUID][]uuid.UUID)
	}
	h.unit.finishedTurns[h.route.AgentID] = append(h.unit.finishedTurns[h.route.AgentID], turnID)
}

// exportReplacedTurn exports the current turn when a new turn is about to
// replace it while it still waits to continue after tools or an output limit.
func (h *Handle) exportReplacedTurn(m *executionMutation) {
	if m.head.PendingToolOutputID != uuid.Nil || m.head.PendingOutputLimitID != uuid.Nil {
		h.exportTurn(m.head.CurrentTurnID)
	}
}

func (u *Unit) enqueueOtelExports(ctx context.Context) error {
	q := dbsqlc.New(u.DB())
	for agentID, turnIDs := range u.finishedTurns {
		if err := q.EnqueueOtelExportDeliveries(ctx, dbsqlc.EnqueueOtelExportDeliveriesParams{
			AgentID: agentID, TurnIds: turnIDs,
		}); err != nil {
			return fmt.Errorf("enqueue otel export deliveries: %w", err)
		}
	}
	return nil
}
