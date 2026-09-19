package integrationstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/secretops"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var ErrIntegrationRuntimeLeaseLost = errors.New("integration runtime lease lost")

// RuntimeRevision pins the configuration and credential actually used to open
// the provider connection. It contains no decrypted secret material.
type RuntimeRevision struct {
	ProjectID, ConnectionID uuid.UUID
	Key                     string
	ConnectionUpdatedAt     time.Time
	CredentialVersionID     uuid.UUID
}

type IntegrationRuntimeLease struct {
	RuntimeRevision
	Token uuid.UUID
}

type IntegrationRuntimeClaim struct {
	Lease      IntegrationRuntimeLease
	Checkpoint json.RawMessage
}

func (r RuntimeRevision) validate() error {
	if r.ProjectID == uuid.Nil || r.ConnectionID == uuid.Nil || r.CredentialVersionID == uuid.Nil ||
		r.ConnectionUpdatedAt.IsZero() ||
		len(r.Key) == 0 ||
		len(r.Key) > 128 ||
		dbsafe.Text(r.Key) != nil {
		return storeerr.InvalidRequest(
			errors.New("runtime requires project, connection, key and credential/configuration revisions"),
		)
	}
	return nil
}

func validateRuntimeLeaseDuration(duration time.Duration) error {
	if duration < 5*time.Second || duration > time.Minute {
		return storeerr.InvalidRequest(errors.New("runtime lease must be between five seconds and one minute"))
	}
	return nil
}

func (s *Store) ListPersistentIntegrationConnections(
	ctx context.Context,
	after uuid.UUID,
	limit int,
) ([]IntegrationInboxConnection, error) {
	if err := validateInboxBatch(limit); err != nil {
		return nil, err
	}
	rows, err := s.q.ListPersistentIntegrationConnections(
		ctx,
		dbsqlc.ListPersistentIntegrationConnectionsParams{AfterID: storeutil.IDFromNil(after), RowLimit: int32(limit)},
	)
	if err != nil {
		return nil, err
	}
	result := make([]IntegrationInboxConnection, 0, len(rows))
	for _, row := range rows {
		result = append(result, IntegrationInboxConnection{ProjectID: row.ProjectID, ConnectionID: row.ID})
	}
	return result, nil
}

