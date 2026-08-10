package secretstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) RewrapSecretVersionsByKeyID(
	ctx context.Context,
	keyID string,
) (RewrapSecretVersionsByKeyIDResult, error) {
	if keyID == "" {
		return RewrapSecretVersionsByKeyIDResult{}, errors.New("key id is required")
	}
	if s.secretKeyWrapper == nil {
		return RewrapSecretVersionsByKeyIDResult{}, errors.New("secret key wrapper is required")
	}
	activeKeyID, err := s.secretKeyWrapper.ActiveKeyID(ctx)
	if err != nil {
		return RewrapSecretVersionsByKeyIDResult{}, err
	}
	if keyID == activeKeyID {
		return RewrapSecretVersionsByKeyIDResult{}, fmt.Errorf("cannot rewrap active key %q", keyID)
	}
	rows, err := s.q.ListSecretVersionsByKeyID(
		ctx,
		dbsqlc.ListSecretVersionsByKeyIDParams{KeyID: keyID},
	)
	if err != nil {
		return RewrapSecretVersionsByKeyIDResult{}, fmt.Errorf(
			"list secret versions by key id: %w",
			err,
		)
	}
	result := RewrapSecretVersionsByKeyIDResult{Scanned: len(rows)}
	for _, row := range rows {
		version := secretVersionFromListByKeyID(row)
		rewrapped, err := secrets.RewrapPayloadKey(
			ctx,
			s.secretKeyWrapper,
			encryptedPayloadFromSecretVersion(version),
			secrets.AssociatedData{
				OrgID:         version.OrgID.String(),
				SecretID:      version.SecretID.String(),
				VersionID:     version.ID.String(),
				VersionNumber: version.VersionNumber,
				Kind:          secrets.Kind(row.Kind),
			},
		)
		if err != nil {
			return result, err
		}
		if _, err := s.q.UpdateSecretVersionKeyEnvelope(
			ctx,
			dbsqlc.UpdateSecretVersionKeyEnvelopeParams{
				KeyID:             rewrapped.KeyID,
				DekWrappedBy:      rewrapped.DEKWrappedBy,
				EncryptedDek:      rewrapped.EncryptedDEK,
				EncryptedDekNonce: rewrapped.EncryptedDEKNonce,
				OrgID:             version.OrgID,
				SecretID:          version.SecretID,
				ID:                version.ID,
				PreviousKeyID:     keyID,
			},
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return result, fmt.Errorf("update secret version key envelope: %w", err)
		}
		result.Rewrapped++
	}
	remaining, err := s.q.CountSecretVersionsByKeyID(
		ctx,
		dbsqlc.CountSecretVersionsByKeyIDParams{KeyID: keyID},
	)
	if err != nil {
		return result, fmt.Errorf("count remaining secret versions by key id: %w", err)
	}
	result.Remaining = int(remaining)
	return result, nil
}

func (s *Store) AuthorizeSecretForProjectReference(
	ctx context.Context,
	input AuthorizeSecretForProjectReferenceInput,
) (bool, error) {
	if isNilID(input.OrgID) || isNilID(input.ProjectID) || isNilID(input.SecretID) {
		return false, invalidSecretRequest("org, project, and secret are required")
	}
	allowed, err := s.q.SecretAvailableToProject(
		ctx,
		dbsqlc.SecretAvailableToProjectParams{
			OrgID:     input.OrgID,
			ProjectID: &input.ProjectID,
			SecretID:  input.SecretID,
		},
	)
	if err != nil {
		return false, fmt.Errorf("authorize secret for project reference: %w", err)
	}
	return allowed, nil
}

