package executionstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateAgentConfig(ctx context.Context, input CreateAgentConfigInput) (AgentConfigRecord, error) {
	if input.ProjectID == uuid.Nil {
		return AgentConfigRecord{}, errors.New("project id is required")
	}
	input = withDefaultAgentConfigCompilation(input)

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentConfigRecord{}, fmt.Errorf("begin create agent config: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.q.WithTx(tx)
	project, err := loadProjectTx(ctx, qtx, input.ProjectID)
	if err != nil {
		return AgentConfigRecord{}, err
	}
	input.OrgID = project.OrgID
	if err := lifecyclelock.EnterActiveProject(ctx, tx, project.OrgID, input.ProjectID); err != nil {
		return AgentConfigRecord{}, err
	}
	record, err := insertAgentConfigTx(ctx, qtx, input)
	if err != nil {
		return AgentConfigRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AgentConfigRecord{}, fmt.Errorf("commit create agent config: %w", err)
	}
	return record, nil
}

type CreateAgentConfigInput struct {
	OrgID                   uuid.UUID
	ProjectID               uuid.UUID
	Source                  string
	SourceFormat            string
	SourceHash              string
	ConfiguredModelID       uuid.UUID
	CompiledDefinition      json.RawMessage
	EffectiveDefinitionHash string
}

type AgentConfigRecord struct {
	ID                      uuid.UUID       `json:"id"`
	OrgID                   uuid.UUID       `json:"org_id"`
	ProjectID               uuid.UUID       `json:"project_id"`
	Source                  string          `json:"source,omitempty"`
	SourceFormat            string          `json:"source_format,omitempty"`
	SourceHash              string          `json:"source_hash,omitempty"`
	ConfiguredModelID       uuid.UUID       `json:"configured_model_id"`
	CompiledDefinition      json.RawMessage `json:"compiled_definition"`
	EffectiveDefinitionHash string          `json:"effective_definition_hash"`
	CreatedAt               time.Time       `json:"created_at"`
	Created                 bool            `json:"-"`
}

type AgentConfigSnapshotRecord struct {
	AgentConfig        AgentConfigRecord
	InputEventSequence int64
}

func (s *Store) CaptureAgentConfigForModelContext(
	ctx context.Context,
	projectID, agentID uuid.UUID,
) (AgentConfigSnapshotRecord, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil {
		return AgentConfigSnapshotRecord{}, errors.New("project and agent are required")
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentConfigSnapshotRecord{}, fmt.Errorf(
			"begin capture agent config for model context: %w",
			err,
		)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: projectID, ID: agentID},
	); errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		return AgentConfigSnapshotRecord{}, storeerr.ErrNotFound
	} else if err != nil {
		return AgentConfigSnapshotRecord{}, fmt.Errorf(
			"lock agent for model context config capture: %w",
			err,
		)
	}
	// The lock and the capture are deliberately separate statements. Under
	// READ COMMITTED, the second statement gets a fresh snapshot after any
	// transaction that previously held the agent lock has committed, so the
	// config pointer and event watermark are captured from the same durable
	// boundary.
	row, err := qtx.CaptureAgentConfigForModelContext(
		ctx,
		dbsqlc.CaptureAgentConfigForModelContextParams{ProjectID: projectID, AgentID: agentID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentConfigSnapshotRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return AgentConfigSnapshotRecord{}, fmt.Errorf(
			"capture agent config for model context: %w",
			err,
		)
	}
	if err := tx.Commit(ctx); err != nil {
		return AgentConfigSnapshotRecord{}, fmt.Errorf(
			"commit capture agent config for model context: %w",
			err,
		)
	}
	return agentConfigSnapshotFromSQLC(row), nil
}

