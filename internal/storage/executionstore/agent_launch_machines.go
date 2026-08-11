package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type launchMachineSource struct {
	Index              int
	Contract           agentconfig.RuntimeMachine
	MachineID          ID
	MachinePoolID      ID
	GrantID            ID
	PoolGrantForLaunch dbsqlc.GetActiveProjectMachinePoolGrantForLaunchRow
	Provisioning       MachineProvisioningConfig
	MachineCwd         string
	MachineEnvironment MachineEnvironment
	BindingConfig      MachineBindingConfig
}

type launchMachineBindingRequest struct {
	Source        launchMachineSource
	PoolSlotIndex int
}

func decodeLaunchMachineSources(
	contract agentconfig.RuntimeContract,
) ([]launchMachineSource, error) {
	if len(contract.MachineSources) == 0 {
		return nil, nil
	}
	out := make([]launchMachineSource, 0, len(contract.MachineSources))
	for index, machine := range contract.MachineSources {
		source := launchMachineSource{Index: index, Contract: machine}
		if err := validateRuntimeMachineSource(index, machine); err != nil {
			return nil, err
		}
		if machine.MachineID != "" {
			machineID, err := publicid.Decode(publicid.KindMachine, machine.MachineID)
			if err != nil {
				return nil, fmt.Errorf("machine_sources[%d].machine_id must be a machine public id: %w", index, err)
			}
			source.MachineID = machineID
		}
		if machine.MachinePoolID != "" {
			machinePoolID, err := publicid.Decode(publicid.KindMachinePool, machine.MachinePoolID)
			if err != nil {
				return nil, fmt.Errorf("machine_sources[%d].machine_pool_id must be a machine pool public id: %w", index, err)
			}
			source.MachinePoolID = machinePoolID
		}
		out = append(out, source)
	}
	return out, nil
}

func expandLaunchMachineBindingRequests(
	sources []launchMachineSource,
) ([]launchMachineBindingRequest, error) {
	var bindings []launchMachineBindingRequest
	for _, source := range sources {
		if source.MachineID != NilID {
			bindings = append(bindings, launchMachineBindingRequest{Source: source})
			continue
		}
		if source.MachinePoolID == NilID {
			return nil, fmt.Errorf("machine_sources[%d] has no machine source", source.Index)
		}
		for slotIndex := range source.Contract.InitialNumMachines {
			bindings = append(
				bindings,
				launchMachineBindingRequest{Source: source, PoolSlotIndex: slotIndex},
			)
		}
	}
	return bindings, nil
}

func (s *Store) resolveLaunchMachineSourcesTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, projectID ID,
	sources []launchMachineSource,
) error {
	machineIDs := make([]ID, 0, len(sources))
	for _, source := range sources {
		if source.MachineID != NilID {
			machineIDs = append(machineIDs, source.MachineID)
		}
	}
	sort.Slice(machineIDs, func(i, j int) bool {
		return machineIDs[i].String() < machineIDs[j].String()
	})
	for _, machineID := range machineIDs {
		if err := qtx.LockMachineEnvironmentKey(
			ctx,
			dbsqlc.LockMachineEnvironmentKeyParams{MachineID: machineID},
		); err != nil {
			return fmt.Errorf("lock machine environment: %w", err)
		}
	}

	var poolIndexes []int
	for index := range sources {
		switch {
		case sources[index].MachineID != NilID:
			grant, err := qtx.GetActiveProjectMachineGrantForMachine(
				ctx,
				dbsqlc.GetActiveProjectMachineGrantForMachineParams{
					ProjectID: projectID,
					MachineID: sources[index].MachineID,
				},
			)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf(
						"machine_sources[%d].machine_id does not have an active project grant: %w",
						sources[index].Index,
						storeerr.ErrNotFound,
					)
				}
				return fmt.Errorf("resolve launch machine grant: %w", err)
			}
			sources[index].GrantID = grant.ID
			machineEnvironment, err := MachineEnvironmentFromColumns(grant.MachineEnv, grant.MachineSecretEnv)
			if err != nil {
				return fmt.Errorf("machine_sources[%d] machine environment: %w", sources[index].Index, err)
			}
			environmentOverlay := runtimeMachineEnvironmentOverlay(sources[index].Contract)
			if _, err := resolveMachineEnvironmentTx(
				ctx,
				qtx,
				orgID,
				projectID,
				machineEnvironment,
				environmentOverlay,
			); err != nil {
				return fmt.Errorf("machine_sources[%d] environment: %w", sources[index].Index, err)
			}
			sources[index].BindingConfig = MachineBindingConfig{
				Cwd:                sources[index].Contract.Cwd,
				EnvironmentOverlay: environmentOverlay,
			}
		case sources[index].MachinePoolID != NilID:
			poolIndexes = append(poolIndexes, index)
		}
	}
	sort.Slice(poolIndexes, func(i, j int) bool {
		left := sources[poolIndexes[i]]
		right := sources[poolIndexes[j]]
		return left.MachinePoolID.String() < right.MachinePoolID.String()
	})
	// Lock pool grants in a deterministic order so concurrent launches that
	// reference the same pools in different config orders cannot deadlock.
	for _, index := range poolIndexes {
		poolGrant, err := qtx.GetActiveProjectMachinePoolGrantForLaunch(
			ctx,
			dbsqlc.GetActiveProjectMachinePoolGrantForLaunchParams{
				OrgID:         orgID,
				ProjectID:     projectID,
				MachinePoolID: sources[index].MachinePoolID,
			},
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf(
					"machine_sources[%d].machine_pool_id does not have an active project pool grant: %w",
					sources[index].Index,
					storeerr.ErrNotFound,
				)
			}
			return fmt.Errorf("load launch machine pool grant: %w", err)
		}
		resolved, err := s.ResolvePoolMachineTx(
			ctx,
			qtx,
			poolGrant,
			sources[index].Contract,
		)
		if err != nil {
			return fmt.Errorf("machine_sources[%d] configuration: %w", sources[index].Index, err)
		}
		sources[index].PoolGrantForLaunch = poolGrant
		sources[index].Provisioning = resolved.Provisioning
		sources[index].MachineCwd = resolved.MachineCwd
		sources[index].MachineEnvironment = resolved.MachineEnvironment
		sources[index].BindingConfig = resolved.BindingConfig
	}
	return nil
}

func ensurePoolCapacityForConfigTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, projectID ID,
	poolGrant dbsqlc.GetActiveProjectMachinePoolGrantForLaunchRow,
	machineProvisioning MachineProvisioningConfig,
	requestedMachines int,
) error {
	if requestedMachines <= 0 {
		return nil
	}
	if !poolGrant.NewManagedWorkAllowed {
		return storeerr.ErrManagedWorkAdmissionDenied
	}
	requested, err := resourcesFromMachineProvisioning(machineProvisioning)
	if err != nil {
		return err
	}
	poolUsage, err := qtx.GetActivePoolMachineUsage(
		ctx,
		dbsqlc.GetActivePoolMachineUsageParams{OrgID: orgID, MachinePoolID: &poolGrant.MachinePoolID},
	)
	if err != nil {
		return fmt.Errorf("get active pool machine usage: %w", err)
	}
	if int(poolUsage.Machines)+requestedMachines > int(poolGrant.PoolMaxTotalMachines) {
		return fmt.Errorf("machine pool capacity exceeded: %w", storeerr.ErrStateTransitionConflict)
	}
	if err := checkLaunchResourceCaps(
		poolUsage.Cpu,
		poolUsage.MemoryMb,
		requested,
		requestedMachines,
		knownResourceCaps(requested, MachineResourceLimits{
			MaxTotalCPU:        intPtrFromSQLC(poolGrant.PoolMaxTotalCpu),
			MaxTotalMemoryMB:   intPtrFromSQLC(poolGrant.PoolMaxTotalMemoryMb),
			MinMachineCPU:      intPtrFromSQLC(poolGrant.PoolMinMachineCpu),
			MinMachineMemoryMB: intPtrFromSQLC(poolGrant.PoolMinMachineMemoryMb),
			MaxMachineCPU:      intPtrFromSQLC(poolGrant.PoolMaxMachineCpu),
			MaxMachineMemoryMB: intPtrFromSQLC(poolGrant.PoolMaxMachineMemoryMb),
		}),
	); err != nil {
		return fmt.Errorf("machine pool %w", err)
	}
	projectPoolUsage, err := qtx.GetActiveProjectMachinePoolUsage(
		ctx,
		dbsqlc.GetActiveProjectMachinePoolUsageParams{
			OrgID:         orgID,
			ProjectID:     projectID,
			MachinePoolID: &poolGrant.MachinePoolID,
		},
	)
	if err != nil {
		return fmt.Errorf("get active project machine pool usage: %w", err)
	}
	if poolGrant.GrantMaxTotalMachines != nil &&
		int(projectPoolUsage.Machines)+requestedMachines > int(*poolGrant.GrantMaxTotalMachines) {
		return fmt.Errorf("project machine pool capacity exceeded: %w", storeerr.ErrStateTransitionConflict)
	}
	if err := checkLaunchResourceCaps(
		projectPoolUsage.Cpu,
		projectPoolUsage.MemoryMb,
		requested,
		requestedMachines,
		knownResourceCaps(requested, MachineResourceLimits{
			MaxTotalCPU:        intPtrFromSQLC(poolGrant.GrantMaxTotalCpu),
			MaxTotalMemoryMB:   intPtrFromSQLC(poolGrant.GrantMaxTotalMemoryMb),
			MinMachineCPU:      intPtrFromSQLC(poolGrant.GrantMinMachineCpu),
			MinMachineMemoryMB: intPtrFromSQLC(poolGrant.GrantMinMachineMemoryMb),
			MaxMachineCPU:      intPtrFromSQLC(poolGrant.GrantMaxMachineCpu),
			MaxMachineMemoryMB: intPtrFromSQLC(poolGrant.GrantMaxMachineMemoryMb),
		}),
	); err != nil {
		return fmt.Errorf("project machine pool %w", err)
	}
	return nil
}

func insertAgentMachineBindingTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input insertAgentMachineBindingInput,
) (AgentMachineBindingRecord, error) {
	if input.MachineRef == "" {
		return AgentMachineBindingRecord{}, errors.New("launch agent machine ref is required")
	}
	row, err := qtx.InsertAgentMachineBinding(
		ctx,
		dbsqlc.InsertAgentMachineBindingParams{
			ProjectID:             input.ProjectID,
			AgentID:               input.AgentID,
			CreateToolCallID:      sqlcIDFromNil(input.CreateToolCallID),
			ProjectMachineGrantID: input.ProjectMachineGrantID,
			MachineRef:            input.MachineRef,
			BindingKind:           string(input.BindingKind),
			Description:           input.Description,
			Cwd:                   input.Cwd,
			EnvOverlay:            normalizedJSON(input.EnvOverlay),
			SecretEnvOverlay:      normalizedJSON(input.SecretEnvOverlay),
			Metadata:              normalizedJSON(input.Metadata),
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AgentMachineBindingRecord{}, storeerr.ErrIdempotencyConflict
		}
		if storeutil.IsUniqueViolation(err) {
			return AgentMachineBindingRecord{}, storeerr.ErrIdempotencyConflict
		}
		return AgentMachineBindingRecord{}, fmt.Errorf("upsert launch agent machine binding: %w", err)
	}
	return agentMachineBindingRecordFromSQLC(row), nil
}

func allocateNewPoolMachineForAgentTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, projectID, agentID ID,
	binding launchMachineBindingRequest,
	machineRef string,
) (AgentMachineBindingRecord, error) {
	source := binding.Source
	poolGrant := source.PoolGrantForLaunch
	if poolGrant.ID == NilID {
		return AgentMachineBindingRecord{}, errors.New("launch machine pool grant was not resolved")
	}
	provisioningColumns, err := machineProvisioningToColumns(source.Provisioning)
	if err != nil {
		return AgentMachineBindingRecord{}, fmt.Errorf("prepare launch machine columns: %w", err)
	}
	machineEnv, machineSecretEnv, err := machineEnvironmentToColumns(source.MachineEnvironment)
	if err != nil {
		return AgentMachineBindingRecord{}, fmt.Errorf("prepare launch machine environment: %w", err)
	}
	bindingEnvOverlay, bindingSecretEnvOverlay, err := MachineEnvironmentOverlayToColumns(
		source.BindingConfig.EnvironmentOverlay,
	)
	if err != nil {
		return AgentMachineBindingRecord{}, fmt.Errorf("prepare launch binding environment: %w", err)
	}
	machineRow, err := insertMachineWithResourceLimitTx(ctx, qtx, dbsqlc.InsertMachineParams{
		OrgID:                  orgID,
		MachinePoolID:          &poolGrant.MachinePoolID,
		SourceKind:             string(MachineSourceKindPool),
		DisplayName:            "Instance of " + poolGrant.PoolName,
		Description:            source.Contract.Description,
		Provider:               poolGrant.Provider,
		LifecycleState:         string(MachineLifecycleStateProvisioning),
		Cpu:                    provisioningColumns.CPU,
		MemoryMb:               provisioningColumns.MemoryMB,
		Cwd:                    source.MachineCwd,
		Env:                    machineEnv,
		SecretEnv:              machineSecretEnv,
		ProviderOptions:        &provisioningColumns.ProviderOptions,
		LifecycleReasonMessage: "",
		Metadata:               normalizedJSON(poolGrant.Metadata),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AgentMachineBindingRecord{}, storeerr.ErrIdempotencyConflict
		}
		return AgentMachineBindingRecord{}, fmt.Errorf("insert pool machine for launch: %w", err)
	}
	grantRow, err := qtx.UpsertProjectMachineGrant(ctx, dbsqlc.UpsertProjectMachineGrantParams{
		OrgID:                     orgID,
		ProjectID:                 projectID,
		MachineID:                 machineRow.ID,
		SourceKind:                string(ProjectMachineGrantSourceKindPool),
		ProjectMachinePoolGrantID: &poolGrant.ID,
		Description:               poolGrant.Description,
		IdempotencyKey: sqlcTextFromEmpty(
			machineSourceSlotChildIdempotencyKey(agentID, source.Index, binding.PoolSlotIndex, "machine-grant"),
		),
		Metadata: json.RawMessage(`{}`),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AgentMachineBindingRecord{}, storeerr.ErrIdempotencyConflict
		}
		return AgentMachineBindingRecord{}, fmt.Errorf(
			"insert generated project machine grant: %w",
			err,
		)
	}
	return insertAgentMachineBindingTx(ctx, qtx, insertAgentMachineBindingInput{
		ProjectID:             projectID,
		AgentID:               agentID,
		ProjectMachineGrantID: grantRow.ID,
		MachineRef:            machineRef,
		BindingKind:           MachineBindingKindPool,
		Description:           source.Contract.Description,
		Cwd:                   source.BindingConfig.Cwd,
		EnvOverlay:            bindingEnvOverlay,
		SecretEnvOverlay:      bindingSecretEnvOverlay,
		Metadata:              json.RawMessage(`{}`),
	})
}
