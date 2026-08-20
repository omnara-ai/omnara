package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type defaultMachinePoolReconciliation struct {
	template DefaultMachinePoolTemplate
	current  MachinePoolRecord
	desired  CreateMachinePoolInput
	changed  bool
}

func (s *Store) ReconcileDefaultMachinePoolsTx(
	ctx context.Context,
	tx pgx.Tx,
	orgID ID,
	templates []DefaultMachinePoolTemplate,
	rows []dbsqlc.MachinePool,
	apply bool,
) ([]string, error) {
	qtx := s.q.WithTx(tx)
	targets := make([]defaultMachinePoolReconciliation, 0, len(rows))
	for _, template := range templates {
		name := template.createInput(NilID).Name
		for _, row := range rows {
			if row.Name != name {
				continue
			}
			if row.OrgID != orgID {
				return nil, fmt.Errorf("default machine pool %q belongs to another organization", name)
			}
			targets = append(targets, defaultMachinePoolReconciliation{
				template: template,
				current:  machinePoolRecordFromSQLC(row),
			})
		}
	}
	if apply {
		poolRefs := make([]lifecyclelock.PoolRef, 0, len(targets))
		for _, target := range targets {
			poolRefs = append(poolRefs, lifecyclelock.PoolRef{
				OrgID: orgID, PoolID: target.current.ID,
			})
		}
		if err := lifecyclelock.Pools(ctx, tx, poolRefs); err != nil {
			return nil, err
		}
		for i := range targets {
			row, err := qtx.GetMachinePool(ctx, dbsqlc.GetMachinePoolParams{
				OrgID: orgID, ID: targets[i].current.ID,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, storeerr.ErrNotFound
			}
			if err != nil {
				return nil, fmt.Errorf("revalidate default machine pool: %w", err)
			}
			targets[i].current = machinePoolRecordFromSQLC(row)
		}
	}
	var changes []string
	var machineRefs []lifecyclelock.MachineRef
	for i := range targets {
		current := targets[i].current
		name := targets[i].template.createInput(NilID).Name
		desired := targets[i].template.createInput(current.OrgID)
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
		targets[i].desired = desired
		targets[i].changed = !sameMachinePoolIntent(current, desired)
		if !targets[i].changed {
			continue
		}
		changes = append(changes, fmt.Sprintf("org %s: update machine pool %q", current.OrgID, name))
		if apply && current.RuntimeProtectionEnabled != desired.RuntimeProtectionEnabled {
			machineIDs, err := qtx.ListMachinePoolMachineIDsForLifecycle(
				ctx,
				dbsqlc.ListMachinePoolMachineIDsForLifecycleParams{
					OrgID: orgID, MachinePoolID: current.ID,
				},
			)
			if err != nil {
				return nil, fmt.Errorf("list default machine pool machines: %w", err)
			}
			for _, machineID := range machineIDs {
				machineRefs = append(machineRefs, lifecyclelock.MachineRef{
					OrgID: orgID, MachineID: machineID,
				})
			}
		}
	}
	if !apply {
		return changes, nil
	}
	if err := lifecyclelock.Machines(ctx, tx, machineRefs); err != nil {
		return nil, err
	}
	for _, target := range targets {
		if !target.changed {
			continue
		}
		if _, err := updateMachinePoolRow(ctx, qtx, target.current.ID, target.desired); err != nil {
			return nil, fmt.Errorf("update default machine pool %q: %w", target.desired.Name, err)
		}
		if target.current.RuntimeProtectionEnabled != target.desired.RuntimeProtectionEnabled {
			if err := qtx.ClearMachinePoolRuntimeMismatch(
				ctx,
				dbsqlc.ClearMachinePoolRuntimeMismatchParams{
					OrgID: orgID, MachinePoolID: target.current.ID,
				},
			); err != nil {
				return nil, fmt.Errorf("clear machine pool runtime mismatch: %w", err)
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
}
