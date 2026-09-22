package httpapi

import (
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func appSetupInputError(err error) error {
	var discordErr *discord.APIError
	if errors.As(err, &discordErr) {
		switch discordErr.Code {
		case discord.ScopeMismatch, discord.PermanentFailure:
			return apierror.FromCode(openapi.ErrorCodeInvalidRequest,
				"Discord application or bot identity could not be verified")
		case discord.RateLimited:
			return apierror.FromCode(openapi.ErrorCodeRateLimited,
				"Discord app setup is rate limited; retry later")
		default:
			return apierror.FromCode(openapi.ErrorCodeServiceUnavailable,
				"Discord identity verification is unavailable; retry later")
		}
	}
	var githubErr *github.APIError
	if !errors.As(err, &githubErr) {
		return apierror.ProjectScoped(err)
	}
	switch githubErr.Code {
	case github.UnsupportedAccount:
		return apierror.FromCode(openapi.ErrorCodeInvalidRequest,
			"Guided GitHub setup supports user or organization Apps and installations; "+
				"enterprise-owned Apps and enterprise-level installations are not supported")
	case github.ScopeMismatch, github.PermanentFailure:
		return apierror.FromCode(openapi.ErrorCodeInvalidRequest,
			"GitHub App, installation, or bot identity could not be verified")
	case github.RateLimited:
		return apierror.FromCode(openapi.ErrorCodeRateLimited,
			"GitHub app setup is rate limited; retry later")
	default:
		return apierror.FromCode(openapi.ErrorCodeServiceUnavailable,
			"GitHub identity verification is unavailable; retry later")
	}
}

func appCredentialAlreadyVerified(
	current *integrationstore.ProjectAppRecord,
	input integrationstore.ConfigureProjectAppInput,
) bool {
	if current == nil || current.State != integrationstore.ProjectAppStateActive ||
		current.CredentialSecretID != input.CredentialSecretID ||
		input.CredentialVersionID == uuid.Nil {
		return false
	}
	var metadata struct {
		VersionID uuid.UUID `json:"verified_credential_version_id"`
	}
	return json.Unmarshal(current.ProviderMetadata, &metadata) == nil &&
		metadata.VersionID == input.CredentialVersionID
}

func setVerifiedAppIdentity(
	input *integrationstore.ConfigureProjectAppInput,
	current *integrationstore.ProjectAppRecord,
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
