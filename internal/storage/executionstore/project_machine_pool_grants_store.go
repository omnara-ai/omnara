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
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
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
	MaxMachineCPU                        *int            `json:"max_machine_cpu,omitempty"`
	MaxMachineMemoryMB                   *int            `json:"max_machine_memory_mb,omitempty"`
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
	MaxMachineCPU                        *int
	MaxMachineMemoryMB                   *int
	IdempotencyKey                       string
	Metadata                             json.RawMessage
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
	if input.MaxTotalMachines != nil && (*input.MaxTotalMachines < 0 || *input.MaxTotalMachines > math.MaxInt32) {
		return ProjectMachinePoolGrantRecord{}, storeerr.InvalidRequest(fmt.Errorf(
			"pool grant max_total_machines must be between 0 and %d when set",
			math.MaxInt32,
		))

	}
	if input.MaxTotalCPU != nil && (*input.MaxTotalCPU < 0 || *input.MaxTotalCPU > math.MaxInt32) {
		return ProjectMachinePoolGrantRecord{}, storeerr.InvalidRequest(fmt.Errorf(
			"pool grant max_total_cpu must be between 0 and %d when set",
			math.MaxInt32,
		))

	}
	if input.MaxTotalMemoryMB != nil && (*input.MaxTotalMemoryMB < 0 || *input.MaxTotalMemoryMB > math.MaxInt32) {
		return ProjectMachinePoolGrantRecord{}, storeerr.InvalidRequest(fmt.Errorf(
			"pool grant max_total_memory_mb must be between 0 and %d when set",
			math.MaxInt32,
		))

	}
	if input.MaxMachineCPU != nil && (*input.MaxMachineCPU <= 0 || *input.MaxMachineCPU > math.MaxInt32) {
		return ProjectMachinePoolGrantRecord{}, storeerr.InvalidRequest(fmt.Errorf(
			"pool grant max_machine_cpu must be between 1 and %d when set",
			math.MaxInt32,
		))

	}
	if input.MaxMachineMemoryMB != nil &&
		(*input.MaxMachineMemoryMB <= 0 || *input.MaxMachineMemoryMB > math.MaxInt32) {
		return ProjectMachinePoolGrantRecord{}, storeerr.InvalidRequest(fmt.Errorf(
			"pool grant max_machine_memory_mb must be between 1 and %d when set",
			math.MaxInt32,
		))

	}
	if strings.ContainsRune(input.DefaultCwd, 0) {
		return ProjectMachinePoolGrantRecord{}, storeerr.InvalidRequest(
			errors.New("pool grant default_cwd cannot contain NUL"),
		)
	}
	grantProvisioningOverlay, err := machineProvisioningOverlayFromColumns(
		input.DefaultMachineCPU,
		input.DefaultMachineMemoryMB,
		input.DefaultMachineProviderOptionsOverlay,
	)
	if err != nil {
		return ProjectMachinePoolGrantRecord{}, storeerr.InvalidRequest(
			fmt.Errorf("project machine pool grant default_machine fields: %w", err))

	}
	grantEnvironmentOverlay, err := machineEnvironmentOverlayFromColumns(
		input.DefaultMachineEnvOverlay,
		input.DefaultMachineSecretEnvOverlay,
	)
	if err != nil {
		return ProjectMachinePoolGrantRecord{}, storeerr.InvalidRequest(
			fmt.Errorf("project machine pool grant default_machine fields: %w", err))

	}
	input.DefaultMachineEnvOverlay = normalizedJSON(input.DefaultMachineEnvOverlay)
	input.DefaultMachineSecretEnvOverlay = normalizedJSON(input.DefaultMachineSecretEnvOverlay)
	input.DefaultMachineProviderOptionsOverlay = normalizedJSON(input.DefaultMachineProviderOptionsOverlay)
	input.Metadata, err = normalizedJSONObject(input.Metadata, "project machine pool grant metadata")
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
			if !sameProjectMachinePoolGrantCreateInput(grant, input) {
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
	if err := validateProjectMachinePoolGrantStaticPolicy(input, poolRecord); err != nil {
		return ProjectMachinePoolGrantRecord{}, storeerr.InvalidRequest(err)
	}
	poolDefaultProvisioning, err := MachineProvisioningFromDefaults(
		poolRecord.DefaultMachineCPU,
		poolRecord.DefaultMachineMemoryMB,
		poolRecord.DefaultMachineProviderOptions,
	)
	if err != nil {
		return ProjectMachinePoolGrantRecord{}, fmt.Errorf("machine pool default_machine fields: %w", err)
	}
	poolDefaultEnvironment, err := MachineEnvironmentFromColumns(
		poolRecord.DefaultMachineEnv,
		poolRecord.DefaultMachineSecretEnv,
	)
	if err != nil {
		return ProjectMachinePoolGrantRecord{}, fmt.Errorf("machine pool default_machine fields: %w", err)
	}
	projectProvisioning, err := s.ResolveMachineProvisioning(
		poolRecord.Provider,
		MachinePoolProviderPolicy{
			DefaultProvisioning: poolDefaultProvisioning,
			ResourceLimits: MachineResourceLimits{
				MaxTotalCPU:        poolRecord.MaxTotalCPU,
				MaxTotalMemoryMB:   poolRecord.MaxTotalMemoryMB,
				MaxMachineCPU:      poolRecord.MaxMachineCPU,
				MaxMachineMemoryMB: poolRecord.MaxMachineMemoryMB,
			},
			ProviderConfig: poolRecord.ProviderConfig,
		},
		grantProvisioningOverlay,
		MachineProvisioningOverlay{},
	)
	if err != nil {
		return ProjectMachinePoolGrantRecord{}, fmt.Errorf("project machine pool grant default_machine fields: %w", err)
	}
	_, err = resolveMachineEnvironmentTx(
		ctx,
		qtx,
		input.OrgID,
		input.ProjectID,
		poolDefaultEnvironment,
		grantEnvironmentOverlay,
	)
	if err != nil {
		return ProjectMachinePoolGrantRecord{}, fmt.Errorf("project machine pool grant default_machine fields: %w", err)
	}
	perMachineLimits := resolveProjectMachinePoolGrantPerMachineLimits(poolRecord, input)
	projectMachineResources, err := resourcesFromMachineProvisioning(projectProvisioning)
	if err != nil {
		return ProjectMachinePoolGrantRecord{}, fmt.Errorf("resolved project machine pool grant config: %w", err)
	}
	if err := validateMachineResourcesWithinPerMachineLimits(projectMachineResources, perMachineLimits); err != nil {
		return ProjectMachinePoolGrantRecord{}, fmt.Errorf("resolved project machine pool grant config %w", err)
	}
	row, err := qtx.UpsertProjectMachinePoolGrant(
		ctx,
		dbsqlc.UpsertProjectMachinePoolGrantParams{
			OrgID:                                input.OrgID,
			ProjectID:                            input.ProjectID,
			MachinePoolID:                        input.MachinePoolID,
			Description:                          input.Description,
			DefaultMachineCpu:                    sqlcInt32Ptr(input.DefaultMachineCPU),
			DefaultMachineMemoryMb:               sqlcInt32Ptr(input.DefaultMachineMemoryMB),
			DefaultMachineEnvOverlay:             input.DefaultMachineEnvOverlay,
			DefaultMachineSecretEnvOverlay:       input.DefaultMachineSecretEnvOverlay,
			DefaultMachineProviderOptionsOverlay: input.DefaultMachineProviderOptionsOverlay,
			DefaultCwd:                           input.DefaultCwd,
			MaxTotalMachines:                     sqlcInt32Ptr(input.MaxTotalMachines),
			MaxTotalCpu:                          sqlcInt32Ptr(input.MaxTotalCPU),
			MaxTotalMemoryMb:                     sqlcInt32Ptr(input.MaxTotalMemoryMB),
			MaxMachineCpu:                        sqlcInt32Ptr(input.MaxMachineCPU),
			MaxMachineMemoryMb:                   sqlcInt32Ptr(input.MaxMachineMemoryMB),
			IdempotencyKey:                       sqlcTextFromEmpty(input.IdempotencyKey),
			Metadata:                             input.Metadata,
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
			return ProjectMachinePoolGrantRecord{}, storeerr.ErrIdempotencyConflict
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

func sameProjectMachinePoolGrantCreateInput(
	grant ProjectMachinePoolGrantRecord,
	input CreateProjectMachinePoolGrantInput,
) bool {
	return grant.OrgID == input.OrgID &&
		grant.ProjectID == input.ProjectID &&
		grant.MachinePoolID == input.MachinePoolID &&
		grant.Description == input.Description &&
		sameIntPtr(grant.DefaultMachineCPU, input.DefaultMachineCPU) &&
		sameIntPtr(grant.DefaultMachineMemoryMB, input.DefaultMachineMemoryMB) &&
		sameJSON(grant.DefaultMachineEnvOverlay, input.DefaultMachineEnvOverlay) &&
		sameJSON(grant.DefaultMachineSecretEnvOverlay, input.DefaultMachineSecretEnvOverlay) &&
		sameJSON(grant.DefaultMachineProviderOptionsOverlay, input.DefaultMachineProviderOptionsOverlay) &&
		grant.DefaultCwd == input.DefaultCwd &&
		sameIntPtr(grant.MaxTotalMachines, input.MaxTotalMachines) &&
		sameIntPtr(grant.MaxTotalCPU, input.MaxTotalCPU) &&
		sameIntPtr(grant.MaxTotalMemoryMB, input.MaxTotalMemoryMB) &&
		sameIntPtr(grant.MaxMachineCPU, input.MaxMachineCPU) &&
		sameIntPtr(grant.MaxMachineMemoryMB, input.MaxMachineMemoryMB) &&
		sameJSON(grant.Metadata, input.Metadata)
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
