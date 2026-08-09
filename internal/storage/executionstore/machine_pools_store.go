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
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/secretops"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/patch"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type MachineSourceKind string

const (
	MachineSourceKindBYO  MachineSourceKind = "byo"
	MachineSourceKindPool MachineSourceKind = "pool"
)

type MachineLifecycleState string

const (
	MachineLifecycleStateProvisioning    MachineLifecycleState = "provisioning"
	MachineLifecycleStateProvisionFailed MachineLifecycleState = "provision_failed"
	MachineLifecycleStateActive          MachineLifecycleState = "active"
	MachineLifecycleStateDeleting        MachineLifecycleState = "deleting"
	MachineLifecycleStateDeleteFailed    MachineLifecycleState = "delete_failed"
	MachineLifecycleStateDeleted         MachineLifecycleState = "deleted"
)

type MachineConnectionState string

const (
	MachineConnectionStateOnline  MachineConnectionState = "online"
	MachineConnectionStateAsleep  MachineConnectionState = "asleep"
	MachineConnectionStateOffline MachineConnectionState = "offline"
)

type MachineRecord struct {
	ID                           ID                     `json:"id"`
	OrgID                        ID                     `json:"org_id"`
	MachinePoolID                ID                     `json:"machine_pool_id,omitempty"`
	SourceKind                   MachineSourceKind      `json:"source_kind"`
	DisplayName                  string                 `json:"display_name,omitempty"`
	Description                  string                 `json:"description,omitempty"`
	Provider                     string                 `json:"provider"`
	LifecycleState               MachineLifecycleState  `json:"lifecycle_state"`
	ProviderResourceID           string                 `json:"provider_resource_id,omitempty"`
	ProviderProvisionAttemptedAt *time.Time             `json:"-"`
	ConnectionState              MachineConnectionState `json:"connection_state"`
	SandboxURL                   string                 `json:"-"`
	LastObservedAt               *time.Time             `json:"last_observed_at,omitempty"`
	CPU                          *int                   `json:"cpu,omitempty"`
	MemoryMB                     *int                   `json:"memory_mb,omitempty"`
	Cwd                          string                 `json:"cwd"`
	Env                          json.RawMessage        `json:"env,omitempty"`
	SecretEnv                    json.RawMessage        `json:"secret_env,omitempty"`
	ProviderOptions              json.RawMessage        `json:"provider_options,omitempty"`
	IdempotencyKey               string                 `json:"idempotency_key,omitempty"`
	LifecycleReasonCode          string                 `json:"lifecycle_reason_code,omitempty"`
	LifecycleReasonMessage       string                 `json:"lifecycle_reason_message,omitempty"`
	NextReconcileAfter           *time.Time             `json:"next_reconcile_after,omitempty"`
	ProvisionAttempts            int32                  `json:"provision_attempts"`
	DeleteAttempts               int32                  `json:"delete_attempts"`
	Metadata                     json.RawMessage        `json:"metadata"`
	DeletedAt                    *time.Time             `json:"deleted_at,omitempty"`
	CreatedAt                    time.Time              `json:"created_at"`
	UpdatedAt                    time.Time              `json:"updated_at"`
	LifecycleChangedAt           time.Time              `json:"-"`
	LifecycleVersion             int64                  `json:"-"`
	Created                      bool                   `json:"-"`
}

type MachineSummaryRecord struct {
	ID              ID                     `json:"id"`
	OrgID           ID                     `json:"org_id"`
	SourceKind      MachineSourceKind      `json:"source_kind"`
	DisplayName     string                 `json:"display_name,omitempty"`
	Description     string                 `json:"description,omitempty"`
	Provider        string                 `json:"provider"`
	LifecycleState  MachineLifecycleState  `json:"lifecycle_state"`
	ConnectionState MachineConnectionState `json:"connection_state"`
	LastObservedAt  *time.Time             `json:"last_observed_at,omitempty"`
	DeletedAt       *time.Time             `json:"deleted_at,omitempty"`
	CreatedAt       time.Time              `json:"created_at"`
	UpdatedAt       time.Time              `json:"updated_at"`
}

type MachinePoolRecord struct {
	ID                            ID              `json:"id"`
	OrgID                         ID              `json:"org_id"`
	Name                          string          `json:"name"`
	ManagementKind                management.Kind `json:"-"`
	Description                   string          `json:"description"`
	Provider                      string          `json:"provider"`
	DefaultMachineCPU             *int            `json:"default_machine_cpu"`
	DefaultMachineMemoryMB        *int            `json:"default_machine_memory_mb"`
	DefaultMachineEnv             json.RawMessage `json:"default_machine_env"`
	DefaultMachineSecretEnv       json.RawMessage `json:"default_machine_secret_env"`
	DefaultMachineProviderOptions json.RawMessage `json:"default_machine_provider_options"`
	DefaultCwd                    string          `json:"default_cwd"`
	ProviderConfig                json.RawMessage `json:"-"`
	ProviderAuthSecretID          ID              `json:"provider_auth_secret_id,omitempty"`
	ProviderAuthEnvVar            string          `json:"-"`
	RuntimeProtectionEnabled      bool            `json:"runtime_protection_enabled"`
	MaxTotalMachines              int32           `json:"max_total_machines"`
	MaxTotalCPU                   *int            `json:"max_total_cpu"`
	MaxTotalMemoryMB              *int            `json:"max_total_memory_mb"`
	MaxMachineCPU                 *int            `json:"max_machine_cpu"`
	MaxMachineMemoryMB            *int            `json:"max_machine_memory_mb"`
	Metadata                      json.RawMessage `json:"metadata"`
	DeletedAt                     *time.Time      `json:"deleted_at,omitempty"`
	CreatedAt                     time.Time       `json:"created_at"`
	UpdatedAt                     time.Time       `json:"updated_at"`
	Created                       bool            `json:"-"`
}

