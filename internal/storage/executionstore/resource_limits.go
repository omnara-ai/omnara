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
	resourceAgents              = "agents"
	resourceMachineDaemonTokens = "machine_daemon_tokens"
	resourceMachinePools        = "machine_pools"
	resourceMachines            = "machines"

	MaxAgentConfigsPerProject          = 10_000
	MaxActiveAgentProfilesPerProject   = 1_000
	MaxActiveAgentsPerProject          = 10_000
	MaxActiveBYODaemonTokensPerMachine = 20
	MaxActiveTenantMachinePoolsPerOrg  = 100
	MaxLiveMachinesPerOrg              = 10_000
)

func lockResourceCreation(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	resourceKind string,
	scope string,
) error {
	return resourceguard.Lock(ctx, qtx, resourceKind, scope)
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
	machineCount, err := qtx.CountLiveMachinesForOrg(
		ctx,
		dbsqlc.CountLiveMachinesForOrgParams{OrgID: input.OrgID},
	)
	if err != nil {
		return dbsqlc.InsertMachineRow{}, fmt.Errorf("count live machines: %w", err)
	}
	if machineCount > MaxLiveMachinesPerOrg {
		return dbsqlc.InsertMachineRow{}, resourceLimitExceeded(
			"live machines",
			MaxLiveMachinesPerOrg,
		)
	}
	return row, nil
}
