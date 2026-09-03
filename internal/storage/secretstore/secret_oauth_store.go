package secretstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) RotateProjectAvailableOAuthSecret(
	ctx context.Context,
	input RotateProjectAvailableOAuthSecretInput,
) (SecretRecord, error) {
	if s.secretKeyWrapper == nil {
		return SecretRecord{}, errors.New("secret key wrapper is required")
	}
	if isNilID(input.ProjectID) || isNilID(input.Lease.OrgID) || isNilID(input.Lease.SecretID) ||
		isNilID(input.Lease.OwnerToken) || isNilID(input.Lease.ExpectedCurrentVersionID) {
		return SecretRecord{}, invalidSecretRequest(
			"org, project, secret, refresh lease owner, and expected version are required",
		)
	}
	material, err := secrets.CanonicalizeMaterial(input.Material)
	if err != nil {
		return SecretRecord{}, invalidSecretRequest("%v", err)
	}
	if err := s.ValidateProjectSecretReference(
		ctx,
		input.Lease.OrgID,
		input.ProjectID,
		input.Lease.SecretID,
		SecretKindOAuthTokenSet,
	); err != nil {
		return SecretRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SecretRecord{}, fmt.Errorf("begin rotate project oauth secret: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if err := lockSecretOwnerLifecycleShared(
		ctx,
		qtx,
		input.Lease.OrgID,
		input.Lease.SecretID,
	); err != nil {
		return SecretRecord{}, err
	}
	if _, err := qtx.LockSecret(
		ctx,
		dbsqlc.LockSecretParams{OrgID: input.Lease.OrgID, ID: input.Lease.SecretID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SecretRecord{}, storeerr.ErrNotFound
		}
		return SecretRecord{}, fmt.Errorf("lock secret: %w", err)
	}
	_, err = qtx.LockSecretOAuthRefreshLease(
		ctx,
		dbsqlc.LockSecretOAuthRefreshLeaseParams{
			OrgID:                   input.Lease.OrgID,
			SecretID:                input.Lease.SecretID,
			OwnerToken:              input.Lease.OwnerToken,
			ExpectedSecretVersionID: input.Lease.ExpectedCurrentVersionID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SecretRecord{}, storeerr.ErrConflict
		}
		return SecretRecord{}, fmt.Errorf("lock oauth refresh lease: %w", err)
	}
	active, err := qtx.SecretOAuthRefreshLeaseActive(
		ctx,
		dbsqlc.SecretOAuthRefreshLeaseActiveParams{
			OrgID:      input.Lease.OrgID,
			SecretID:   input.Lease.SecretID,
			OwnerToken: input.Lease.OwnerToken,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SecretRecord{}, storeerr.ErrConflict
		}
		return SecretRecord{}, fmt.Errorf("check oauth refresh lease: %w", err)
	}
	if !active {
		return SecretRecord{}, storeerr.ErrConflict
	}
	secret, err := getSecretTx(ctx, qtx, input.Lease.OrgID, input.Lease.SecretID)
	if err != nil {
		return SecretRecord{}, err
	}
	if secret.Kind != SecretKindOAuthTokenSet {
		return SecretRecord{}, invalidSecretRequest(
			"secret kind %q does not match expected kind %q",
			secret.Kind,
			SecretKindOAuthTokenSet,
		)
	}
	if secret.CurrentVersionID != input.Lease.ExpectedCurrentVersionID {
		return SecretRecord{}, storeerr.ErrConflict
	}
	versionNumber, err := qtx.NextSecretVersionNumber(
		ctx,
		dbsqlc.NextSecretVersionNumberParams{SecretID: input.Lease.SecretID},
	)
	if err != nil {
		return SecretRecord{}, fmt.Errorf("next secret version number: %w", err)
	}
	versionID, err := newSecretUUID()
	if err != nil {
		return SecretRecord{}, err
	}
	if _, err := s.insertSecretVersionTx(ctx, qtx, insertSecretVersionTxInput{
		OrgID:         input.Lease.OrgID,
		SecretID:      input.Lease.SecretID,
		VersionID:     versionID,
		VersionNumber: versionNumber,
		Material:      material,
	}); err != nil {
		return SecretRecord{}, err
	}
	if _, err := qtx.SetSecretCurrentVersion(
		ctx,
		dbsqlc.SetSecretCurrentVersionParams{
			CurrentVersionID: &versionID,
			OrgID:            input.Lease.OrgID,
			ID:               input.Lease.SecretID,
		},
	); err != nil {
		return SecretRecord{}, fmt.Errorf("set secret current version: %w", err)
	}
	updated, err := getSecretTx(ctx, qtx, input.Lease.OrgID, input.Lease.SecretID)
	if err != nil {
		return SecretRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SecretRecord{}, fmt.Errorf("commit rotate project oauth secret: %w", err)
	}
	return updated, nil
}

func (s *Store) AcquireProjectOAuthRefreshLease(
	ctx context.Context,
	input AcquireProjectOAuthRefreshLeaseInput,
) (OAuthRefreshLeaseRecord, bool, error) {
	if input.TTL < time.Millisecond {
		return OAuthRefreshLeaseRecord{}, false, invalidSecretRequest(
			"refresh lease ttl must be at least one millisecond",
		)
	}
	if err := s.ValidateProjectSecretReference(
		ctx,
		input.OrgID,
		input.ProjectID,
		input.SecretID,
		SecretKindOAuthTokenSet,
	); err != nil {
		return OAuthRefreshLeaseRecord{}, false, err
	}
	ownerToken, err := newSecretUUID()
	if err != nil {
		return OAuthRefreshLeaseRecord{}, false, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OAuthRefreshLeaseRecord{}, false, fmt.Errorf("begin acquire oauth refresh lease: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if err := lockSecretOwnerLifecycleShared(
		ctx,
		qtx,
		input.OrgID,
		input.SecretID,
	); err != nil {
		return OAuthRefreshLeaseRecord{}, false, err
	}
	if _, err := qtx.LockSecret(
		ctx,
		dbsqlc.LockSecretParams{OrgID: input.OrgID, ID: input.SecretID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return OAuthRefreshLeaseRecord{}, false, storeerr.ErrNotFound
		}
		return OAuthRefreshLeaseRecord{}, false, fmt.Errorf("lock secret for oauth refresh lease: %w", err)
	}
	secret, err := getSecretTx(ctx, qtx, input.OrgID, input.SecretID)
	if err != nil {
		return OAuthRefreshLeaseRecord{}, false, err
	}
	if secret.Kind != SecretKindOAuthTokenSet {
		return OAuthRefreshLeaseRecord{}, false, invalidSecretRequest(
			"secret kind %q does not match expected kind %q",
			secret.Kind,
			SecretKindOAuthTokenSet,
		)
	}
	row, err := qtx.AcquireSecretOAuthRefreshLease(ctx, dbsqlc.AcquireSecretOAuthRefreshLeaseParams{
		OrgID:                   input.OrgID,
		SecretID:                input.SecretID,
		OwnerToken:              ownerToken,
		ExpectedSecretVersionID: secret.CurrentVersionID,
		TtlMilliseconds:         input.TTL.Milliseconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthRefreshLeaseRecord{}, false, nil
	}
	if err != nil {
		return OAuthRefreshLeaseRecord{}, false, fmt.Errorf("acquire oauth refresh lease: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return OAuthRefreshLeaseRecord{}, false, fmt.Errorf("commit oauth refresh lease: %w", err)
	}
	return OAuthRefreshLeaseRecord{
		OrgID:                    input.OrgID,
		SecretID:                 input.SecretID,
		OwnerToken:               row.OwnerToken,
		ExpectedCurrentVersionID: row.ExpectedSecretVersionID,
	}, true, nil
}

func (s *Store) ReleaseProjectOAuthRefreshLease(
	ctx context.Context,
	lease OAuthRefreshLeaseRecord,
) error {
	if isNilID(lease.OrgID) || isNilID(lease.SecretID) || isNilID(lease.OwnerToken) {
		return invalidSecretRequest("org, secret, and owner token are required")
	}
	if err := s.q.ReleaseSecretOAuthRefreshLease(
		ctx,
		dbsqlc.ReleaseSecretOAuthRefreshLeaseParams{
			OrgID:      lease.OrgID,
			SecretID:   lease.SecretID,
			OwnerToken: lease.OwnerToken,
		},
	); err != nil {
		return fmt.Errorf("release oauth refresh lease: %w", err)
	}
	return nil
}

func oauthAccessTokenExpiry(kind secrets.Kind, ttl time.Duration) (bool, int64, error) {
	if ttl < 0 {
		return false, 0, errors.New("oauth access token ttl cannot be negative")
	}
	if ttl == 0 {
		return false, 0, nil
	}
	if kind != SecretKindOAuthTokenSet {
		return false, 0, errors.New("oauth access token ttl requires an oauth token-set secret")
	}
	seconds := int64(ttl / time.Second)
	if seconds == 0 {
		return false, 0, errors.New("oauth access token ttl must have at least one second remaining")
	}
	return true, seconds, nil
}

func (s *Store) MCPOAuthFlowConsumed(ctx context.Context, flowID ID) (bool, error) {
	if isNilID(flowID) {
		return false, errors.New("flow id is required")
	}
	consumed, err := s.q.MCPOAuthFlowConsumed(
		ctx,
		dbsqlc.MCPOAuthFlowConsumedParams{McpOauthFlowID: &flowID},
	)
	if err != nil {
		return false, fmt.Errorf("check mcp oauth flow consumed: %w", err)
	}
	return consumed, nil
}