func (record MachinePoolRecord) ProviderPolicy() (MachinePoolProviderPolicy, error) {
	defaults, err := MachineProvisioningFromDefaults(
		record.DefaultMachineCPU,
		record.DefaultMachineMemoryMB,
		record.DefaultMachineProviderOptions,
	)
	if err != nil {
		return MachinePoolProviderPolicy{}, err
	}
	return MachinePoolProviderPolicy{
		DefaultProvisioning: defaults,
		ResourceLimits: MachineResourceLimits{
			MaxTotalCPU:        record.MaxTotalCPU,
			MaxTotalMemoryMB:   record.MaxTotalMemoryMB,
			MaxMachineCPU:      record.MaxMachineCPU,
			MaxMachineMemoryMB: record.MaxMachineMemoryMB,
		},
		ProviderConfig:           record.ProviderConfig,
		RuntimeProtectionEnabled: record.RuntimeProtectionEnabled,
	}, nil
}

type CreateMachinePoolInput struct {
	OrgID                         ID
	Name                          string
	ManagementKind                management.Kind
	Description                   string
	Provider                      string
	DefaultMachineCPU             *int
	DefaultMachineMemoryMB        *int
	DefaultMachineEnv             json.RawMessage
	DefaultMachineSecretEnv       json.RawMessage
	DefaultMachineProviderOptions json.RawMessage
	DefaultCwd                    string
	ProviderConfig                json.RawMessage
	ProviderAuthSecretID          ID
	ProviderAuthEnvVar            string
	RuntimeProtectionEnabled      *bool
	MaxTotalMachines              int32
	MaxTotalCPU                   *int
	MaxTotalMemoryMB              *int
	MaxMachineCPU                 *int
	MaxMachineMemoryMB            *int
	Metadata                      json.RawMessage
}

type UpdateMachinePoolInput struct {
	OrgID                         ID
	ID                            ID
	Name                          *string
	Description                   *string
	DefaultMachineCPU             patch.NullableInt
	DefaultMachineMemoryMB        patch.NullableInt
	DefaultMachineEnv             json.RawMessage
	DefaultMachineSecretEnv       json.RawMessage
	DefaultMachineProviderOptions json.RawMessage
	DefaultCwd                    *string
	ProviderConfig                json.RawMessage
	ProviderAuthSecretID          *ID
	RuntimeProtectionEnabled      *bool
	MaxTotalMachines              *int32
	MaxTotalCPU                   patch.NullableInt
	MaxTotalMemoryMB              patch.NullableInt
	MaxMachineCPU                 patch.NullableInt
	MaxMachineMemoryMB            patch.NullableInt
	Metadata                      json.RawMessage
}

type DefaultMachinePoolTemplate struct {
	Name                          string          `json:"name"`
	Description                   string          `json:"description"`
	Provider                      string          `json:"provider"`
	DefaultMachineCPU             *int            `json:"default_machine_cpu"`
	DefaultMachineMemoryMB        *int            `json:"default_machine_memory_mb"`
	DefaultMachineEnv             json.RawMessage `json:"default_machine_env"`
	DefaultMachineSecretEnv       json.RawMessage `json:"default_machine_secret_env"`
	DefaultMachineProviderOptions json.RawMessage `json:"default_machine_provider_options"`
	DefaultCwd                    string          `json:"default_cwd"`
	ProviderConfig                json.RawMessage `json:"provider_config"`
	ProviderAuthEnvVar            string          `json:"provider_auth_env_var"`
	MaxTotalMachines              int32           `json:"max_total_machines"`
	MaxTotalCPU                   *int            `json:"max_total_cpu"`
	MaxTotalMemoryMB              *int            `json:"max_total_memory_mb"`
	MaxMachineCPU                 *int            `json:"max_machine_cpu"`
	MaxMachineMemoryMB            *int            `json:"max_machine_memory_mb"`
	Metadata                      json.RawMessage `json:"metadata"`
}

