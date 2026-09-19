package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"
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

func (s strictOpenAPIServer) CreateIntegrationConnection(
	ctx context.Context,
	request openapi.CreateIntegrationConnectionRequestObject,
) (openapi.CreateIntegrationConnectionResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	input, err := s.integrationConnectionInput(ctx, scope, request.Body, nil)
	if err != nil {
		return nil, integrationConnectionInputError(err)
	}
	record, err := s.server.store.Integrations().CreateIntegrationConnection(ctx, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := integrationConnectionResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.CreateIntegrationConnection201JSONResponse(response), nil
}

func (s strictOpenAPIServer) GetIntegrationConnection(
	ctx context.Context,
	request openapi.GetIntegrationConnectionRequestObject,
) (openapi.GetIntegrationConnectionResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindIntegrationConnection, request.IntegrationConnectionID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	record, err := s.server.store.Integrations().GetIntegrationConnection(ctx, scope.project.ID, id)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := integrationConnectionResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.GetIntegrationConnection200JSONResponse(response), nil
}

func (s strictOpenAPIServer) UpdateIntegrationConnection(
	ctx context.Context,
	request openapi.UpdateIntegrationConnectionRequestObject,
) (openapi.UpdateIntegrationConnectionResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindIntegrationConnection, request.IntegrationConnectionID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	current, err := s.server.store.Integrations().GetIntegrationConnection(ctx, scope.project.ID, id)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	input, err := s.integrationConnectionInput(ctx, scope, request.Body, &current)
	if err != nil {
		return nil, integrationConnectionInputError(err)
	}
	var record integrationstore.IntegrationConnectionRecord
	if len(input.ProviderIdentity) > 0 {
		input.SourceVerifiedIdentityRevision = current.UpdatedAt
		record, err = s.server.store.Integrations().UpdateIntegrationConnectionWithVerifiedIdentity(
			ctx, id, input,
		)
	} else {
		record, err = s.server.store.Integrations().UpdateIntegrationConnection(ctx, id, input)
	}
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := integrationConnectionResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.UpdateIntegrationConnection200JSONResponse(response), nil
}

func (s strictOpenAPIServer) integrationConnectionInput(
	ctx context.Context,
	scope projectScopeRecord,
	body *openapi.SaveIntegrationConnectionRequest,
	current *integrationstore.IntegrationConnectionRecord,
) (integrationstore.SaveIntegrationConnectionInput, error) {
	var input integrationstore.SaveIntegrationConnectionInput
	if body == nil {
		return input, storeerr.InvalidRequest(errors.New("request body is required"))
	}
	principal, err := userPrincipalFromContext(ctx)
	if err != nil {
		return input, *err
	}
	input = integrationstore.SaveIntegrationConnectionInput{
		OrgID: scope.project.OrgID, ProjectID: scope.project.ID, InstalledByUserID: principal.ID,
		Provider: string(body.Provider), ProviderTenantID: body.ProviderTenantId, ProviderAccountRef: body.ProviderAccountRef,
		ProviderAgentDisplayName: stringValue(body.ProviderAgentDisplayName),
		State:                    integrationstore.IntegrationConnectionStateActive,
	}
	if body.State != nil {
		input.State = integrationstore.IntegrationConnectionState(*body.State)
	}
	if body.ProviderConfig != nil {
		config, err := json.Marshal(body.ProviderConfig)
		if err != nil {
			return input, storeerr.InvalidRequest(err)
		}
		input.ProviderConfig = config
	}
	if current == nil && input.Provider == integrationstore.IntegrationProviderSlack {
		return input, storeerr.InvalidRequest(errors.New("create Slack connections through OAuth setup"))
	}
	kind, kindErr := integrationstore.IntegrationConnectionCredentialKind(input.Provider)
	if kindErr != nil {
		return input, storeerr.InvalidRequest(kindErr)
	}
	var decodeErr error
	input.CredentialSecretID, decodeErr = publicid.Decode(publicid.KindSecret, body.CredentialSecretId)
	if decodeErr != nil {
		return input, storeerr.InvalidRequest(decodeErr)
	}
	if current != nil && current.Provider == integrationstore.IntegrationProviderSlack &&
		input.CredentialSecretID != current.CredentialSecretID {
		return input, storeerr.InvalidRequest(errors.New("change Slack credentials through OAuth setup"))
	}
	// Key unwrapping and provider credential parsing happen outside the storage
	// transaction. The save checks this revision after taking the secret lock.
	credential, readErr := s.server.store.Secrets().ReadProjectAvailableSecretPayload(
		ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID: input.OrgID, ProjectID: input.ProjectID, SecretID: input.CredentialSecretID, Kind: kind,
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
		appID, parseErr := strconv.ParseInt(strings.TrimSpace(credential.Payload[secrets.KeyAppID]), 10, 64)
		if parseErr != nil || appID <= 0 {
			return input, storeerr.InvalidRequest(errors.New("github credential requires a positive numeric App ID"))
		}
		installationID, parseErr := strconv.ParseInt(strings.TrimSpace(input.ProviderAccountRef), 10, 64)
		if parseErr != nil || installationID <= 0 {
			return input, storeerr.InvalidRequest(
				errors.New("github provider_account_ref must be a positive numeric Installation ID"),
			)
		}
		tenantID, parseErr := strconv.ParseInt(strings.TrimSpace(input.ProviderTenantID), 10, 64)
		if parseErr != nil || tenantID != appID {
			return input, storeerr.InvalidRequest(errors.New("github credential App ID must match provider_tenant_id"))
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
		input.ProviderTenantID, input.ProviderAccountRef = strconv.FormatInt(appID, 10), strconv.FormatInt(installationID, 10)
		var observed github.AppIdentity
		if current != nil {
			_ = json.Unmarshal(current.ProviderIdentity, &observed)
		}
		// Saving active GitHub setup is also its explicit refresh action: the
		// App's bot login can change without any credential or numeric ID change.
		// Disabling an existing credential binding must not require provider I/O.
		disableExisting := current != nil && input.State == integrationstore.IntegrationConnectionStateDisabled &&
			input.CredentialSecretID == current.CredentialSecretID
		if !disableExisting {
			identity, identityErr := client.CheckAppIdentity(ctx)
			if identityErr != nil {
				return input, identityErr
			}
			if observed.BotUserID > 0 && observed.BotUserID != identity.BotUserID {
				return input, &github.APIError{Code: github.ScopeMismatch}
			}
			if err := setVerifiedConnectionIdentity(&input, current, identity); err != nil {
				return input, err
			}
		}
	}
	if input.Provider == integrationstore.IntegrationProviderDiscord {
		config := s.server.discordClientConfig
		config.Credentials = discord.Credentials{
			ApplicationID: input.ProviderTenantID,
			BotUserID:     input.ProviderAccountRef,
			BotToken:      credential.Payload[secrets.KeyValue],
		}
		_, parseErr := discord.NewClient(config)
		if parseErr != nil {
			return input, storeerr.InvalidRequest(parseErr)
		}
		var observed discord.Identity
		if current != nil {
			_ = json.Unmarshal(current.ProviderIdentity, &observed)
		}
		if !connectionCredentialAlreadyVerified(current, input) || observed.ApplicationID != input.ProviderTenantID ||
			observed.BotUserID != input.ProviderAccountRef {
			identity, identityErr := discord.DiscoverIdentity(ctx, config)
			if identityErr != nil {
				return input, identityErr
			}
			if err := setVerifiedConnectionIdentity(&input, current, identity); err != nil {
				return input, err
			}
		}
	}
	return input, nil
}

func (s strictOpenAPIServer) ListIntegrationConnections(
	ctx context.Context,
	request openapi.ListIntegrationConnectionsRequestObject,
) (openapi.ListIntegrationConnectionsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	filters := integrationstore.IntegrationConnectionListFilters{}
	if request.Params.OauthFlowId != nil {
		id, ok := parseOpenAPIPublicID(publicid.KindIntegrationOAuthFlow, *request.Params.OauthFlowId)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid oauth_flow_id")
		}
		filters.OAuthFlowID = id
	}
	extra := struct{ OAuthFlowID string }{}
	if filters.OAuthFlowID != uuid.Nil {
		extra.OAuthFlowID = filters.OAuthFlowID.String()
	}
	scopeKey := scope.project.OrgID.String() + "/" + scope.project.ID.String()
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: request.Params.Name, Sort: optionalString(request.Params.Sort),
		Cursor: request.Params.Cursor, ListKind: "integration_connections",
		Scope: scopeKey, IDKind: publicid.KindIntegrationConnection,
		AllowedSorts: defaultResourceSorts, Extra: extra,
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Integrations().ListIntegrationConnectionsForProject(
		ctx,
		integrationstore.ListIntegrationConnectionsForProjectInput{
			ProjectID: scope.project.ID,
			Filters:   filters,
			List:      list,
			Limit:     limit,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	data := make([]openapi.IntegrationConnection, 0, len(page.Connections))
	for _, connection := range page.Connections {
		response, err := integrationConnectionResponse(connection)
		if err != nil {
			return nil, err
		}
		data = append(data, response)
	}
	next, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "integration_connections", scopeKey,
		publicid.KindIntegrationConnection, extra,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListIntegrationConnections200JSONResponse(openapi.ListIntegrationConnectionsResponse{
		Data:       data,
		NextCursor: nullableFromPtr(next),
	}), nil
}

func integrationConnectionResponse(record integrationstore.IntegrationConnectionRecord) (
	openapi.IntegrationConnection,
	error,
) {
	id, err := publicID(publicid.KindIntegrationConnection, record.ID)
	if err != nil {
		return openapi.IntegrationConnection{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.IntegrationConnection{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapi.IntegrationConnection{}, err
	}
	credentialID, err := idOrNil(publicid.KindSecret, record.CredentialSecretID)
	if err != nil {
		return openapi.IntegrationConnection{}, err
	}
	var config openapi.IntegrationConnectionConfig
	if err := json.Unmarshal(record.ProviderConfig, &config); err != nil {
		return openapi.IntegrationConnection{}, err
	}
	return openapi.IntegrationConnection{
		Id:                       id,
		OrgId:                    orgID,
		ProjectId:                projectID,
		Provider:                 openapi.IntegrationProvider(record.Provider),
		State:                    openapi.IntegrationConnectionState(record.State),
		ProviderTenantId:         record.ProviderTenantID,
		ProviderAccountRef:       record.ProviderAccountRef,
		ProviderAgentDisplayName: record.ProviderAgentDisplayName,
		CredentialSecretId:       credentialID,
		ProviderConfig:           config,
		CreatedAt:                record.CreatedAt,
		UpdatedAt:                record.UpdatedAt,
	}, nil
}

func (s strictOpenAPIServer) DeleteIntegrationConnection(
	ctx context.Context,
	request openapi.DeleteIntegrationConnectionRequestObject,
) (openapi.DeleteIntegrationConnectionResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	connectionID, ok := parseOpenAPIPublicID(publicid.KindIntegrationConnection, request.IntegrationConnectionID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if err := s.server.store.Integrations().DeleteIntegrationConnection(ctx, scope.project.ID, connectionID); err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapi.DeleteIntegrationConnection204Response{}, nil
}
