package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/management"
)

const defaultProviderRuntimePageSize int32 = 200

// ProviderRuntimeScopeKey is an opaque, non-secret identity for provider
// resources that can be observed with one credential and provider config.
type ProviderRuntimeScopeKey string

type ProviderRuntimeCandidate struct {
	ScopeKey                     ProviderRuntimeScopeKey
	OrgID                        ID
	MachineID                    ID
	MachinePoolID                ID
	Provider                     string
	ProviderResourceID           string
	MachineProvisioning          MachineProvisioningConfig
	LifecycleVersion             int64
	CurrentDaemonRuntimeID       ID
	InactiveSince                time.Time
	ProviderRuntimeMismatchSince *time.Time
	WakeAttemptExpiresAt         *time.Time
	ManagementKind               management.Kind
	ProviderConfig               json.RawMessage
	ProviderAuthSecretID         ID
	ProviderAuthEnvVar           string
	ProviderAuthVersionID        ID
}

type ProviderRuntimeInactiveKind uint8

const (
	ProviderRuntimeInactive ProviderRuntimeInactiveKind = iota
	ProviderRuntimeTerminated
)

type ListProviderRuntimeCandidatesInput struct {
	AfterMachineID ID
	Limit          int32
}

type ProviderRuntimeMismatchCursor struct {
	MismatchSince time.Time
	MachineID     ID
}

type ListDueProviderRuntimeMismatchesInput struct {
	After             ProviderRuntimeMismatchCursor
	Limit             int32
	ConfirmationGrace time.Duration
	InactivityGrace   time.Duration
}