func (defaultPoolTemplate DefaultMachinePoolTemplate) createInput(orgID ID) CreateMachinePoolInput {
	return CreateMachinePoolInput{
		OrgID:                         orgID,
		Name:                          strings.TrimSpace(defaultPoolTemplate.Name),
		ManagementKind:                management.Cluster,
		Description:                   defaultPoolTemplate.Description,
		Provider:                      defaultPoolTemplate.Provider,
		DefaultMachineCPU:             defaultPoolTemplate.DefaultMachineCPU,
		DefaultMachineMemoryMB:        defaultPoolTemplate.DefaultMachineMemoryMB,
		DefaultMachineEnv:             defaultPoolTemplate.DefaultMachineEnv,
		DefaultMachineSecretEnv:       defaultPoolTemplate.DefaultMachineSecretEnv,
		DefaultMachineProviderOptions: defaultPoolTemplate.DefaultMachineProviderOptions,
		DefaultCwd:                    defaultPoolTemplate.DefaultCwd,
		ProviderConfig:                defaultPoolTemplate.ProviderConfig,
		ProviderAuthEnvVar:            defaultPoolTemplate.ProviderAuthEnvVar,
		MaxTotalMachines:              defaultPoolTemplate.MaxTotalMachines,
		MaxTotalCPU:                   defaultPoolTemplate.MaxTotalCPU,
		MaxTotalMemoryMB:              defaultPoolTemplate.MaxTotalMemoryMB,
		MaxMachineCPU:                 defaultPoolTemplate.MaxMachineCPU,
		MaxMachineMemoryMB:            defaultPoolTemplate.MaxMachineMemoryMB,
		Metadata:                      defaultPoolTemplate.Metadata,
	}
}

func ValidateDefaultMachinePoolTemplate(defaultPoolTemplate DefaultMachinePoolTemplate) error {
	input := defaultPoolTemplate.createInput(NilID)
	if input.Name == "" {
		return errors.New("name is required")
	}
	_, err := prepareMachinePoolConfigInput(&input)
	return err
}

