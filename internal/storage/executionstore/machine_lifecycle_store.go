package executionstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	DefaultPoolMachineProvisionFailureLimit = 3

	defaultMachineLifecycleListLimit     = 20
	poolMachineProvisioningLeaseDuration = 3 * time.Minute
	poolMachineDeletionLeaseDuration     = 2 * time.Minute
	StaleMachineBootstrapAge             = 5 * time.Minute
	missingProviderResourceFinalityAge   = 24 * time.Hour
	machineDeletingReason                = "machine_deleting"
)

type PoolMachineProvisionFailureInput struct {
	OrgID                  ID
	MachineID              ID
	ProvisionAttempt       int32
	LifecycleReasonCode    string
	LifecycleReasonMessage string
	RetryDelay             time.Duration
}

type AdmitPoolMachineProvisioningInput struct {
	OrgID            ID
	MachineID        ID
	MachinePoolID    ID
	ProvisionAttempt int32
	Facts            MachineResourceFacts
}

type PoolMachineProvisioningAdmission struct {
	Facts     MachineResourceFacts
	UpdatedAt time.Time
}

type RecordPoolMachineProvisioningResourceInput struct {
	OrgID              ID
	MachineID          ID
	ProviderResourceID string
	ProvisionAttempt   int32
}

type RecordPoolMachineDeletionResourceInput struct {
	OrgID              ID
	MachineID          ID
	ProviderResourceID string
	DeleteAttempt      int32
}

type PoolMachineProviderResourceObservation struct {
	ProviderResourceID string
	UpdatedAt          time.Time
}

type MachineDeletingInput struct {
	OrgID                    ID
	MachineID                ID
	LifecycleReasonCode      string
	LifecycleReasonMessage   string
	ExpectedLifecycleVersion int64
}

type MachineDeleteFailureInput struct {
	OrgID                  ID
	MachineID              ID
	LifecycleReasonCode    string
	LifecycleReasonMessage string
	RetryDelay             time.Duration
	DeleteAttempt          int32
}

type PoolMachineCleanupCandidate struct {
	Machine       MachineRecord
	ReasonCode    string
	ReasonMessage string
}

type PoolMachineDeletionClaim struct {
	Machine                            MachineRecord
	CanFinalizeMissingProviderResource bool
}

type PoolMachineProvisioningClaim struct {
	Machine                   MachineRecord
	GrantProjectID            ID
	BindingEnvironmentOverlay MachineEnvironmentOverlay
}