func (s *Store) ListProviderRuntimeDiscoveryCandidates(
	ctx context.Context,
	input ListProviderRuntimeCandidatesInput,
) ([]ProviderRuntimeCandidate, error) {
	rows, err := s.q.ListProviderRuntimeDiscoveryCandidates(
		ctx,
		dbsqlc.ListProviderRuntimeDiscoveryCandidatesParams{
			CursorSet:      input.AfterMachineID != NilID,
			AfterMachineID: input.AfterMachineID,
			RowLimit:       providerRuntimePageLimit(input.Limit),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list provider runtime discovery candidates: %w", err)
	}
	out := make([]ProviderRuntimeCandidate, 0, len(rows))
	for _, row := range rows {
		candidate, err := providerRuntimeCandidateFromColumns(
			row.ScopeKey,
			row.OrgID,
			row.MachineID,
			row.MachinePoolID,
			row.Provider,
			row.ProviderResourceID,
			row.Cpu,
			row.MemoryMb,
			row.ProviderOptions,
			row.LifecycleVersion,
			row.CurrentDaemonRuntimeID,
			row.InactiveSince,
			row.ProviderRuntimeMismatchSince,
			row.WakeAttemptExpiresAt,
			row.ManagementKind,
			row.ProviderConfig,
			row.ProviderAuthSecretID,
			row.ProviderAuthEnvVar,
			row.ProviderAuthVersionID,
		)
		if err != nil {
			return nil, fmt.Errorf("decode provider runtime discovery candidate: %w", err)
		}
		out = append(out, candidate)
	}
	return out, nil
}

func (s *Store) ListDueProviderRuntimeMismatches(
	ctx context.Context,
	input ListDueProviderRuntimeMismatchesInput,
) ([]ProviderRuntimeCandidate, error) {
	cursorSet := input.After.MachineID != NilID
	if cursorSet != !input.After.MismatchSince.IsZero() {
		return nil, errors.New("due provider runtime cursor requires mismatch time and machine id")
	}
	if input.ConfirmationGrace < time.Millisecond || input.InactivityGrace < time.Millisecond {
		return nil, errors.New(
			"provider runtime reconciliation graces must be at least one millisecond",
		)
	}
	rows, err := s.q.ListDueProviderRuntimeMismatches(
		ctx,
		dbsqlc.ListDueProviderRuntimeMismatchesParams{
			ConfirmationGraceMilliseconds: input.ConfirmationGrace.Milliseconds(),
			InactivityGraceMilliseconds:   input.InactivityGrace.Milliseconds(),
			CursorSet:                     cursorSet,
			AfterMismatchSince:            input.After.MismatchSince,
			AfterMachineID:                input.After.MachineID,
			RowLimit:                      providerRuntimePageLimit(input.Limit),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list due provider runtime mismatches: %w", err)
	}
	out := make([]ProviderRuntimeCandidate, 0, len(rows))
	for _, row := range rows {
		candidate, err := providerRuntimeCandidateFromColumns(
			row.ScopeKey,
			row.OrgID,
			row.MachineID,
			row.MachinePoolID,
			row.Provider,
			row.ProviderResourceID,
			row.Cpu,
			row.MemoryMb,
			row.ProviderOptions,
			row.LifecycleVersion,
			row.CurrentDaemonRuntimeID,
			row.InactiveSince,
			row.ProviderRuntimeMismatchSince,
			row.WakeAttemptExpiresAt,
			row.ManagementKind,
			row.ProviderConfig,
			row.ProviderAuthSecretID,
			row.ProviderAuthEnvVar,
			row.ProviderAuthVersionID,
		)
		if err != nil {
			return nil, fmt.Errorf("decode due provider runtime mismatch: %w", err)
		}
		out = append(out, candidate)
	}
	return out, nil
}

func providerRuntimePageLimit(limit int32) int32 {
	if limit <= 0 {
		limit = defaultProviderRuntimePageSize
	}
	return limit
}

func providerRuntimeCandidateFromColumns(
	scopeKey string,
	orgID, machineID ID,
	machinePoolID *ID,
	provider string,
	providerResourceID *string,
	cpu, memoryMB *int32,
	providerOptions *json.RawMessage,
	lifecycleVersion int64,
	currentDaemonRuntimeID *ID,
	inactiveSince time.Time,
	mismatchSince *time.Time,
	wakeAttemptExpiresAt *time.Time,
	managementKind string,
	providerConfig json.RawMessage,
	providerAuthSecretID *ID,
	providerAuthEnvVar string,
	providerAuthVersionID *ID,
) (ProviderRuntimeCandidate, error) {
	if machinePoolID == nil || providerResourceID == nil || currentDaemonRuntimeID == nil ||
		*providerResourceID == "" || scopeKey == "" {
		return ProviderRuntimeCandidate{}, errors.New("provider runtime candidate is missing required identity")
	}
	kind := management.Kind(managementKind)
	if err := management.Validate(kind); err != nil {
		return ProviderRuntimeCandidate{}, err
	}
	provisioning, err := machineProvisioningFromColumns(
		intPtrFromSQLC(cpu),
		intPtrFromSQLC(memoryMB),
		rawMessageFromSQLCPtr(providerOptions),
	)
	if err != nil {
		return ProviderRuntimeCandidate{}, err
	}
	return ProviderRuntimeCandidate{
		ScopeKey:                     ProviderRuntimeScopeKey(scopeKey),
		OrgID:                        orgID,
		MachineID:                    machineID,
		MachinePoolID:                *machinePoolID,
		Provider:                     provider,
		ProviderResourceID:           *providerResourceID,
		MachineProvisioning:          provisioning,
		LifecycleVersion:             lifecycleVersion,
		CurrentDaemonRuntimeID:       *currentDaemonRuntimeID,
		InactiveSince:                inactiveSince,
		ProviderRuntimeMismatchSince: mismatchSince,
		WakeAttemptExpiresAt:         wakeAttemptExpiresAt,
		ManagementKind:               kind,
		ProviderConfig:               providerConfig,
		ProviderAuthSecretID:         idFromSQLCPtr(providerAuthSecretID),
		ProviderAuthEnvVar:           providerAuthEnvVar,
		ProviderAuthVersionID:        idFromSQLCPtr(providerAuthVersionID),
	}, nil
}

func (s *Store) MarkProviderRuntimeMismatch(
	ctx context.Context,
	candidate ProviderRuntimeCandidate,
) (bool, error) {
	if candidate.ProviderRuntimeMismatchSince != nil {
		return false, nil
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin provider runtime mismatch mark: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	if _, err := qtx.LockMachineForLifecycle(
		ctx,
		dbsqlc.LockMachineForLifecycleParams{
			OrgID: candidate.OrgID,
			ID:    candidate.MachineID,
		},
	); errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("lock machine for provider runtime mismatch: %w", err)
	}

	_, err = qtx.MarkProviderRuntimeMismatch(ctx, dbsqlc.MarkProviderRuntimeMismatchParams{
		OrgID:              candidate.OrgID,
		MachineID:          candidate.MachineID,
		MachinePoolID:      candidate.MachinePoolID,
		LifecycleVersion:   candidate.LifecycleVersion,
		Provider:           candidate.Provider,
		ProviderResourceID: sqlcTextFromEmpty(candidate.ProviderResourceID),
		DaemonRuntimeID:    candidate.CurrentDaemonRuntimeID,
		InactiveSince:      candidate.InactiveSince,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("mark provider runtime mismatch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit provider runtime mismatch mark: %w", err)
	}
	return true, nil
}

type ProviderRuntimeInactiveObservationResult struct {
	Applied            bool
	WakeAttemptCleared bool
}

func (s *Store) ApplyProviderRuntimeInactiveObservation(
	ctx context.Context,
	candidate ProviderRuntimeCandidate,
	kind ProviderRuntimeInactiveKind,
) (ProviderRuntimeInactiveObservationResult, error) {
	if kind != ProviderRuntimeInactive && kind != ProviderRuntimeTerminated {
		return ProviderRuntimeInactiveObservationResult{}, errors.New(
			"provider runtime inactive kind is invalid",
		)
	}
	if candidate.ProviderRuntimeMismatchSince == nil && candidate.WakeAttemptExpiresAt == nil {
		return ProviderRuntimeInactiveObservationResult{}, nil
	}
	row, err := s.q.ApplyProviderRuntimeInactiveObservation(
		ctx,
		dbsqlc.ApplyProviderRuntimeInactiveObservationParams{
			ClearActiveWake:      kind == ProviderRuntimeTerminated,
			OrgID:                candidate.OrgID,
			MachineID:            candidate.MachineID,
			MachinePoolID:        candidate.MachinePoolID,
			LifecycleVersion:     candidate.LifecycleVersion,
			Provider:             candidate.Provider,
			ProviderResourceID:   sqlcTextFromEmpty(candidate.ProviderResourceID),
			MismatchSince:        candidate.ProviderRuntimeMismatchSince,
			WakeAttemptExpiresAt: candidate.WakeAttemptExpiresAt,
			DaemonRuntimeID:      candidate.CurrentDaemonRuntimeID,
			InactiveSince:        candidate.InactiveSince,
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProviderRuntimeInactiveObservationResult{}, nil
	}
	if err != nil {
		return ProviderRuntimeInactiveObservationResult{}, fmt.Errorf(
			"apply inactive provider runtime observation: %w",
			err,
		)
	}
	return ProviderRuntimeInactiveObservationResult{
		Applied:            true,
		WakeAttemptCleared: row.WakeAttemptCleared,
	}, nil
}

type ClaimProviderRuntimeMismatchDeletionInput struct {
	Candidate         ProviderRuntimeCandidate
	ConfirmationGrace time.Duration
	InactivityGrace   time.Duration
}

func (s *Store) ClaimProviderRuntimeMismatchDeletion(
	ctx context.Context,
	input ClaimProviderRuntimeMismatchDeletionInput,
) (PoolMachineDeletionClaim, bool, error) {
	candidate := input.Candidate
	if candidate.OrgID == NilID || candidate.MachineID == NilID ||
		candidate.MachinePoolID == NilID || candidate.CurrentDaemonRuntimeID == NilID ||
		candidate.Provider == "" || candidate.ProviderResourceID == "" ||
		candidate.ProviderRuntimeMismatchSince == nil {
		return PoolMachineDeletionClaim{}, false, errors.New(
			"provider runtime mismatch deletion requires complete candidate identity",
		)
	}
	if input.ConfirmationGrace < time.Millisecond || input.InactivityGrace < time.Millisecond {
		return PoolMachineDeletionClaim{}, false, errors.New(
			"provider runtime reconciliation graces must be at least one millisecond",
		)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return PoolMachineDeletionClaim{}, false, fmt.Errorf(
			"begin provider runtime mismatch deletion claim: %w",
			err,
		)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	txNotifications := s.newTxNotifications()
	_, err = qtx.LockProviderRuntimeProtectionPool(
		ctx,
		dbsqlc.LockProviderRuntimeProtectionPoolParams{
			OrgID:                candidate.OrgID,
			MachinePoolID:        candidate.MachinePoolID,
			Provider:             candidate.Provider,
			ManagementKind:       string(candidate.ManagementKind),
			ProviderConfig:       candidate.ProviderConfig,
			ProviderAuthSecretID: sqlcIDFromNil(candidate.ProviderAuthSecretID),
			ProviderAuthEnvVar:   candidate.ProviderAuthEnvVar,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineDeletionClaim{}, false, nil
	}
	if err != nil {
		return PoolMachineDeletionClaim{}, false, fmt.Errorf(
			"lock provider runtime protection pool: %w",
			err,
		)
	}
	if candidate.ManagementKind == management.Tenant {
		if candidate.ProviderAuthSecretID == NilID || candidate.ProviderAuthVersionID == NilID {
			return PoolMachineDeletionClaim{}, false, errors.New(
				"tenant provider runtime mismatch deletion requires a credential version",
			)
		}
		if _, err := qtx.LockProviderRuntimeCredential(
			ctx,
			dbsqlc.LockProviderRuntimeCredentialParams{
				OrgID:                 candidate.OrgID,
				ProviderAuthSecretID:  candidate.ProviderAuthSecretID,
				ProviderAuthVersionID: sqlcIDFromNil(candidate.ProviderAuthVersionID),
			},
		); errors.Is(err, pgx.ErrNoRows) {
			return PoolMachineDeletionClaim{}, false, nil
		} else if err != nil {
			return PoolMachineDeletionClaim{}, false, fmt.Errorf(
				"lock provider runtime credential: %w",
				err,
			)
		}
	} else if candidate.ProviderAuthVersionID != NilID {
		return PoolMachineDeletionClaim{}, false, errors.New(
			"cluster provider runtime mismatch deletion has a tenant credential version",
		)
	}
	if _, err := qtx.LockMachineForLifecycle(
		ctx,
		dbsqlc.LockMachineForLifecycleParams{
			OrgID: candidate.OrgID,
			ID:    candidate.MachineID,
		},
	); errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineDeletionClaim{}, false, nil
	} else if err != nil {
		return PoolMachineDeletionClaim{}, false, fmt.Errorf(
			"lock provider runtime mismatch machine: %w",
			err,
		)
	}
	row, err := qtx.ClaimProviderRuntimeMismatchDeletion(
		ctx,
		dbsqlc.ClaimProviderRuntimeMismatchDeletionParams{
			ClaimTimeoutSeconds:           int64(poolMachineDeletionLeaseDuration / time.Second),
			OrgID:                         candidate.OrgID,
			MachineID:                     candidate.MachineID,
			MachinePoolID:                 candidate.MachinePoolID,
			LifecycleVersion:              candidate.LifecycleVersion,
			Provider:                      candidate.Provider,
			ProviderResourceID:            sqlcTextFromEmpty(candidate.ProviderResourceID),
			MismatchSince:                 *candidate.ProviderRuntimeMismatchSince,
			ConfirmationGraceMilliseconds: input.ConfirmationGrace.Milliseconds(),
			DaemonRuntimeID:               candidate.CurrentDaemonRuntimeID,
			InactiveSince:                 candidate.InactiveSince,
			InactivityGraceMilliseconds:   input.InactivityGrace.Milliseconds(),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineDeletionClaim{}, false, nil
	}
	if err != nil {
		return PoolMachineDeletionClaim{}, false, fmt.Errorf(
			"claim provider runtime mismatch deletion: %w",
			err,
		)
	}
	claim := providerRuntimeMismatchDeletionClaimFromSQLC(row)
	claim, err = s.finalizePoolMachineDeletionClaimTx(
		ctx,
		tx,
		qtx,
		txNotifications,
		claim,
		"claim provider runtime mismatch deletion",
		ProcessToolReasonMachineUnreachable,
	)
	if err != nil {
		return PoolMachineDeletionClaim{}, false, err
	}
	return claim, true, nil
}
