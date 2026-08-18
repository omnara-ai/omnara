package executionstore

import (
	"context"
	"fmt"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	resourceAgentConfigs        = "agent_configs"
	resourceAgentProfiles       = "agent_profiles"
	resourceCronTriggers        = "cron_triggers"
	resourceAgents              = "agents"
	resourceMachineDaemonTokens = "machine_daemon_tokens"
	resourceMachinePools        = "machine_pools"
	resourceMachines            = "machines"
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
	orgID ID,
) (dbsqlc.EffectiveResourceLimit, error) {
	return resourceguard.ResolveLimits(ctx, qtx, orgID)
}

func resourceLimitExceeded(resource string, limit int64) error {
	return fmt.Errorf("%s limit of %d reached: %w", resource, limit, storeerr.ErrConflict)
}

func insertMachineWithResourceLimitTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input dbsqlc.InsertMachineParams,
) (dbsqlc.InsertMachineRow, error) {
	row, err := qtx.InsertMachine(ctx, input)
	if err != nil {
		return dbsqlc.InsertMachineRow{}, err
	}
	if err := lockResourceCreation(ctx, qtx, resourceMachines, input.OrgID.String()); err != nil {
		return dbsqlc.InsertMachineRow{}, err
	}
	limits, err := resolveResourceLimits(ctx, qtx, input.OrgID)
	if err != nil {
		return dbsqlc.InsertMachineRow{}, err
	}
	machineCount, err := qtx.CountLiveMachinesForOrg(
		ctx,
		dbsqlc.CountLiveMachinesForOrgParams{OrgID: input.OrgID},
	)
	if err != nil {
		return dbsqlc.InsertMachineRow{}, fmt.Errorf("count live machines: %w", err)
	}
	if machineCount > limits.MaxLiveMachinesPerOrg {
		return dbsqlc.InsertMachineRow{}, resourceLimitExceeded(
			"live machines",
			limits.MaxLiveMachinesPerOrg,
		)
	}
	return row, nil
}