func (s *Store) ClaimIntegrationRuntime(
	ctx context.Context,
	revision RuntimeRevision,
	duration time.Duration,
) (IntegrationRuntimeClaim, bool, error) {
	if err := revision.validate(); err != nil {
		return IntegrationRuntimeClaim{}, false, err
	}
	if err := validateRuntimeLeaseDuration(duration); err != nil {
		return IntegrationRuntimeClaim{}, false, err
	}
	claimable, err := s.q.IntegrationConnectionRuntimeClaimable(ctx, dbsqlc.IntegrationConnectionRuntimeClaimableParams{
		ProjectID: revision.ProjectID, ConnectionID: revision.ConnectionID, RuntimeKey: revision.Key,
		ConnectionUpdatedAt: revision.ConnectionUpdatedAt, CredentialVersionID: revision.CredentialVersionID,
	})
	if err != nil || !claimable {
		return IntegrationRuntimeClaim{}, false, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationRuntimeClaim{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.lockRuntimeAuthority(ctx, tx, revision); err != nil {
		return IntegrationRuntimeClaim{}, false, err
	}
	token := uuid.New()
	q := dbsqlc.New(tx)
	_, err = q.ClaimIntegrationConnectionRuntime(ctx, dbsqlc.ClaimIntegrationConnectionRuntimeParams{
		ProjectID: revision.ProjectID, ConnectionID: revision.ConnectionID, RuntimeKey: revision.Key,
		ConnectionUpdatedAt: revision.ConnectionUpdatedAt, CredentialVersionID: revision.CredentialVersionID,
		ClaimToken: &token, LeaseMilliseconds: duration.Milliseconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationRuntimeClaim{}, false, nil
	}
	if err != nil {
		return IntegrationRuntimeClaim{}, false, fmt.Errorf("claim integration runtime: %w", err)
	}
	lease := IntegrationRuntimeLease{RuntimeRevision: revision, Token: token}
	// INSERT may wait behind another owner; validate time in a fresh statement.
	row, err := q.ReadIntegrationConnectionRuntimeLease(ctx, runtimeLeaseParams(lease))
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationRuntimeClaim{}, false, nil
	}
	if err != nil {
		return IntegrationRuntimeClaim{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationRuntimeClaim{}, false, err
	}
	claim := IntegrationRuntimeClaim{Lease: lease}
	if row.Checkpoint != nil {
		claim.Checkpoint = *row.Checkpoint
	}
	return claim, true, nil
}

func (s *Store) lockRuntimeAuthority(ctx context.Context, tx pgx.Tx, revision RuntimeRevision) error {
	if err := s.enterInboxConnection(ctx, tx, revision.ProjectID, revision.ConnectionID); err != nil {
		return err
	}
	q := dbsqlc.New(tx)
	connection, err := getIntegrationConnection(ctx, q, revision.ProjectID, revision.ConnectionID)
	if err != nil {
		return err
	}
	if connection.Provider != IntegrationProviderDiscord || !connection.UpdatedAt.Equal(revision.ConnectionUpdatedAt) {
		return ErrIntegrationRuntimeLeaseLost
	}
	if _, err := secretops.LockReference(ctx, tx, connection.OrgID, connection.CredentialSecretID); err != nil {
		return err
	}
	secret, err := q.GetProjectAvailableSecret(
		ctx,
		dbsqlc.GetProjectAvailableSecretParams{
			OrgID:     connection.OrgID,
			ProjectID: connection.ProjectID,
			SecretID:  connection.CredentialSecretID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrIntegrationRuntimeLeaseLost
	}
	if err != nil {
		return err
	}
	if secret.CurrentVersionID == nil || *secret.CurrentVersionID != revision.CredentialVersionID {
		return ErrIntegrationRuntimeLeaseLost
	}
	return nil
}

func runtimeLeaseParams(lease IntegrationRuntimeLease) dbsqlc.ReadIntegrationConnectionRuntimeLeaseParams {
	return dbsqlc.ReadIntegrationConnectionRuntimeLeaseParams{
		ProjectID:           lease.ProjectID,
		ConnectionID:        lease.ConnectionID,
		RuntimeKey:          lease.Key,
		ConnectionUpdatedAt: lease.ConnectionUpdatedAt,
		CredentialVersionID: lease.CredentialVersionID,
		ClaimToken:          lease.Token,
	}
}

func (s *Store) withIntegrationRuntime(
	ctx context.Context,
	lease IntegrationRuntimeLease,
	apply func(*dbsqlc.Queries) error,
) error {
	if err := lease.RuntimeRevision.validate(); err != nil {
		return err
	}
	if lease.Token == uuid.Nil {
		return storeerr.InvalidRequest(errors.New("runtime claim token is required"))
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.lockRuntimeAuthority(ctx, tx, lease.RuntimeRevision); err != nil {
		return err
	}
	q := dbsqlc.New(tx)
	if _, err := q.LockIntegrationConnectionRuntime(
		ctx,
		dbsqlc.LockIntegrationConnectionRuntimeParams{
			ProjectID:    lease.ProjectID,
			ConnectionID: lease.ConnectionID,
			RuntimeKey:   lease.Key,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrIntegrationRuntimeLeaseLost
		}
		return err
	}
	if _, err := q.ReadIntegrationConnectionRuntimeLease(ctx, runtimeLeaseParams(lease)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrIntegrationRuntimeLeaseLost
		}
		return err
	}
	if err := apply(q); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RenewIntegrationRuntime(
	ctx context.Context,
	lease IntegrationRuntimeLease,
	duration time.Duration,
) error {
	if err := validateRuntimeLeaseDuration(duration); err != nil {
		return err
	}
	return s.withIntegrationRuntime(ctx, lease, func(q *dbsqlc.Queries) error {
		rows, err := q.RenewIntegrationConnectionRuntime(ctx, dbsqlc.RenewIntegrationConnectionRuntimeParams{
			ProjectID:         lease.ProjectID,
			ConnectionID:      lease.ConnectionID,
			RuntimeKey:        lease.Key,
			ClaimToken:        lease.Token,
			LeaseMilliseconds: duration.Milliseconds(),
		})
		if err == nil && rows != 1 {
			return ErrIntegrationRuntimeLeaseLost
		}
		return err
	})
}

// CommitIntegrationRuntime records verified receipt bytes and the provider resume
// checkpoint in the same transaction. Nil receipt advances an irrelevant dispatch;
// nil checkpoint deliberately resets a provider session that can no longer resume.
func (s *Store) CommitIntegrationRuntime(
	ctx context.Context,
	lease IntegrationRuntimeLease,
	checkpoint json.RawMessage,
	receipt *VerifiedIntegrationReceipt,
) error {
	if checkpoint != nil {
		if len(checkpoint) > 65536 {
			return storeerr.InvalidRequest(errors.New("runtime checkpoint exceeds 65536 bytes"))
		}
		var err error
		checkpoint, err = normalizedJSONObject(checkpoint, "checkpoint")
		if err != nil {
			return storeerr.InvalidRequest(err)
		}
	}
	var checkpointValue *json.RawMessage
	if checkpoint != nil {
		checkpointValue = &checkpoint
	}
	if receipt != nil {
		if err := validateIntegrationReceipt(*receipt); err != nil {
			return err
		}
		if receipt.ProjectID != lease.ProjectID || receipt.ConnectionID != lease.ConnectionID {
			return storeerr.ErrUnauthorized
		}
	}
	return s.withIntegrationRuntime(ctx, lease, func(q *dbsqlc.Queries) error {
		if receipt != nil {
			_, err := q.InsertIntegrationInboxReceipt(ctx, dbsqlc.InsertIntegrationInboxReceiptParams{
				ProjectID:    receipt.ProjectID,
				ConnectionID: receipt.ConnectionID,
				ReceiptKey:   receipt.ReceiptKey,
				Payload:      receipt.Payload,
			})
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		rows, err := q.CheckpointIntegrationConnectionRuntime(ctx, dbsqlc.CheckpointIntegrationConnectionRuntimeParams{
			ProjectID:    lease.ProjectID,
			ConnectionID: lease.ConnectionID,
			RuntimeKey:   lease.Key,
			ClaimToken:   lease.Token,
			Checkpoint:   checkpointValue,
		})
		if err == nil && rows != 1 {
			return ErrIntegrationRuntimeLeaseLost
		}
		return err
	})
}

func (s *Store) ReleaseIntegrationRuntime(
	ctx context.Context,
	lease IntegrationRuntimeLease,
	delay time.Duration,
	failure string,
) error {
	if err := lease.RuntimeRevision.validate(); err != nil {
		return err
	}
	if lease.Token == uuid.Nil || delay < 0 || delay > 24*time.Hour || len(failure) > 4096 ||
		dbsafe.Text(failure) != nil {
		return storeerr.InvalidRequest(errors.New("invalid runtime release"))
	}
	rows, err := s.q.ReleaseIntegrationConnectionRuntime(ctx, dbsqlc.ReleaseIntegrationConnectionRuntimeParams{
		ProjectID: lease.ProjectID, ConnectionID: lease.ConnectionID, RuntimeKey: lease.Key, ClaimToken: lease.Token,
		DelayMilliseconds: delay.Milliseconds(), LastError: storeutil.TextFromEmpty(failure),
	})
	if err == nil && rows != 1 {
		return ErrIntegrationRuntimeLeaseLost
	}
	return err
}
