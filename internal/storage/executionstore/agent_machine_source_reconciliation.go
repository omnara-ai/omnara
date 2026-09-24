package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) reconcileAgentMachineSourcesTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	projectID, agentID uuid.UUID,
	currentContract, nextContract agentconfig.RuntimeContract,
	nextSources []launchMachineSource,
) ([]MachineRecord, error) {
	if reflect.DeepEqual(currentContract.MachineSources, nextContract.MachineSources) {
		return nil, nil
	}

	currentSources, err := decodeLaunchMachineSources(currentContract)
	if err != nil {
		return nil, err
	}
	currentMachines := make(map[uuid.UUID]launchMachineSource, len(currentSources))
	currentPools := make(map[uuid.UUID]launchMachineSource, len(currentSources))
	nextMachines := make(map[uuid.UUID]launchMachineSource, len(nextSources))
	nextPools := make(map[uuid.UUID]launchMachineSource, len(nextSources))
	for _, source := range currentSources {
		if source.Contract.MachineID != uuid.Nil {
			currentMachines[source.Contract.MachineID] = source
		} else {
			currentPools[source.Contract.MachinePoolID] = source
		}
	}
	for _, source := range nextSources {
		if source.Contract.MachineID != uuid.Nil {
			nextMachines[source.Contract.MachineID] = source
		} else {
			nextPools[source.Contract.MachinePoolID] = source
		}
	}
	var deleteMachines []MachineRecord
	for _, source := range currentSources {
		if source.Contract.MachineID != uuid.Nil {
			if _, ok := nextMachines[source.Contract.MachineID]; ok {
				continue
			}
			grant, err := qtx.GetActiveProjectMachineGrantForMachine(
				ctx,
				dbsqlc.GetActiveProjectMachineGrantForMachineParams{
					ProjectID: projectID,
					MachineID: source.Contract.MachineID,
				},
			)
			if err == nil {
				if err := completeExecutionRevokedProcessesTx(
					ctx,
					txNotifications,
					tx,
					qtx,
					executionRevokedProcessScope{
						projectID:             projectID,
						agentID:               agentID,
						projectMachineGrantID: grant.ID,
					},
					"agent_config_machine_source_removed",
				); err != nil {
					return nil, fmt.Errorf("complete removed explicit machine source processes: %w", err)
				}
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("load removed explicit machine source grant: %w", err)
			}
			_, err = qtx.ReleaseExplicitAgentMachineBinding(
				ctx,
				dbsqlc.ReleaseExplicitAgentMachineBindingParams{
					ProjectID: projectID,
					AgentID:   agentID,
					MachineID: source.Contract.MachineID,
				},
			)
			if err != nil {
				return nil, fmt.Errorf("release explicit machine binding: %w", err)
			}
			continue
		}
		if _, ok := nextPools[source.Contract.MachinePoolID]; ok {
			continue
		}
		machineRows, err := qtx.MarkRemovedAgentPoolSourceMachinesDeleting(
			ctx,
			dbsqlc.MarkRemovedAgentPoolSourceMachinesDeletingParams{
				LifecycleReasonCode:    storeutil.TextFromEmpty("agent_config_machine_source_removed"),
				LifecycleReasonMessage: "cleaning up machine after machine source removal",
				ProjectID:              projectID,
				AgentID:                agentID,
				MachinePoolID:          source.Contract.MachinePoolID,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("mark removed pool source machines deleting: %w", err)
		}
		for _, row := range machineRows {
			deleteMachines = append(
				deleteMachines,
				machineRecordFromMarkRemovedAgentPoolSourceMachinesDeletingSQLC(row),
			)
		}
	}
	var poolMachines []PoolMachineRecord
	for _, source := range nextSources {
		if source.Contract.MachineID != uuid.Nil {
			current, exists := currentMachines[source.Contract.MachineID]
			if !exists {
				envOverlay, secretEnvOverlay, err := MachineEnvironmentOverlayToColumns(
					source.BindingConfig.EnvironmentOverlay,
				)
				if err != nil {
					return nil, fmt.Errorf("prepare added machine source environment: %w", err)
				}
				if _, err := insertAgentMachineBindingTx(ctx, qtx, insertAgentMachineBindingInput{
					ProjectID:             projectID,
					AgentID:               agentID,
					ProjectMachineGrantID: source.GrantID,
					BindingKind:           MachineBindingKindExplicit,
					Description:           source.Contract.Description,
					Cwd:                   source.BindingConfig.Cwd,
					EnvOverlay:            envOverlay,
					SecretEnvOverlay:      secretEnvOverlay,
					Metadata:              json.RawMessage(`{}`),
				}); err != nil {
					return nil, err
				}
				continue
			}
			if sameMachineBindingConfig(current.Contract, source.Contract) {
				continue
			}
			binding, err := qtx.GetAgentMachineBindingByMachine(
				ctx,
				dbsqlc.GetAgentMachineBindingByMachineParams{
					ProjectID:   projectID,
					AgentID:     agentID,
					MachineID:   source.Contract.MachineID,
					BindingKind: string(MachineBindingKindExplicit),
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, storeerr.ErrStateTransitionConflict
			}
			if err != nil {
				return nil, fmt.Errorf("load explicit machine binding: %w", err)
			}
			if err := updateAgentMachineBindingConfigTx(ctx, qtx, projectID, agentID, binding.ID, source); err != nil {
				return nil, err
			}
			continue
		}
		current, exists := currentPools[source.Contract.MachinePoolID]
		if !exists {
			continue
		}
		if sameMachineBindingConfig(current.Contract, source.Contract) {
			continue
		}
		if poolMachines == nil {
			poolMachines, err = listPoolMachinesTx(ctx, qtx, projectID, agentID)
			if err != nil {
				return nil, err
			}
		}
		for _, machine := range poolMachines {
			if machine.Machine.MachinePoolID != source.Contract.MachinePoolID ||
				machine.Machine.LifecycleState == MachineLifecycleStateDeleting ||
				machine.Machine.LifecycleState == MachineLifecycleStateDeleteFailed {
				continue
			}
			machineEnvironment, err := MachineEnvironmentFromColumns(
				machine.Machine.Env,
				machine.Machine.SecretEnv,
			)
			if err != nil {
				return nil, fmt.Errorf("load pool machine environment: %w", err)
			}
			if _, err := resolveMachineEnvironment(
				machineEnvironment,
				source.BindingConfig.EnvironmentOverlay,
			); err != nil {
				return nil, fmt.Errorf("pool machine binding environment: %w", err)
			}
			if err := updateAgentMachineBindingConfigTx(
				ctx,
				qtx,
				projectID,
				agentID,
				machine.Binding.ID,
				source,
			); err != nil {
				return nil, err
			}
		}
	}
	return deleteMachines, nil
}

func sameMachineBindingConfig(left, right agentconfig.RuntimeMachine) bool {
	return left.Cwd == right.Cwd &&
		left.Description == right.Description &&
		storeutil.SameIntPtr(left.DeleteAfterIdleMinutes, right.DeleteAfterIdleMinutes) &&
		reflect.DeepEqual(left.EnvOverlay, right.EnvOverlay) &&
		reflect.DeepEqual(left.SecretEnvOverlay, right.SecretEnvOverlay)
}

func updateAgentMachineBindingConfigTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, bindingID uuid.UUID,
	source launchMachineSource,
) error {
	envOverlay, secretEnvOverlay, err := MachineEnvironmentOverlayToColumns(
		source.BindingConfig.EnvironmentOverlay,
	)
	if err != nil {
		return fmt.Errorf("prepare machine binding environment: %w", err)
	}
	updated, err := qtx.UpdateAttachedAgentMachineBindingConfig(
		ctx,
		dbsqlc.UpdateAttachedAgentMachineBindingConfigParams{
			Description:            source.Contract.Description,
			Cwd:                    source.BindingConfig.Cwd,
			EnvOverlay:             envOverlay,
			SecretEnvOverlay:       secretEnvOverlay,
			DeleteAfterIdleMinutes: storeutil.Int32Ptr(source.BindingConfig.DeleteAfterIdleMinutes),
			ProjectID:              projectID,
			AgentID:                agentID,
			ID:                     bindingID,
		},
	)
	if err != nil {
		return fmt.Errorf("update machine binding config: %w", err)
	}
	if updated != 1 {
		return storeerr.ErrStateTransitionConflict
	}
	return nil
}
