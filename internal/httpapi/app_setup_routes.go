package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s strictOpenAPIServer) ConfigureProjectApp(
	ctx context.Context,
	request openapi.ConfigureProjectAppRequestObject,
) (openapi.ConfigureProjectAppResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	current, err := s.projectAppForSetup(ctx, scope, request.AppID)
	if err != nil {
		return nil, err
	}
	input, err := s.projectAppSetupInput(ctx, scope, request.Body, &current)
	if err != nil {
		return nil, appSetupInputError(err)
	}
	record, err := s.server.store.Integrations().ConfigureProjectApp(ctx, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := projectAppResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.ConfigureProjectApp200JSONResponse(response), nil
}

// DisconnectProjectApp requires project management, and needs neither a user
// principal nor provider/credential access. Setup endpoints require a user.
func (s strictOpenAPIServer) DisconnectProjectApp(
	ctx context.Context,
	request openapi.DisconnectProjectAppRequestObject,
) (openapi.DisconnectProjectAppResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	current, err := s.projectAppForSetup(ctx, scope, request.AppID)
	if err != nil {
		return nil, err
	}
	_, err = s.server.store.Integrations().
		DisconnectProjectApp(ctx, integrationstore.DisconnectProjectAppInput{ProjectID: scope.project.ID, AppID: current.ID})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	current, err = s.server.store.Integrations().GetProjectApp(ctx, scope.project.ID, current.ID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := projectAppResponse(current)
	if err != nil {
		return nil, err
	}
	return openapi.DisconnectProjectApp200JSONResponse(response), nil
}

func (s strictOpenAPIServer) projectAppForSetup(
	ctx context.Context,
	scope projectScopeRecord,
	ref string,
) (integrationstore.ProjectAppRecord, error) {
	id, ok := parseOpenAPIPublicID(publicid.KindProjectApp, ref)
	if !ok {
		return integrationstore.ProjectAppRecord{}, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"invalid app id",
		)
	}
	app, err := s.server.store.Integrations().GetProjectApp(ctx, scope.project.ID, id)
	if err != nil {
		return integrationstore.ProjectAppRecord{}, apierror.ProjectScoped(err)
	}
	return app, nil
}

func (s strictOpenAPIServer) projectAppSetupInput(
	ctx context.Context,
	scope projectScopeRecord,
	body *openapi.ConfigureProjectAppRequest,
	current *integrationstore.ProjectAppRecord,
) (integrationstore.ConfigureProjectAppInput, error) {
	var input integrationstore.ConfigureProjectAppInput
	if body == nil {
		return input, storeerr.InvalidRequest(errors.New("request body is required"))
	}
	principal, err := userPrincipalFromContext(ctx)
	if err != nil {
		return input, *err
	}
	input = integrationstore.ConfigureProjectAppInput{
		OrgID:                    scope.project.OrgID,
		ProjectID:                scope.project.ID,
		InstalledByUserID:        principal.ID,
		AppID:                    current.ID,
		ExpectedSetupRevision:    body.ExpectedSetupRevision,
		Provider:                 current.Provider,
		ProviderTenantID:         strings.TrimSpace(body.ProviderTenantId),
		ProviderAccountRef:       strings.TrimSpace(stringValue(body.ProviderAccountRef)),
		ProviderAgentDisplayName: stringValue(body.ProviderAgentDisplayName),
		ProviderIdentity:         current.ProviderIdentity,
		ProviderMetadata:         current.ProviderMetadata,
		ProviderConfig:           current.ProviderConfig,
	}
	if input.ExpectedSetupRevision != current.SetupRevision {
		return input, integrationstore.ErrProjectAppSetupChanged
	}
	if current.Provider == integrationstore.IntegrationProviderSlack {
		return input, storeerr.InvalidRequest(
			errors.New("configure Slack credentials through app OAuth setup"),
		)
	}
	if body.ProviderConfig != nil {
		config, err := json.Marshal(body.ProviderConfig)
		if err != nil {
			return input, storeerr.InvalidRequest(err)
		}
		input.ProviderConfig = config
	}
	kind, kindErr := integrationstore.ProjectAppCredentialKind(input.Provider)
	if kindErr != nil {
		return input, storeerr.InvalidRequest(kindErr)
	}
	var decodeErr error
	input.CredentialSecretID, decodeErr = publicid.Decode(
		publicid.KindSecret,
		body.CredentialSecretId,
	)
	if decodeErr != nil {
		return input, storeerr.InvalidRequest(decodeErr)
	}
	// Key unwrapping and provider credential parsing happen outside the storage
	// transaction. The save checks this revision after taking the secret lock.
	credential, readErr := s.server.store.Secrets().ReadProjectAvailableSecretPayload(
		ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID:     input.OrgID,
			ProjectID: input.ProjectID,
			SecretID:  input.CredentialSecretID,
			Kind:      kind,
		},
	)
	if readErr != nil {
		return input, readErr
	}
	input.CredentialVersionID = credential.CurrentVersionID
	if _, validateErr := secrets.ValidatePayload(kind, credential.Payload); validateErr != nil {
		return input, storeerr.InvalidRequest(validateErr)
	}
	if input.Provider == integrationstore.IntegrationProviderGitHub {
		appID, parseErr := strconv.ParseInt(
			strings.TrimSpace(credential.Payload[secrets.KeyAppID]),
			10,
			64,
		)
		if parseErr != nil || appID <= 0 {
			return input, storeerr.InvalidRequest(
				errors.New("github credential requires a positive numeric App ID"),
			)
		}
		installationID, parseErr := strconv.ParseInt(
			strings.TrimSpace(input.ProviderAccountRef),
			10,
			64,
		)
		if parseErr != nil || installationID <= 0 {
			return input, storeerr.InvalidRequest(
				errors.New(
					"github provider_account_ref must be a positive numeric Installation ID",
				),
			)
		}
		tenantID, parseErr := strconv.ParseInt(strings.TrimSpace(input.ProviderTenantID), 10, 64)
		if parseErr != nil || tenantID != appID {
			return input, storeerr.InvalidRequest(
				errors.New("github credential App ID must match provider_tenant_id"),
			)
		}
		config := s.server.githubClientConfig
		config.InstallationID = installationID
		config.Credentials = github.Credentials{
			AppID:         appID,
			PrivateKeyPEM: credential.Payload[secrets.KeyPrivateKey],
			WebhookSecret: credential.Payload[secrets.KeyWebhookSecret],
		}
		client, parseErr := github.NewClient(config)
		if parseErr != nil {
			return input, storeerr.InvalidRequest(parseErr)
		}
		input.CredentialAppID = appID
		input.ProviderTenantID, input.ProviderAccountRef = strconv.FormatInt(
			appID,
			10,
		), strconv.FormatInt(
			installationID,
			10,
		)
		var observed github.AppIdentity
		_ = json.Unmarshal(current.ProviderIdentity, &observed)
		if current.ProviderTenantID != "" &&
			(current.ProviderTenantID != input.ProviderTenantID || current.ProviderAccountRef != input.ProviderAccountRef) {
			return input, storeerr.InvalidRequest(
				errors.New(
					"app provider identity is immutable; create another app for a different account",
				),
			)
		}
		identity, identityErr := client.CheckAppIdentity(ctx)
		if identityErr != nil {
			return input, identityErr
		}
		if observed.BotUserID > 0 && observed.BotUserID != identity.BotUserID {
			return input, &github.APIError{Code: github.ScopeMismatch}
		}
		if body.ProviderAgentDisplayName == nil {
			input.ProviderAgentDisplayName = current.ProviderAgentDisplayName
			if input.ProviderAgentDisplayName == "" {
				input.ProviderAgentDisplayName = identity.DisplayName
			}
		}
		if err := setVerifiedAppIdentity(&input, current, identity); err != nil {
			return input, err
		}
	}
	if input.Provider == integrationstore.IntegrationProviderDiscord {
		if body.ProviderAgentDisplayName == nil {
			input.ProviderAgentDisplayName = current.ProviderAgentDisplayName
		}
		if input.ProviderAccountRef == "" {
			input.ProviderAccountRef = current.ProviderAccountRef
		}
		if current.ProviderTenantID != "" &&
			(current.ProviderTenantID != input.ProviderTenantID || current.ProviderAccountRef != input.ProviderAccountRef) {
			return input, storeerr.InvalidRequest(
				errors.New(
					"app provider identity is immutable; create another app for a different account",
				),
			)
		}
		config := s.server.discordClientConfig
		config.Credentials = discord.Credentials{
			ApplicationID: input.ProviderTenantID,
			BotUserID:     input.ProviderAccountRef,
			BotToken:      credential.Payload[secrets.KeyValue],
		}
		var observed discord.Identity
		_ = json.Unmarshal(current.ProviderIdentity, &observed)
		if !appCredentialAlreadyVerified(current, input) ||
			observed.ApplicationID != input.ProviderTenantID ||
			observed.BotUserID != input.ProviderAccountRef {
			identity, identityErr := discord.DiscoverIdentity(ctx, config)
			if identityErr != nil {
				return input, identityErr
			}
			input.ProviderAccountRef = identity.BotUserID
			if body.ProviderAgentDisplayName == nil {
				input.ProviderAgentDisplayName = identity.DisplayName
			}
			if err := setVerifiedAppIdentity(&input, current, identity); err != nil {
				return input, err
			}
		}
	}
	return input, nil
}
