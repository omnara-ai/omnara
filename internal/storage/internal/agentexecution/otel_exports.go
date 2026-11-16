package agentexecution

import (
	"context"
	"fmt"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

const otelExportBatchDelaySeconds = 5

func (u *Unit) enqueueOtelExports(ctx context.Context) error {
	pending := u.notifications
	if pending == nil || len(pending.AgentEvents()) == 0 {
		return nil
	}
	q := dbsqlc.New(u.DB())
	for agentID, events := range pending.AgentEvents() {
		sequences := make([]int64, 0, len(events))
		for _, event := range events {
			if event.Kind == "model_output" {
				sequences = append(sequences, event.Sequence)
			}
		}
		if len(sequences) == 0 {
			continue
		}
		if err := q.EnqueueOtelExportDeliveries(ctx, dbsqlc.EnqueueOtelExportDeliveriesParams{
			AgentID: agentID, EventSequences: sequences, BatchDelaySeconds: otelExportBatchDelaySeconds,
		}); err != nil {
			return fmt.Errorf("enqueue otel export deliveries: %w", err)
		}
	}
	return nil
}
