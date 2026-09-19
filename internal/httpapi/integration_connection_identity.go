package httpapi

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func connectionCredentialAlreadyVerified(
	current *integrationstore.IntegrationConnectionRecord,
	input integrationstore.SaveIntegrationConnectionInput,
) bool {
	if current == nil || current.CredentialSecretID != input.CredentialSecretID ||
		input.CredentialVersionID == uuid.Nil {
		return false
	}
	var metadata struct {
		VersionID uuid.UUID `json:"verified_credential_version_id"`
	}
	return json.Unmarshal(current.ProviderMetadata, &metadata) == nil && metadata.VersionID == input.CredentialVersionID
}

func setVerifiedConnectionIdentity(
	input *integrationstore.SaveIntegrationConnectionInput,
	current *integrationstore.IntegrationConnectionRecord,
	identity any,
) error {
	metadata := make(map[string]json.RawMessage)
	if current != nil {
		if err := json.Unmarshal(current.ProviderMetadata, &metadata); err != nil {
			return err
		}
	}
	version, err := json.Marshal(input.CredentialVersionID)
	if err != nil {
		return err
	}
	metadata["verified_credential_version_id"] = version
	input.ProviderMetadata, err = json.Marshal(metadata)
	if err != nil {
		return err
	}
	input.ProviderIdentity, err = json.Marshal(identity)
	return err
}