func (s *Store) CaptureAgentConfigForEventWatermark(
	ctx context.Context,
	projectID, agentID uuid.UUID,
	watermark int64,
) (AgentConfigSnapshotRecord, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || watermark <= 0 {
		return AgentConfigSnapshotRecord{}, errors.New(
			"project, agent, and positive event watermark are required",
		)
	}
	row, err := s.q.CaptureAgentConfigForEventWatermark(
		ctx,
		dbsqlc.CaptureAgentConfigForEventWatermarkParams{
			ProjectID:          projectID,
			AgentID:            agentID,
			InputEventSequence: watermark,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentConfigSnapshotRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return AgentConfigSnapshotRecord{}, fmt.Errorf(
			"capture agent config for event watermark: %w",
			err,
		)
	}
	return agentConfigSnapshotAtWatermarkFromSQLC(row), nil
}

func (s *Store) GetAgentConfig(
	ctx context.Context,
	projectID, configID uuid.UUID,
) (AgentConfigRecord, bool, error) {
	if projectID == uuid.Nil || configID == uuid.Nil {
		return AgentConfigRecord{}, false, errors.New("project and agent config are required")
	}
	row, err := s.q.GetAgentConfig(
		ctx,
		dbsqlc.GetAgentConfigParams{ProjectID: projectID, ID: configID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentConfigRecord{}, false, nil
	}
	if err != nil {
		return AgentConfigRecord{}, false, fmt.Errorf("load agent config: %w", err)
	}
	return agentConfigRecordFromSQLC(row), true, nil
}

func insertAgentConfigTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input CreateAgentConfigInput,
) (AgentConfigRecord, error) {
	input = withDefaultAgentConfigCompilation(input)
	if input.Source != "" {
		if _, err := agentconfig.ParseSource(agentconfig.SourceFormat(input.SourceFormat), []byte(input.Source)); err != nil {
			return AgentConfigRecord{}, storeerr.InvalidRequest(err)
		}
	} else if input.SourceFormat != "" || input.SourceHash != "" {
		return AgentConfigRecord{}, storeerr.InvalidRequest(errors.New("source metadata requires source"))
	}
	if input.ConfiguredModelID == uuid.Nil {
		return AgentConfigRecord{}, errors.New("agent config configured model is required")
	}
	if err := validateMemoryStoresTx(ctx, qtx, input.ProjectID, input.CompiledDefinition); err != nil {
		return AgentConfigRecord{}, err
	}
	if err := lockAndValidateAgentConfigModelContractTx(ctx, qtx, input); err != nil {
		return AgentConfigRecord{}, err
	}
	row, err := qtx.UpsertAgentConfigByHash(
		ctx,
		dbsqlc.UpsertAgentConfigByHashParams{
			OrgID:                   input.OrgID,
			ProjectID:               input.ProjectID,
			ConfiguredModelID:       input.ConfiguredModelID,
			Source:                  storeutil.TextFromEmpty(input.Source),
			SourceFormat:            storeutil.TextFromEmpty(input.SourceFormat),
			SourceHash:              storeutil.TextFromEmpty(input.SourceHash),
			CompiledDefinition:      input.CompiledDefinition,
			EffectiveDefinitionHash: input.EffectiveDefinitionHash,
		},
	)
	var record AgentConfigRecord
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Insert-or-select race: a concurrent transaction inserted the same hash
		// and committed after our statement snapshot was taken, so ON CONFLICT
		// DO NOTHING skipped the insert while the select branch could not see
		// the committed row. Re-select on a fresh snapshot.
		existing, selectErr := qtx.GetAgentConfigByHash(
			ctx,
			dbsqlc.GetAgentConfigByHashParams{
				ProjectID:               input.ProjectID,
				EffectiveDefinitionHash: input.EffectiveDefinitionHash,
				SourceFormat:            storeutil.TextFromEmpty(input.SourceFormat),
				SourceHash:              storeutil.TextFromEmpty(input.SourceHash),
			},
		)
		if selectErr != nil {
			return AgentConfigRecord{}, fmt.Errorf(
				"reload agent config after upsert race: %w",
				selectErr,
			)
		}
		record = agentConfigRecordFromSQLC(existing)
		record.Created = false
	case err != nil:
		return AgentConfigRecord{}, fmt.Errorf("upsert agent config: %w", err)
	default:
		record = agentConfigRecordFromUpsertSQLC(row)
		record.Created = row.Inserted
	}
	if !sameAgentConfigAuthority(record, input) {
		return AgentConfigRecord{}, fmt.Errorf(
			"agent config hash collision or canonicalization mismatch: %w",
			storeerr.ErrIdempotencyConflict,
		)
	}
	if record.Created {
		if err := lockResourceCreation(ctx, qtx, resourceAgentConfigs, input.ProjectID.String()); err != nil {
			return AgentConfigRecord{}, err
		}
		limits, err := resolveResourceLimits(ctx, qtx, input.OrgID)
		if err != nil {
			return AgentConfigRecord{}, err
		}
		configCount, err := qtx.CountAgentConfigsForProject(
			ctx,
			dbsqlc.CountAgentConfigsForProjectParams{ProjectID: input.ProjectID},
		)
		if err != nil {
			return AgentConfigRecord{}, fmt.Errorf("count agent configs: %w", err)
		}
		if configCount > limits.MaxAgentConfigsPerProject {
			return AgentConfigRecord{}, resourceLimitExceeded(
				"agent configs",
				limits.MaxAgentConfigsPerProject,
			)
		}
	}
	return record, nil
}

func lockAndValidateAgentConfigModelContractTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input CreateAgentConfigInput,
) error {
	contract, err := agentconfig.RuntimeContractFromCompiled(
		input.CompiledDefinition,
		input.EffectiveDefinitionHash,
	)
	if err != nil {
		return fmt.Errorf("validate agent config runtime contract: %w", err)
	}
	if contract.Model.ConfiguredModelID == uuid.Nil {
		return errors.New("agent config compiled model must include configured_model_id")
	}
	if contract.Model.ConfiguredModelID != input.ConfiguredModelID {
		return fmt.Errorf("compiled configured model does not match agent config row: %w", storeerr.ErrIdempotencyConflict)
	}
	effectiveModel, err := modelstore.ResolveForAgentTx(
		ctx,
		qtx,
		input.OrgID,
		input.ProjectID,
		input.ConfiguredModelID,
		contract.Model.Overrides(),
	)
	if err != nil {
		return fmt.Errorf("resolve configured model for agent config: %w", err)
	}
	if contract.RequiresModelToolSupport() && !effectiveModel.SupportsTools {
		return fmt.Errorf(
			"agent config requires tools but configured model project grant does not support tools: %w",
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
	return nil
}

func sameAgentConfigAuthority(record AgentConfigRecord, input CreateAgentConfigInput) bool {
	return record.Source == input.Source &&
		record.SourceFormat == input.SourceFormat &&
		record.SourceHash == input.SourceHash &&
		record.ConfiguredModelID == input.ConfiguredModelID &&
		sameJSON(record.CompiledDefinition, input.CompiledDefinition) &&
		record.EffectiveDefinitionHash == input.EffectiveDefinitionHash
}

func loadAgentConfigTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, configID uuid.UUID,
) (AgentConfigRecord, error) {
	row, err := qtx.GetAgentConfig(
		ctx,
		dbsqlc.GetAgentConfigParams{ProjectID: projectID, ID: configID},
	)
	if err != nil {
		return AgentConfigRecord{}, fmt.Errorf("load agent config: %w", err)
	}
	return agentConfigRecordFromSQLC(row), nil
}

func lockAgentConfigForUseTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	config AgentConfigRecord,
) error {
	if err := validateMemoryStoresTx(ctx, qtx, config.ProjectID, config.CompiledDefinition); err != nil {
		return err
	}
	_, err := qtx.LockConfiguredModelForUse(ctx, dbsqlc.LockConfiguredModelForUseParams{
		OrgID: config.OrgID,
		ID:    config.ConfiguredModelID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("configured model for agent config is unavailable: %w", storeerr.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("lock configured model for agent config: %w", err)
	}
	return nil
}

func (s *Store) ValidateAgentConfigMachineSources(
	ctx context.Context,
	projectID uuid.UUID,
	compiledDefinition json.RawMessage,
	definitionHash string,
) error {
	if projectID == uuid.Nil {
		return errors.New("project id is required")
	}
	project, err := loadProjectTx(ctx, s.q, projectID)
	if err != nil {
		return err
	}
	contract, err := agentconfig.RuntimeContractFromCompiled(compiledDefinition, definitionHash)
	if err != nil {
		return err
	}
	for index, source := range contract.MachineSources {
		if err := validateRuntimeMachineSource(index, source); err != nil {
			return err
		}
		if source.MachineID != uuid.Nil {
			grant, err := s.q.GetActiveProjectMachineGrantForMachine(
				ctx,
				dbsqlc.GetActiveProjectMachineGrantForMachineParams{ProjectID: projectID, MachineID: source.MachineID},
			)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf(
						"machine_sources[%d].machine_id does not have an active project machine grant: %w",
						index,
						storeerr.ErrNotFound,
					)
				}
				return fmt.Errorf("load machine_sources[%d] machine validation context: %w", index, err)
			}
			machineEnvironment, err := MachineEnvironmentFromColumns(grant.MachineEnv, grant.MachineSecretEnv)
			if err != nil {
				return fmt.Errorf("machine_sources[%d] machine environment: %w", index, err)
			}
			_, err = resolveMachineEnvironmentTx(
				ctx,
				s.q,
				project.OrgID,
				projectID,
				machineEnvironment,
				runtimeMachineEnvironmentOverlay(source),
			)
			if err != nil {
				return fmt.Errorf("machine_sources[%d] environment: %w", index, err)
			}
			continue
		}
		if source.MachinePoolID == uuid.Nil {
			continue
		}
		poolGrant, err := s.q.GetPoolGrantConfigValidationContext(
			ctx,
			dbsqlc.GetPoolGrantConfigValidationContextParams{
				ProjectID:     projectID,
				MachinePoolID: source.MachinePoolID,
			},
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf(
					"machine_sources[%d].machine_pool_id does not have an active project pool grant: %w",
					index,
					storeerr.ErrNotFound,
				)
			}
			return fmt.Errorf("load machine_sources[%d] machine pool validation context: %w", index, err)
		}
		poolDefaultProvisioning, err := MachineProvisioningFromDefaults(
			storeutil.IntPtr(poolGrant.DefaultMachineCpu),
			storeutil.IntPtr(poolGrant.DefaultMachineMemoryMb),
			poolGrant.DefaultMachineProviderOptions,
		)
		if err != nil {
			return fmt.Errorf("machine pool default_machine fields: %w", err)
		}
		poolDefaultEnvironment, err := MachineEnvironmentFromColumns(
			poolGrant.DefaultMachineEnv,
			poolGrant.DefaultMachineSecretEnv,
		)
		if err != nil {
			return fmt.Errorf("machine pool default_machine fields: %w", err)
		}
		projectProvisioningOverlay, err := machineProvisioningOverlayFromColumns(
			storeutil.IntPtr(poolGrant.GrantDefaultMachineCpu),
			storeutil.IntPtr(poolGrant.GrantDefaultMachineMemoryMb),
			poolGrant.GrantDefaultMachineProviderOptionsOverlay,
		)
		if err != nil {
			return fmt.Errorf("project machine pool grant default_machine fields: %w", err)
		}
		projectEnvironmentOverlay, err := machineEnvironmentOverlayFromColumns(
			poolGrant.GrantDefaultMachineEnvOverlay,
			poolGrant.GrantDefaultMachineSecretEnvOverlay,
		)
		if err != nil {
			return fmt.Errorf("project machine pool grant default_machine fields: %w", err)
		}
		machineProvisioning, err := s.ResolveMachineProvisioning(
			poolGrant.Provider,
			MachinePoolProviderPolicy{
				DefaultProvisioning: poolDefaultProvisioning,
				ResourceLimits: MachineResourceLimits{
					MaxTotalCPU:        storeutil.IntPtr(poolGrant.PoolMaxTotalCpu),
					MaxTotalMemoryMB:   storeutil.IntPtr(poolGrant.PoolMaxTotalMemoryMb),
					MinMachineCPU:      storeutil.IntPtr(poolGrant.PoolMinMachineCpu),
					MinMachineMemoryMB: storeutil.IntPtr(poolGrant.PoolMinMachineMemoryMb),
					MaxMachineCPU:      storeutil.IntPtr(poolGrant.PoolMaxMachineCpu),
					MaxMachineMemoryMB: storeutil.IntPtr(poolGrant.PoolMaxMachineMemoryMb),
				},
				ProviderConfig: poolGrant.ProviderConfig,
			},
			projectProvisioningOverlay,
			runtimeMachineProvisioningOverlay(source),
		)
		if err != nil {
			return fmt.Errorf("machine_sources[%d] configuration: %w", index, err)
		}
		machineEnv, err := resolveMachineEnvironmentTx(
			ctx,
			s.q,
			project.OrgID,
			projectID,
			poolDefaultEnvironment,
			projectEnvironmentOverlay,
		)
		if err != nil {
			return fmt.Errorf("machine_sources[%d] environment: %w", index, err)
		}
		_, err = resolveMachineEnvironmentTx(
			ctx,
			s.q,
			project.OrgID,
			projectID,
			machineEnv,
			runtimeMachineEnvironmentOverlay(source),
		)
		if err != nil {
			return fmt.Errorf("machine_sources[%d] environment: %w", index, err)
		}
		machineResources, err := resourcesFromMachineProvisioning(machineProvisioning)
		if err != nil {
			return fmt.Errorf("machine_sources[%d] machine provisioning fields: %w", index, err)
		}
		maxMachineCPU := effectiveOptionalPoolGrantCap(poolGrant.PoolMaxMachineCpu, poolGrant.GrantMaxMachineCpu)
		maxMachineMemoryMB := effectiveOptionalPoolGrantCap(
			poolGrant.PoolMaxMachineMemoryMb,
			poolGrant.GrantMaxMachineMemoryMb,
		)
		perMachineLimits := MachineResourceLimits{
			MinMachineCPU: effectivePoolGrantMinimum(
				storeutil.IntPtr(poolGrant.PoolMinMachineCpu),
				storeutil.IntPtr(poolGrant.GrantMinMachineCpu),
			),
			MinMachineMemoryMB: effectivePoolGrantMinimum(
				storeutil.IntPtr(poolGrant.PoolMinMachineMemoryMb),
				storeutil.IntPtr(poolGrant.GrantMinMachineMemoryMb),
			),
			MaxMachineCPU:      maxMachineCPU,
			MaxMachineMemoryMB: maxMachineMemoryMB,
		}
		if err := validateMachineResourcesWithinPerMachineLimits(
			machineResources,
			perMachineLimits,
		); err != nil {
			return storeerr.InvalidRequest(fmt.Errorf("machine_sources[%d] machine provisioning fields %w", index, err))
		}
	}
	return nil
}

