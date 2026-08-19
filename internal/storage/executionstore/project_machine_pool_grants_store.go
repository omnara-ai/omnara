package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/patch"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"

	"github.com/omnara-ai/omnara/internal/resourcemeta"
)

type ProjectMachinePoolGrantRecord struct {
	ID                                   ID              `json:"id"`
	OrgID                                ID              `json:"org_id"`
	ProjectID                            ID              `json:"project_id"`
	MachinePoolID                        ID              `json:"machine_pool_id"`
	Description                          string          `json:"description"`
	DefaultMachineCPU                    *int            `json:"default_machine_cpu,omitempty"`
	DefaultMachineMemoryMB               *int            `json:"default_machine_memory_mb,omitempty"`
	DefaultMachineEnvOverlay             json.RawMessage `json:"default_machine_env_overlay"`
	DefaultMachineSecretEnvOverlay       json.RawMessage `json:"default_machine_secret_env_overlay"`
	DefaultMachineProviderOptionsOverlay json.RawMessage `json:"default_machine_provider_options_overlay"`
	DefaultCwd                           string          `json:"default_cwd"`
	MaxTotalMachines                     *int            `json:"max_total_machines,omitempty"`
	MaxTotalCPU                          *int            `json:"max_total_cpu,omitempty"`
	MaxTotalMemoryMB                     *int            `json:"max_total_memory_mb,omitempty"`
	MinMachineCPU                        *int            `json:"min_machine_cpu,omitempty"`
	MinMachineMemoryMB                   *int            `json:"min_machine_memory_mb,omitempty"`
	MaxMachineCPU                        *int            `json:"max_machine_cpu,omitempty"`
	MaxMachineMemoryMB                   *int            `json:"max_machine_memory_mb,omitempty"`
	DeleteAfterIdleMinutes               *int            `json:"delete_after_idle_minutes,omitempty"`
	IdempotencyKey                       string          `json:"idempotency_key,omitempty"`
	Metadata                             json.RawMessage `json:"metadata"`
	CreatedAt                            time.Time       `json:"created_at"`
	UpdatedAt                            time.Time       `json:"updated_at"`
	Created                              bool            `json:"-"`
}

type ListProjectMachinePoolGrantsInput struct {
	OrgID     ID
	ProjectID ID
	Limit     int
	List      listing.Options
}

