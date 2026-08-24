package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func validateRuntimeMachineSource(index int, machine agentconfig.RuntimeMachine) error {
	if machine.MachineID != "" && machine.MachinePoolID != "" {
		return fmt.Errorf("machine_sources[%d] cannot set both machine_id and machine_pool_id", index)
	}
	if machine.MachineID == "" && machine.MachinePoolID == "" {
		return fmt.Errorf("machine_sources[%d] has no machine source", index)
	}
	if machine.MachineID != "" {
		if machine.MaxMachines != 0 {
			return fmt.Errorf("machine_sources[%d].max_machines is only valid for machine_pool_id sources", index)
		}
		if machine.InitialNumMachines != 0 {
			return fmt.Errorf(
				"machine_sources[%d].initial_num_machines is only valid for machine_pool_id sources",
				index,
			)
		}
		if hasMachineProvisioningOverlay(machine) {
			return fmt.Errorf(
				"machine_sources[%d] machine provisioning fields are only valid for machine_pool_id sources",
				index,
			)
		}
	}
	if machine.MachinePoolID != "" {
		if machine.MaxMachines < 0 {
			return fmt.Errorf("machine_sources[%d].max_machines cannot be negative", index)
		}
		if machine.InitialNumMachines < 0 {
			return fmt.Errorf("machine_sources[%d].initial_num_machines cannot be negative", index)
		}
		if machine.InitialNumMachines > machine.MaxMachines {
			return fmt.Errorf("machine_sources[%d].initial_num_machines cannot exceed max_machines", index)
		}
	}
	return nil
}

func resolveMachinePoolName(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID ID,
	machinePoolName string,
) (ID, bool, error) {
	if machinePoolName == "" {
		return NilID, false, nil
	}
	normalizedName, err := resourcename.Normalize("machine pool name", machinePoolName)
	if err != nil {
		return NilID, false, err
	}
	pool, err := qtx.GetMachinePoolByName(
		ctx,
		dbsqlc.GetMachinePoolByNameParams{OrgID: orgID, Name: normalizedName},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return NilID, false, nil
	}
	if err != nil {
		return NilID, false, fmt.Errorf("load machine pool %q: %w", machinePoolName, err)
	}
	return pool.ID, true, nil
}
