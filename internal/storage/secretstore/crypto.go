package secretstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

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

func normalizedSecretMetadata(metadata json.RawMessage) (json.RawMessage, error) {
	if len(metadata) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if len(metadata) > MaxSecretMetadataBytes {
		return nil, fmt.Errorf("secret metadata exceeds %d bytes", MaxSecretMetadataBytes)
	}
	var value map[string]string
	if err := json.Unmarshal(metadata, &value); err != nil {
		return nil, fmt.Errorf("parse secret metadata: %w", err)
	}
	if value == nil {
		return nil, errors.New("secret metadata must be a JSON object with string values")
	}
	return json.Marshal(value)
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
