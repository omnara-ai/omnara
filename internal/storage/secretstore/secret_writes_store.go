package secretstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateTx(
	ctx context.Context,
	tx pgx.Tx,
	input CreateSecretInput,
) (SecretRecord, SecretVersionRecord, error) {
	return s.createSecretTx(ctx, s.q.WithTx(tx), input)
}

func (s *Store) CreateSecret(
	ctx context.Context,
	input CreateSecretInput,
) (SecretRecord, SecretVersionRecord, error) {
	if input.ManagementKind == management.Cluster {
		return SecretRecord{}, SecretVersionRecord{}, invalidSecretRequest("cluster-managed secrets are reserved")
	}
	input.ManagementKind = management.Tenant
	if err := s.validateCreateSecretInput(ctx, &input); err != nil {
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, fmt.Errorf("begin create secret: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	record, version, err := s.createSecretTx(ctx, qtx, input)
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	if err := resourceguard.Lock(
		ctx,
		qtx,
		resourceSecrets,
		resourceguard.OwnerScope(
			input.OrgID,
			input.OwnerKind,
			input.OwnerProjectID,
			input.OwnerUserID,
		),
	); err != nil {
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	limits, err := resourceguard.ResolveLimits(ctx, qtx, input.OrgID)
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	secretCount, err := qtx.CountActiveTenantSecretsForOwner(
		ctx,
		dbsqlc.CountActiveTenantSecretsForOwnerParams{
			OrgID:          input.OrgID,
			OwnerKind:      input.OwnerKind,
			OwnerProjectID: sqlcIDFromNil(input.OwnerProjectID),
			OwnerUserID:    sqlcIDFromNil(input.OwnerUserID),
		},
	)
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, fmt.Errorf(
			"count active tenant secrets: %w",
			err,
		)
	}
	if secretCount > limits.MaxActiveTenantSecretsPerOwner {
		return SecretRecord{}, SecretVersionRecord{}, resourceLimitExceeded(
			"active secrets",
			limits.MaxActiveTenantSecretsPerOwner,
		)
	}
	if err := tx.Commit(ctx); err != nil {
		return SecretRecord{}, SecretVersionRecord{}, fmt.Errorf("commit create secret: %w", err)
	}
	return record, version, nil
}

func (s *Store) createSecretTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input CreateSecretInput,
) (SecretRecord, SecretVersionRecord, error) {
	normalizedName, err := normalizeSecretName(input.Name)
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	input.Name = normalizedName
	material, err := secrets.CanonicalizeMaterial(input.Material)
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, invalidSecretRequest("%v", err)
	}
	metadata, err := normalizedSecretMetadata(input.Metadata)
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, invalidSecretRequest("%v", err)
	}
	if err := validateSecretOwnerMembershipTx(
		ctx,
		qtx,
		input.OrgID,
		input.OwnerKind,
		input.OwnerUserID,
	); err != nil {
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	secretID, err := newSecretUUID()
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	versionID, err := newSecretUUID()
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	inserted, err := qtx.InsertSecret(ctx, dbsqlc.InsertSecretParams{
		ID:               secretID,
		OrgID:            input.OrgID,
		ManagementKind:   string(input.ManagementKind),
		OwnerKind:        input.OwnerKind,
		OwnerProjectID:   sqlcIDFromNil(input.OwnerProjectID),
		OwnerUserID:      sqlcIDFromNil(input.OwnerUserID),
		Name:             input.Name,
		Kind:             string(material.Kind),
		Metadata:         metadata,
		CurrentVersionID: &versionID,
	})
	if err != nil {
		if storeutil.IsUniqueViolation(err) {
			return SecretRecord{}, SecretVersionRecord{}, storeerr.ErrConflict
		}
		return SecretRecord{}, SecretVersionRecord{}, fmt.Errorf("insert secret: %w", err)
	}
	version, err := s.insertSecretVersionTx(ctx, qtx, insertSecretVersionTxInput{
		OrgID:          input.OrgID,
		SecretID:       inserted.ID,
		VersionID:      versionID,
		VersionNumber:  1,
		Material:       material,
		MCPOAuthFlowID: input.MCPOAuthFlowID,
	})
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	record, err := getSecretTx(ctx, qtx, input.OrgID, inserted.ID)
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	return record, version, nil
}