func (s *Store) ValidateProjectSecretReference(
	ctx context.Context,
	orgID, projectID, secretID ID,
	expectedKind secrets.Kind,
) error {
	if isNilID(orgID) || isNilID(projectID) || isNilID(secretID) || expectedKind == "" {
		return invalidSecretRequest("org, project, secret, and expected kind are required")
	}
	access, err := s.GetProjectAvailableSecret(ctx, orgID, projectID, secretID)
	if err != nil {
		if errors.Is(err, storeerr.ErrNotFound) {
			return fmt.Errorf("secret is not available to the project: %w", storeerr.ErrNotFound)
		}
		return err
	}
	if access.Secret.Kind != expectedKind {
		return invalidSecretRequest(
			"secret kind %q does not match expected kind %q",
			access.Secret.Kind,
			expectedKind,
		)
	}
	return nil
}

func (s *Store) ReadProjectAvailableSecretPayload(
	ctx context.Context,
	input ReadProjectAvailableSecretPayloadInput,
) (SecretPayloadRecord, error) {
	if s.secretKeyWrapper == nil {
		return SecretPayloadRecord{}, errors.New("secret key wrapper is required")
	}
	if isNilID(input.OrgID) || isNilID(input.ProjectID) || isNilID(input.SecretID) || input.Kind == "" {
		return SecretPayloadRecord{}, invalidSecretRequest("org, project, secret, and expected kind are required")
	}
	access, err := s.GetProjectAvailableSecret(ctx, input.OrgID, input.ProjectID, input.SecretID)
	if err != nil {
		if errors.Is(err, storeerr.ErrNotFound) {
			return SecretPayloadRecord{}, fmt.Errorf("secret is not available to the project: %w", storeerr.ErrNotFound)
		}
		return SecretPayloadRecord{}, err
	}
	record := access.Secret
	if record.Kind != input.Kind {
		return SecretPayloadRecord{}, invalidSecretRequest(
			"secret kind %q does not match expected kind %q",
			record.Kind,
			input.Kind,
		)
	}
	version, err := s.q.GetSecretVersion(
		ctx,
		dbsqlc.GetSecretVersionParams{
			OrgID:    input.OrgID,
			SecretID: input.SecretID,
			ID:       record.CurrentVersionID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SecretPayloadRecord{}, storeerr.ErrNotFound
		}
		return SecretPayloadRecord{}, fmt.Errorf("get current secret version: %w", err)
	}
	versionRecord := secretVersionFromGetSQLC(version)
	payload, err := secrets.DecryptPayload(
		ctx,
		s.secretKeyWrapper,
		encryptedPayloadFromSecretVersion(versionRecord),
		secrets.AssociatedData{
			OrgID:         versionRecord.OrgID.String(),
			SecretID:      versionRecord.SecretID.String(),
			VersionID:     versionRecord.ID.String(),
			VersionNumber: versionRecord.VersionNumber,
			Kind:          record.Kind,
		},
	)
	if err != nil {
		return SecretPayloadRecord{}, fmt.Errorf("decrypt secret payload: %w", err)
	}
	return SecretPayloadRecord{
		Payload:                   payload,
		CurrentVersionID:          record.CurrentVersionID,
		OAuthAccessTokenExpires:   version.OauthAccessTokenExpires,
		OAuthAccessTokenRemaining: time.Duration(version.OauthAccessTokenRemainingSeconds * float64(time.Second)),
	}, nil
}