func (s *Store) ResolveAgentConfigMachineName(
	ctx context.Context,
	projectID uuid.UUID,
	machineName string,
) (uuid.UUID, error) {
	if projectID == uuid.Nil || machineName == "" {
		return uuid.Nil, errors.New("project and machine name are required")
	}
	normalizedName, err := resourcename.CanonicalizeRequired("machine name", machineName)
	if err != nil {
		return uuid.Nil, storeerr.InvalidRequest(err)
	}
	row, err := s.q.GetActiveProjectMachineGrantForMachineName(
		ctx,
		dbsqlc.GetActiveProjectMachineGrantForMachineNameParams{ProjectID: projectID, MachineName: normalizedName},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, storeerr.ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve machine name: %w", err)
	}
	return row.MachineID, nil
}

func (s *Store) ResolveAgentConfigMachinePoolName(
	ctx context.Context,
	orgID, projectID uuid.UUID,
	machinePoolName string,
) (uuid.UUID, error) {
	if orgID == uuid.Nil || projectID == uuid.Nil || machinePoolName == "" {
		return uuid.Nil, errors.New("org, project, and machine pool name are required")
	}
	machinePoolID, ok, err := resolveMachinePoolName(ctx, s.q, orgID, machinePoolName)
	if err != nil {
		return uuid.Nil, err
	}
	if !ok {
		return uuid.Nil, storeerr.ErrNotFound
	}
	if _, err := s.GetActiveProjectMachinePoolGrantForMachinePool(ctx, projectID, machinePoolID); err != nil {
		return uuid.Nil, err
	}
	return machinePoolID, nil
}

