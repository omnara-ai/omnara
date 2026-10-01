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

func (s strictOpenAPIServer) ConfigureIntegration(
	ctx context.Context,
	request openapi.ConfigureIntegrationRequestObject,
) (openapi.ConfigureIntegrationResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	current, err := s.integrationForSetup(ctx, scope, request.IntegrationID)
	if err != nil {
		return nil, err
	}
	input, err := s.integrationSetupInput(ctx, scope, request.Body, &current)
	if err != nil {
		return nil, integrationSetupInputError(ctx, err)
	}
	record, err := s.server.store.Integrations().ConfigureIntegration(ctx, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := integrationResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.ConfigureIntegration200JSONResponse(response), nil
}

func (s strictOpenAPIServer) DisconnectIntegration(
	ctx context.Context,
	request openapi.DisconnectIntegrationRequestObject,
) (openapi.DisconnectIntegrationResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	current, err := s.integrationForSetup(ctx, scope, request.IntegrationID)
	if err != nil {
		return nil, err
	}
	_, err = s.server.store.Integrations().
		DisconnectIntegration(
			ctx,
			integrationstore.DisconnectIntegrationInput{ProjectID: scope.project.ID, IntegrationID: current.ID},
		)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	current, err = s.server.store.Integrations().GetIntegration(ctx, scope.project.ID, current.ID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := integrationResponse(current)
	if err != nil {
		return nil, err
	}
	return openapi.DisconnectIntegration200JSONResponse(response), nil
}

func (s strictOpenAPIServer) integrationForSetup(
	ctx context.Context,
	scope projectScopeRecord,
	ref string,
) (integrationstore.IntegrationRecord, error) {
	id, ok := parseOpenAPIPublicID(publicid.KindIntegration, ref)
	if !ok {
		return integrationstore.IntegrationRecord{}, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"invalid integration id",
		)
	}
	integration, err := s.server.store.Integrations().GetIntegration(ctx, scope.project.ID, id)
	if err != nil {
		return integrationstore.IntegrationRecord{}, apierror.ProjectScoped(err)
	}
	return integration, nil
}

func (s strictOpenAPIServer) integrationSetupInput(
	ctx context.Context,
	scope projectScopeRecord,
	body *openapi.ConfigureIntegrationRequest,
	current *integrationstore.IntegrationRecord,
) (integrationstore.ConfigureIntegrationInput, error) {
	var input integrationstore.ConfigureIntegrationInput
	if body == nil {
		return input, storeerr.InvalidRequest(errors.New("request body is required"))
	}
	principal, err := userPrincipalFromContext(ctx)
	if err != nil {
		return input, *err
	}
	input = integrationstore.ConfigureIntegrationInput{
		OrgID:                    scope.project.OrgID,
		ProjectID:                scope.project.ID,
		InstalledByUserID:        principal.ID,
		IntegrationID:            current.ID,
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
		return input, integrationstore.ErrIntegrationSetupChanged
	}
	if current.Provider == integrationstore.IntegrationProviderSlack {
		return input, storeerr.InvalidRequest(
			errors.New("configure Slack credentials through integration OAuth setup"),
		)
	}
	if body.ProviderConfig != nil {
		config, err := json.Marshal(body.ProviderConfig)
		if err != nil {
			return input, storeerr.InvalidRequest(err)
		}
		input.ProviderConfig = config
	}
	kind, kindErr := integrationstore.IntegrationCredentialKind(input.Provider)
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
					"integration provider identity is immutable; create another integration for a different account",
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
		if err := setVerifiedIntegrationIdentity(&input, current, identity); err != nil {
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
					"integration provider identity is immutable; create another integration for a different account",
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
		if !integrationCredentialAlreadyVerified(current, input) ||
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
			if err := setVerifiedIntegrationIdentity(&input, current, identity); err != nil {
				return input, err
			}
		}
	}
	return input, nil
}
