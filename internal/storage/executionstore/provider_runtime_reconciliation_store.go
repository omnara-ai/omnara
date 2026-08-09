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
	ManagementKind               management.Kind
	ProviderConfig               json.RawMessage
	ProviderAuthSecretID         ID
	ProviderAuthEnvVar           string
	ProviderAuthVersionID        ID
	PoolUpdatedAt                time.Time
}

type ListProviderRuntimeCandidatesInput struct {
	AfterMachineID ID
	Limit          int32
}

type ListDueProviderRuntimeMismatchesInput struct {
	ListProviderRuntimeCandidatesInput
	SourceAfterMismatchSince time.Time
	ConfirmationGrace        time.Duration
	InactivityGrace          time.Duration
}

func (s *Store) ListProviderRuntimeDiscoveryCandidates(
	ctx context.Context,
	input ListProviderRuntimeCandidatesInput,
) ([]ProviderRuntimeCandidate, error) {
	cursorSet, limit := prepareProviderRuntimePage(input)
	rows, err := s.q.ListProviderRuntimeDiscoveryCandidates(
		ctx,
		dbsqlc.ListProviderRuntimeDiscoveryCandidatesParams{
			CursorSet:      cursorSet,
			AfterMachineID: input.AfterMachineID,
			RowLimit:       limit,
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
			row.ManagementKind,
			row.ProviderConfig,
			row.ProviderAuthSecretID,
			row.ProviderAuthEnvVar,
			row.ProviderAuthVersionID,
			row.PoolUpdatedAt,
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
	cursorSet, limit := prepareProviderRuntimePage(input.ListProviderRuntimeCandidatesInput)
	if cursorSet != !input.SourceAfterMismatchSince.IsZero() {
		return nil, errors.New("due provider runtime cursor requires mismatch time and machine id")
	}
	if input.ConfirmationGrace < time.Millisecond || input.InactivityGrace < time.Millisecond {
		return nil, errors.New("provider runtime mismatch grace must be at least one millisecond")
	}
	rows, err := s.q.ListDueProviderRuntimeMismatches(
		ctx,
		dbsqlc.ListDueProviderRuntimeMismatchesParams{
			ConfirmationGraceMilliseconds: input.ConfirmationGrace.Milliseconds(),
			InactivityGraceMilliseconds:   input.InactivityGrace.Milliseconds(),
			CursorSet:                     cursorSet,
			AfterMismatchSince:            input.SourceAfterMismatchSince,
			AfterMachineID:                input.AfterMachineID,
			RowLimit:                      limit,
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
			row.ManagementKind,
			row.ProviderConfig,
			row.ProviderAuthSecretID,
			row.ProviderAuthEnvVar,
			row.ProviderAuthVersionID,
			row.PoolUpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("decode due provider runtime mismatch: %w", err)
		}
		out = append(out, candidate)
	}
	return out, nil
}

func prepareProviderRuntimePage(input ListProviderRuntimeCandidatesInput) (bool, int32) {
	cursorSet := input.AfterMachineID != NilID
	limit := input.Limit
	if limit <= 0 {
		limit = defaultProviderRuntimePageSize
	}
	return cursorSet, limit
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
	managementKind string,
	providerConfig json.RawMessage,
	providerAuthSecretID *ID,
	providerAuthEnvVar string,
	providerAuthVersionID *ID,
	poolUpdatedAt time.Time,
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
		ManagementKind:               kind,
		ProviderConfig:               providerConfig,
		ProviderAuthSecretID:         idFromSQLCPtr(providerAuthSecretID),
		ProviderAuthEnvVar:           providerAuthEnvVar,
		ProviderAuthVersionID:        idFromSQLCPtr(providerAuthVersionID),
		PoolUpdatedAt:                poolUpdatedAt,
	}, nil
}

func (s *Store) MarkProviderRuntimeMismatch(
	ctx context.Context,
	candidate ProviderRuntimeCandidate,
) (time.Time, bool, error) {
	markedAt, err := s.q.MarkProviderRuntimeMismatch(ctx, dbsqlc.MarkProviderRuntimeMismatchParams{
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
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("mark provider runtime mismatch: %w", err)
	}
	if markedAt == nil {
		return time.Time{}, false, errors.New("mark provider runtime mismatch returned no timestamp")
	}
	return *markedAt, true, nil
}

func (s *Store) ClearProviderRuntimeMismatch(
	ctx context.Context,
	candidate ProviderRuntimeCandidate,
) (bool, error) {
	if candidate.ProviderRuntimeMismatchSince == nil {
		return false, nil
	}
	_, err := s.q.ClearProviderRuntimeMismatch(ctx, dbsqlc.ClearProviderRuntimeMismatchParams{
		OrgID:              candidate.OrgID,
		MachineID:          candidate.MachineID,
		MachinePoolID:      candidate.MachinePoolID,
		LifecycleVersion:   candidate.LifecycleVersion,
		Provider:           candidate.Provider,
		ProviderResourceID: sqlcTextFromEmpty(candidate.ProviderResourceID),
		MismatchSince:      *candidate.ProviderRuntimeMismatchSince,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("clear provider runtime mismatch: %w", err)
	}
	return true, nil
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
			"provider runtime mismatch grace must be at least one millisecond",
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
			PoolUpdatedAt:        candidate.PoolUpdatedAt,
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
	)
	if err != nil {
		return PoolMachineDeletionClaim{}, false, err
	}
	return claim, true, nil
}