func withDefaultAgentConfigCompilation(input CreateAgentConfigInput) CreateAgentConfigInput {
	if input.Source != "" && input.SourceFormat == "" {
		input.SourceFormat = string(agentconfig.SourceFormatYAML)
	}
	if input.Source != "" && input.SourceHash == "" {
		input.SourceHash = agentConfigSourceHash(input.Source)
	}
	input.CompiledDefinition = normalizedJSON(input.CompiledDefinition)
	if input.EffectiveDefinitionHash == "" {
		input.EffectiveDefinitionHash = configDefinitionHash(input.CompiledDefinition)
	}
	return input
}

func agentConfigSourceHash(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

func configDefinitionHash(definition json.RawMessage) string {
	normalized := normalizedJSON(definition)
	var canonical any
	if err := json.Unmarshal(normalized, &canonical); err == nil {
		if encoded, err := json.Marshal(canonical); err == nil {
			normalized = encoded
		}
	}
	sum := sha256.Sum256(normalized)
	return hex.EncodeToString(sum[:])
}

func (s *Store) ResolveAgentConfigProfileName(
	ctx context.Context,
	projectID uuid.UUID,
	profileName string,
) (uuid.UUID, error) {
	if projectID == uuid.Nil || profileName == "" {
		return uuid.Nil, errors.New("project and profile name are required")
	}
	normalizedName, err := resourcename.CanonicalizeRequired("agent profile name", profileName)
	if err != nil {
		return uuid.Nil, storeerr.InvalidRequest(err)
	}
	id, err := s.q.GetAgentProfileIDByName(
		ctx,
		dbsqlc.GetAgentProfileIDByNameParams{ProjectID: projectID, Name: normalizedName},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, storeerr.ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve agent profile name: %w", err)
	}
	return id, nil
}

func validateMemoryStoresTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID uuid.UUID,
	raw json.RawMessage,
) error {
	var config struct {
		Stores []agentconfig.MemoryStoreCompiled `json:"memory_stores"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return fmt.Errorf("validate memory stores: %w", err)
	}
	sort.Slice(config.Stores, func(i, j int) bool { return config.Stores[i].PublicID < config.Stores[j].PublicID })
	for _, store := range config.Stores {
		if store.Access != "read_only" && store.Access != "read_write" {
			return storeerr.InvalidRequest(errors.New("invalid memory store access"))
		}
		id, err := publicid.Decode(publicid.KindMemoryStore, store.PublicID)
		if err != nil {
			return storeerr.InvalidRequest(err)
		}
		_, err = q.LockMemoryStoreForConfig(ctx, dbsqlc.LockMemoryStoreForConfigParams{
			ProjectID: projectID,
			ID:        id,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("memory store is unavailable: %w", storeerr.ErrNotFound)
			}
			return fmt.Errorf("validate memory stores: %w", err)
		}
	}
	return nil
}
