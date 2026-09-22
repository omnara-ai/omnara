package logent

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/log"
)

func MemoryCleanupFailed(ctx context.Context, operation string, orgID, projectID, storeID uuid.UUID, err error) {
	event := log.NewEvent(ctx, "memory.cleanup_failed", log.Fields{
		"memory.cleanup.operation": operation,
		"org.id":                   orgID,
		"project.id":               projectID,
		"memory_store.id":          storeID,
	})
	event.Level(log.WarnLevel)
	event.Error(err)
	event.Done(ctx)
}