type insertSecretVersionTxInput struct {
	OrgID          ID
	SecretID       ID
	VersionID      ID
	VersionNumber  int32
	Material       secrets.CanonicalMaterial
	MCPOAuthFlowID ID
}

func (s *Store) insertSecretVersionTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input insertSecretVersionTxInput,
) (SecretVersionRecord, error) {
	encrypted, err := s.encryptSecretPayload(
		ctx,
		input.OrgID,
		input.SecretID,
		input.VersionID,
		input.VersionNumber,
		input.Material.Kind,
		input.Material.Payload,
	)
	if err != nil {
		return SecretVersionRecord{}, err
	}
	oauthAccessTokenTTL, err := input.Material.OAuthAccessTokenLifetime.Remaining()
	if err != nil {
		return SecretVersionRecord{}, invalidSecretRequest("%v", err)
	}
	oauthAccessTokenExpires, oauthAccessTokenTTLSeconds, err := oauthAccessTokenExpiry(
		input.Material.Kind,
		oauthAccessTokenTTL,
	)
	if err != nil {
		return SecretVersionRecord{}, invalidSecretRequest("%v", err)
	}
	version, err := qtx.InsertSecretVersion(ctx, dbsqlc.InsertSecretVersionParams{
		ID:                         input.VersionID,
		OrgID:                      input.OrgID,
		SecretID:                   input.SecretID,
		VersionNumber:              input.VersionNumber,
		PayloadKeys:                encrypted.PayloadKeys,
		EncryptionScheme:           encrypted.EncryptionScheme,
		KeyID:                      encrypted.KeyID,
		DekWrappedBy:               encrypted.DEKWrappedBy,
		EncryptedDek:               encrypted.EncryptedDEK,
		EncryptedDekNonce:          encrypted.EncryptedDEKNonce,
		Nonce:                      encrypted.Nonce,
		Ciphertext:                 encrypted.Ciphertext,
		McpOauthFlowID:             sqlcIDFromNil(input.MCPOAuthFlowID),
		OauthAccessTokenExpires:    oauthAccessTokenExpires,
		OauthAccessTokenTtlSeconds: oauthAccessTokenTTLSeconds,
	})
	if err != nil {
		if storeutil.IsUniqueViolationOnConstraint(err, "secret_versions_mcp_oauth_flow_id_idx") {
			return SecretVersionRecord{}, storeerr.ErrMCPOAuthFlowConsumed
		}
		return SecretVersionRecord{}, fmt.Errorf("insert secret version: %w", err)
	}
	return secretVersionFromSQLC(version), nil
}