func (s *Store) ClaimPoolMachineForProvisioning(
	ctx context.Context,
	orgID, machineID ID,
) (PoolMachineProvisioningClaim, bool, error) {
	if isNilID(orgID) || isNilID(machineID) {
		return PoolMachineProvisioningClaim{}, false, errors.New("org and machine are required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return PoolMachineProvisioningClaim{}, false, fmt.Errorf("begin claim pool machine for provisioning: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockPoolMachineForProvisioningClaim(
		ctx,
		dbsqlc.LockPoolMachineForProvisioningClaimParams{OrgID: orgID, ID: machineID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PoolMachineProvisioningClaim{}, false, nil
		}
		return PoolMachineProvisioningClaim{}, false, fmt.Errorf("lock pool machine for provisioning claim: %w", err)
	}
	row, err := qtx.ClaimPoolMachineForProvisioning(
		ctx,
		dbsqlc.ClaimPoolMachineForProvisioningParams{
			OrgID:                orgID,
			ID:                   machineID,
			ClaimTimeoutSeconds:  int64(poolMachineProvisioningLeaseDuration / time.Second),
			MaxProvisionAttempts: DefaultPoolMachineProvisionFailureLimit,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineProvisioningClaim{}, false, nil
	}
	if err != nil {
		return PoolMachineProvisioningClaim{}, false, fmt.Errorf("claim pool machine for provisioning: %w", err)
	}
	bindingEnvironmentOverlay, err := machineEnvironmentOverlayFromColumns(
		row.BindingEnvOverlay,
		row.BindingSecretEnvOverlay,
	)
	if err != nil {
		return PoolMachineProvisioningClaim{}, false, fmt.Errorf("pool machine binding environment overlay: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PoolMachineProvisioningClaim{}, false, fmt.Errorf("commit pool machine provisioning claim: %w", err)
	}
	return PoolMachineProvisioningClaim{
		Machine:                   machineRecordFromClaimPoolProvisioningSQLC(row),
		GrantProjectID:            idFromSQLCPtr(row.GrantProjectID),
		BindingEnvironmentOverlay: bindingEnvironmentOverlay,
	}, true, nil
}

func (s *Store) AdmitPoolMachineProvisioning(
	ctx context.Context,
	input AdmitPoolMachineProvisioningInput,
) (PoolMachineProvisioningAdmission, error) {
	if isNilID(input.OrgID) || isNilID(input.MachineID) || isNilID(input.MachinePoolID) {
		return PoolMachineProvisioningAdmission{}, errors.New(
			"org, machine, and machine pool are required",
		)
	}
	if input.ProvisionAttempt <= 0 {
		return PoolMachineProvisioningAdmission{}, errors.New("provision attempt is required")
	}
	resolved, err := resourcesFromMachineProvisioning(MachineProvisioningConfig{
		CPU:      input.Facts.CPU,
		MemoryMB: input.Facts.MemoryMB,
	})
	if err != nil {
		return PoolMachineProvisioningAdmission{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return PoolMachineProvisioningAdmission{}, fmt.Errorf(
			"begin admit pool machine provisioning: %w",
			err,
		)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	projectID, err := qtx.LockPoolMachineGrant(
		ctx,
		dbsqlc.LockPoolMachineGrantParams{
			OrgID:     input.OrgID,
			MachineID: input.MachineID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineProvisioningAdmission{}, fmt.Errorf(
			"pool machine grant is unavailable: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	if err != nil {
		return PoolMachineProvisioningAdmission{}, fmt.Errorf(
			"lock pool machine grant for provisioning: %w",
			err,
		)
	}
	locked, err := qtx.LockPoolMachineProvisioningResources(
		ctx,
		dbsqlc.LockPoolMachineProvisioningResourcesParams{
			OrgID:            input.OrgID,
			ID:               input.MachineID,
			MachinePoolID:    sqlcIDFromNil(input.MachinePoolID),
			ProvisionAttempt: input.ProvisionAttempt,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineProvisioningAdmission{}, fmt.Errorf(
			"machine provisioning claim changed: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	if err != nil {
		return PoolMachineProvisioningAdmission{}, fmt.Errorf(
			"lock pool machine provisioning resources: %w",
			err,
		)
	}
	current := MachineResourceFacts{
		CPU:      intPtrFromSQLC(locked.Cpu),
		MemoryMB: intPtrFromSQLC(locked.MemoryMb),
	}
	if current.CPU != nil && !sameIntPtr(current.CPU, input.Facts.CPU) {
		return PoolMachineProvisioningAdmission{}, fmt.Errorf(
			"provider resolved a different cpu: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	if current.MemoryMB != nil && !sameIntPtr(current.MemoryMB, input.Facts.MemoryMB) {
		return PoolMachineProvisioningAdmission{}, fmt.Errorf(
			"provider resolved a different memory_mb: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	if sameIntPtr(current.CPU, input.Facts.CPU) &&
		sameIntPtr(current.MemoryMB, input.Facts.MemoryMB) {
		if err := tx.Commit(ctx); err != nil {
			return PoolMachineProvisioningAdmission{}, fmt.Errorf(
				"commit replayed pool machine provisioning admission: %w",
				err,
			)
		}
		return PoolMachineProvisioningAdmission{
			Facts:     current,
			UpdatedAt: locked.UpdatedAt,
		}, nil
	}
	poolGrant, err := qtx.GetActiveProjectMachinePoolGrantForLaunch(
		ctx,
		dbsqlc.GetActiveProjectMachinePoolGrantForLaunchParams{
			OrgID:         input.OrgID,
			ProjectID:     projectID,
			MachinePoolID: input.MachinePoolID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineProvisioningAdmission{}, fmt.Errorf(
			"pool machine grant is unavailable: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	if err != nil {
		return PoolMachineProvisioningAdmission{}, fmt.Errorf(
			"load pool machine capacity for provisioning: %w",
			err,
		)
	}
	currentResources, err := resourcesFromMachineProvisioning(MachineProvisioningConfig{
		CPU:      current.CPU,
		MemoryMB: current.MemoryMB,
	})
	if err != nil {
		return PoolMachineProvisioningAdmission{}, err
	}
	poolUsage, err := qtx.GetActivePoolMachineUsage(
		ctx,
		dbsqlc.GetActivePoolMachineUsageParams{
			OrgID:         input.OrgID,
			MachinePoolID: sqlcIDFromNil(input.MachinePoolID),
		},
	)
	if err != nil {
		return PoolMachineProvisioningAdmission{}, fmt.Errorf(
			"get pool usage for provisioning: %w",
			err,
		)
	}
	if err := checkProvisioningResourceAdmission(
		poolUsage.Cpu,
		poolUsage.MemoryMb,
		currentResources,
		resolved,
		MachineResourceLimits{
			MaxTotalCPU:        intPtrFromSQLC(poolGrant.PoolMaxTotalCpu),
			MaxTotalMemoryMB:   intPtrFromSQLC(poolGrant.PoolMaxTotalMemoryMb),
			MaxMachineCPU:      intPtrFromSQLC(poolGrant.PoolMaxMachineCpu),
			MaxMachineMemoryMB: intPtrFromSQLC(poolGrant.PoolMaxMachineMemoryMb),
		},
	); err != nil {
		return PoolMachineProvisioningAdmission{}, fmt.Errorf("machine pool %w", err)
	}
	projectUsage, err := qtx.GetActiveProjectMachinePoolUsage(
		ctx,
		dbsqlc.GetActiveProjectMachinePoolUsageParams{
			OrgID:         input.OrgID,
			ProjectID:     projectID,
			MachinePoolID: sqlcIDFromNil(input.MachinePoolID),
		},
	)
	if err != nil {
		return PoolMachineProvisioningAdmission{}, fmt.Errorf(
			"get project pool usage for provisioning: %w",
			err,
		)
	}
	if err := checkProvisioningResourceAdmission(
		projectUsage.Cpu,
		projectUsage.MemoryMb,
		currentResources,
		resolved,
		MachineResourceLimits{
			MaxTotalCPU:        intPtrFromSQLC(poolGrant.GrantMaxTotalCpu),
			MaxTotalMemoryMB:   intPtrFromSQLC(poolGrant.GrantMaxTotalMemoryMb),
			MaxMachineCPU:      intPtrFromSQLC(poolGrant.GrantMaxMachineCpu),
			MaxMachineMemoryMB: intPtrFromSQLC(poolGrant.GrantMaxMachineMemoryMb),
		},
	); err != nil {
		return PoolMachineProvisioningAdmission{}, fmt.Errorf("project machine pool %w", err)
	}
	row, err := qtx.EnrichPoolMachineProvisioningResources(
		ctx,
		dbsqlc.EnrichPoolMachineProvisioningResourcesParams{
			Cpu:              sqlcInt32Ptr(input.Facts.CPU),
			MemoryMb:         sqlcInt32Ptr(input.Facts.MemoryMB),
			OrgID:            input.OrgID,
			ID:               input.MachineID,
			MachinePoolID:    sqlcIDFromNil(input.MachinePoolID),
			ProvisionAttempt: input.ProvisionAttempt,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineProvisioningAdmission{}, fmt.Errorf(
			"machine provisioning facts changed: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	if err != nil {
		return PoolMachineProvisioningAdmission{}, fmt.Errorf(
			"persist pool machine provisioning resources: %w",
			err,
		)
	}
	admission := PoolMachineProvisioningAdmission{
		Facts: MachineResourceFacts{
			CPU:      intPtrFromSQLC(row.Cpu),
			MemoryMB: intPtrFromSQLC(row.MemoryMb),
		},
		UpdatedAt: row.UpdatedAt,
	}
	if err := tx.Commit(ctx); err != nil {
		return PoolMachineProvisioningAdmission{}, fmt.Errorf(
			"commit pool machine provisioning admission: %w",
			err,
		)
	}
	return admission, nil
}

func (s *Store) RecordPoolMachineProvisioningResource(
	ctx context.Context,
	input RecordPoolMachineProvisioningResourceInput,
) (PoolMachineProviderResourceObservation, error) {
	if isNilID(input.OrgID) || isNilID(input.MachineID) ||
		strings.TrimSpace(input.ProviderResourceID) == "" {
		return PoolMachineProviderResourceObservation{}, errors.New(
			"org, machine, and provider resource id are required",
		)
	}
	if input.ProvisionAttempt <= 0 {
		return PoolMachineProviderResourceObservation{}, errors.New("provision attempt is required")
	}
	row, err := s.q.RecordPoolMachineProvisioningResource(
		ctx,
		dbsqlc.RecordPoolMachineProvisioningResourceParams{
			ProviderResourceID: input.ProviderResourceID,
			OrgID:              input.OrgID,
			ID:                 input.MachineID,
			ProvisionAttempt:   input.ProvisionAttempt,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineProviderResourceObservation{}, fmt.Errorf(
			"record pool machine provisioning resource: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	if err != nil {
		return PoolMachineProviderResourceObservation{}, fmt.Errorf(
			"record pool machine provisioning resource: %w",
			err,
		)
	}
	return PoolMachineProviderResourceObservation{
		ProviderResourceID: row.ProviderResourceID,
		UpdatedAt:          row.UpdatedAt,
	}, nil
}

func (s *Store) CompletePoolMachineProvisioning(
	ctx context.Context,
	orgID, machineID ID,
	providerResourceID, sandboxURL string,
	provisionAttempt int32,
) error {
	if isNilID(orgID) || isNilID(machineID) || providerResourceID == "" {
		return errors.New("org, machine, and provider resource id are required")
	}
	if provisionAttempt <= 0 {
		return errors.New("provision attempt is required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin complete pool machine provisioning: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockPoolMachineForProvisioningClaim(
		ctx,
		dbsqlc.LockPoolMachineForProvisioningClaimParams{OrgID: orgID, ID: machineID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrStateTransitionConflict
		}
		return fmt.Errorf("lock pool machine for provisioning completion: %w", err)
	}
	_, err = qtx.CompletePoolMachineProvisioning(
		ctx,
		dbsqlc.CompletePoolMachineProvisioningParams{
			OrgID:              orgID,
			ID:                 machineID,
			ProviderResourceID: sqlcTextFromEmpty(providerResourceID),
			SandboxUrl:         sqlcTextFromEmpty(sandboxURL),
			ProvisionAttempt:   provisionAttempt,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return fmt.Errorf("complete pool machine provisioning: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit complete pool machine provisioning: %w", err)
	}
	return nil
}

func (s *Store) MarkPoolMachineProvisionFailed(
	ctx context.Context,
	input PoolMachineProvisionFailureInput,
) error {
	if isNilID(input.OrgID) || isNilID(input.MachineID) {
		return errors.New("org and machine are required")
	}
	if input.ProvisionAttempt <= 0 {
		return errors.New("provision attempt is required")
	}
	if input.RetryDelay < time.Millisecond {
		return errors.New("retry delay must be at least one millisecond")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin mark pool machine provision failed: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockPoolMachineForProvisioningClaim(
		ctx,
		dbsqlc.LockPoolMachineForProvisioningClaimParams{OrgID: input.OrgID, ID: input.MachineID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrStateTransitionConflict
		}
		return fmt.Errorf("lock pool machine for provisioning failure: %w", err)
	}
	_, err = qtx.MarkPoolMachineProvisionFailed(
		ctx,
		dbsqlc.MarkPoolMachineProvisionFailedParams{
			OrgID:                  input.OrgID,
			ID:                     input.MachineID,
			ProvisionAttempt:       input.ProvisionAttempt,
			LifecycleReasonCode:    sqlcTextFromEmpty(input.LifecycleReasonCode),
			LifecycleReasonMessage: input.LifecycleReasonMessage,
			RetryDelayMilliseconds: input.RetryDelay.Milliseconds(),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return fmt.Errorf("mark pool machine provision failed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit mark pool machine provision failed: %w", err)
	}
	return nil
}

func (s *Store) MarkPoolMachineDeleting(ctx context.Context, input MachineDeletingInput) (MachineRecord, bool, error) {
	if isNilID(input.OrgID) || isNilID(input.MachineID) {
		return MachineRecord{}, false, errors.New("org and machine are required")
	}
	if input.LifecycleReasonCode == "" || input.LifecycleReasonMessage == "" {
		return MachineRecord{}, false, errors.New("lifecycle reason code and message are required")
	}
	if input.ExpectedLifecycleVersion <= 0 {
		return MachineRecord{}, false, errors.New("expected lifecycle version is required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MachineRecord{}, false, fmt.Errorf("begin mark pool machine deleting: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockMachineForLifecycle(
		ctx,
		dbsqlc.LockMachineForLifecycleParams{OrgID: input.OrgID, ID: input.MachineID},
	); errors.Is(err, pgx.ErrNoRows) {
		return MachineRecord{}, false, nil
	} else if err != nil {
		return MachineRecord{}, false, fmt.Errorf("lock pool machine for deletion intent: %w", err)
	}
	row, err := qtx.MarkPoolMachineDeleting(ctx, dbsqlc.MarkPoolMachineDeletingParams{
		OrgID:                    input.OrgID,
		ID:                       input.MachineID,
		LifecycleReasonCode:      sqlcTextFromEmpty(input.LifecycleReasonCode),
		LifecycleReasonMessage:   input.LifecycleReasonMessage,
		ExpectedLifecycleVersion: input.ExpectedLifecycleVersion,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return MachineRecord{}, false, nil
	}
	if err != nil {
		return MachineRecord{}, false, fmt.Errorf("mark pool machine deleting: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return MachineRecord{}, false, fmt.Errorf("commit mark pool machine deleting: %w", err)
	}
	return machineRecordFromMarkPoolMachineDeletingSQLC(row), true, nil
}

func (s *Store) ListPoolMachinesForProvisioning(
	ctx context.Context,
	limit int32,
) ([]MachineRecord, error) {
	if limit <= 0 {
		limit = defaultMachineLifecycleListLimit
	}
	rows, err := s.q.ListPoolMachinesForProvisioning(
		ctx,
		dbsqlc.ListPoolMachinesForProvisioningParams{
			MaxProvisionAttempts: DefaultPoolMachineProvisionFailureLimit,
			LimitCount:           limit,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list pool machines for provisioning: %w", err)
	}
	out := make([]MachineRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, machineRecordFromListPoolProvisioningSQLC(row))
	}
	return out, nil
}

func (s *Store) ListPoolMachinesForCleanup(
	ctx context.Context,
	provisionFailureLimit, limit int32,
) ([]PoolMachineCleanupCandidate, error) {
	if provisionFailureLimit <= 0 {
		provisionFailureLimit = DefaultPoolMachineProvisionFailureLimit
	}
	if limit <= 0 {
		limit = defaultMachineLifecycleListLimit
	}
	rows, err := s.q.ListPoolMachinesForCleanup(
		ctx,
		dbsqlc.ListPoolMachinesForCleanupParams{
			MaxProvisionAttempts:     provisionFailureLimit,
			StaleBootstrapAgeSeconds: int64(StaleMachineBootstrapAge / time.Second),
			LimitCount:               limit,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list pool machines for cleanup: %w", err)
	}
	out := make([]PoolMachineCleanupCandidate, 0, len(rows))
	for _, row := range rows {
		out = append(out, poolMachineCleanupCandidateFromSQLC(row))
	}
	return out, nil
}

func (s *Store) ClaimPoolMachineDeletion(
	ctx context.Context,
	input MachineDeletingInput,
) (PoolMachineDeletionClaim, bool, error) {
	if isNilID(input.OrgID) || isNilID(input.MachineID) {
		return PoolMachineDeletionClaim{}, false, errors.New("org and machine are required")
	}
	if input.LifecycleReasonCode == "" || input.LifecycleReasonMessage == "" {
		return PoolMachineDeletionClaim{}, false, errors.New("lifecycle reason code and message are required")
	}
	if input.ExpectedLifecycleVersion <= 0 {
		return PoolMachineDeletionClaim{}, false, errors.New("expected lifecycle version is required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return PoolMachineDeletionClaim{}, false, fmt.Errorf("begin claim pool machine deletion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	txNotifications := s.newTxNotifications()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockMachineForLifecycle(
		ctx,
		dbsqlc.LockMachineForLifecycleParams{OrgID: input.OrgID, ID: input.MachineID},
	); errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineDeletionClaim{}, false, nil
	} else if err != nil {
		return PoolMachineDeletionClaim{}, false, fmt.Errorf("lock pool machine for deletion claim: %w", err)
	}
	row, err := qtx.ClaimPoolMachineDeletion(ctx, dbsqlc.ClaimPoolMachineDeletionParams{
		OrgID:                             input.OrgID,
		ID:                                input.MachineID,
		LifecycleReasonCode:               sqlcTextFromEmpty(input.LifecycleReasonCode),
		LifecycleReasonMessage:            input.LifecycleReasonMessage,
		ExpectedLifecycleVersion:          input.ExpectedLifecycleVersion,
		MaxProvisionAttempts:              DefaultPoolMachineProvisionFailureLimit,
		StaleBootstrapAgeSeconds:          int64(StaleMachineBootstrapAge / time.Second),
		ClaimTimeoutSeconds:               int64(poolMachineDeletionLeaseDuration / time.Second),
		MissingResourceFinalityAgeSeconds: int64(missingProviderResourceFinalityAge / time.Second),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineDeletionClaim{}, false, nil
	}
	if err != nil {
		return PoolMachineDeletionClaim{}, false, fmt.Errorf("claim pool machine deletion: %w", err)
	}
	claim := poolMachineDeletionClaimFromSQLC(row)
	claim, err = s.finalizePoolMachineDeletionClaimTx(
		ctx,
		tx,
		qtx,
		txNotifications,
		claim,
		"claim pool machine deletion",
		machineDeletingReason,
	)
	if err != nil {
		return PoolMachineDeletionClaim{}, false, err
	}
	return claim, true, nil
}

func (s *Store) finalizePoolMachineDeletionClaimTx(
	ctx context.Context,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	txNotifications *notifications.TxNotifications,
	claim PoolMachineDeletionClaim,
	operation string,
	terminalWorkReason string,
) (PoolMachineDeletionClaim, error) {
	machine := claim.Machine
	active, err := qtx.ListActiveDaemonRuntimesForUpdate(
		ctx,
		dbsqlc.ListActiveDaemonRuntimesForUpdateParams{
			OrgID: machine.OrgID, MachineID: machine.ID,
		},
	)
	if err != nil {
		return PoolMachineDeletionClaim{}, fmt.Errorf("list active runtimes for deletion intent: %w", err)
	}
	for _, runtime := range active {
		if _, err := qtx.ForceEndDaemonRuntime(ctx, dbsqlc.ForceEndDaemonRuntimeParams{
			OrgID:     machine.OrgID,
			MachineID: machine.ID,
			ID:        runtime.ID,
			Reason:    sqlcTextFromEmpty(machineDeletingReason),
			Message:   "",
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return PoolMachineDeletionClaim{}, fmt.Errorf("end runtime for deletion intent: %w", err)
		}
		txNotifications.AddDaemonRuntimeEnded(
			runtime.ID,
			runtime.MachineID,
			notifications.DaemonRuntimeEndMachineDecommissioned,
		)
	}
	if err := completeMachineLifecycleTerminalWorkTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		machine.OrgID,
		machine.ID,
		terminalWorkReason,
	); err != nil {
		return PoolMachineDeletionClaim{}, err
	}
	if err := qtx.RevokeMachineDaemonTokensForMachine(
		ctx,
		dbsqlc.RevokeMachineDaemonTokensForMachineParams{
			OrgID: machine.OrgID, MachineID: machine.ID, Reason: machineDeletingReason,
		},
	); err != nil {
		return PoolMachineDeletionClaim{}, fmt.Errorf(
			"revoke machine daemon tokens for deletion intent: %w",
			err,
		)
	}
	lease, err := qtx.FinalizePoolMachineDeletionClaim(
		ctx,
		dbsqlc.FinalizePoolMachineDeletionClaimParams{
			ClaimTimeoutSeconds:      int64(poolMachineDeletionLeaseDuration / time.Second),
			OrgID:                    machine.OrgID,
			ID:                       machine.ID,
			ExpectedLifecycleVersion: machine.LifecycleVersion,
			DeleteAttempt:            machine.DeleteAttempts,
		},
	)
	if err != nil {
		return PoolMachineDeletionClaim{}, fmt.Errorf("finalize pool machine deletion claim: %w", err)
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, operation); err != nil {
		return PoolMachineDeletionClaim{}, err
	}
	claim.Machine.NextReconcileAfter = lease.NextReconcileAfter
	claim.Machine.UpdatedAt = lease.UpdatedAt
	return claim, nil
}

func (s *Store) RecordPoolMachineDeletionResource(
	ctx context.Context,
	input RecordPoolMachineDeletionResourceInput,
) (PoolMachineProviderResourceObservation, error) {
	if isNilID(input.OrgID) || isNilID(input.MachineID) ||
		strings.TrimSpace(input.ProviderResourceID) == "" {
		return PoolMachineProviderResourceObservation{}, errors.New(
			"org, machine, and provider resource id are required",
		)
	}
	if input.DeleteAttempt <= 0 {
		return PoolMachineProviderResourceObservation{}, errors.New("delete attempt is required")
	}
	row, err := s.q.RecordPoolMachineDeletionResource(
		ctx,
		dbsqlc.RecordPoolMachineDeletionResourceParams{
			ProviderResourceID: input.ProviderResourceID,
			OrgID:              input.OrgID,
			ID:                 input.MachineID,
			DeleteAttempt:      input.DeleteAttempt,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineProviderResourceObservation{}, fmt.Errorf(
			"record pool machine deletion resource: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	if err != nil {
		return PoolMachineProviderResourceObservation{}, fmt.Errorf(
			"record pool machine deletion resource: %w",
			err,
		)
	}
	return PoolMachineProviderResourceObservation{
		ProviderResourceID: row.ProviderResourceID,
		UpdatedAt:          row.UpdatedAt,
	}, nil
}

func (s *Store) MarkMachineDeleteFailed(ctx context.Context, input MachineDeleteFailureInput) error {
	if isNilID(input.OrgID) || isNilID(input.MachineID) {
		return errors.New("org and machine are required")
	}
	if input.DeleteAttempt <= 0 {
		return errors.New("delete attempt is required")
	}
	if input.RetryDelay < time.Millisecond {
		return errors.New("retry delay must be at least one millisecond")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin mark machine delete failed: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockPoolMachineDeletionAttemptForLifecycle(
		ctx,
		dbsqlc.LockPoolMachineDeletionAttemptForLifecycleParams{
			OrgID:         input.OrgID,
			ID:            input.MachineID,
			DeleteAttempt: input.DeleteAttempt,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrStateTransitionConflict
		}
		return fmt.Errorf("lock pool machine for deletion failure: %w", err)
	}
	_, err = qtx.MarkMachineDeleteFailed(
		ctx,
		dbsqlc.MarkMachineDeleteFailedParams{
			OrgID:                  input.OrgID,
			ID:                     input.MachineID,
			LifecycleReasonCode:    sqlcTextFromEmpty(input.LifecycleReasonCode),
			LifecycleReasonMessage: input.LifecycleReasonMessage,
			RetryDelayMilliseconds: input.RetryDelay.Milliseconds(),
			DeleteAttempt:          input.DeleteAttempt,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return fmt.Errorf("mark machine delete failed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit mark machine delete failed: %w", err)
	}
	return nil
}

func (s *Store) CompletePoolMachineDeletion(
	ctx context.Context,
	orgID, machineID ID,
	deleteAttempt int32,
) error {
	if isNilID(orgID) || isNilID(machineID) {
		return errors.New("org and machine are required")
	}
	if deleteAttempt <= 0 {
		return errors.New("delete attempt is required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin complete pool machine deletion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	txNotifications := s.newTxNotifications()
	qtx := dbsqlc.New(tx)
	poolID, err := qtx.GetPoolMachinePoolIDForLifecycle(
		ctx,
		dbsqlc.GetPoolMachinePoolIDForLifecycleParams{OrgID: orgID, ID: machineID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return fmt.Errorf("load machine pool for deletion completion: %w", err)
	}
	if poolID == nil {
		return errors.New("pool machine has no machine pool")
	}
	if err := lifecyclelock.Pools(
		ctx,
		tx,
		[]lifecyclelock.PoolRef{{OrgID: orgID, PoolID: *poolID}},
	); err != nil {
		return err
	}
	lockedMachine, err := qtx.LockPoolMachineDeletionAttemptForLifecycle(
		ctx,
		dbsqlc.LockPoolMachineDeletionAttemptForLifecycleParams{
			OrgID:         orgID,
			ID:            machineID,
			DeleteAttempt: deleteAttempt,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return fmt.Errorf("lock machine for deletion completion: %w", err)
	}
	if lockedMachine.MachinePoolID == nil || *lockedMachine.MachinePoolID != *poolID {
		return errors.New("pool machine changed machine pool during deletion completion")
	}
	active, err := qtx.ListActiveDaemonRuntimesForUpdate(
		ctx,
		dbsqlc.ListActiveDaemonRuntimesForUpdateParams{OrgID: orgID, MachineID: machineID},
	)
	if err != nil {
		return fmt.Errorf("list active runtimes for deletion completion: %w", err)
	}
	for _, runtime := range active {
		if _, err := qtx.ForceEndDaemonRuntime(ctx, dbsqlc.ForceEndDaemonRuntimeParams{
			OrgID:     orgID,
			MachineID: machineID,
			ID:        runtime.ID,
			Reason:    sqlcTextFromEmpty("machine_deleted"),
			Message:   "",
		}); err != nil &&
			!errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("end runtime for deletion completion: %w", err)
		}
		txNotifications.AddDaemonRuntimeEnded(
			runtime.ID,
			runtime.MachineID,
			notifications.DaemonRuntimeEndMachineDecommissioned,
		)
	}
	if err := completeMachineLifecycleTerminalWorkTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		orgID,
		machineID,
		"machine_deleted",
	); err != nil {
		return err
	}
	if err := qtx.RevokeMachineDaemonTokensForMachine(ctx, dbsqlc.RevokeMachineDaemonTokensForMachineParams{
		OrgID:     orgID,
		MachineID: machineID,
		Reason:    "machine_deleted",
	}); err != nil {
		return fmt.Errorf("revoke machine daemon tokens for deletion completion: %w", err)
	}
	if err := qtx.ReleaseAgentMachineBindingsForMachine(ctx, dbsqlc.ReleaseAgentMachineBindingsForMachineParams{
		OrgID:     orgID,
		MachineID: machineID,
	}); err != nil {
		return fmt.Errorf("release agent machine bindings for deletion completion: %w", err)
	}
	if err := qtx.DeletePoolProjectMachineGrantsForMachine(
		ctx,
		dbsqlc.DeletePoolProjectMachineGrantsForMachineParams{
			OrgID:     orgID,
			MachineID: machineID,
		},
	); err != nil {
		return fmt.Errorf("delete pool project machine grants for deletion completion: %w", err)
	}
	if _, err := qtx.DeletePoolMachine(ctx, dbsqlc.DeletePoolMachineParams{
		OrgID:         orgID,
		ID:            machineID,
		DeleteAttempt: deleteAttempt,
	}); err != nil {
		return fmt.Errorf("delete machine for deletion completion: %w", err)
	}
	if err := qtx.ReleaseMachinePoolCredentialIfIdle(ctx, dbsqlc.ReleaseMachinePoolCredentialIfIdleParams{
		OrgID:         orgID,
		MachinePoolID: *poolID,
	}); err != nil {
		return fmt.Errorf("release machine pool credential for deletion completion: %w", err)
	}
	if _, err := qtx.DestroyUnreferencedSecretVersionsForDeletedOrg(
		ctx,
		dbsqlc.DestroyUnreferencedSecretVersionsForDeletedOrgParams{OrgID: orgID},
	); err != nil {
		return fmt.Errorf("destroy deleted-org secret versions for deletion completion: %w", err)
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "complete pool machine deletion"); err != nil {
		return err
	}
	return nil
}
