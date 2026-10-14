package logent

import (
	"context"

	"github.com/google/uuid"
	log "github.com/omnara-ai/omnara/observability/wideevent"
)

func MemoryStagedFileDiscardFailed(ctx context.Context, orgID, projectID, storeID uuid.UUID, err error) {
	event := log.NewEvent(ctx, "memory.cleanup_failed", log.Fields{
		"memory.cleanup.operation": "discard_staged_file",
		"org.id":                   orgID,
		"project.id":               projectID,
		"memory_store.id":          storeID,
	})
	event.Level(log.WarnLevel)
	event.Error(err)
	event.Done(ctx)
}