func (s *Store) UpdateSecretMetadata(
	ctx context.Context,
	input UpdateSecretMetadataInput,
) (SecretRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.SecretID) || isNilID(input.Actor.ID) {
		return SecretRecord{}, invalidSecretRequest("org, secret, and actor are required")
	}
	metadata, err := normalizedSecretMetadata(input.Metadata)
	if err != nil {
		return SecretRecord{}, invalidSecretRequest("%v", err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SecretRecord{}, fmt.Errorf("begin update secret metadata: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockSecret(
		ctx,
		dbsqlc.LockSecretParams{OrgID: input.OrgID, ID: input.SecretID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SecretRecord{}, storeerr.ErrNotFound
		}
		return SecretRecord{}, fmt.Errorf("lock secret for metadata update: %w", err)
	}
	current, err := getSecretTx(ctx, qtx, input.OrgID, input.SecretID)
	if err != nil {
		return SecretRecord{}, err
	}
	if err := management.RequireTenant(current.ManagementKind, "secrets"); err != nil {
		return SecretRecord{}, err
	}
	if err := s.authorizeSecretManage(ctx, current, input.Actor); err != nil {
		return SecretRecord{}, err
	}
	normalizedName, err := normalizeSecretName(input.Name)
	if err != nil {
		return SecretRecord{}, err
	}
	input.Name = normalizedName
	row, err := qtx.UpdateSecretMetadata(
		ctx,
		dbsqlc.UpdateSecretMetadataParams{
			OrgID:    input.OrgID,
			ID:       input.SecretID,
			Name:     input.Name,
			Metadata: metadata,
		},
	)
	if err != nil {
		if storeutil.IsUniqueViolation(err) {
			return SecretRecord{}, storeerr.ErrConflict
		}
		return SecretRecord{}, fmt.Errorf("update secret metadata: %w", err)
	}
	record, err := getSecretTx(ctx, qtx, input.OrgID, row.ID)
	if err != nil {
		return SecretRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SecretRecord{}, fmt.Errorf("commit update secret metadata: %w", err)
	}
	return record, nil
}

func (s *Store) CreateSecretVersion(
	ctx context.Context,
	input CreateSecretVersionInput,
) (SecretRecord, SecretVersionRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.SecretID) || isNilID(input.Actor.ID) {
		return SecretRecord{}, SecretVersionRecord{}, invalidSecretRequest(
			"org, secret, and actor are required",
		)
	}
	material, err := secrets.CanonicalizeMaterial(input.Material)
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, invalidSecretRequest("%v", err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, fmt.Errorf(
			"begin create secret version: %w",
			err,
		)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockSecret(
		ctx,
		dbsqlc.LockSecretParams{OrgID: input.OrgID, ID: input.SecretID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SecretRecord{}, SecretVersionRecord{}, storeerr.ErrNotFound
		}
		return SecretRecord{}, SecretVersionRecord{}, fmt.Errorf("lock secret: %w", err)
	}
	secret, err := getSecretTx(ctx, qtx, input.OrgID, input.SecretID)
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	if err := management.RequireTenant(secret.ManagementKind, "secrets"); err != nil {
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	if material.Kind != secret.Kind {
		return SecretRecord{}, SecretVersionRecord{}, invalidSecretRequest(
			"secret material kind %q does not match secret kind %q",
			material.Kind,
			secret.Kind,
		)
	}
	if err := s.authorizeSecretManage(ctx, secret, input.Actor); err != nil {
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	if secret.Kind == SecretKindOAuthTokenSet {
		if err := qtx.DeleteSecretOAuthLeases(
			ctx,
			dbsqlc.DeleteSecretOAuthLeasesParams{
				OrgID:    input.OrgID,
				SecretID: input.SecretID,
			},
		); err != nil {
			return SecretRecord{}, SecretVersionRecord{}, fmt.Errorf("invalidate oauth refresh lease: %w", err)
		}
	}
	versionNumber, err := qtx.NextSecretVersionNumber(
		ctx,
		dbsqlc.NextSecretVersionNumberParams{SecretID: input.SecretID},
	)
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, fmt.Errorf(
			"next secret version number: %w",
			err,
		)
	}
	versionID, err := newSecretUUID()
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	version, err := s.insertSecretVersionTx(ctx, qtx, insertSecretVersionTxInput{
		OrgID:          input.OrgID,
		SecretID:       input.SecretID,
		VersionID:      versionID,
		VersionNumber:  versionNumber,
		Material:       material,
		MCPOAuthFlowID: input.MCPOAuthFlowID,
	})
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	if _, err := qtx.SetSecretCurrentVersion(
		ctx,
		dbsqlc.SetSecretCurrentVersionParams{
			CurrentVersionID: &version.ID,
			OrgID:            input.OrgID,
			ID:               input.SecretID,
		},
	); err != nil {
		return SecretRecord{}, SecretVersionRecord{}, fmt.Errorf(
			"set current secret version: %w",
			err,
		)
	}
	if input.SecretMetadata != nil {
		metadata, err := normalizedSecretMetadata(input.SecretMetadata)
		if err != nil {
			return SecretRecord{}, SecretVersionRecord{}, invalidSecretRequest("%v", err)
		}
		if _, err := qtx.UpdateSecretMetadata(
			ctx,
			dbsqlc.UpdateSecretMetadataParams{
				Name:     secret.Name,
				Metadata: metadata,
				OrgID:    input.OrgID,
				ID:       input.SecretID,
			},
		); err != nil {
			return SecretRecord{}, SecretVersionRecord{}, fmt.Errorf(
				"update secret metadata with version: %w",
				err,
			)
		}
	}
	updated, err := getSecretTx(ctx, qtx, input.OrgID, input.SecretID)
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SecretRecord{}, SecretVersionRecord{}, fmt.Errorf(
			"commit create secret version: %w",
			err,
		)
	}
	return updated, version, nil
}

func (s *Store) DeleteSecret(ctx context.Context, input DeleteSecretInput) (SecretRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.SecretID) || isNilID(input.Actor.ID) {
		return SecretRecord{}, invalidSecretRequest("org, secret, and actor are required")
	}
	record, err := s.GetSecret(ctx, input.OrgID, input.SecretID)
	if err != nil {
		return SecretRecord{}, err
	}
	if err := management.RequireTenant(record.ManagementKind, "secrets"); err != nil {
		return SecretRecord{}, err
	}
	if err := s.authorizeSecretManage(ctx, record, input.Actor); err != nil {
		return SecretRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SecretRecord{}, fmt.Errorf("begin delete secret: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	referenced, err := qtx.SecretIsReferenced(ctx, dbsqlc.SecretIsReferencedParams{
		OrgID: input.OrgID, SecretID: input.SecretID,
	})
	if err != nil {
		return SecretRecord{}, fmt.Errorf("check secret references: %w", err)
	}
	if referenced {
		return SecretRecord{}, storeerr.ErrConflict
	}
	if _, err := qtx.DeleteSecret(ctx, dbsqlc.DeleteSecretParams{
		OrgID: input.OrgID, ID: input.SecretID,
	}); err != nil {
		return SecretRecord{}, fmt.Errorf("delete secret: %w", err)
	}
	if err := qtx.DeleteSecretGrantsForSecret(ctx, dbsqlc.DeleteSecretGrantsForSecretParams{
		OrgID: input.OrgID, SecretID: input.SecretID,
	}); err != nil {
		return SecretRecord{}, fmt.Errorf("delete secret grants: %w", err)
	}
	if err := qtx.DeleteSecretVersions(ctx, dbsqlc.DeleteSecretVersionsParams{
		OrgID: input.OrgID, SecretID: input.SecretID,
	}); err != nil {
		return SecretRecord{}, fmt.Errorf("destroy secret versions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return SecretRecord{}, fmt.Errorf("commit delete secret: %w", err)
	}
	return record, nil
}

func (s *Store) validateCreateSecretInput(ctx context.Context, input *CreateSecretInput) error {
	if s.secretKeyWrapper == nil {
		return errors.New("secret key wrapper is required")
	}
	if isNilID(input.OrgID) || input.OwnerKind == "" || input.Material == nil ||
		isNilID(input.Actor.ID) {
		return invalidSecretRequest("org, owner kind, material, and actor are required")
	}
	normalizedName, err := normalizeSecretName(input.Name)
	if err != nil {
		return err
	}
	input.Name = normalizedName
	if err := management.Validate(input.ManagementKind); err != nil {
		return invalidSecretRequest("%v", err)
	}
	return s.AuthorizeSecretOwnerManage(ctx, input.OrgID, SecretOwner{
		Kind: input.OwnerKind, ProjectID: input.OwnerProjectID, UserID: input.OwnerUserID,
	}, input.Actor)
}

func normalizeSecretName(name string) (string, error) {
	if name == "" {
		return "", invalidSecretName("secret name is required")
	}
	normalizedName, err := resourcename.Normalize("secret name", name)
	if err != nil {
		return "", invalidSecretName("%v", err)
	}
	return normalizedName, nil
}

func newSecretUUID() (ID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return NilID, fmt.Errorf("generate uuidv7: %w", err)
	}
	return id, nil
}
