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
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/secretops"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var ErrIntegrationRuntimeLeaseLost = errors.New("integration runtime lease lost")

const DiscordRuntimeKey = "discord/shard/0"

type IntegrationRuntimeRevision struct {
	ProjectID, IntegrationID uuid.UUID
	Key                      string
	SetupRevision            int64
	CredentialVersionID      uuid.UUID
}

type IntegrationRuntimeLease struct {
	IntegrationRuntimeRevision
	Token uuid.UUID
}

type IntegrationRuntimeClaim struct {
	Lease      IntegrationRuntimeLease
	Checkpoint json.RawMessage
}

type IntegrationRuntimeFailure struct {
	Message string
	RetryAt time.Time
}

func (s *Store) CountUnclaimedDiscordIntegrations(ctx context.Context) (int64, error) {
	return s.q.CountUnclaimedIntegrationRuntimes(ctx, dbsqlc.CountUnclaimedIntegrationRuntimesParams{
		IntegrationTypes: integrationdefinition.IntegrationTypesForProvider(integrationdefinition.ProviderDiscord),
		RuntimeKey:       DiscordRuntimeKey,
	})
}

func (s *Store) GetIntegrationRuntimeFailure(
	ctx context.Context,
	projectID, integrationID uuid.UUID,
	setupRevision int64,
) (IntegrationRuntimeFailure, error) {
	row, err := s.q.GetIntegrationRuntimeFailure(ctx, dbsqlc.GetIntegrationRuntimeFailureParams{
		ProjectID: projectID, IntegrationID: integrationID, SetupRevision: setupRevision,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationRuntimeFailure{}, storeerr.ErrNotFound
	}
	if err != nil {
		return IntegrationRuntimeFailure{}, err
	}
	return IntegrationRuntimeFailure{Message: row.Message, RetryAt: row.RetryAt}, nil
}

func (r IntegrationRuntimeRevision) validate() error {
	if r.ProjectID == uuid.Nil || r.IntegrationID == uuid.Nil || r.CredentialVersionID == uuid.Nil ||
		r.SetupRevision <= 0 ||
		len(r.Key) == 0 ||
		len(r.Key) > 128 ||
		dbsafe.Text(r.Key) != nil {
		return storeerr.InvalidRequest(
			errors.New("runtime requires project, integration, key and credential/configuration revisions"),
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

func (s *Store) ListPersistentIntegrations(
	ctx context.Context,
	after uuid.UUID,
	limit int,
) ([]IntegrationInboxIntegration, error) {
	if err := validateInboxBatch(limit); err != nil {
		return nil, err
	}
	rows, err := s.q.ListPersistentIntegrations(
		ctx,
		dbsqlc.ListPersistentIntegrationsParams{
			IntegrationTypes: integrationdefinition.IntegrationTypesForProvider(integrationdefinition.ProviderDiscord),
			AfterID:          storeutil.IDFromNil(after), RowLimit: int32(limit),
		},
	)
	if err != nil {
		return nil, err
	}
	result := make([]IntegrationInboxIntegration, 0, len(rows))
	for _, row := range rows {
		result = append(result, IntegrationInboxIntegration{ProjectID: row.ProjectID, IntegrationID: row.ID})
	}
	return result, nil
}

func (s *Store) ClaimIntegrationRuntime(
	ctx context.Context,
	revision IntegrationRuntimeRevision,
	duration time.Duration,
) (IntegrationRuntimeClaim, bool, error) {
	if err := revision.validate(); err != nil {
		return IntegrationRuntimeClaim{}, false, err
	}
	if err := validateRuntimeLeaseDuration(duration); err != nil {
		return IntegrationRuntimeClaim{}, false, err
	}
	claimable, err := s.q.IntegrationRuntimeClaimable(ctx, dbsqlc.IntegrationRuntimeClaimableParams{
		ProjectID: revision.ProjectID, IntegrationID: revision.IntegrationID, RuntimeKey: revision.Key,
		SetupRevision: revision.SetupRevision, CredentialVersionID: revision.CredentialVersionID,
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
	_, err = q.ClaimIntegrationRuntime(ctx, dbsqlc.ClaimIntegrationRuntimeParams{
		ProjectID: revision.ProjectID, IntegrationID: revision.IntegrationID, RuntimeKey: revision.Key,
		SetupRevision: revision.SetupRevision, CredentialVersionID: revision.CredentialVersionID,
		ClaimToken: &token, LeaseMilliseconds: duration.Milliseconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationRuntimeClaim{}, false, nil
	}
	if err != nil {
		return IntegrationRuntimeClaim{}, false, fmt.Errorf("claim integration runtime: %w", err)
	}
	lease := IntegrationRuntimeLease{IntegrationRuntimeRevision: revision, Token: token}
	// INSERT may wait behind another owner; validate time in a fresh statement.
	row, err := q.ReadIntegrationRuntimeLease(ctx, runtimeLeaseParams(lease))
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

func (s *Store) lockRuntimeAuthority(ctx context.Context, tx pgx.Tx, revision IntegrationRuntimeRevision) error {
	if err := s.enterInboxIntegration(ctx, tx, revision.ProjectID, revision.IntegrationID); err != nil {
		return err
	}
	q := dbsqlc.New(tx)
	integration, err := getProjectIntegration(ctx, q, revision.ProjectID, revision.IntegrationID)
	if err != nil {
		return err
	}
	if integration.Provider != IntegrationProviderDiscord || integration.SetupRevision != revision.SetupRevision {
		return ErrIntegrationRuntimeLeaseLost
	}
	if _, err := secretops.LockReference(ctx, tx, integration.OrgID, integration.CredentialSecretID); err != nil {
		return err
	}
	secret, err := q.GetProjectAvailableSecret(
		ctx,
		dbsqlc.GetProjectAvailableSecretParams{
			OrgID:     integration.OrgID,
			ProjectID: integration.ProjectID,
			SecretID:  integration.CredentialSecretID,
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

func runtimeLeaseParams(lease IntegrationRuntimeLease) dbsqlc.ReadIntegrationRuntimeLeaseParams {
	return dbsqlc.ReadIntegrationRuntimeLeaseParams{
		ProjectID:           lease.ProjectID,
		IntegrationID:       lease.IntegrationID,
		RuntimeKey:          lease.Key,
		SetupRevision:       lease.SetupRevision,
		CredentialVersionID: lease.CredentialVersionID,
		ClaimToken:          lease.Token,
	}
}

func (s *Store) withIntegrationRuntime(
	ctx context.Context,
	lease IntegrationRuntimeLease,
	apply func(*dbsqlc.Queries) error,
) error {
	if err := lease.IntegrationRuntimeRevision.validate(); err != nil {
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
	if err := s.lockRuntimeAuthority(ctx, tx, lease.IntegrationRuntimeRevision); err != nil {
		return err
	}
	q := dbsqlc.New(tx)
	if _, err := q.LockIntegrationRuntime(
		ctx,
		dbsqlc.LockIntegrationRuntimeParams{
			ProjectID:     lease.ProjectID,
			IntegrationID: lease.IntegrationID,
			RuntimeKey:    lease.Key,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrIntegrationRuntimeLeaseLost
		}
		return err
	}
	if _, err := q.ReadIntegrationRuntimeLease(ctx, runtimeLeaseParams(lease)); err != nil {
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
		rows, err := q.RenewIntegrationRuntime(ctx, dbsqlc.RenewIntegrationRuntimeParams{
			ProjectID:         lease.ProjectID,
			IntegrationID:     lease.IntegrationID,
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
		if receipt.ProjectID != lease.ProjectID || receipt.IntegrationID != lease.IntegrationID {
			return storeerr.ErrUnauthorized
		}
	}
	return s.withIntegrationRuntime(ctx, lease, func(q *dbsqlc.Queries) error {
		if receipt != nil {
			_, err := q.InsertIntegrationInboxReceipt(ctx, dbsqlc.InsertIntegrationInboxReceiptParams{
				ProjectID:     receipt.ProjectID,
				IntegrationID: receipt.IntegrationID,
				ReceiptKey:    receipt.ReceiptKey,
				Payload:       receipt.Payload,
			})
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		rows, err := q.CheckpointIntegrationRuntime(ctx, dbsqlc.CheckpointIntegrationRuntimeParams{
			ProjectID:     lease.ProjectID,
			IntegrationID: lease.IntegrationID,
			RuntimeKey:    lease.Key,
			ClaimToken:    lease.Token,
			Checkpoint:    checkpointValue,
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
	if err := lease.IntegrationRuntimeRevision.validate(); err != nil {
		return err
	}
	if lease.Token == uuid.Nil || delay < 0 || delay > 24*time.Hour || len(failure) > 4096 ||
		dbsafe.Text(failure) != nil {
		return storeerr.InvalidRequest(errors.New("invalid runtime release"))
	}
	rows, err := s.q.ReleaseIntegrationRuntime(ctx, dbsqlc.ReleaseIntegrationRuntimeParams{
		ProjectID: lease.ProjectID, IntegrationID: lease.IntegrationID, RuntimeKey: lease.Key, ClaimToken: lease.Token,
		DelayMilliseconds: delay.Milliseconds(), LastError: storeutil.TextFromEmpty(failure),
	})
	if err == nil && rows != 1 {
		return ErrIntegrationRuntimeLeaseLost
	}
	return err
}