type MachinePoolSummaryRecord struct {
	ID             ID              `json:"id"`
	OrgID          ID              `json:"org_id"`
	Name           string          `json:"name"`
	ManagementKind management.Kind `json:"management_kind"`
	Description    string          `json:"description,omitempty"`
	Provider       string          `json:"provider"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type ProjectMachinePoolGrantListRecord struct {
	Grant       ProjectMachinePoolGrantRecord
	MachinePool MachinePoolSummaryRecord
}

type ListProjectMachinePoolGrantsResult struct {
	Grants  []ProjectMachinePoolGrantListRecord
	HasMore bool
	Next    listing.Cursor
}

type DeleteProjectMachinePoolGrantResult struct {
	Grant    ProjectMachinePoolGrantRecord
	Machines []MachineRecord
}

type CreateProjectMachinePoolGrantInput struct {
	OrgID                                ID
	ProjectID                            ID
	MachinePoolID                        ID
	Description                          string
	DefaultMachineCPU                    *int
	DefaultMachineMemoryMB               *int
	DefaultMachineEnvOverlay             json.RawMessage
	DefaultMachineSecretEnvOverlay       json.RawMessage
	DefaultMachineProviderOptionsOverlay json.RawMessage
	DefaultCwd                           string
	MaxTotalMachines                     *int
	MaxTotalCPU                          *int
	MaxTotalMemoryMB                     *int
	MinMachineCPU                        *int
	MinMachineMemoryMB                   *int
	MaxMachineCPU                        *int
	MaxMachineMemoryMB                   *int
	DeleteAfterIdleMinutes               *int
	IdempotencyKey                       string
	Metadata                             resourcemeta.Metadata
}

type UpdateProjectMachinePoolGrantInput struct {
	OrgID                                ID
	ProjectID                            ID
	ID                                   ID
	Description                          *string
	DefaultMachineCPU                    patch.NullableInt
	DefaultMachineMemoryMB               patch.NullableInt
	DefaultMachineEnvOverlay             *json.RawMessage
	DefaultMachineSecretEnvOverlay       *json.RawMessage
	DefaultMachineProviderOptionsOverlay *json.RawMessage
	DefaultCwd                           *string
	MaxTotalMachines                     patch.NullableInt
	MaxTotalCPU                          patch.NullableInt
	MaxTotalMemoryMB                     patch.NullableInt
	MinMachineCPU                        patch.NullableInt
	MinMachineMemoryMB                   patch.NullableInt
	MaxMachineCPU                        patch.NullableInt
	MaxMachineMemoryMB                   patch.NullableInt
	DeleteAfterIdleMinutes               patch.NullableInt
	Metadata                             *resourcemeta.Metadata
}

type projectMachinePoolGrantConfig struct {
	DefaultMachineCPU                    *int
	DefaultMachineMemoryMB               *int
	DefaultMachineEnvOverlay             json.RawMessage
	DefaultMachineSecretEnvOverlay       json.RawMessage
	DefaultMachineProviderOptionsOverlay json.RawMessage
	DefaultCwd                           string
	MaxTotalMachines                     *int
	MaxTotalCPU                          *int
	MaxTotalMemoryMB                     *int
	MinMachineCPU                        *int
	MinMachineMemoryMB                   *int
	MaxMachineCPU                        *int
	MaxMachineMemoryMB                   *int
	DeleteAfterIdleMinutes               *int
}

func projectMachinePoolGrantConfigFromCreateInput(
	input CreateProjectMachinePoolGrantInput,
) projectMachinePoolGrantConfig {
	return projectMachinePoolGrantConfig{
		DefaultMachineCPU:                    input.DefaultMachineCPU,
		DefaultMachineMemoryMB:               input.DefaultMachineMemoryMB,
		DefaultMachineEnvOverlay:             input.DefaultMachineEnvOverlay,
		DefaultMachineSecretEnvOverlay:       input.DefaultMachineSecretEnvOverlay,
		DefaultMachineProviderOptionsOverlay: input.DefaultMachineProviderOptionsOverlay,
		DefaultCwd:                           input.DefaultCwd,
		MaxTotalMachines:                     input.MaxTotalMachines,
		MaxTotalCPU:                          input.MaxTotalCPU,
		MaxTotalMemoryMB:                     input.MaxTotalMemoryMB,
		MinMachineCPU:                        input.MinMachineCPU,
		MinMachineMemoryMB:                   input.MinMachineMemoryMB,
		MaxMachineCPU:                        input.MaxMachineCPU,
		MaxMachineMemoryMB:                   input.MaxMachineMemoryMB,
		DeleteAfterIdleMinutes:               input.DeleteAfterIdleMinutes,
	}
}

func projectMachinePoolGrantConfigFromRecord(
	record ProjectMachinePoolGrantRecord,
) projectMachinePoolGrantConfig {
	return projectMachinePoolGrantConfig{
		DefaultMachineCPU:                    cloneIntPtr(record.DefaultMachineCPU),
		DefaultMachineMemoryMB:               cloneIntPtr(record.DefaultMachineMemoryMB),
		DefaultMachineEnvOverlay:             record.DefaultMachineEnvOverlay,
		DefaultMachineSecretEnvOverlay:       record.DefaultMachineSecretEnvOverlay,
		DefaultMachineProviderOptionsOverlay: record.DefaultMachineProviderOptionsOverlay,
		DefaultCwd:                           record.DefaultCwd,
		MaxTotalMachines:                     cloneIntPtr(record.MaxTotalMachines),
		MaxTotalCPU:                          cloneIntPtr(record.MaxTotalCPU),
		MaxTotalMemoryMB:                     cloneIntPtr(record.MaxTotalMemoryMB),
		MinMachineCPU:                        cloneIntPtr(record.MinMachineCPU),
		MinMachineMemoryMB:                   cloneIntPtr(record.MinMachineMemoryMB),
		MaxMachineCPU:                        cloneIntPtr(record.MaxMachineCPU),
		MaxMachineMemoryMB:                   cloneIntPtr(record.MaxMachineMemoryMB),
		DeleteAfterIdleMinutes:               cloneIntPtr(record.DeleteAfterIdleMinutes),
	}
}

func applyProjectMachinePoolGrantPatch(
	config *projectMachinePoolGrantConfig,
	input UpdateProjectMachinePoolGrantInput,
) {
	if input.DefaultMachineCPU.Set {
		config.DefaultMachineCPU = cloneIntPtr(input.DefaultMachineCPU.Value)
	}
	if input.DefaultMachineMemoryMB.Set {
		config.DefaultMachineMemoryMB = cloneIntPtr(input.DefaultMachineMemoryMB.Value)
	}
	if input.DefaultMachineEnvOverlay != nil {
		config.DefaultMachineEnvOverlay = *input.DefaultMachineEnvOverlay
	}
	if input.DefaultMachineSecretEnvOverlay != nil {
		config.DefaultMachineSecretEnvOverlay = *input.DefaultMachineSecretEnvOverlay
	}
	if input.DefaultMachineProviderOptionsOverlay != nil {
		config.DefaultMachineProviderOptionsOverlay = *input.DefaultMachineProviderOptionsOverlay
	}
	if input.DefaultCwd != nil {
		config.DefaultCwd = *input.DefaultCwd
	}
	if input.MaxTotalMachines.Set {
		config.MaxTotalMachines = cloneIntPtr(input.MaxTotalMachines.Value)
	}
	if input.MaxTotalCPU.Set {
		config.MaxTotalCPU = cloneIntPtr(input.MaxTotalCPU.Value)
	}
	if input.MaxTotalMemoryMB.Set {
		config.MaxTotalMemoryMB = cloneIntPtr(input.MaxTotalMemoryMB.Value)
	}
	if input.MinMachineCPU.Set {
		config.MinMachineCPU = cloneIntPtr(input.MinMachineCPU.Value)
	}
	if input.MinMachineMemoryMB.Set {
		config.MinMachineMemoryMB = cloneIntPtr(input.MinMachineMemoryMB.Value)
	}
	if input.MaxMachineCPU.Set {
		config.MaxMachineCPU = cloneIntPtr(input.MaxMachineCPU.Value)
	}
	if input.MaxMachineMemoryMB.Set {
		config.MaxMachineMemoryMB = cloneIntPtr(input.MaxMachineMemoryMB.Value)
	}
	if input.DeleteAfterIdleMinutes.Set {
		config.DeleteAfterIdleMinutes = cloneIntPtr(input.DeleteAfterIdleMinutes.Value)
	}
}

func normalizeProjectMachinePoolGrantConfig(
	config projectMachinePoolGrantConfig,
) (projectMachinePoolGrantConfig, MachineProvisioningOverlay, MachineEnvironmentOverlay, error) {
	if config.MaxTotalMachines != nil && (*config.MaxTotalMachines < 0 || *config.MaxTotalMachines > math.MaxInt32) {
		return config, MachineProvisioningOverlay{}, MachineEnvironmentOverlay{}, fmt.Errorf(
			"pool grant max_total_machines must be between 0 and %d when set",
			math.MaxInt32,
		)
	}
	if config.MaxTotalCPU != nil && (*config.MaxTotalCPU < 0 || *config.MaxTotalCPU > math.MaxInt32) {
		return config, MachineProvisioningOverlay{}, MachineEnvironmentOverlay{}, fmt.Errorf(
			"pool grant max_total_cpu must be between 0 and %d when set",
			math.MaxInt32,
		)
	}
	if config.MaxTotalMemoryMB != nil && (*config.MaxTotalMemoryMB < 0 || *config.MaxTotalMemoryMB > math.MaxInt32) {
		return config, MachineProvisioningOverlay{}, MachineEnvironmentOverlay{}, fmt.Errorf(
			"pool grant max_total_memory_mb must be between 0 and %d when set",
			math.MaxInt32,
		)
	}
	if config.MinMachineCPU != nil && (*config.MinMachineCPU < 0 || *config.MinMachineCPU > math.MaxInt32) {
		return config, MachineProvisioningOverlay{}, MachineEnvironmentOverlay{}, fmt.Errorf(
			"pool grant min_machine_cpu must be between 0 and %d",
			math.MaxInt32,
		)
	}
	if config.MinMachineMemoryMB != nil &&
		(*config.MinMachineMemoryMB < 0 || *config.MinMachineMemoryMB > math.MaxInt32) {
		return config, MachineProvisioningOverlay{}, MachineEnvironmentOverlay{}, fmt.Errorf(
			"pool grant min_machine_memory_mb must be between 0 and %d",
			math.MaxInt32,
		)
	}
	if config.MaxMachineCPU != nil && (*config.MaxMachineCPU <= 0 || *config.MaxMachineCPU > math.MaxInt32) {
		return config, MachineProvisioningOverlay{}, MachineEnvironmentOverlay{}, fmt.Errorf(
			"pool grant max_machine_cpu must be between 1 and %d when set",
			math.MaxInt32,
		)
	}
	if config.MaxMachineMemoryMB != nil &&
		(*config.MaxMachineMemoryMB <= 0 || *config.MaxMachineMemoryMB > math.MaxInt32) {
		return config, MachineProvisioningOverlay{}, MachineEnvironmentOverlay{}, fmt.Errorf(
			"pool grant max_machine_memory_mb must be between 1 and %d when set",
			math.MaxInt32,
		)
	}
	if config.DeleteAfterIdleMinutes != nil &&
		(*config.DeleteAfterIdleMinutes != 0 &&
			(*config.DeleteAfterIdleMinutes < 5 || *config.DeleteAfterIdleMinutes > math.MaxInt32)) {
		return config, MachineProvisioningOverlay{}, MachineEnvironmentOverlay{}, fmt.Errorf(
			"pool grant delete_after_idle_minutes must be 0 or between 5 and %d when set",
			math.MaxInt32,
		)
	}
	if strings.ContainsRune(config.DefaultCwd, 0) {
		return config, MachineProvisioningOverlay{}, MachineEnvironmentOverlay{}, errors.New(
			"pool grant default_cwd cannot contain NUL",
		)
	}
	provisioningOverlay, err := machineProvisioningOverlayFromColumns(
		config.DefaultMachineCPU,
		config.DefaultMachineMemoryMB,
		config.DefaultMachineProviderOptionsOverlay,
	)
	if err != nil {
		return config, MachineProvisioningOverlay{}, MachineEnvironmentOverlay{}, fmt.Errorf(
			"project machine pool grant default_machine fields: %w", err)
	}
	environmentOverlay, err := machineEnvironmentOverlayFromColumns(
		config.DefaultMachineEnvOverlay,
		config.DefaultMachineSecretEnvOverlay,
	)
	if err != nil {
		return config, MachineProvisioningOverlay{}, MachineEnvironmentOverlay{}, fmt.Errorf(
			"project machine pool grant default_machine fields: %w", err)
	}
	config.DefaultMachineEnvOverlay = normalizedJSON(config.DefaultMachineEnvOverlay)
	config.DefaultMachineSecretEnvOverlay = normalizedJSON(config.DefaultMachineSecretEnvOverlay)
	config.DefaultMachineProviderOptionsOverlay = normalizedJSON(config.DefaultMachineProviderOptionsOverlay)
	return config, provisioningOverlay, environmentOverlay, nil
}

func (s *Store) validateProjectMachinePoolGrantAgainstPoolTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, projectID ID,
	pool MachinePoolRecord,
	config projectMachinePoolGrantConfig,
	provisioningOverlay MachineProvisioningOverlay,
	environmentOverlay MachineEnvironmentOverlay,
) error {
	if err := validateProjectMachinePoolGrantStaticPolicy(config, pool); err != nil {
		return storeerr.InvalidRequest(err)
	}
	poolDefaultProvisioning, err := MachineProvisioningFromDefaults(
		pool.DefaultMachineCPU,
		pool.DefaultMachineMemoryMB,
		pool.DefaultMachineProviderOptions,
	)
	if err != nil {
		return fmt.Errorf("machine pool default_machine fields: %w", err)
	}
	poolDefaultEnvironment, err := MachineEnvironmentFromColumns(
		pool.DefaultMachineEnv,
		pool.DefaultMachineSecretEnv,
	)
	if err != nil {
		return fmt.Errorf("machine pool default_machine fields: %w", err)
	}
	projectProvisioning, err := s.ResolveMachineProvisioning(
		pool.Provider,
		MachinePoolProviderPolicy{
			DefaultProvisioning: poolDefaultProvisioning,
			ResourceLimits: MachineResourceLimits{
				MaxTotalCPU:        pool.MaxTotalCPU,
				MaxTotalMemoryMB:   pool.MaxTotalMemoryMB,
				MinMachineCPU:      pool.MinMachineCPU,
				MinMachineMemoryMB: pool.MinMachineMemoryMB,
				MaxMachineCPU:      pool.MaxMachineCPU,
				MaxMachineMemoryMB: pool.MaxMachineMemoryMB,
			},
			ProviderConfig: pool.ProviderConfig,
		},
		provisioningOverlay,
		MachineProvisioningOverlay{},
	)
	if err != nil {
		return fmt.Errorf("project machine pool grant default_machine fields: %w", err)
	}
	_, err = resolveMachineEnvironmentTx(
		ctx,
		qtx,
		orgID,
		projectID,
		poolDefaultEnvironment,
		environmentOverlay,
	)
	if err != nil {
		return fmt.Errorf("project machine pool grant default_machine fields: %w", err)
	}
	perMachineLimits := resolveProjectMachinePoolGrantPerMachineLimits(pool, config)
	projectMachineResources, err := resourcesFromMachineProvisioning(projectProvisioning)
	if err != nil {
		return fmt.Errorf("resolved project machine pool grant config: %w", err)
	}
	if err := validateMachineResourcesWithinPerMachineLimits(projectMachineResources, perMachineLimits); err != nil {
		return storeerr.InvalidRequest(fmt.Errorf("resolved project machine pool grant config: %w", err))
	}
	return nil
}

func (s *Store) CreateProjectMachinePoolGrant(
	ctx context.Context,
	input CreateProjectMachinePoolGrantInput,
) (ProjectMachinePoolGrantRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.ProjectID) || isNilID(input.MachinePoolID) {
		return ProjectMachinePoolGrantRecord{}, storeerr.InvalidRequest(errors.New(
			"pool grant org, project, and machine pool are required",
		))
	}
	config, grantProvisioningOverlay, grantEnvironmentOverlay, err := normalizeProjectMachinePoolGrantConfig(
		projectMachinePoolGrantConfigFromCreateInput(input),
	)
	if err != nil {
		return ProjectMachinePoolGrantRecord{}, storeerr.InvalidRequest(err)
	}
	metadata, err := metadataColumn(input.Metadata, "project machine pool grant metadata")
	if err != nil {
		return ProjectMachinePoolGrantRecord{}, storeerr.InvalidRequest(err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectMachinePoolGrantRecord{}, fmt.Errorf("begin create project machine pool grant: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if input.IdempotencyKey != "" {
		replay, replayErr := qtx.GetProjectMachinePoolGrantByIdempotency(
			ctx,
			dbsqlc.GetProjectMachinePoolGrantByIdempotencyParams{
				OrgID:          input.OrgID,
				ProjectID:      input.ProjectID,
				IdempotencyKey: input.IdempotencyKey,
			},
		)
		if replayErr == nil {
			grant := projectMachinePoolGrantFromIdempotency(replay)
			if !sameProjectMachinePoolGrantCreateIntent(grant, input, config) {
				return ProjectMachinePoolGrantRecord{}, storeerr.ErrIdempotencyConflict
			}
			if _, err := qtx.LockMachinePoolForUpdate(
				ctx,
				dbsqlc.LockMachinePoolForUpdateParams{OrgID: input.OrgID, ID: grant.MachinePoolID},
			); err != nil {
				return ProjectMachinePoolGrantRecord{}, fmt.Errorf(
					"lock machine pool for project pool grant replay: %w",
					err,
				)
			}
			if err := tx.Commit(ctx); err != nil {
				return ProjectMachinePoolGrantRecord{}, fmt.Errorf(
					"commit replay create project machine pool grant: %w",
					err,
				)
			}
			return grant, nil
		}
		if !errors.Is(replayErr, pgx.ErrNoRows) {
			return ProjectMachinePoolGrantRecord{}, fmt.Errorf(
				"get project machine pool grant by idempotency: %w",
				replayErr,
			)
		}
	}
	pool, err := qtx.LockMachinePoolForUpdate(
		ctx,
		dbsqlc.LockMachinePoolForUpdateParams{OrgID: input.OrgID, ID: input.MachinePoolID},
	)
	if err != nil {
		return ProjectMachinePoolGrantRecord{}, fmt.Errorf("get machine pool for project pool grant: %w", err)
	}
	poolRecord := machinePoolRecordFromSQLC(pool)
	if err := s.validateProjectMachinePoolGrantAgainstPoolTx(
		ctx,
		qtx,
		input.OrgID,
		input.ProjectID,
		poolRecord,
		config,
		grantProvisioningOverlay,
		grantEnvironmentOverlay,
	); err != nil {
		return ProjectMachinePoolGrantRecord{}, err
	}
	row, err := qtx.UpsertProjectMachinePoolGrant(
		ctx,
		dbsqlc.UpsertProjectMachinePoolGrantParams{
			OrgID:                                input.OrgID,
			ProjectID:                            input.ProjectID,
			MachinePoolID:                        input.MachinePoolID,
			Description:                          input.Description,
			DefaultMachineCpu:                    sqlcInt32Ptr(config.DefaultMachineCPU),
			DefaultMachineMemoryMb:               sqlcInt32Ptr(config.DefaultMachineMemoryMB),
			DefaultMachineEnvOverlay:             config.DefaultMachineEnvOverlay,
			DefaultMachineSecretEnvOverlay:       config.DefaultMachineSecretEnvOverlay,
			DefaultMachineProviderOptionsOverlay: config.DefaultMachineProviderOptionsOverlay,
			DefaultCwd:                           config.DefaultCwd,
			MaxTotalMachines:                     sqlcInt32Ptr(config.MaxTotalMachines),
			MaxTotalCpu:                          sqlcInt32Ptr(config.MaxTotalCPU),
			MaxTotalMemoryMb:                     sqlcInt32Ptr(config.MaxTotalMemoryMB),
			MinMachineCpu:                        sqlcInt32Ptr(config.MinMachineCPU),
			MinMachineMemoryMb:                   sqlcInt32Ptr(config.MinMachineMemoryMB),
			MaxMachineCpu:                        sqlcInt32Ptr(config.MaxMachineCPU),
			MaxMachineMemoryMb:                   sqlcInt32Ptr(config.MaxMachineMemoryMB),
			DeleteAfterIdleMinutes:               sqlcInt32Ptr(config.DeleteAfterIdleMinutes),
			IdempotencyKey:                       sqlcTextFromEmpty(input.IdempotencyKey),
			Metadata:                             metadata,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if _, poolErr := qtx.GetMachinePool(ctx, dbsqlc.GetMachinePoolParams{
				OrgID: input.OrgID,
				ID:    input.MachinePoolID,
			}); poolErr != nil {
				return ProjectMachinePoolGrantRecord{}, fmt.Errorf(
					"get machine pool for project pool grant: %w",
					poolErr,
				)
			}
			return ProjectMachinePoolGrantRecord{}, storeerr.Tag(storeerr.ErrConflict, errors.New(
				"a grant for this machine pool already exists on this project",
			))
		}
		if storeutil.IsUniqueViolation(err) {
			return ProjectMachinePoolGrantRecord{}, storeerr.ErrIdempotencyConflict
		}
		return ProjectMachinePoolGrantRecord{}, fmt.Errorf("upsert project machine pool grant: %w", err)
	}
	grant := projectMachinePoolGrantFromUpsert(row)
	grant.Created = true
	if err := tx.Commit(ctx); err != nil {
		return ProjectMachinePoolGrantRecord{}, fmt.Errorf("commit create project machine pool grant: %w", err)
	}
	return grant, nil
}

func sameProjectMachinePoolGrantCreateIntent(
	grant ProjectMachinePoolGrantRecord,
	input CreateProjectMachinePoolGrantInput,
	config projectMachinePoolGrantConfig,
) bool {
	return grant.OrgID == input.OrgID &&
		grant.ProjectID == input.ProjectID &&
		grant.MachinePoolID == input.MachinePoolID &&
		grant.Description == input.Description &&
		sameIntPtr(grant.DefaultMachineCPU, config.DefaultMachineCPU) &&
		sameIntPtr(grant.DefaultMachineMemoryMB, config.DefaultMachineMemoryMB) &&
		sameJSON(grant.DefaultMachineEnvOverlay, config.DefaultMachineEnvOverlay) &&
		sameJSON(grant.DefaultMachineSecretEnvOverlay, config.DefaultMachineSecretEnvOverlay) &&
		sameJSON(grant.DefaultMachineProviderOptionsOverlay, config.DefaultMachineProviderOptionsOverlay) &&
		grant.DefaultCwd == config.DefaultCwd &&
		sameIntPtr(grant.MaxTotalMachines, config.MaxTotalMachines) &&
		sameIntPtr(grant.MaxTotalCPU, config.MaxTotalCPU) &&
		sameIntPtr(grant.MaxTotalMemoryMB, config.MaxTotalMemoryMB) &&
		sameIntPtr(grant.MinMachineCPU, config.MinMachineCPU) &&
		sameIntPtr(grant.MinMachineMemoryMB, config.MinMachineMemoryMB) &&
		sameIntPtr(grant.MaxMachineCPU, config.MaxMachineCPU) &&
		sameIntPtr(grant.MaxMachineMemoryMB, config.MaxMachineMemoryMB) &&
		sameIntPtr(grant.DeleteAfterIdleMinutes, config.DeleteAfterIdleMinutes) &&
		sameMetadata(grant.Metadata, input.Metadata)
}

func (s *Store) UpdateProjectMachinePoolGrant(
	ctx context.Context,
	input UpdateProjectMachinePoolGrantInput,
) (ProjectMachinePoolGrantRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.ProjectID) || isNilID(input.ID) {
		return ProjectMachinePoolGrantRecord{}, storeerr.InvalidRequest(errors.New(
			"pool grant org, project, and id are required",
		))
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectMachinePoolGrantRecord{}, fmt.Errorf("begin update project machine pool grant: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	ref, err := qtx.GetProjectMachinePoolGrant(
		ctx,
		dbsqlc.GetProjectMachinePoolGrantParams{OrgID: input.OrgID, ProjectID: input.ProjectID, ID: input.ID},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProjectMachinePoolGrantRecord{}, storeerr.ErrNotFound
		}
		return ProjectMachinePoolGrantRecord{}, fmt.Errorf("get project machine pool grant for update: %w", err)
	}
	pool, err := qtx.LockMachinePoolForUpdate(
		ctx,
		dbsqlc.LockMachinePoolForUpdateParams{OrgID: input.OrgID, ID: ref.MachinePoolID},
	)
	if err != nil {
		return ProjectMachinePoolGrantRecord{}, fmt.Errorf(
			"lock machine pool for project machine pool grant update: %w",
			err,
		)
	}
	// Re-read after the pool lock; grant mutations serialize on the pool row.
	row, err := qtx.GetProjectMachinePoolGrant(
		ctx,
		dbsqlc.GetProjectMachinePoolGrantParams{OrgID: input.OrgID, ProjectID: input.ProjectID, ID: input.ID},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProjectMachinePoolGrantRecord{}, storeerr.ErrNotFound
		}
		return ProjectMachinePoolGrantRecord{}, fmt.Errorf("get project machine pool grant for update: %w", err)
	}
	current := projectMachinePoolGrantFromGet(row)
	description := current.Description
	if input.Description != nil {
		description = *input.Description
	}
	metadata := current.Metadata
	if input.Metadata != nil {
		metadata, err = metadataColumn(*input.Metadata, "project machine pool grant metadata")
		if err != nil {
			return ProjectMachinePoolGrantRecord{}, storeerr.InvalidRequest(err)
		}
	}
	config := projectMachinePoolGrantConfigFromRecord(current)
	applyProjectMachinePoolGrantPatch(&config, input)
	config, provisioningOverlay, environmentOverlay, err := normalizeProjectMachinePoolGrantConfig(config)
	if err != nil {
		return ProjectMachinePoolGrantRecord{}, storeerr.InvalidRequest(err)
	}
	poolRecord := machinePoolRecordFromSQLC(pool)
	if err := s.validateProjectMachinePoolGrantAgainstPoolTx(
		ctx,
		qtx,
		input.OrgID,
		input.ProjectID,
		poolRecord,
		config,
		provisioningOverlay,
		environmentOverlay,
	); err != nil {
		return ProjectMachinePoolGrantRecord{}, err
	}
	updated, err := qtx.UpdateProjectMachinePoolGrant(
		ctx,
		dbsqlc.UpdateProjectMachinePoolGrantParams{
			Description:                          description,
			DefaultMachineCpu:                    sqlcInt32Ptr(config.DefaultMachineCPU),
			DefaultMachineMemoryMb:               sqlcInt32Ptr(config.DefaultMachineMemoryMB),
			DefaultMachineEnvOverlay:             config.DefaultMachineEnvOverlay,
			DefaultMachineSecretEnvOverlay:       config.DefaultMachineSecretEnvOverlay,
			DefaultMachineProviderOptionsOverlay: config.DefaultMachineProviderOptionsOverlay,
			DefaultCwd:                           config.DefaultCwd,
			MaxTotalMachines:                     sqlcInt32Ptr(config.MaxTotalMachines),
			MaxTotalCpu:                          sqlcInt32Ptr(config.MaxTotalCPU),
			MaxTotalMemoryMb:                     sqlcInt32Ptr(config.MaxTotalMemoryMB),
			MinMachineCpu:                        sqlcInt32Ptr(config.MinMachineCPU),
			MinMachineMemoryMb:                   sqlcInt32Ptr(config.MinMachineMemoryMB),
			MaxMachineCpu:                        sqlcInt32Ptr(config.MaxMachineCPU),
			MaxMachineMemoryMb:                   sqlcInt32Ptr(config.MaxMachineMemoryMB),
			DeleteAfterIdleMinutes:               sqlcInt32Ptr(config.DeleteAfterIdleMinutes),
			Metadata:                             metadata,
			OrgID:                                input.OrgID,
			ProjectID:                            input.ProjectID,
			ID:                                   input.ID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProjectMachinePoolGrantRecord{}, storeerr.ErrNotFound
		}
		return ProjectMachinePoolGrantRecord{}, fmt.Errorf("update project machine pool grant: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectMachinePoolGrantRecord{}, fmt.Errorf("commit update project machine pool grant: %w", err)
	}
	return projectMachinePoolGrantFromUpdate(updated), nil
}

func (s *Store) GetProjectMachinePoolGrant(
	ctx context.Context,
	orgID, projectID, id ID,
) (ProjectMachinePoolGrantRecord, error) {
	row, err := s.q.GetProjectMachinePoolGrant(
		ctx,
		dbsqlc.GetProjectMachinePoolGrantParams{OrgID: orgID, ProjectID: projectID, ID: id},
	)
	if err != nil {
		return ProjectMachinePoolGrantRecord{}, fmt.Errorf("get project machine pool grant: %w", err)
	}
	return projectMachinePoolGrantFromGet(row), nil
}

func (s *Store) GetActiveProjectMachinePoolGrantForMachinePool(
	ctx context.Context,
	projectID, machinePoolID ID,
) (ProjectMachinePoolGrantRecord, error) {
	row, err := s.q.GetActiveProjectMachinePoolGrantForMachinePool(
		ctx,
		dbsqlc.GetActiveProjectMachinePoolGrantForMachinePoolParams{ProjectID: projectID, MachinePoolID: machinePoolID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectMachinePoolGrantRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ProjectMachinePoolGrantRecord{}, fmt.Errorf(
			"get active project machine pool grant for machine pool: %w",
			err,
		)
	}
	return projectMachinePoolGrantFromActiveMachinePool(row), nil
}

func (s *Store) ListProjectMachinePoolGrants(
	ctx context.Context,
	input ListProjectMachinePoolGrantsInput,
) (ListProjectMachinePoolGrantsResult, error) {
	if isNilID(input.OrgID) || isNilID(input.ProjectID) {
		return ListProjectMachinePoolGrantsResult{}, errors.New("org and project are required")
	}
	if input.Limit <= 0 {
		return ListProjectMachinePoolGrantsResult{}, errors.New("limit must be positive")
	}
	input.List = listing.Normalize(input.List)
	if !listing.SortAllowed(input.List.SortField, "name", "created_at", "updated_at") {
		return ListProjectMachinePoolGrantsResult{}, errors.New("unsupported sort")
	}
	params := dbsqlc.ListProjectMachinePoolGrantsParams{
		OrgID:     input.OrgID,
		ProjectID: input.ProjectID,
		RowLimit:  int64(input.Limit) + 1,
		SortField: input.List.SortField, SortDesc: input.List.SortDesc, NamePattern: input.List.NamePattern,
	}
	if input.List.After.Set {
		params.CursorSet, params.CursorKey, params.CursorID = true, input.List.After.Key, input.List.After.ID
	}
	rows, err := s.q.ListProjectMachinePoolGrants(ctx, params)
	if err != nil {
		return ListProjectMachinePoolGrantsResult{}, fmt.Errorf("list project machine pool grants: %w", err)
	}
	result := ListProjectMachinePoolGrantsResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	result.Grants = make([]ProjectMachinePoolGrantListRecord, 0, len(rows))
	for _, row := range rows {
		result.Grants = append(result.Grants, ProjectMachinePoolGrantListRecord{
			Grant: projectMachinePoolGrantFromList(row),
			MachinePool: MachinePoolSummaryRecord{
				ID:             row.MachinePoolID,
				OrgID:          row.OrgID,
				Name:           row.PoolName,
				ManagementKind: management.Kind(row.PoolManagementKind),
				Description:    row.PoolDescription, Provider: row.PoolProvider,
				CreatedAt: row.PoolCreatedAt, UpdatedAt: row.PoolUpdatedAt,
			},
		})
		result.Next = listing.Cursor{Set: true, IsNull: row.SortIsNull, Key: row.SortKey, ID: row.ID}
	}
	return result, nil
}

func (s *Store) DeleteProjectMachinePoolGrant(
	ctx context.Context,
	orgID, projectID, id ID,
) (DeleteProjectMachinePoolGrantResult, error) {
	if isNilID(orgID) || isNilID(projectID) || isNilID(id) {
		return DeleteProjectMachinePoolGrantResult{}, errors.New("pool grant org, project, and id are required")
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DeleteProjectMachinePoolGrantResult{}, fmt.Errorf("begin revoke project machine pool grant: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	existing, err := qtx.GetProjectMachinePoolGrant(
		ctx,
		dbsqlc.GetProjectMachinePoolGrantParams{OrgID: orgID, ProjectID: projectID, ID: id},
	)
	if err != nil {
		return DeleteProjectMachinePoolGrantResult{}, fmt.Errorf("get project machine pool grant for delete: %w", err)
	}
	if _, err := qtx.LockMachinePoolForUpdate(
		ctx,
		dbsqlc.LockMachinePoolForUpdateParams{OrgID: orgID, ID: existing.MachinePoolID},
	); err != nil {
		return DeleteProjectMachinePoolGrantResult{}, fmt.Errorf(
			"lock machine pool for project machine pool grant delete: %w",
			err,
		)
	}
	if _, err := qtx.LockProjectMachinePoolGrantMachinesForUpdate(
		ctx,
		dbsqlc.LockProjectMachinePoolGrantMachinesForUpdateParams{
			OrgID:                     orgID,
			ProjectID:                 projectID,
			ProjectMachinePoolGrantID: &id,
		},
	); err != nil {
		return DeleteProjectMachinePoolGrantResult{}, fmt.Errorf(
			"lock project machine pool grant machines for delete: %w",
			err,
		)
	}
	machineRows, err := qtx.MarkPoolGrantMachinesDeleting(
		ctx,
		dbsqlc.MarkPoolGrantMachinesDeletingParams{
			OrgID:                     orgID,
			ProjectID:                 projectID,
			ProjectMachinePoolGrantID: id,
		},
	)
	if err != nil {
		return DeleteProjectMachinePoolGrantResult{}, fmt.Errorf("mark pool grant machines deleting: %w", err)
	}
	// Process completion joins the grant rows, so it runs before the delete;
	// deleting the pool grant cascades to its generated machine grants.
	if err := completeExecutionRevokedProcessesTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		executionRevokedProcessScope{
			projectID:                 projectID,
			projectMachinePoolGrantID: id,
		},
		"project_machine_grant_revoked",
	); err != nil {
		return DeleteProjectMachinePoolGrantResult{}, err
	}
	row, err := qtx.DeleteProjectMachinePoolGrant(
		ctx,
		dbsqlc.DeleteProjectMachinePoolGrantParams{OrgID: orgID, ProjectID: projectID, ID: id},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DeleteProjectMachinePoolGrantResult{}, storeerr.ErrNotFound
		}
		return DeleteProjectMachinePoolGrantResult{}, fmt.Errorf("delete project machine pool grant: %w", err)
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "delete project machine pool grant"); err != nil {
		return DeleteProjectMachinePoolGrantResult{}, err
	}
	result := DeleteProjectMachinePoolGrantResult{
		Grant:    projectMachinePoolGrantFromDelete(row),
		Machines: make([]MachineRecord, 0, len(machineRows)),
	}
	for _, machineRow := range machineRows {
		result.Machines = append(result.Machines, machineRecordFromMarkPoolGrantMachinesDeletingSQLC(machineRow))
	}
	return result, nil
}

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
