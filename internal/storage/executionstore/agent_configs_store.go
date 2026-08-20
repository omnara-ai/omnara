package executionstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateAgentConfig(ctx context.Context, input CreateAgentConfigInput) (AgentConfigRecord, error) {
	if isNilID(input.ProjectID) {
		return AgentConfigRecord{}, errors.New("project id is required")
	}
	input.Definition = normalizedJSON(input.Definition)
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
	OrgID                   ID
	ProjectID               ID
	Definition              json.RawMessage
	Source                  string
	SourceFormat            string
	SourceHash              string
	ConfiguredModelID       ID
	CompiledDefinition      json.RawMessage
	CompilerVersion         string
	EffectiveDefinitionHash string
}

type AgentConfigRecord struct {
	ID                      ID              `json:"id"`
	OrgID                   ID              `json:"org_id"`
	ProjectID               ID              `json:"project_id"`
	Definition              json.RawMessage `json:"definition"`
	Source                  string          `json:"source,omitempty"`
	SourceFormat            string          `json:"source_format,omitempty"`
	SourceHash              string          `json:"source_hash,omitempty"`
	ConfiguredModelID       ID              `json:"configured_model_id"`
	CompiledDefinition      json.RawMessage `json:"compiled_definition"`
	CompilerVersion         string          `json:"compiler_version"`
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
	projectID, agentID ID,
) (AgentConfigSnapshotRecord, error) {
	if isNilID(projectID) || isNilID(agentID) {
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
	projectID, agentID ID,
	watermark int64,
) (AgentConfigSnapshotRecord, error) {
	if isNilID(projectID) || isNilID(agentID) || watermark <= 0 {
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
	projectID, configID ID,
) (AgentConfigRecord, bool, error) {
	if isNilID(projectID) || isNilID(configID) {
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
	input.Definition = normalizedJSON(input.Definition)
	input = withDefaultAgentConfigCompilation(input)
	if isNilID(input.ConfiguredModelID) {
		return AgentConfigRecord{}, errors.New("agent config configured model is required")
	}
	if err := validateAgentConfigModelContractTx(ctx, qtx, input); err != nil {
		return AgentConfigRecord{}, err
	}
	row, err := qtx.UpsertAgentConfigByHash(
		ctx,
		dbsqlc.UpsertAgentConfigByHashParams{
			OrgID:                   input.OrgID,
			ProjectID:               input.ProjectID,
			ConfiguredModelID:       input.ConfiguredModelID,
			Definition:              input.Definition,
			Source:                  input.Source,
			SourceFormat:            input.SourceFormat,
			SourceHash:              input.SourceHash,
			CompiledDefinition:      input.CompiledDefinition,
			CompilerVersion:         input.CompilerVersion,
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
				SourceFormat:            input.SourceFormat,
				SourceHash:              input.SourceHash,
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

func validateAgentConfigModelContractTx(ctx context.Context, qtx *dbsqlc.Queries, input CreateAgentConfigInput) error {
	contract, err := agentconfig.RuntimeContractFromCompiled(
		input.CompiledDefinition,
		input.CompilerVersion,
		input.EffectiveDefinitionHash,
	)
	if err != nil {
		return fmt.Errorf("validate agent config runtime contract: %w", err)
	}
	if contract.Model.ConfiguredModelID == "" {
		return errors.New("agent config compiled model must include configured_model_id")
	}
	compiledModelID, err := ParseID(contract.Model.ConfiguredModelID)
	if err != nil {
		return fmt.Errorf("parse compiled configured model id: %w", err)
	}
	if compiledModelID != input.ConfiguredModelID {
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
	return sameJSON(record.Definition, input.Definition) &&
		record.Source == input.Source &&
		record.SourceFormat == input.SourceFormat &&
		record.SourceHash == input.SourceHash &&
		record.ConfiguredModelID == input.ConfiguredModelID &&
		sameJSON(record.CompiledDefinition, input.CompiledDefinition) &&
		record.CompilerVersion == input.CompilerVersion &&
		record.EffectiveDefinitionHash == input.EffectiveDefinitionHash
}

func loadAgentConfigTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, configID ID,
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

func lockAgentConfigModelForUseTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	config AgentConfigRecord,
) error {
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
	projectID ID,
	compiledDefinition json.RawMessage,
	compilerVersion, definitionHash string,
) error {
	if isNilID(projectID) {
		return errors.New("project id is required")
	}
	project, err := loadProjectTx(ctx, s.q, projectID)
	if err != nil {
		return err
	}
	contract, err := agentconfig.RuntimeContractFromCompiled(compiledDefinition, compilerVersion, definitionHash)
	if err != nil {
		return err
	}
	for index, source := range contract.MachineSources {
		if err := validateRuntimeMachineSource(index, source); err != nil {
			return err
		}
		if source.MachineID != "" {
			machineID, err := publicid.Decode(publicid.KindMachine, source.MachineID)
			if err != nil {
				return fmt.Errorf("machine_sources[%d].machine_id: %w", index, err)
			}
			grant, err := s.q.GetActiveProjectMachineGrantForMachine(
				ctx,
				dbsqlc.GetActiveProjectMachineGrantForMachineParams{ProjectID: projectID, MachineID: machineID},
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
		if source.MachinePoolID == "" {
			continue
		}
		machinePoolID, err := publicid.Decode(publicid.KindMachinePool, source.MachinePoolID)
		if err != nil {
			return fmt.Errorf("machine_sources[%d].machine_pool_id: %w", index, err)
		}
		poolGrant, err := s.q.GetPoolGrantConfigValidationContext(
			ctx,
			dbsqlc.GetPoolGrantConfigValidationContextParams{
				ProjectID:     projectID,
				MachinePoolID: machinePoolID,
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
			intPtrFromSQLC(poolGrant.DefaultMachineCpu),
			intPtrFromSQLC(poolGrant.DefaultMachineMemoryMb),
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
			intPtrFromSQLC(poolGrant.GrantDefaultMachineCpu),
			intPtrFromSQLC(poolGrant.GrantDefaultMachineMemoryMb),
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
					MaxTotalCPU:        intPtrFromSQLC(poolGrant.PoolMaxTotalCpu),
					MaxTotalMemoryMB:   intPtrFromSQLC(poolGrant.PoolMaxTotalMemoryMb),
					MinMachineCPU:      intPtrFromSQLC(poolGrant.PoolMinMachineCpu),
					MinMachineMemoryMB: intPtrFromSQLC(poolGrant.PoolMinMachineMemoryMb),
					MaxMachineCPU:      intPtrFromSQLC(poolGrant.PoolMaxMachineCpu),
					MaxMachineMemoryMB: intPtrFromSQLC(poolGrant.PoolMaxMachineMemoryMb),
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
				intPtrFromSQLC(poolGrant.PoolMinMachineCpu),
				intPtrFromSQLC(poolGrant.GrantMinMachineCpu),
			),
			MinMachineMemoryMB: effectivePoolGrantMinimum(
				intPtrFromSQLC(poolGrant.PoolMinMachineMemoryMb),
				intPtrFromSQLC(poolGrant.GrantMinMachineMemoryMb),
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

func (s *Store) ResolveAgentConfigMachineName(ctx context.Context, projectID ID, machineName string) (ID, error) {
	if isNilID(projectID) || machineName == "" {
		return NilID, errors.New("project and machine name are required")
	}
	row, err := s.q.GetActiveProjectMachineGrantForMachineName(
		ctx,
		dbsqlc.GetActiveProjectMachineGrantForMachineNameParams{ProjectID: projectID, MachineName: machineName},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return NilID, storeerr.ErrNotFound
	}
	if err != nil {
		return NilID, fmt.Errorf("resolve machine name: %w", err)
	}
	return row.MachineID, nil
}

func (s *Store) ResolveAgentConfigMachinePoolName(
	ctx context.Context,
	orgID, projectID ID,
	machinePoolName string,
) (ID, error) {
	if isNilID(orgID) || isNilID(projectID) || machinePoolName == "" {
		return NilID, errors.New("org, project, and machine pool name are required")
	}
	machinePoolID, ok, err := resolveMachinePoolName(ctx, s.q, orgID, machinePoolName)
	if err != nil {
		return NilID, err
	}
	if !ok {
		return NilID, storeerr.ErrNotFound
	}
	if _, err := s.GetActiveProjectMachinePoolGrantForMachinePool(ctx, projectID, machinePoolID); err != nil {
		return NilID, err
	}
	return machinePoolID, nil
}

func withDefaultAgentConfigCompilation(input CreateAgentConfigInput) CreateAgentConfigInput {
	if len(input.CompiledDefinition) == 0 {
		input.CompiledDefinition = input.Definition
	}
	if input.SourceFormat == "" {
		input.SourceFormat = string(agentconfig.SourceFormatYAML)
	}
	if input.SourceHash == "" {
		input.SourceHash = agentConfigSourceHash(input.Source)
	}
	input.CompiledDefinition = normalizedJSON(input.CompiledDefinition)
	if len(input.Definition) == 0 || string(input.Definition) == "null" {
		input.Definition = input.CompiledDefinition
	}
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
