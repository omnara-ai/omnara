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

var ErrAppRuntimeLeaseLost = errors.New("app runtime lease lost")

// AppRuntimeRevision pins the setup and credential used to open the provider
// session. Behavior settings do not fence it. It contains no secret material.
type AppRuntimeRevision struct {
	ProjectID, AppID    uuid.UUID
	Key                 string
	SetupRevision       int64
	CredentialVersionID uuid.UUID
}

type AppRuntimeLease struct {
	AppRuntimeRevision
	Token uuid.UUID
}

type AppRuntimeClaim struct {
	Lease      AppRuntimeLease
	Checkpoint json.RawMessage
}

func (r AppRuntimeRevision) validate() error {
	if r.ProjectID == uuid.Nil || r.AppID == uuid.Nil || r.CredentialVersionID == uuid.Nil ||
		r.SetupRevision <= 0 ||
		len(r.Key) == 0 ||
		len(r.Key) > 128 ||
		dbsafe.Text(r.Key) != nil {
		return storeerr.InvalidRequest(
			errors.New("runtime requires project, app, key and credential/configuration revisions"),
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

func (s *Store) ListPersistentApps(
	ctx context.Context,
	after uuid.UUID,
	limit int,
) ([]IntegrationInboxApp, error) {
	if err := validateInboxBatch(limit); err != nil {
		return nil, err
	}
	rows, err := s.q.ListPersistentApps(
		ctx,
		dbsqlc.ListPersistentAppsParams{AfterID: storeutil.IDFromNil(after), RowLimit: int32(limit)},
	)
	if err != nil {
		return nil, err
	}
	result := make([]IntegrationInboxApp, 0, len(rows))
	for _, row := range rows {
		result = append(result, IntegrationInboxApp{ProjectID: row.ProjectID, AppID: row.ID})
	}
	return result, nil
}

func (s *Store) ClaimAppRuntime(
	ctx context.Context,
	revision AppRuntimeRevision,
	duration time.Duration,
) (AppRuntimeClaim, bool, error) {
	if err := revision.validate(); err != nil {
		return AppRuntimeClaim{}, false, err
	}
	if err := validateRuntimeLeaseDuration(duration); err != nil {
		return AppRuntimeClaim{}, false, err
	}
	claimable, err := s.q.AppRuntimeClaimable(ctx, dbsqlc.AppRuntimeClaimableParams{
		ProjectID: revision.ProjectID, AppID: revision.AppID, RuntimeKey: revision.Key,
		SetupRevision: revision.SetupRevision, CredentialVersionID: revision.CredentialVersionID,
	})
	if err != nil || !claimable {
		return AppRuntimeClaim{}, false, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AppRuntimeClaim{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.lockRuntimeAuthority(ctx, tx, revision); err != nil {
		return AppRuntimeClaim{}, false, err
	}
	token := uuid.New()
	q := dbsqlc.New(tx)
	_, err = q.ClaimAppRuntime(ctx, dbsqlc.ClaimAppRuntimeParams{
		ProjectID: revision.ProjectID, AppID: revision.AppID, RuntimeKey: revision.Key,
		SetupRevision: revision.SetupRevision, CredentialVersionID: revision.CredentialVersionID,
		ClaimToken: &token, LeaseMilliseconds: duration.Milliseconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return AppRuntimeClaim{}, false, nil
	}
	if err != nil {
		return AppRuntimeClaim{}, false, fmt.Errorf("claim integration runtime: %w", err)
	}
	lease := AppRuntimeLease{AppRuntimeRevision: revision, Token: token}
	// INSERT may wait behind another owner; validate time in a fresh statement.
	row, err := q.ReadAppRuntimeLease(ctx, runtimeLeaseParams(lease))
	if errors.Is(err, pgx.ErrNoRows) {
		return AppRuntimeClaim{}, false, nil
	}
	if err != nil {
		return AppRuntimeClaim{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AppRuntimeClaim{}, false, err
	}
	claim := AppRuntimeClaim{Lease: lease}
	if row.Checkpoint != nil {
		claim.Checkpoint = *row.Checkpoint
	}
	return claim, true, nil
}

func (s *Store) lockRuntimeAuthority(ctx context.Context, tx pgx.Tx, revision AppRuntimeRevision) error {
	if err := s.enterInboxApp(ctx, tx, revision.ProjectID, revision.AppID); err != nil {
		return err
	}
	q := dbsqlc.New(tx)
	app, err := getProjectApp(ctx, q, revision.ProjectID, revision.AppID)
	if err != nil {
		return err
	}
	if app.Provider != IntegrationProviderDiscord || app.SetupRevision != revision.SetupRevision {
		return ErrAppRuntimeLeaseLost
	}
	if _, err := secretops.LockReference(ctx, tx, app.OrgID, app.CredentialSecretID); err != nil {
		return err
	}
	secret, err := q.GetProjectAvailableSecret(
		ctx,
		dbsqlc.GetProjectAvailableSecretParams{
			OrgID:     app.OrgID,
			ProjectID: app.ProjectID,
			SecretID:  app.CredentialSecretID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAppRuntimeLeaseLost
	}
	if err != nil {
		return err
	}
	if secret.CurrentVersionID == nil || *secret.CurrentVersionID != revision.CredentialVersionID {
		return ErrAppRuntimeLeaseLost
	}
	return nil
}

func runtimeLeaseParams(lease AppRuntimeLease) dbsqlc.ReadAppRuntimeLeaseParams {
	return dbsqlc.ReadAppRuntimeLeaseParams{
		ProjectID:           lease.ProjectID,
		AppID:               lease.AppID,
		RuntimeKey:          lease.Key,
		SetupRevision:       lease.SetupRevision,
		CredentialVersionID: lease.CredentialVersionID,
		ClaimToken:          lease.Token,
	}
}

func (s *Store) withAppRuntime(
	ctx context.Context,
	lease AppRuntimeLease,
	apply func(*dbsqlc.Queries) error,
) error {
	if err := lease.AppRuntimeRevision.validate(); err != nil {
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
	if err := s.lockRuntimeAuthority(ctx, tx, lease.AppRuntimeRevision); err != nil {
		return err
	}
	q := dbsqlc.New(tx)
	if _, err := q.LockAppRuntime(
		ctx,
		dbsqlc.LockAppRuntimeParams{
			ProjectID:  lease.ProjectID,
			AppID:      lease.AppID,
			RuntimeKey: lease.Key,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAppRuntimeLeaseLost
		}
		return err
	}
	if _, err := q.ReadAppRuntimeLease(ctx, runtimeLeaseParams(lease)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAppRuntimeLeaseLost
		}
		return err
	}
	if err := apply(q); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RenewAppRuntime(
	ctx context.Context,
	lease AppRuntimeLease,
	duration time.Duration,
) error {
	if err := validateRuntimeLeaseDuration(duration); err != nil {
		return err
	}
	return s.withAppRuntime(ctx, lease, func(q *dbsqlc.Queries) error {
		rows, err := q.RenewAppRuntime(ctx, dbsqlc.RenewAppRuntimeParams{
			ProjectID:         lease.ProjectID,
			AppID:             lease.AppID,
			RuntimeKey:        lease.Key,
			ClaimToken:        lease.Token,
			LeaseMilliseconds: duration.Milliseconds(),
		})
		if err == nil && rows != 1 {
			return ErrAppRuntimeLeaseLost
		}
		return err
	})
}

// CommitAppRuntime records verified receipt bytes and the provider resume
// checkpoint in the same transaction. Nil receipt advances an irrelevant dispatch;
// nil checkpoint deliberately resets a provider session that can no longer resume.
func (s *Store) CommitAppRuntime(
	ctx context.Context,
	lease AppRuntimeLease,
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
		if receipt.ProjectID != lease.ProjectID || receipt.AppID != lease.AppID {
			return storeerr.ErrUnauthorized
		}
	}
	return s.withAppRuntime(ctx, lease, func(q *dbsqlc.Queries) error {
		if receipt != nil {
			_, err := q.InsertIntegrationInboxReceipt(ctx, dbsqlc.InsertIntegrationInboxReceiptParams{
				ProjectID:  receipt.ProjectID,
				AppID:      receipt.AppID,
				ReceiptKey: receipt.ReceiptKey,
				Payload:    receipt.Payload,
			})
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		rows, err := q.CheckpointAppRuntime(ctx, dbsqlc.CheckpointAppRuntimeParams{
			ProjectID:  lease.ProjectID,
			AppID:      lease.AppID,
			RuntimeKey: lease.Key,
			ClaimToken: lease.Token,
			Checkpoint: checkpointValue,
		})
		if err == nil && rows != 1 {
			return ErrAppRuntimeLeaseLost
		}
		return err
	})
}

func (s *Store) ReleaseAppRuntime(
	ctx context.Context,
	lease AppRuntimeLease,
	delay time.Duration,
	failure string,
) error {
	if err := lease.AppRuntimeRevision.validate(); err != nil {
		return err
	}
	if lease.Token == uuid.Nil || delay < 0 || delay > 24*time.Hour || len(failure) > 4096 ||
		dbsafe.Text(failure) != nil {
		return storeerr.InvalidRequest(errors.New("invalid runtime release"))
	}
	rows, err := s.q.ReleaseAppRuntime(ctx, dbsqlc.ReleaseAppRuntimeParams{
		ProjectID: lease.ProjectID, AppID: lease.AppID, RuntimeKey: lease.Key, ClaimToken: lease.Token,
		DelayMilliseconds: delay.Milliseconds(), LastError: storeutil.TextFromEmpty(failure),
	})
	if err == nil && rows != 1 {
		return ErrAppRuntimeLeaseLost
	}
	return err
}
