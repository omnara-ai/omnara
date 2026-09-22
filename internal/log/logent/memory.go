package logent

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/log"
)

type MemoryCleanupOperation string

const (
	MemoryCleanupDeleteStore        MemoryCleanupOperation = "delete_store"
	MemoryCleanupDeleteProject      MemoryCleanupOperation = "delete_project"
	MemoryCleanupDeleteOrganization MemoryCleanupOperation = "delete_organization"
	MemoryCleanupDiscardStagedFile  MemoryCleanupOperation = "discard_staged_file"
)

func MemoryCleanupFailed(ctx context.Context, operation MemoryCleanupOperation, orgID, projectID, storeID uuid.UUID, err error) {
	event := log.NewEvent(ctx, "memory.cleanup_failed", log.Fields{
		"memory.cleanup.operation": string(operation),
		"org.id":                   orgID,
		"project.id":               projectID,
		"memory_store.id":          storeID,
	})
	switch operation {
	case MemoryCleanupDeleteStore, MemoryCleanupDeleteProject, MemoryCleanupDeleteOrganization:
		event.Level(log.ErrorLevel)
		event.Attach(log.Fields{"memory.cleanup.gave_up": true})
	default:
		event.Level(log.WarnLevel)
	}
	event.Error(err)
	event.Done(ctx)
}
