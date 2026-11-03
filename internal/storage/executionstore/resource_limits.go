package executionstore

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
)

const (
	resourceAgentProfiles = "agent_profiles"
	resourceCronTriggers  = "cron_triggers"
	resourceMachinePools  = "machine_pools"
)

func lockResourceCreation(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	resourceKind string,
	scope string,
) error {
	return resourceguard.Lock(ctx, qtx, resourceKind, scope)
}

func resolveResourceLimits(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID uuid.UUID,
) (dbsqlc.EffectiveResourceLimit, error) {
	return resourceguard.ResolveLimits(ctx, qtx, orgID)
}
