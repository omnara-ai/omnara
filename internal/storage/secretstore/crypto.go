package secretstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"

	"github.com/omnara-ai/omnara/internal/resourcemeta"
)

// ErrInvalidSecretName marks validation detail safe to return publicly.
var ErrInvalidSecretName = errors.New("invalid secret name")

func (s *Store) encryptSecretPayload(
	ctx context.Context,
	orgID, secretID, versionID ID,
	versionNumber int32,
	kind secrets.Kind,
	payload secrets.Payload,
) (secrets.EncryptedPayload, error) {
	if s.secretKeyWrapper == nil {
		return secrets.EncryptedPayload{}, errors.New("secret key wrapper is required")
	}
	return secrets.EncryptPayload(
		ctx,
		s.secretKeyWrapper,
		kind,
		payload,
		secrets.AssociatedData{
			OrgID:         orgID.String(),
			SecretID:      secretID.String(),
			VersionID:     versionID.String(),
			VersionNumber: versionNumber,
			Kind:          kind,
		},
	)
}

func invalidSecretRequest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", storeerr.ErrInvalidSecretRequest, fmt.Sprintf(format, args...))
}

func invalidSecretName(format string, args ...any) error {
	detail := fmt.Errorf(format, args...)
	return storeerr.Tag(ErrInvalidSecretName, storeerr.InvalidRequest(detail))
}

func encryptedPayloadFromSecretVersion(version SecretVersionRecord) secrets.EncryptedPayload {
	return secrets.EncryptedPayload{
		EncryptionScheme:  version.EncryptionScheme,
		KeyID:             version.KeyID,
		DEKWrappedBy:      version.DEKWrappedBy,
		EncryptedDEK:      version.EncryptedDEK,
		EncryptedDEKNonce: version.EncryptedDEKNonce,
		Nonce:             version.Nonce,
		Ciphertext:        version.Ciphertext,
		PayloadKeys:       version.PayloadKeys,
	}
}

func normalizedSecretMetadata(metadata resourcemeta.Metadata) (json.RawMessage, error) {
	if err := metadata.Validate(); err != nil {
		return nil, err
	}
	raw, err := metadata.JSON()
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxSecretMetadataBytes {
		return nil, fmt.Errorf("secret metadata exceeds %d bytes", MaxSecretMetadataBytes)
	}
	return raw, nil
}

func secretMetadataFilterJSON(metadata map[string]string) (json.RawMessage, error) {
	if len(metadata) == 0 {
		return json.RawMessage(`{}`), nil
	}
	for key := range metadata {
		if key == "" {
			return nil, errors.New("secret metadata filter keys must be non-empty")
		}
	}
	out, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal secret metadata filter: %w", err)
	}
	return out, nil
}
