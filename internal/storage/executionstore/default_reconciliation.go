package executionstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func (s *Store) ReconcileDefaultMachinePoolsTx(
	ctx context.Context,
	tx pgx.Tx,
	templates []DefaultMachinePoolTemplate,
	rows []dbsqlc.MachinePool,
	apply bool,
) ([]string, error) {
	qtx := s.q.WithTx(tx)
	var changes []string
	for _, template := range templates {
		name := template.createInput(NilID).Name
		for _, row := range rows {
			if row.Name != name {
				continue
			}
			current := machinePoolRecordFromSQLC(row)
			if apply {
				locked, err := qtx.LockMachinePoolForUpdate(
					ctx,
					dbsqlc.LockMachinePoolForUpdateParams{OrgID: current.OrgID, ID: current.ID},
				)
				if err != nil {
					return nil, fmt.Errorf("lock default machine pool %q: %w", name, err)
				}
				current = machinePoolRecordFromSQLC(locked)
			}
			desired := template.createInput(current.OrgID)
			desired.MaxTotalMachines = current.MaxTotalMachines
			desired.MaxTotalCPU = current.MaxTotalCPU
			desired.MaxTotalMemoryMB = current.MaxTotalMemoryMB
			desired.MaxMachineCPU = current.MaxMachineCPU
			desired.MaxMachineMemoryMB = current.MaxMachineMemoryMB
			if current.Provider != desired.Provider || current.ProviderAuthEnvVar != desired.ProviderAuthEnvVar {
				return nil, fmt.Errorf(
					"default machine pool %q cannot change provider or provider_auth_env_var",
					name,
				)
			}
			poolDefaults, err := prepareMachinePoolCreateInput(&desired)
			if err != nil {
				return nil, fmt.Errorf("default machine pool %q: %w", name, err)
			}
			if err := s.validatePoolDefaultsTx(ctx, qtx, desired, poolDefaults); err != nil {
				return nil, fmt.Errorf("default machine pool %q: %w", name, err)
			}
			if sameMachinePoolIntent(current, desired) {
				continue
			}
			changes = append(changes, fmt.Sprintf("org %s: update machine pool %q", current.OrgID, name))
			if apply {
				if _, err := updateMachinePoolRow(ctx, qtx, current.ID, desired); err != nil {
					return nil, fmt.Errorf("update default machine pool %q: %w", name, err)
				}
				if current.RuntimeProtectionEnabled != desired.RuntimeProtectionEnabled {
					if err := qtx.ClearMachinePoolRuntimeMismatch(
						ctx,
						dbsqlc.ClearMachinePoolRuntimeMismatchParams{
							OrgID: current.OrgID, MachinePoolID: current.ID,
						},
					); err != nil {
						return nil, fmt.Errorf("clear machine pool runtime mismatch: %w", err)
					}
				}
			}
		}
	}
	return changes, nil
}
