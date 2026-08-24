package executionstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/resourcename"
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
		name, err := resourcename.Normalize("machine pool name", template.Name)
		if err != nil {
			return nil, fmt.Errorf("default machine pool: %w", err)
		}
		template.Name = name
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
			if current.Provider != desired.Provider || current.ProviderAuthEnvVar != desired.ProviderAuthEnvVar {
				return nil, fmt.Errorf(
					"default machine pool %q cannot change provider or provider_auth_env_var",
					name,
				)
			}
			preserveClusterMachinePoolEditableFields(current, &desired)
			poolDefaults, err := prepareMachinePoolCreateInput(&desired)
			if err != nil {
				return nil, fmt.Errorf("default machine pool %q: %w", name, err)
			}
			// Preserved organization-owned secret references may outlive the secret and must not block reconciliation.
			validationDefaults := poolDefaults
			validationDefaults.Environment.SecretEnv = nil
			if err := s.validatePoolDefaultsTx(ctx, qtx, desired, validationDefaults); err != nil {
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

func preserveClusterMachinePoolEditableFields(
	current MachinePoolRecord,
	desired *CreateMachinePoolInput,
) {
	desired.DefaultMachineCPU = current.DefaultMachineCPU
	desired.DefaultMachineMemoryMB = current.DefaultMachineMemoryMB
	desired.DefaultMachineEnv = current.DefaultMachineEnv
	desired.DefaultMachineSecretEnv = current.DefaultMachineSecretEnv
	desired.MinMachineCPU = current.MinMachineCPU
	desired.MinMachineMemoryMB = current.MinMachineMemoryMB
	desired.MaxMachineCPU = current.MaxMachineCPU
	desired.MaxMachineMemoryMB = current.MaxMachineMemoryMB
	desired.DeleteAfterIdleMinutes = current.DeleteAfterIdleMinutes
}