func (s *Store) ReadOrgOwnedSecretPayload(
	ctx context.Context,
	input ReadOrgOwnedSecretPayloadInput,
) (SecretPayloadRecord, error) {
	if s.secretKeyWrapper == nil {
		return SecretPayloadRecord{}, errors.New("secret key wrapper is required")
	}
	if isNilID(input.OrgID) || isNilID(input.SecretID) || input.Kind == "" {
		return SecretPayloadRecord{}, invalidSecretRequest("org, secret, management kind, and kind are required")
	}
	if err := management.Validate(input.ManagementKind); err != nil {
		return SecretPayloadRecord{}, invalidSecretRequest("%v", err)
	}
	record, err := s.GetSecret(ctx, input.OrgID, input.SecretID)
	if err != nil {
		return SecretPayloadRecord{}, err
	}
	if record.OwnerKind != SecretOwnerOrg || record.ManagementKind != input.ManagementKind {
		return SecretPayloadRecord{}, storeerr.ErrNotFound
	}
	if record.Kind != input.Kind {
		return SecretPayloadRecord{}, invalidSecretRequest(
			"secret kind %q does not match expected kind %q",
			record.Kind,
			input.Kind,
		)
	}
	version, err := s.q.GetSecretVersion(
		ctx,
		dbsqlc.GetSecretVersionParams{OrgID: input.OrgID, SecretID: input.SecretID, ID: record.CurrentVersionID},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SecretPayloadRecord{}, storeerr.ErrNotFound
		}
		return SecretPayloadRecord{}, fmt.Errorf("get current secret version: %w", err)
	}
	versionRecord := secretVersionFromGetSQLC(version)
	payload, err := secrets.DecryptPayload(
		ctx,
		s.secretKeyWrapper,
		encryptedPayloadFromSecretVersion(versionRecord),
		secrets.AssociatedData{
			OrgID:         versionRecord.OrgID.String(),
			SecretID:      versionRecord.SecretID.String(),
			VersionID:     versionRecord.ID.String(),
			VersionNumber: versionRecord.VersionNumber,
			Kind:          record.Kind,
		},
	)
	if err != nil {
		return SecretPayloadRecord{}, fmt.Errorf("decrypt secret payload: %w", err)
	}
	return SecretPayloadRecord{
		Payload:                   payload,
		CurrentVersionID:          record.CurrentVersionID,
		OAuthAccessTokenExpires:   version.OauthAccessTokenExpires,
		OAuthAccessTokenRemaining: time.Duration(version.OauthAccessTokenRemainingSeconds * float64(time.Second)),
	}, nil
}

func (s *Store) ReadMachinePoolDeletionCredentialPayload(
	ctx context.Context,
	input ReadMachinePoolDeletionCredentialInput,
) (SecretPayloadRecord, error) {
	if s.secretKeyWrapper == nil {
		return SecretPayloadRecord{}, errors.New("secret key wrapper is required")
	}
	if isNilID(input.OrgID) || isNilID(input.MachinePoolID) {
		return SecretPayloadRecord{}, invalidSecretRequest("org and machine pool are required")
	}
	row, err := s.q.GetMachinePoolDeletionCredentialVersion(
		ctx,
		dbsqlc.GetMachinePoolDeletionCredentialVersionParams{
			OrgID:         input.OrgID,
			MachinePoolID: input.MachinePoolID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return SecretPayloadRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return SecretPayloadRecord{}, fmt.Errorf("get machine pool deletion credential: %w", err)
	}
	version := SecretVersionRecord{
		ID:                row.ID,
		OrgID:             row.OrgID,
		SecretID:          row.SecretID,
		VersionNumber:     row.VersionNumber,
		PayloadKeys:       append([]string(nil), row.PayloadKeys...),
		EncryptionScheme:  row.EncryptionScheme,
		KeyID:             row.KeyID,
		DEKWrappedBy:      row.DekWrappedBy,
		EncryptedDEK:      append([]byte(nil), row.EncryptedDek...),
		EncryptedDEKNonce: append([]byte(nil), row.EncryptedDekNonce...),
		Nonce:             append([]byte(nil), row.Nonce...),
		Ciphertext:        append([]byte(nil), row.Ciphertext...),
		CreatedAt:         row.CreatedAt,
	}
	payload, err := secrets.DecryptPayload(
		ctx,
		s.secretKeyWrapper,
		encryptedPayloadFromSecretVersion(version),
		secrets.AssociatedData{
			OrgID:         version.OrgID.String(),
			SecretID:      version.SecretID.String(),
			VersionID:     version.ID.String(),
			VersionNumber: version.VersionNumber,
			Kind:          secrets.Kind(row.Kind),
		},
	)
	if err != nil {
		return SecretPayloadRecord{}, fmt.Errorf("decrypt machine pool deletion credential: %w", err)
	}
	return SecretPayloadRecord{Payload: payload, CurrentVersionID: version.ID}, nil
}