func (s *Store) CreateMachinePool(
	ctx context.Context,
	input CreateMachinePoolInput,
) (MachinePoolRecord, error) {
	if input.ManagementKind == management.Cluster {
		return MachinePoolRecord{}, storeerr.InvalidRequest(errors.New("cluster-managed machine pools are reserved"))
	}
	poolDefaults, err := prepareMachinePoolCreateInput(&input)
	if err != nil {
		return MachinePoolRecord{}, storeerr.InvalidRequest(err)
	}
	if err := validateMachinePoolProviderAuth(ctx, s.q, input.OrgID, input.ProviderAuthSecretID); err != nil {
		return MachinePoolRecord{}, err
	}
	if err := s.validatePoolDefaultsTx(ctx, s.q, input, poolDefaults); err != nil {
		return MachinePoolRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MachinePoolRecord{}, fmt.Errorf("begin create machine pool: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	record, err := insertMachinePool(ctx, qtx, input)
	if err == nil {
		if err := lockResourceCreation(ctx, qtx, resourceMachinePools, input.OrgID.String()); err != nil {
			return MachinePoolRecord{}, err
		}
		poolCount, err := qtx.CountActiveTenantMachinePoolsForOrg(
			ctx,
			dbsqlc.CountActiveTenantMachinePoolsForOrgParams{OrgID: input.OrgID},
		)
		if err != nil {
			return MachinePoolRecord{}, fmt.Errorf("count active tenant machine pools: %w", err)
		}
		if poolCount > MaxActiveTenantMachinePoolsPerOrg {
			return MachinePoolRecord{}, resourceLimitExceeded(
				"active machine pools",
				MaxActiveTenantMachinePoolsPerOrg,
			)
		}
		if err := tx.Commit(ctx); err != nil {
			return MachinePoolRecord{}, fmt.Errorf("commit create machine pool: %w", err)
		}
		return record, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		if storeutil.IsUniqueViolation(err) {
			return MachinePoolRecord{}, storeerr.ErrIdempotencyConflict
		}
		return MachinePoolRecord{}, fmt.Errorf("insert machine pool: %w", err)
	}
	row, err := qtx.GetMachinePoolByName(
		ctx,
		dbsqlc.GetMachinePoolByNameParams{OrgID: input.OrgID, Name: input.Name},
	)
	if err != nil {
		return MachinePoolRecord{}, fmt.Errorf("get machine pool by name: %w", err)
	}
	record = machinePoolRecordFromSQLC(row)
	if record.DeletedAt != nil {
		return MachinePoolRecord{}, storeerr.ErrIdempotencyConflict
	}
	if record.Name != input.Name ||
		record.ManagementKind != input.ManagementKind ||
		record.Description != input.Description ||
		record.Provider != input.Provider ||
		!sameIntPtr(record.DefaultMachineCPU, input.DefaultMachineCPU) ||
		!sameIntPtr(record.DefaultMachineMemoryMB, input.DefaultMachineMemoryMB) ||
		!sameJSON(record.DefaultMachineEnv, input.DefaultMachineEnv) ||
		!sameJSON(record.DefaultMachineSecretEnv, input.DefaultMachineSecretEnv) ||
		!sameJSON(record.DefaultMachineProviderOptions, input.DefaultMachineProviderOptions) ||
		record.DefaultCwd != input.DefaultCwd ||
		!sameJSON(record.ProviderConfig, input.ProviderConfig) ||
		record.ProviderAuthSecretID != input.ProviderAuthSecretID ||
		record.ProviderAuthEnvVar != input.ProviderAuthEnvVar ||
		record.RuntimeProtectionEnabled != *input.RuntimeProtectionEnabled ||
		record.MaxTotalMachines != input.MaxTotalMachines ||
		!sameIntPtr(record.MaxTotalCPU, input.MaxTotalCPU) ||
		!sameIntPtr(record.MaxTotalMemoryMB, input.MaxTotalMemoryMB) ||
		!sameIntPtr(record.MaxMachineCPU, input.MaxMachineCPU) ||
		!sameIntPtr(record.MaxMachineMemoryMB, input.MaxMachineMemoryMB) ||
		!sameJSON(record.Metadata, input.Metadata) {
		return MachinePoolRecord{}, storeerr.ErrIdempotencyConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return MachinePoolRecord{}, fmt.Errorf("commit replay create machine pool: %w", err)
	}
	return record, nil
}

func prepareMachinePoolCreateInput(
	input *CreateMachinePoolInput,
) (machinePoolDefaults, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.ManagementKind == "" {
		input.ManagementKind = management.Tenant
	}
	if input.RuntimeProtectionEnabled == nil {
		input.RuntimeProtectionEnabled = machinePoolBoolPtr(true)
	}
	if isNilID(input.OrgID) || input.Name == "" || input.Provider == "" {
		return machinePoolDefaults{}, errors.New("org, name, and provider are required")
	}
	switch input.ManagementKind {
	case management.Tenant:
	case management.Cluster:
	default:
		return machinePoolDefaults{}, errors.New("machine pool management kind is invalid")
	}
	return prepareMachinePoolConfigInput(input)
}

func prepareMachinePoolConfigInput(
	input *CreateMachinePoolInput,
) (machinePoolDefaults, error) {
	if input.MaxTotalMachines < 0 {
		return machinePoolDefaults{}, errors.New("max_total_machines cannot be negative")
	}
	if input.MaxTotalCPU != nil && (*input.MaxTotalCPU < 0 || *input.MaxTotalCPU > math.MaxInt32) {
		return machinePoolDefaults{}, fmt.Errorf(
			"max_total_cpu must be between 0 and %d",
			math.MaxInt32,
		)
	}
	if input.MaxTotalMemoryMB != nil && (*input.MaxTotalMemoryMB < 0 || *input.MaxTotalMemoryMB > math.MaxInt32) {
		return machinePoolDefaults{}, fmt.Errorf(
			"max_total_memory_mb must be between 0 and %d when set",
			math.MaxInt32,
		)
	}
	if input.MaxMachineCPU != nil && (*input.MaxMachineCPU <= 0 || *input.MaxMachineCPU > math.MaxInt32) {
		return machinePoolDefaults{}, fmt.Errorf(
			"max_machine_cpu must be between 1 and %d",
			math.MaxInt32,
		)
	}
	if input.MaxMachineMemoryMB != nil && (*input.MaxMachineMemoryMB <= 0 || *input.MaxMachineMemoryMB > math.MaxInt32) {
		return machinePoolDefaults{}, fmt.Errorf(
			"max_machine_memory_mb must be between 1 and %d when set",
			math.MaxInt32,
		)
	}
	if strings.ContainsRune(input.DefaultCwd, 0) {
		return machinePoolDefaults{}, errors.New("default_cwd cannot contain NUL")
	}
	input.ProviderAuthEnvVar = strings.TrimSpace(input.ProviderAuthEnvVar)
	switch input.ManagementKind {
	case management.Tenant:
		if isNilID(input.ProviderAuthSecretID) {
			return machinePoolDefaults{}, errors.New("provider_auth_secret_id is required")
		}
		if input.ProviderAuthEnvVar != "" {
			return machinePoolDefaults{}, errors.New(
				"provider_auth_env_var is only valid for cluster-managed machine pools",
			)
		}
	case management.Cluster:
		if !isNilID(input.ProviderAuthSecretID) {
			return machinePoolDefaults{}, errors.New(
				"provider_auth_secret_id is only valid for tenant-managed machine pools",
			)
		}
		if input.ProviderAuthEnvVar == "" {
			return machinePoolDefaults{}, errors.New("provider_auth_env_var is required")
		}
	}
	if len(input.DefaultMachineProviderOptions) == 0 || string(input.DefaultMachineProviderOptions) == "null" {
		return machinePoolDefaults{}, errors.New(
			"machine pool default_machine fields requires provider_options",
		)
	}
	poolProvisioning, err := MachineProvisioningFromDefaults(
		input.DefaultMachineCPU,
		input.DefaultMachineMemoryMB,
		input.DefaultMachineProviderOptions,
	)
	if err != nil {
		return machinePoolDefaults{}, fmt.Errorf("machine pool default_machine fields %w", err)
	}
	poolEnvironment, err := MachineEnvironmentFromColumns(
		input.DefaultMachineEnv,
		input.DefaultMachineSecretEnv,
	)
	if err != nil {
		return machinePoolDefaults{}, fmt.Errorf("machine pool default_machine fields %w", err)
	}
	provisioningColumns, err := machineProvisioningToColumns(poolProvisioning)
	if err != nil {
		return machinePoolDefaults{}, fmt.Errorf("machine pool default_machine fields: %w", err)
	}
	env, secretEnv, err := machineEnvironmentToColumns(poolEnvironment)
	if err != nil {
		return machinePoolDefaults{}, fmt.Errorf("machine pool default_machine fields: %w", err)
	}
	input.DefaultMachineCPU = intPtrFromSQLC(provisioningColumns.CPU)
	input.DefaultMachineMemoryMB = intPtrFromSQLC(provisioningColumns.MemoryMB)
	input.DefaultMachineEnv = env
	input.DefaultMachineSecretEnv = secretEnv
	input.DefaultMachineProviderOptions = provisioningColumns.ProviderOptions
	poolMachineResources, err := resourcesFromMachineProvisioning(poolProvisioning)
	if err != nil {
		return machinePoolDefaults{}, fmt.Errorf("machine pool default_machine fields: %w", err)
	}
	if err := validateMachineResourcesWithinPerMachineLimits(poolMachineResources, MachineResourceLimits{
		MaxMachineCPU:      input.MaxMachineCPU,
		MaxMachineMemoryMB: input.MaxMachineMemoryMB,
	}); err != nil {
		return machinePoolDefaults{}, fmt.Errorf("machine pool default_machine fields %w", err)
	}
	input.ProviderConfig = normalizedJSON(input.ProviderConfig)
	input.Metadata = normalizedJSON(input.Metadata)
	return machinePoolDefaults{Provisioning: poolProvisioning, Environment: poolEnvironment}, nil
}

func validateMachinePoolProviderAuth(ctx context.Context, qtx *dbsqlc.Queries, orgID, providerAuthSecretID ID) error {
	credential, err := secretops.GetFacts(ctx, qtx, orgID, providerAuthSecretID)
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrNotFound
	}
	if err != nil {
		return err
	}
	if credential.OwnerKind != secretstore.SecretOwnerOrg {
		return fmt.Errorf("machine pool provider auth secret must be org-owned: %w", storeerr.ErrNotFound)
	}
	if credential.ManagementKind != management.Tenant {
		return fmt.Errorf("machine pool provider auth secret must be tenant-managed: %w", storeerr.ErrNotFound)
	}
	if credential.Kind != secretstore.SecretKindGeneric {
		return fmt.Errorf(
			"machine pool provider auth secret kind %q does not match required kind %q: %w",
			credential.Kind,
			secretstore.SecretKindGeneric,
			storeerr.ErrInvalidSecretRequest,
		)
	}
	return nil
}

func insertMachinePool(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input CreateMachinePoolInput,
) (MachinePoolRecord, error) {
	row, err := qtx.InsertMachinePool(ctx, dbsqlc.InsertMachinePoolParams{
		OrgID:                         input.OrgID,
		Name:                          input.Name,
		ManagementKind:                string(input.ManagementKind),
		Description:                   input.Description,
		Provider:                      input.Provider,
		DefaultMachineCpu:             sqlcInt32Ptr(input.DefaultMachineCPU),
		DefaultMachineMemoryMb:        sqlcInt32Ptr(input.DefaultMachineMemoryMB),
		DefaultMachineEnv:             input.DefaultMachineEnv,
		DefaultMachineSecretEnv:       input.DefaultMachineSecretEnv,
		DefaultMachineProviderOptions: input.DefaultMachineProviderOptions,
		DefaultCwd:                    input.DefaultCwd,
		ProviderConfig:                input.ProviderConfig,
		ProviderAuthSecretID:          sqlcIDFromNil(input.ProviderAuthSecretID),
		ProviderAuthEnvVar:            input.ProviderAuthEnvVar,
		RuntimeProtectionEnabled:      *input.RuntimeProtectionEnabled,
		MaxTotalMachines:              input.MaxTotalMachines,
		MaxTotalCpu:                   sqlcInt32Ptr(input.MaxTotalCPU),
		MaxTotalMemoryMb:              sqlcInt32Ptr(input.MaxTotalMemoryMB),
		MaxMachineCpu:                 sqlcInt32Ptr(input.MaxMachineCPU),
		MaxMachineMemoryMb:            sqlcInt32Ptr(input.MaxMachineMemoryMB),
		Metadata:                      input.Metadata,
	})
	if err != nil {
		return MachinePoolRecord{}, err
	}
	record := machinePoolRecordFromSQLC(row)
	record.Created = true
	return record, nil
}

func (s *Store) UpdateMachinePool(
	ctx context.Context,
	input UpdateMachinePoolInput,
) (MachinePoolRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.ID) {
		return MachinePoolRecord{}, errors.New("org and machine pool are required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MachinePoolRecord{}, fmt.Errorf("begin update machine pool: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	locked, err := qtx.LockMachinePoolForUpdate(
		ctx,
		dbsqlc.LockMachinePoolForUpdateParams{OrgID: input.OrgID, ID: input.ID},
	)
	if err != nil {
		return MachinePoolRecord{}, fmt.Errorf("lock machine pool for update: %w", err)
	}
	if management.Kind(locked.ManagementKind) == management.Cluster {
		return MachinePoolRecord{}, fmt.Errorf(
			"cluster-managed machine pools cannot be updated: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	merged := CreateMachinePoolInput{
		OrgID:                         input.OrgID,
		Name:                          locked.Name,
		ManagementKind:                management.Kind(locked.ManagementKind),
		Description:                   locked.Description,
		Provider:                      locked.Provider,
		DefaultMachineCPU:             intPtrFromSQLC(locked.DefaultMachineCpu),
		DefaultMachineMemoryMB:        intPtrFromSQLC(locked.DefaultMachineMemoryMb),
		DefaultMachineEnv:             locked.DefaultMachineEnv,
		DefaultMachineSecretEnv:       locked.DefaultMachineSecretEnv,
		DefaultMachineProviderOptions: locked.DefaultMachineProviderOptions,
		DefaultCwd:                    locked.DefaultCwd,
		ProviderConfig:                locked.ProviderConfig,
		ProviderAuthSecretID:          idFromSQLCPtr(locked.ProviderAuthSecretID),
		ProviderAuthEnvVar:            locked.ProviderAuthEnvVar,
		RuntimeProtectionEnabled:      machinePoolBoolPtr(locked.RuntimeProtectionEnabled),
		MaxTotalMachines:              locked.MaxTotalMachines,
		MaxTotalCPU:                   intPtrFromSQLC(locked.MaxTotalCpu),
		MaxTotalMemoryMB:              intPtrFromSQLC(locked.MaxTotalMemoryMb),
		MaxMachineCPU:                 intPtrFromSQLC(locked.MaxMachineCpu),
		MaxMachineMemoryMB:            intPtrFromSQLC(locked.MaxMachineMemoryMb),
		Metadata:                      locked.Metadata,
	}
	if input.Name != nil {
		merged.Name = *input.Name
	}
	if input.Description != nil {
		merged.Description = *input.Description
	}
	if input.DefaultMachineCPU.Set {
		merged.DefaultMachineCPU = input.DefaultMachineCPU.Value
	}
	if input.DefaultMachineMemoryMB.Set {
		merged.DefaultMachineMemoryMB = input.DefaultMachineMemoryMB.Value
	}
	if len(input.DefaultMachineEnv) > 0 {
		merged.DefaultMachineEnv = input.DefaultMachineEnv
	}
	if len(input.DefaultMachineSecretEnv) > 0 {
		merged.DefaultMachineSecretEnv = input.DefaultMachineSecretEnv
	}
	if len(input.DefaultMachineProviderOptions) > 0 {
		merged.DefaultMachineProviderOptions = input.DefaultMachineProviderOptions
	}
	if input.DefaultCwd != nil {
		merged.DefaultCwd = *input.DefaultCwd
	}
	if len(input.ProviderConfig) > 0 {
		merged.ProviderConfig = input.ProviderConfig
	}
	if input.ProviderAuthSecretID != nil {
		merged.ProviderAuthSecretID = *input.ProviderAuthSecretID
	}
	if input.RuntimeProtectionEnabled != nil {
		merged.RuntimeProtectionEnabled = machinePoolBoolPtr(*input.RuntimeProtectionEnabled)
	}
	if input.MaxTotalMachines != nil {
		merged.MaxTotalMachines = *input.MaxTotalMachines
	}
	if input.MaxTotalCPU.Set {
		merged.MaxTotalCPU = input.MaxTotalCPU.Value
	}
	if input.MaxTotalMemoryMB.Set {
		merged.MaxTotalMemoryMB = input.MaxTotalMemoryMB.Value
	}
	if input.MaxMachineCPU.Set {
		merged.MaxMachineCPU = input.MaxMachineCPU.Value
	}
	if input.MaxMachineMemoryMB.Set {
		merged.MaxMachineMemoryMB = input.MaxMachineMemoryMB.Value
	}
	if len(input.Metadata) > 0 {
		merged.Metadata = input.Metadata
	}
	merged.Name = strings.TrimSpace(merged.Name)
	if merged.Name == "" {
		return MachinePoolRecord{}, storeerr.InvalidRequest(errors.New("name is required"))
	}
	poolDefaults, err := prepareMachinePoolConfigInput(&merged)
	if err != nil {
		return MachinePoolRecord{}, storeerr.InvalidRequest(err)
	}
	if merged.ManagementKind == management.Tenant {
		if err := validateMachinePoolProviderAuth(ctx, qtx, merged.OrgID, merged.ProviderAuthSecretID); err != nil {
			return MachinePoolRecord{}, err
		}
	}
	if err := s.validatePoolDefaultsTx(ctx, qtx, merged, poolDefaults); err != nil {
		return MachinePoolRecord{}, err
	}
	row, err := qtx.UpdateMachinePool(ctx, dbsqlc.UpdateMachinePoolParams{
		OrgID:                         input.OrgID,
		ID:                            input.ID,
		Name:                          merged.Name,
		Description:                   merged.Description,
		DefaultMachineCpu:             sqlcInt32Ptr(merged.DefaultMachineCPU),
		DefaultMachineMemoryMb:        sqlcInt32Ptr(merged.DefaultMachineMemoryMB),
		DefaultMachineEnv:             merged.DefaultMachineEnv,
		DefaultMachineSecretEnv:       merged.DefaultMachineSecretEnv,
		DefaultMachineProviderOptions: merged.DefaultMachineProviderOptions,
		DefaultCwd:                    merged.DefaultCwd,
		ProviderConfig:                merged.ProviderConfig,
		ProviderAuthSecretID:          sqlcIDFromNil(merged.ProviderAuthSecretID),
		RuntimeProtectionEnabled:      *merged.RuntimeProtectionEnabled,
		MaxTotalMachines:              merged.MaxTotalMachines,
		MaxTotalCpu:                   sqlcInt32Ptr(merged.MaxTotalCPU),
		MaxTotalMemoryMb:              sqlcInt32Ptr(merged.MaxTotalMemoryMB),
		MaxMachineCpu:                 sqlcInt32Ptr(merged.MaxMachineCPU),
		MaxMachineMemoryMb:            sqlcInt32Ptr(merged.MaxMachineMemoryMB),
		Metadata:                      merged.Metadata,
	})
	if err != nil {
		if storeutil.IsUniqueViolation(err) {
			return MachinePoolRecord{}, storeerr.ErrConflict
		}
		return MachinePoolRecord{}, fmt.Errorf("update machine pool: %w", err)
	}
	record := machinePoolRecordFromSQLC(row)
	if locked.RuntimeProtectionEnabled != record.RuntimeProtectionEnabled {
		if err := qtx.ClearMachinePoolRuntimeMismatchMarkers(ctx, dbsqlc.ClearMachinePoolRuntimeMismatchMarkersParams{
			OrgID:         input.OrgID,
			MachinePoolID: input.ID,
		}); err != nil {
			return MachinePoolRecord{}, fmt.Errorf("clear machine pool runtime mismatch markers: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return MachinePoolRecord{}, fmt.Errorf("commit update machine pool: %w", err)
	}
	return record, nil
}

func machinePoolBoolPtr(value bool) *bool {
	return &value
}

func runtimeProtectionEnabled(value *bool) bool {
	return value == nil || *value
}

func (s *Store) GetMachinePool(ctx context.Context, orgID, id ID) (MachinePoolRecord, error) {
	row, err := s.q.GetMachinePool(ctx, dbsqlc.GetMachinePoolParams{OrgID: orgID, ID: id})
	if err != nil {
		return MachinePoolRecord{}, fmt.Errorf("get machine pool: %w", err)
	}
	return machinePoolRecordFromSQLC(row), nil
}

func (s *Store) GetMachinePoolForLifecycle(
	ctx context.Context,
	orgID, id ID,
) (MachinePoolRecord, error) {
	row, err := s.q.GetMachinePoolForLifecycle(
		ctx,
		dbsqlc.GetMachinePoolForLifecycleParams{OrgID: orgID, ID: id},
	)
	if err != nil {
		return MachinePoolRecord{}, fmt.Errorf("get machine pool for lifecycle: %w", err)
	}
	return machinePoolRecordFromSQLC(row), nil
}

type ListMachinePoolsInput struct {
	OrgID ID
	Limit int
	List  listing.Options
}

type ListMachinePoolsResult struct {
	Pools   []MachinePoolRecord
	HasMore bool
	Next    listing.Cursor
}

func (s *Store) ListMachinePools(ctx context.Context, input ListMachinePoolsInput) (ListMachinePoolsResult, error) {
	if isNilID(input.OrgID) {
		return ListMachinePoolsResult{}, errors.New("org id is required")
	}
	if input.Limit <= 0 {
		return ListMachinePoolsResult{}, errors.New("limit must be positive")
	}
	input.List = listing.Normalize(input.List)
	if !listing.SortAllowed(input.List.SortField, "name", "created_at", "updated_at") {
		return ListMachinePoolsResult{}, errors.New("unsupported sort")
	}
	params := dbsqlc.ListMachinePoolsParams{
		OrgID:       input.OrgID,
		RowLimit:    int64(input.Limit) + 1,
		SortField:   input.List.SortField,
		SortDesc:    input.List.SortDesc,
		NamePattern: input.List.NamePattern,
	}
	if input.List.After.Set {
		params.CursorSet, params.CursorKey, params.CursorID = true, input.List.After.Key, input.List.After.ID
	}
	rows, err := s.q.ListMachinePools(ctx, params)
	if err != nil {
		return ListMachinePoolsResult{}, fmt.Errorf("list machine pools: %w", err)
	}
	result := ListMachinePoolsResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	result.Pools = make([]MachinePoolRecord, 0, len(rows))
	for _, row := range rows {
		result.Pools = append(result.Pools, machinePoolRecordFromListSQLC(row))
		result.Next = listing.Cursor{Set: true, IsNull: row.SortIsNull, Key: row.SortKey, ID: row.ID}
	}
	return result, nil
}

func (s *Store) DeleteMachinePool(ctx context.Context, orgID, id ID) ([]MachineRecord, error) {
	if isNilID(orgID) || isNilID(id) {
		return nil, errors.New("org and machine pool are required")
	}
	// The cluster guard is API policy, not part of the shared teardown:
	// organization deletion removes cluster-managed pools through the same
	// helper. management_kind is immutable, so this pre-check cannot race.
	pool, err := s.GetMachinePool(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if pool.ManagementKind == management.Cluster {
		return nil, fmt.Errorf("cluster-managed machine pools cannot be deleted: %w", storeerr.ErrStateTransitionConflict)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin delete machine pool: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	txNotifications := s.newTxNotifications()
	machines, err := s.DeleteMachinePoolTx(ctx, tx, txNotifications, orgID, id)
	if err != nil {
		return nil, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "delete machine pool"); err != nil {
		return nil, err
	}
	return machines, nil
}

func (s *Store) DeleteMachinePoolTx(
	ctx context.Context,
	tx pgx.Tx,
	txNotifications *notifications.TxNotifications,
	orgID, id ID,
) ([]MachineRecord, error) {
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockMachinePoolForUpdate(
		ctx,
		dbsqlc.LockMachinePoolForUpdateParams{OrgID: orgID, ID: id},
	); err != nil {
		return nil, fmt.Errorf("lock machine pool for delete: %w", err)
	}
	if _, err := qtx.LockMachinePoolMachinesForUpdate(
		ctx,
		dbsqlc.LockMachinePoolMachinesForUpdateParams{OrgID: orgID, MachinePoolID: &id},
	); err != nil {
		return nil, fmt.Errorf("lock machine pool machines for archive: %w", err)
	}
	if _, err := qtx.DeleteMachinePool(
		ctx,
		dbsqlc.DeleteMachinePoolParams{OrgID: orgID, ID: id},
	); err != nil {
		return nil, fmt.Errorf("delete machine pool: %w", err)
	}
	machineRows, err := qtx.MarkMachinePoolMachinesDeleting(ctx, dbsqlc.MarkMachinePoolMachinesDeletingParams{
		OrgID:                  orgID,
		MachinePoolID:          id,
		LifecycleReasonCode:    sqlcTextFromEmpty("machine_pool_deleted"),
		LifecycleReasonMessage: "machine pool deleted",
	})
	if err != nil {
		return nil, fmt.Errorf("mark machine pool machines deleting: %w", err)
	}
	poolGrantRefs, err := qtx.ListProjectMachinePoolGrantRefsForMachinePool(
		ctx,
		dbsqlc.ListProjectMachinePoolGrantRefsForMachinePoolParams{
			OrgID:         orgID,
			MachinePoolID: id,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list project machine pool grants for machine pool: %w", err)
	}
	// Process completion joins the grant rows, so it runs before the deletes.
	for _, poolGrantRef := range poolGrantRefs {
		if err := completeExecutionRevokedProcessesTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			executionRevokedProcessScope{
				projectID:                 poolGrantRef.ProjectID,
				projectMachinePoolGrantID: poolGrantRef.ID,
			},
			"project_machine_grant_revoked",
		); err != nil {
			return nil, fmt.Errorf("complete processes for deleted project machine pool grant: %w", err)
		}
	}
	if err := qtx.DeletePoolProjectMachineGrantsForMachinePool(
		ctx,
		dbsqlc.DeletePoolProjectMachineGrantsForMachinePoolParams{
			OrgID:         orgID,
			MachinePoolID: id,
		},
	); err != nil {
		return nil, fmt.Errorf("delete pool project machine grants for machine pool: %w", err)
	}
	if _, err := qtx.DeleteProjectMachinePoolGrantsForMachinePool(
		ctx,
		dbsqlc.DeleteProjectMachinePoolGrantsForMachinePoolParams{
			OrgID:         orgID,
			MachinePoolID: id,
		},
	); err != nil {
		return nil, fmt.Errorf("delete project machine pool grants for machine pool: %w", err)
	}
	// When no machines need provider teardown the credential is released
	// immediately; otherwise DeletePoolMachine releases it once the last
	// machine finishes.
	if err := qtx.ReleaseMachinePoolCredentialIfIdle(
		ctx,
		dbsqlc.ReleaseMachinePoolCredentialIfIdleParams{OrgID: orgID, MachinePoolID: id},
	); err != nil {
		return nil, fmt.Errorf("release machine pool credential: %w", err)
	}
	machines := make([]MachineRecord, 0, len(machineRows))
	for _, machineRow := range machineRows {
		machines = append(machines, machineRecordFromMarkMachinePoolMachinesDeletingSQLC(machineRow))
	}
	return machines, nil
}

func (s *Store) GetMachine(ctx context.Context, orgID, id ID) (MachineRecord, error) {
	row, err := s.q.GetMachine(ctx, dbsqlc.GetMachineParams{OrgID: orgID, ID: id})
	if err != nil {
		return MachineRecord{}, fmt.Errorf("get machine: %w", err)
	}
	record := machineRecordFromGetSQLC(row)
	record.SandboxURL = row.SandboxUrl
	return record, nil
}
