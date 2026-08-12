package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s strictOpenAPIServer) CreateAgentConfig(
	ctx context.Context,
	request openapi.CreateAgentConfigRequestObject,
) (openapi.CreateAgentConfigResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.createAgentConfig(ctx, request, scope.project)
}

func (s strictOpenAPIServer) createAgentConfig(
	ctx context.Context,
	request openapi.CreateAgentConfigRequestObject,
	project identitystore.ProjectRecord,
) (openapi.CreateAgentConfigResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	compiled, err := s.server.compileAgentConfigBodyForProject(
		ctx,
		project,
		string(request.Body.SourceFormat),
		request.Body.Source,
	)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	config, err := s.server.store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               project.ID,
		Definition:              compiled.Definition,
		Source:                  compiled.Source,
		SourceFormat:            compiled.SourceFormat,
		ConfiguredModelID:       compiled.ConfiguredModelID,
		CompiledDefinition:      compiled.CompiledDefinition,
		CompilerVersion:         compiled.CompilerVersion,
		EffectiveDefinitionHash: compiled.DefinitionHash,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := s.server.agentConfigResponseFromRecord(ctx, config)
	if err != nil {
		return nil, err
	}
	if config.Created {
		return openapi.CreateAgentConfig201JSONResponse(response), nil
	}
	return openapi.CreateAgentConfig200JSONResponse(response), nil
}

func (s strictOpenAPIServer) GetAgentConfig(
	ctx context.Context,
	request openapi.GetAgentConfigRequestObject,
) (openapi.GetAgentConfigResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.getAgentConfig(ctx, request, scope.project)
}

func (s strictOpenAPIServer) getAgentConfig(
	ctx context.Context,
	request openapi.GetAgentConfigRequestObject,
	project identitystore.ProjectRecord,
) (openapi.GetAgentConfigResponseObject, error) {
	configID, ok := parseOpenAPIPublicID(publicid.KindAgentConfig, request.AgentConfigID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	config, found, err := s.server.store.Execution().GetAgentConfig(ctx, project.ID, configID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	if !found {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	response, err := s.server.agentConfigResponseFromRecord(ctx, config)
	if err != nil {
		return nil, err
	}
	return openapi.GetAgentConfig200JSONResponse(response), nil
}

func (s strictOpenAPIServer) CreateAgentProfile(
	ctx context.Context,
	request openapi.CreateAgentProfileRequestObject,
) (openapi.CreateAgentProfileResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.createAgentProfile(ctx, request, scope.project)
}

func (s strictOpenAPIServer) createAgentProfile(
	ctx context.Context,
	request openapi.CreateAgentProfileRequestObject,
	project identitystore.ProjectRecord,
) (openapi.CreateAgentProfileResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	if strings.TrimSpace(request.Body.Config) == "" {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "config is required")
	}
	configID, ok := parseOpenAPIPublicID(publicid.KindAgentConfig, request.Body.Config)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid config")
	}
	principal, _ := principalFromContext(ctx)
	if principal.ID == storage.NilID {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden,
			"authenticated user principal is required to create an agent profile")
	}
	idempotencyKey := ""
	if request.Params.IdempotencyKey != nil {
		idempotencyKey = *request.Params.IdempotencyKey
	}
	profile, err := s.server.store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID:       project.ID,
		Name:            request.Body.Name,
		CurrentConfigID: configID,
		IdempotencyKey:  idempotencyKey,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := s.server.agentProfileResponseFromRecord(ctx, profile)
	if err != nil {
		return nil, err
	}
	if profile.Created {
		return openapi.CreateAgentProfile201JSONResponse(response), nil
	}
	return openapi.CreateAgentProfile200JSONResponse(response), nil
}

func (s strictOpenAPIServer) UpdateAgentProfile(
	ctx context.Context,
	request openapi.UpdateAgentProfileRequestObject,
) (openapi.UpdateAgentProfileResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.updateAgentProfile(ctx, request, scope.project)
}

func (s strictOpenAPIServer) updateAgentProfile(
	ctx context.Context,
	request openapi.UpdateAgentProfileRequestObject,
	project identitystore.ProjectRecord,
) (openapi.UpdateAgentProfileResponseObject, error) {
	profileID, ok := parseOpenAPIPublicID(publicid.KindAgentProfile, request.AgentProfileID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	principal, _ := principalFromContext(ctx)
	if principal.ID == storage.NilID {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden,
			"authenticated user principal is required to update an agent profile")
	}
	hasName := request.Body.Name != nil
	hasConfig := request.Body.Config != nil || request.Body.ExpectedCurrentConfigId != nil
	if hasName == hasConfig {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest,
			"provide either name or config with expected_current_config_id")
	}
	if hasName {
		name := strings.TrimSpace(*request.Body.Name)
		if name == "" {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "name is required")
		}
		profile, err := s.server.store.Execution().RenameAgentProfile(
			ctx,
			executionstore.RenameAgentProfileInput{
				ProjectID: project.ID,
				ProfileID: profileID,
				Name:      name,
			},
		)
		if err != nil {
			return nil, apierror.ProjectScoped(err)
		}
		response, err := s.server.agentProfileResponseFromRecord(ctx, profile)
		if err != nil {
			return nil, err
		}
		return openapi.UpdateAgentProfile200JSONResponse(response), nil
	}
	if request.Body.ExpectedCurrentConfigId == nil ||
		strings.TrimSpace(*request.Body.ExpectedCurrentConfigId) == "" {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "expected_current_config_id is required")
	}
	expectedCurrentConfigID, ok := parseOpenAPIPublicID(publicid.KindAgentConfig, *request.Body.ExpectedCurrentConfigId)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid expected_current_config_id")
	}
	if request.Body.Config == nil || strings.TrimSpace(*request.Body.Config) == "" {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "config is required")
	}
	configID, ok := parseOpenAPIPublicID(publicid.KindAgentConfig, *request.Body.Config)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid config")
	}
	idempotencyKey := ""
	if request.Params.IdempotencyKey != nil {
		idempotencyKey = *request.Params.IdempotencyKey
	}
	profile, err := s.server.store.Execution().RetargetAgentProfile(ctx, executionstore.RetargetAgentProfileInput{
		ProjectID:               project.ID,
		ProfileID:               profileID,
		ExpectedCurrentConfigID: expectedCurrentConfigID,
		IdempotencyKey:          idempotencyKey,
		ConfigID:                configID,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := s.server.agentProfileResponseFromRecord(ctx, profile)
	if err != nil {
		return nil, err
	}
	return openapi.UpdateAgentProfile200JSONResponse(response), nil
}

func (s strictOpenAPIServer) GetAgentProfile(
	ctx context.Context,
	request openapi.GetAgentProfileRequestObject,
) (openapi.GetAgentProfileResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.getAgentProfile(ctx, request, scope.project)
}

func (s strictOpenAPIServer) getAgentProfile(
	ctx context.Context,
	request openapi.GetAgentProfileRequestObject,
	project identitystore.ProjectRecord,
) (openapi.GetAgentProfileResponseObject, error) {
	profileID, ok := parseOpenAPIPublicID(publicid.KindAgentProfile, request.AgentProfileID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	profile, err := s.server.store.Execution().GetAgentProfile(ctx, project.ID, profileID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := s.server.agentProfileResponseFromRecord(ctx, profile)
	if err != nil {
		return nil, err
	}
	return openapi.GetAgentProfile200JSONResponse(response), nil
}

func (s strictOpenAPIServer) DeleteAgentProfile(
	ctx context.Context,
	request openapi.DeleteAgentProfileRequestObject,
) (openapi.DeleteAgentProfileResponseObject, error) {
	if r, ok := openAPIHTTPRequest(ctx); ok && r.Header.Get("Idempotency-Key") != "" {
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"idempotency key is not supported for agent profile delete",
		)
	}
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.deleteAgentProfile(ctx, request, scope.project)
}

func (s strictOpenAPIServer) deleteAgentProfile(
	ctx context.Context,
	request openapi.DeleteAgentProfileRequestObject,
	project identitystore.ProjectRecord,
) (openapi.DeleteAgentProfileResponseObject, error) {
	profileID, ok := parseOpenAPIPublicID(publicid.KindAgentProfile, request.AgentProfileID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if err := s.server.store.Execution().DeleteAgentProfile(ctx, project.ID, profileID); err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapi.DeleteAgentProfile204Response{}, nil
}

func (s strictOpenAPIServer) CreateIntegrationOAuthSetup(
	ctx context.Context,
	request openapi.CreateIntegrationOAuthSetupRequestObject,
) (openapi.CreateIntegrationOAuthSetupResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.createIntegrationOAuthSetup(ctx, request, scope.project)
}

func (s strictOpenAPIServer) createIntegrationOAuthSetup(
	ctx context.Context,
	request openapi.CreateIntegrationOAuthSetupRequestObject,
	project identitystore.ProjectRecord,
) (openapi.CreateIntegrationOAuthSetupResponseObject, error) {
	if s.server.publicURL == "" || s.server.secretKeyWrapper == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable,
			"integration oauth requires a configured public URL and secret encryption keys")
	}
	agentProfileID, ok := parseOpenAPIPublicID(publicid.KindAgentProfile, request.AgentProfileID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	principal, _ := principalFromContext(ctx)
	provider := integrationstore.IntegrationProviderSlack
	if request.Body.Provider != nil {
		provider = strings.TrimSpace(*request.Body.Provider)
	}
	if provider == "" {
		provider = integrationstore.IntegrationProviderSlack
	}
	clientID := strings.TrimSpace(request.Body.ClientId)
	clientSecret := strings.TrimSpace(request.Body.ClientSecret)
	signingSecret := strings.TrimSpace(request.Body.SigningSecret)
	if !supportedIntegrationOAuthProvider(provider) {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "unsupported integration provider")
	}
	if provider == integrationstore.IntegrationProviderSlack {
		if err := validateSlackSetupPublicURL(s.server.publicURL); err != nil {
			return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, err.Error())
		}
	}
	if clientID == "" || clientSecret == "" || signingSecret == "" {
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"client_id, client_secret, and signing_secret are required",
		)
	}
	profile, err := s.server.store.Execution().GetAgentProfile(ctx, project.ID, agentProfileID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	if !agentConfigHasIntegrationSendTool(profile.CurrentConfig) {
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"agent profile config must enable send_integration_message",
		)
	}
	now := time.Now().UTC()
	flowID, err := uuid.NewV7()
	if err != nil {
		logpkg.Error(ctx, fmt.Errorf("generate integration oauth flow id: %w", err))
		return nil, fmt.Errorf("internal server error")
	}
	expiresAt := now.Add(integrationOAuthStateTTL)
	returnTo := ""
	if request.Body.ReturnTo != nil {
		returnTo = *request.Body.ReturnTo
	}
	stateToken, err := s.server.encodeIntegrationOAuthState(ctx, integrationOAuthState{
		FlowID:            flowID,
		OrgID:             project.OrgID,
		ProjectID:         project.ID,
		AgentProfileID:    agentProfileID,
		InstalledByUserID: principal.ID,
		Provider:          provider,
		ClientID:          clientID,
		ClientSecret:      clientSecret,
		SigningSecret:     signingSecret,
		ExpiresAt:         expiresAt,
		ReturnTo:          returnTo,
	})
	if err != nil {
		if errors.Is(err, errIntegrationOAuthStateTooLarge) {
			return nil, apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				"request fields are too large for the oauth state parameter",
			)
		}
		logpkg.Error(ctx, fmt.Errorf("start integration oauth flow: %w", err))
		return nil, fmt.Errorf("state generation failed")
	}
	redirectURI := s.server.absolutePublicURL(integrationOAuthCallbackPath)
	eventsURL := s.server.absolutePublicURL(integrationEventsPath)
	actionsURL := s.server.absolutePublicURL(integrationActionsPath)
	installURL, err := s.server.integrationOAuthAuthorizeURL(provider, clientID, redirectURI, stateToken)
	if err != nil {
		if errors.Is(err, errIntegrationOAuthStateTooLarge) {
			return nil, apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				"request fields are too large for the oauth state parameter",
			)
		}
		logpkg.Error(ctx, fmt.Errorf("build integration oauth authorization URL: %w", err))
		return nil, fmt.Errorf("internal server error")
	}
	return openapi.CreateIntegrationOAuthSetup201JSONResponse(openapi.IntegrationOAuthSetup{
		Provider:    provider,
		OauthUrl:    installURL,
		RedirectUri: redirectURI,
		EventsUrl:   eventsURL,
		ActionsUrl:  actionsURL,
		ExpiresAt:   expiresAt,
	}), nil
}

func (s strictOpenAPIServer) CreateSlackSetup(
	ctx context.Context,
	request openapi.CreateSlackSetupRequestObject,
) (openapi.CreateSlackSetupResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.createSlackSetup(ctx, request, scope.project)
}

func (s strictOpenAPIServer) createSlackSetup(
	ctx context.Context,
	request openapi.CreateSlackSetupRequestObject,
	project identitystore.ProjectRecord,
) (openapi.CreateSlackSetupResponseObject, error) {
	if s.server.publicURL == "" || s.server.secretKeyWrapper == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable,
			"slack setup requires a configured public URL and secret encryption keys")
	}
	if err := validateSlackSetupPublicURL(s.server.publicURL); err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, err.Error())
	}
	agentProfileID, ok := parseOpenAPIPublicID(publicid.KindAgentProfile, request.AgentProfileID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	principal, _ := principalFromContext(ctx)
	appName := strings.TrimSpace(request.Body.AppName)
	appConfigurationToken := strings.TrimSpace(request.Body.AppConfigurationToken)
	if appName == "" || appConfigurationToken == "" {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "app_name and app_configuration_token are required")
	}
	if utf8.RuneCountInString(appName) > slack.AppNameMaxRunes {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "app_name must be 35 characters or fewer")
	}
	if strings.EqualFold(appName, "slackbot") {
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"Slack reserves this app name. Choose a different name.",
		)
	}
	profile, err := s.server.store.Execution().GetAgentProfile(ctx, project.ID, agentProfileID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	if !agentConfigHasIntegrationSendTool(profile.CurrentConfig) {
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"agent profile config must enable send_integration_message",
		)
	}
	appIcon, err := slackSetupAppIcon(*request.Body)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	redirectURI := s.server.absolutePublicURL(integrationOAuthCallbackPath)
	eventsURL := s.server.absolutePublicURL(integrationEventsPath)
	actionsURL := s.server.absolutePublicURL(integrationActionsPath)
	outboundCtx, cancel := context.WithTimeout(ctx, integrationOAuthTimeout)
	defer cancel()
	app, err := slack.CreateManifestApp(
		outboundCtx,
		s.server.slackOAuth,
		appConfigurationToken,
		slack.BuildAppManifest(appName, eventsURL, actionsURL, redirectURI),
	)
	if err != nil {
		logpkg.Error(ctx, fmt.Errorf("create slack app manifest: %w", err))
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "slack app creation failed: "+err.Error())
	}
	iconCtx, iconCancel := context.WithTimeout(outboundCtx, 3*time.Second)
	defer iconCancel()
	if err := slack.SetAppIcon(iconCtx, s.server.slackOAuth, appConfigurationToken, app.AppID, appIcon); err != nil {
		logpkg.Error(ctx, fmt.Errorf("set slack app icon: %w", err))
	}
	now := time.Now().UTC()
	flowID, err := uuid.NewV7()
	if err != nil {
		logpkg.Error(ctx, fmt.Errorf("generate slack oauth flow id: %w", err))
		return nil, fmt.Errorf("internal server error")
	}
	expiresAt := now.Add(integrationOAuthStateTTL)
	returnTo := ""
	if request.Body.ReturnTo != nil {
		returnTo = *request.Body.ReturnTo
	}
	stateToken, err := s.server.encodeIntegrationOAuthState(ctx, integrationOAuthState{
		FlowID:            flowID,
		OrgID:             project.OrgID,
		ProjectID:         project.ID,
		AgentProfileID:    agentProfileID,
		InstalledByUserID: principal.ID,
		Provider:          integrationstore.IntegrationProviderSlack,
		ClientID:          app.ClientID,
		ClientSecret:      app.ClientSecret,
		SigningSecret:     app.SigningSecret,
		BotDisplayName:    appName,
		ExpiresAt:         expiresAt,
		ReturnTo:          returnTo,
	})
	if err != nil {
		if errors.Is(err, errIntegrationOAuthStateTooLarge) {
			return nil, apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				"request fields are too large for the oauth state parameter",
			)
		}
		logpkg.Error(ctx, fmt.Errorf("start slack oauth flow: %w", err))
		return nil, fmt.Errorf("state generation failed")
	}
	installURL, err := s.server.integrationOAuthAuthorizeURL(
		integrationstore.IntegrationProviderSlack,
		app.ClientID,
		redirectURI,
		stateToken,
	)
	if err != nil {
		if errors.Is(err, errIntegrationOAuthStateTooLarge) {
			return nil, apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				"request fields are too large for the oauth state parameter",
			)
		}
		logpkg.Error(ctx, fmt.Errorf("build slack oauth authorization URL: %w", err))
		return nil, fmt.Errorf("internal server error")
	}
	return openapi.CreateSlackSetup201JSONResponse(openapi.SlackSetup{
		Provider:    integrationstore.IntegrationProviderSlack,
		SlackAppId:  app.AppID,
		OauthUrl:    installURL,
		RedirectUri: redirectURI,
		EventsUrl:   eventsURL,
		ActionsUrl:  actionsURL,
		ExpiresAt:   expiresAt,
	}), nil
}

func (s strictOpenAPIServer) GetAgent(
	ctx context.Context,
	request openapi.GetAgentRequestObject,
) (openapi.GetAgentResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.getAgent(ctx, request, scope.agent)
}

func (s strictOpenAPIServer) getAgent(
	ctx context.Context,
	request openapi.GetAgentRequestObject,
	agent executionstore.AgentRecord,
) (openapi.GetAgentResponseObject, error) {
	response, err := s.server.currentAgentResponse(ctx, agent)
	if err != nil {
		return nil, err
	}
	return openapi.GetAgent200JSONResponse(response), nil
}

func (s strictOpenAPIServer) ListAgents(
	ctx context.Context,
	request openapi.ListAgentsRequestObject,
) (openapi.ListAgentsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.listAgents(ctx, request.Params, scope.project)
}

func (s strictOpenAPIServer) listAgents(
	ctx context.Context,
	params openapi.ListAgentsParams,
	project identitystore.ProjectRecord,
) (openapi.ListAgentsResponseObject, error) {
	limit, err := parseOpenAPIPageLimit(params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	filters := executionstore.AgentListFilters{}
	if params.AgentProfileId != nil && *params.AgentProfileId != "" {
		agentProfileID, err := publicid.Decode(publicid.KindAgentProfile, *params.AgentProfileId)
		if err != nil {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid agent profile filter")
		}
		filters.AgentProfileID = &agentProfileID
	}
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: params.Name, Sort: optionalString(params.Sort),
		Cursor: params.Cursor, ListKind: "agents",
		Scope: project.OrgID.String() + "/" + project.ID.String(), IDKind: publicid.KindAgent,
		AllowedSorts: defaultResourceSorts,
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Execution().ListAgentsForProject(ctx, executionstore.ListAgentsForProjectInput{
		ProjectID: project.ID, Filters: filters, List: list, Limit: limit,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	data := make([]openapi.Agent, 0, len(page.Agents))
	for _, agent := range page.Agents {
		response, err := publicAgentResponseFromRecord(agent)
		if err != nil {
			return nil, err
		}
		data = append(data, response)
	}
	nextCursor, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "agents",
		project.OrgID.String()+"/"+project.ID.String(), publicid.KindAgent, nil,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListAgents200JSONResponse(openapi.ListAgentsResponse{
		Data:       data,
		NextCursor: nullableFromPtr(nextCursor),
	}), nil
}

func (s strictOpenAPIServer) ListAgentProfiles(
	ctx context.Context,
	request openapi.ListAgentProfilesRequestObject,
) (openapi.ListAgentProfilesResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.listAgentProfiles(ctx, request.Params, scope.project)
}

func (s strictOpenAPIServer) listAgentProfiles(
	ctx context.Context,
	params openapi.ListAgentProfilesParams,
	project identitystore.ProjectRecord,
) (openapi.ListAgentProfilesResponseObject, error) {
	limit, err := parseOpenAPIPageLimit(params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	filters := executionstore.AgentProfileListFilters{}
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: params.Name, Sort: optionalString(params.Sort),
		Cursor: params.Cursor, ListKind: "agent_profiles",
		Scope: project.OrgID.String() + "/" + project.ID.String(), IDKind: publicid.KindAgentProfile,
		AllowedSorts: defaultResourceSorts,
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Execution().ListAgentProfilesForProject(
		ctx,
		executionstore.ListAgentProfilesForProjectInput{
			ProjectID: project.ID, Filters: filters, List: list, Limit: limit,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	data := make([]openapi.AgentProfile, 0, len(page.Profiles))
	for _, profile := range page.Profiles {
		response, err := s.server.agentProfileResponseFromRecord(ctx, profile)
		if err != nil {
			return nil, err
		}
		data = append(data, response)
	}
	nextCursor, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "agent_profiles",
		project.OrgID.String()+"/"+project.ID.String(), publicid.KindAgentProfile, nil,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListAgentProfiles200JSONResponse(openapi.ListAgentProfilesResponse{
		Data:       data,
		NextCursor: nullableFromPtr(nextCursor),
	}), nil
}

func (s strictOpenAPIServer) CreateAgent(
	ctx context.Context,
	request openapi.CreateAgentRequestObject,
) (openapi.CreateAgentResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.createAgent(ctx, request, scope.project)
}

func (s strictOpenAPIServer) createAgent(
	ctx context.Context,
	request openapi.CreateAgentRequestObject,
	project identitystore.ProjectRecord,
) (openapi.CreateAgentResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	principal, ok := principalFromContext(ctx)
	if !ok || !identitystore.IsAccountPrincipal(principal) {
		return nil, apierror.FromCode(
			openapi.ErrorCodeForbidden,
			"authenticated account principal is required to create an agent",
		)
	}
	profileID := storage.NilID
	if request.Body.Profile != nil && *request.Body.Profile != "" {
		var ok bool
		profileID, ok = parseOpenAPIPublicID(publicid.KindAgentProfile, *request.Body.Profile)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid profile")
		}
	}
	if request.Body.Config == "" {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "config is required")
	}
	configID, ok := parseOpenAPIPublicID(publicid.KindAgentConfig, request.Body.Config)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid config")
	}
	idempotencyKey := ""
	if request.Params.IdempotencyKey != nil {
		idempotencyKey = *request.Params.IdempotencyKey
	}
	message := ""
	if request.Body.Message != nil {
		message = *request.Body.Message
	}
	name := ""
	if request.Body.Name != nil {
		name = strings.TrimSpace(*request.Body.Name)
	}
	result, err := s.server.store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      project.ID,
		ProfileID:      profileID,
		AgentConfigID:  configID,
		LaunchedBy:     principal,
		Name:           name,
		Message:        message,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	s.server.startLaunchMachineProvisioning(ctx, logpkg.LoggerFromContext(ctx), result)
	logent.Agent(ctx, result.Agent)
	if result.AgentInput.ID != storage.NilID {
		logent.AgentInput(ctx, result.AgentInput)
	}
	logent.MCPConnections(ctx, result.MCPConnections)
	if result.Created {
		response, err := s.server.launchAgentResponse(ctx, result)
		if err != nil {
			return nil, err
		}
		return openapi.CreateAgent201JSONResponse(response), nil
	}
	response, err := s.server.currentAgentResponse(ctx, result.Agent)
	if err != nil {
		return nil, err
	}
	return openapi.CreateAgent200JSONResponse(response), nil
}

func (s strictOpenAPIServer) UpdateAgentConfig(
	ctx context.Context,
	request openapi.UpdateAgentConfigRequestObject,
) (openapi.UpdateAgentConfigResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.updateAgentConfig(ctx, request, scope.project, scope.agent)
}

func (s strictOpenAPIServer) updateAgentConfig(
	ctx context.Context,
	request openapi.UpdateAgentConfigRequestObject,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
) (openapi.UpdateAgentConfigResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	principal, ok := principalFromContext(ctx)
	if !ok || !identitystore.IsAccountPrincipal(principal) {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden,
			"authenticated account principal is required to change an agent config")
	}
	compiled, err := s.server.compileAgentConfigBodyForProject(
		ctx,
		project,
		string(request.Body.SourceFormat),
		request.Body.Source,
	)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	idempotencyKey := ""
	if request.Params.IdempotencyKey != nil {
		idempotencyKey = *request.Params.IdempotencyKey
	}
	result, err := s.server.store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: executionstore.CreateAgentConfigInput{
			ProjectID:               project.ID,
			Definition:              compiled.Definition,
			Source:                  compiled.Source,
			SourceFormat:            compiled.SourceFormat,
			ConfiguredModelID:       compiled.ConfiguredModelID,
			CompiledDefinition:      compiled.CompiledDefinition,
			CompilerVersion:         compiled.CompilerVersion,
			EffectiveDefinitionHash: compiled.DefinitionHash,
		},
		AgentID:        agent.ID,
		ActorType:      principal.Type,
		ActorID:        principal.ID,
		Reason:         "api",
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	s.server.startPoolMachineDeletion(ctx, result.DeleteMachines)
	config, err := s.server.agentConfigResponseFromRecord(ctx, result.AgentConfig)
	if err != nil {
		return nil, err
	}
	input, err := publicAgentInputResponseFromRecord(result.ConfigChange.AgentInput)
	if err != nil {
		return nil, err
	}
	eventID, err := publicID(publicid.KindAgentEvent, result.ConfigChange.Event.ID)
	if err != nil {
		return nil, err
	}
	return openapi.UpdateAgentConfig200JSONResponse(
		openapi.UpdateAgentConfigResponse{AgentConfig: config, AgentInput: input, EventId: eventID},
	), nil
}

func (s *Server) startLaunchMachineProvisioning(
	parent context.Context,
	logger *slog.Logger,
	result executionstore.LaunchAgentResult,
) {
	if s.machinePoolManager == nil || len(result.ProvisionMachineIDs) == 0 {
		return
	}
	orgID := result.Agent.OrgID
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), launchMachineProvisioningTimeout)
		defer cancel()
		for _, machineID := range result.ProvisionMachineIDs {
			if err := s.machinePoolManager.ProvisionMachine(ctx, orgID, machineID); err != nil {
				logger.Warn(
					"launch machine provisioning failed",
					"org_id",
					orgID,
					"machine_id",
					machineID,
					"error",
					err,
				)
			}
		}
	}()
}

func (s *Server) startPoolMachineDeletion(parent context.Context, machines []executionstore.MachineRecord) {
	if s.machinePoolManager == nil || len(machines) == 0 {
		return
	}
	logger := s.log
	if logger == nil {
		logger = slog.Default()
	}
	go func() {
		ctx, cancel := context.WithTimeout(
			context.WithoutCancel(parent),
			machinepool.DefaultImmediateDeletionTimeout,
		)
		defer cancel()
		if _, err := s.machinePoolManager.DeleteMachines(ctx, machines); err != nil {
			logger.Warn("pool machine deletion failed", "error", err)
		}
	}()
}

func (s *Server) currentAgentResponse(
	ctx context.Context,
	record executionstore.AgentRecord,
) (openapi.GetAgentResponse, error) {
	agent, err := publicAgentResponseFromRecord(record)
	if err != nil {
		return openapi.GetAgentResponse{}, err
	}
	records, err := s.store.Execution().ListAgentMachineBindings(ctx, record.ProjectID, record.ID)
	if err != nil {
		return openapi.GetAgentResponse{}, apierror.ProjectScoped(err)
	}
	bindings := make([]openapi.AgentMachineBinding, 0, len(records))
	for _, record := range records {
		binding, err := publicAgentMachineBindingResponse(record)
		if err != nil {
			return openapi.GetAgentResponse{}, err
		}
		bindings = append(bindings, binding)
	}
	connections, err := s.store.Execution().ListAgentMCPConnections(ctx, record.ProjectID, record.ID)
	if err != nil {
		return openapi.GetAgentResponse{}, apierror.ProjectScoped(err)
	}
	mcpConnections := make([]openapi.AgentMCPConnection, 0, len(connections))
	for _, connection := range connections {
		mcpConnections = append(mcpConnections, openapi.AgentMCPConnection{
			ServerKey:       connection.ServerKey,
			EndpointUrl:     connection.EndpointURL,
			State:           openapi.AgentMCPConnectionState(connection.State),
			ProtocolVersion: ptrFromNonEmpty(connection.ProtocolVersion),
			InitializeError: connection.InitializeError,
			CreatedAt:       connection.CreatedAt,
			UpdatedAt:       connection.UpdatedAt,
		})
	}
	return openapi.GetAgentResponse{
		Agent:           agent,
		MachineBindings: bindings,
		McpConnections:  mcpConnections,
	}, nil
}

func (s *Server) launchAgentResponse(
	ctx context.Context,
	result executionstore.LaunchAgentResult,
) (openapi.LaunchAgentResponse, error) {
	agent, err := publicAgentResponseFromRecord(result.Agent)
	if err != nil {
		return openapi.LaunchAgentResponse{}, err
	}
	config, err := s.agentConfigResponseFromRecord(ctx, result.AgentConfig)
	if err != nil {
		return openapi.LaunchAgentResponse{}, err
	}
	response := openapi.LaunchAgentResponse{
		Agent:       agent,
		AgentConfig: config,
	}
	bindings := make([]openapi.AgentMachineBinding, 0, len(result.MachineBindings))
	for _, record := range result.MachineBindings {
		binding, err := publicAgentMachineBindingResponse(record)
		if err != nil {
			return openapi.LaunchAgentResponse{}, err
		}
		bindings = append(bindings, binding)
	}
	response.MachineBindings = bindings
	if result.AgentInput.ID != storage.NilID {
		input, err := publicAgentInputResponseFromRecordWithContent(result.AgentInput, result.InputContentBlocks)
		if err != nil {
			return openapi.LaunchAgentResponse{}, err
		}
		response.AgentInput = &input
	}
	return response, nil
}

func publicAgentMachineBindingResponse(
	record executionstore.AgentMachineBindingRecord,
) (openapi.AgentMachineBinding, error) {
	id, err := publicID(publicid.KindAgentMachineBinding, record.ID)
	if err != nil {
		return openapi.AgentMachineBinding{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapi.AgentMachineBinding{}, err
	}
	agentID, err := publicID(publicid.KindAgent, record.AgentID)
	if err != nil {
		return openapi.AgentMachineBinding{}, err
	}
	machineID, err := publicID(publicid.KindMachine, record.MachineID)
	if err != nil {
		return openapi.AgentMachineBinding{}, err
	}
	var envOverlay map[string]*string
	if err := json.Unmarshal(record.EnvOverlay, &envOverlay); err != nil {
		return openapi.AgentMachineBinding{}, err
	}
	var secretEnvOverlay map[string]*openapi.SecretID
	if err := json.Unmarshal(record.SecretEnvOverlay, &secretEnvOverlay); err != nil {
		return openapi.AgentMachineBinding{}, err
	}
	return openapi.AgentMachineBinding{
		Id:               id,
		ProjectId:        projectID,
		AgentId:          agentID,
		MachineId:        machineID,
		MachineRef:       record.MachineRef,
		BindingKind:      openapi.AgentMachineBindingKind(record.BindingKind),
		State:            openapi.AgentMachineBindingState(record.State),
		Description:      record.Description,
		Cwd:              record.Cwd,
		EnvOverlay:       envOverlay,
		SecretEnvOverlay: secretEnvOverlay,
		CreatedAt:        record.CreatedAt,
		UpdatedAt:        record.UpdatedAt,
	}, nil
}

func (s *Server) agentProfileResponseFromRecord(
	ctx context.Context,
	record executionstore.AgentProfileRecord,
) (openapi.AgentProfile, error) {
	id, err := publicID(publicid.KindAgentProfile, record.ID)
	if err != nil {
		return openapi.AgentProfile{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.AgentProfile{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapi.AgentProfile{}, err
	}
	currentConfigID, err := publicID(publicid.KindAgentConfig, record.CurrentConfigID)
	if err != nil {
		return openapi.AgentProfile{}, err
	}
	currentConfig, err := s.agentConfigResponseFromRecord(ctx, record.CurrentConfig)
	if err != nil {
		return openapi.AgentProfile{}, err
	}
	return openapi.AgentProfile{
		Id:                id,
		OrgId:             orgID,
		ProjectId:         projectID,
		Name:              record.Name,
		CurrentConfigId:   currentConfigID,
		CurrentGeneration: int32(record.CurrentGeneration),
		CurrentConfig:     currentConfig,
		CreatedAt:         record.CreatedAt,
		UpdatedAt:         record.UpdatedAt,
	}, nil
}

func (s *Server) agentConfigResponseFromRecord(
	ctx context.Context,
	record executionstore.AgentConfigRecord,
) (openapi.AgentConfig, error) {
	id, err := publicID(publicid.KindAgentConfig, record.ID)
	if err != nil {
		return openapi.AgentConfig{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.AgentConfig{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapi.AgentConfig{}, err
	}
	response := openapi.AgentConfig{
		Id:                      id,
		OrgId:                   orgID,
		ProjectId:               projectID,
		EffectiveDefinitionHash: record.EffectiveDefinitionHash,
		CreatedAt:               record.CreatedAt,
	}
	if record.Source != "" {
		source := record.Source
		response.Source = &source
	}
	if record.SourceFormat != "" {
		sourceFormat := openapi.AgentConfigSourceFormat(record.SourceFormat)
		response.SourceFormat = &sourceFormat
	}
	if record.CompilerVersion != "" {
		compilerVersion := record.CompilerVersion
		response.CompilerVersion = &compilerVersion
	}
	contract, err := agentconfig.RuntimeContractFromCompiled(
		record.CompiledDefinition,
		record.CompilerVersion,
		record.EffectiveDefinitionHash,
	)
	if err != nil {
		return openapi.AgentConfig{}, err
	}
	configuredModel, err := s.store.Models().GetConfiguredModelDisplay(ctx, record.OrgID, record.ConfiguredModelID)
	if err != nil {
		return openapi.AgentConfig{}, err
	}
	revision, err := s.store.Models().GetConfiguredModelRevisionDisplay(
		ctx,
		record.OrgID,
		configuredModel.CurrentRevisionID,
	)
	if err != nil {
		return openapi.AgentConfig{}, err
	}
	effectiveModel, err := s.agentConfigEffectiveModel(ctx, record, configuredModel, revision, contract.Model)
	if err != nil {
		return openapi.AgentConfig{}, err
	}
	configuredModelID, err := publicID(publicid.KindConfiguredModel, record.ConfiguredModelID)
	if err != nil {
		return openapi.AgentConfig{}, err
	}
	configuredModelRevisionID, err := publicID(publicid.KindConfiguredModelRevision, revision.ID)
	if err != nil {
		return openapi.AgentConfig{}, err
	}
	response.Model = openapi.AgentConfigModel{
		ProviderConfig:         revision.ProviderConfigName,
		Name:                   revision.ConfiguredModelName,
		ConfiguredModelId:      configuredModelID,
		CurrentRevisionId:      configuredModelRevisionID,
		ProviderModelSlug:      revision.ProviderModelSlug,
		ApiFormat:              openapi.ModelAPIFormat(revision.APIFormat),
		ApiVariant:             string(revision.APIVariant),
		ContextWindowTokens:    effectiveModel.ContextWindowTokens,
		MaxOutputTokens:        effectiveModel.MaxOutputTokens,
		DefaultMaxOutputTokens: nullableFromPtr(effectiveModel.DefaultMaxOutputTokens),
		DefaultCacheRetention: openapi.ModelCacheRetention(
			modelCacheRetention(effectiveModel.DefaultCacheRetention),
		),
		SupportsTools:             effectiveModel.SupportsTools,
		SupportsReasoning:         effectiveModel.SupportsReasoning,
		DefaultReasoningEffort:    effectiveModel.DefaultReasoningEffort,
		SupportedReasoningEfforts: cloneStringSlice(effectiveModel.SupportedReasoningEfforts),
		InputModalities:           cloneStringSlice(effectiveModel.InputModalities),
		OutputModalities:          cloneStringSlice(effectiveModel.OutputModalities),
	}
	hash := instructionHash(contract.Instruction)
	response.InstructionHash = &hash
	return response, nil
}

func (s *Server) agentConfigEffectiveModel(
	ctx context.Context,
	record executionstore.AgentConfigRecord,
	configuredModel modelstore.ConfiguredModelRecord,
	revision modelstore.ConfiguredModelRevisionDisplayRecord,
	model agentconfig.ModelCompiled,
) (modelstore.ConfiguredModelRevisionRecord, error) {
	options := executionstore.AgentModelOptionsFromCompiledModel(model)
	grant, err := s.store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx,
		record.OrgID,
		record.ProjectID,
		record.ConfiguredModelID,
	)
	if err == nil {
		return modelstore.EffectiveConfiguredModelForAgentOptions(
			revision.APIFormat,
			configuredModel,
			grant,
			options,
		)
	}
	if !storeerr.IsNotFound(err) {
		return modelstore.ConfiguredModelRevisionRecord{}, err
	}
	return modelstore.EffectiveConfiguredModelRevisionForAgentOptions(
		revision.APIFormat,
		revision.ConfiguredModelRevisionRecord,
		options,
	)
}

func modelCacheRetention(value string) string {
	if value == "" {
		return modelstore.ModelCacheRetentionNone
	}
	return value
}

func instructionHash(instruction string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(instruction)))
	return hex.EncodeToString(sum[:])
}

func agentConfigSourceFormatFromString(value string) (agentconfig.SourceFormat, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(agentconfig.SourceFormatYAML):
		return agentconfig.SourceFormatYAML, nil
	case string(agentconfig.SourceFormatJSON):
		return agentconfig.SourceFormatJSON, nil
	case "":
		return "", fmt.Errorf("source_format is required")
	default:
		return "", fmt.Errorf("unsupported source_format %q; use yaml or json", value)
	}
}

type compiledAgentConfigBody struct {
	Definition         json.RawMessage
	Source             string
	SourceFormat       string
	ConfiguredModelID  storage.ID
	CompiledDefinition json.RawMessage
	CompilerVersion    string
	DefinitionHash     string
}

func (s *Server) compileAgentConfigBodyForProject(
	ctx context.Context,
	project identitystore.ProjectRecord,
	sourceFormatRaw, source string,
) (compiledAgentConfigBody, error) {
	sourceFormat, err := agentConfigSourceFormatFromString(sourceFormatRaw)
	if err != nil {
		return compiledAgentConfigBody{}, err
	}
	if source == "" {
		return compiledAgentConfigBody{}, fmt.Errorf("source is required")
	}
	opts := s.agentConfigOptions
	opts.ValidateSecretID = func(secretID string, expectedKind secrets.Kind) error {
		decoded, err := publicid.Decode(publicid.KindSecret, secretID)
		if err != nil {
			return err
		}
		return s.store.Secrets().ValidateProjectSecretReference(ctx, project.OrgID, project.ID, decoded, expectedKind)
	}
	opts.ResolveModelSelection = func(
		providerConfigName string,
		configuredModelName string,
	) (agentconfig.ResolvedModelSelection, error) {
		providerConfig, err := s.store.Models().GetModelProviderConfigByName(ctx, project.OrgID, providerConfigName)
		if err != nil {
			if storeerr.IsNotFound(err) {
				return agentconfig.ResolvedModelSelection{}, fmt.Errorf(
					"model.provider_config %q was not found: %w",
					providerConfigName,
					storeerr.ErrNotFound,
				)
			}
			return agentconfig.ResolvedModelSelection{}, err
		}
		configuredModel, err := s.store.Models().GetConfiguredModelByName(
			ctx,
			project.OrgID,
			providerConfig.ID,
			configuredModelName,
		)
		if err != nil {
			if storeerr.IsNotFound(err) {
				return agentconfig.ResolvedModelSelection{}, fmt.Errorf(
					"configured model name %q is not configured for model.provider_config %q: %w",
					configuredModelName,
					providerConfigName,
					storeerr.ErrNotFound,
				)
			}
			return agentconfig.ResolvedModelSelection{}, err
		}
		grant, err := s.store.Models().GetActiveProjectModelGrantForConfiguredModel(
			ctx,
			project.OrgID,
			project.ID,
			configuredModel.ID,
		)
		if err != nil {
			if storeerr.IsNotFound(err) {
				return agentconfig.ResolvedModelSelection{}, fmt.Errorf(
					"configured model name %q on model.provider_config %q does not have an active project grant: %w",
					configuredModelName,
					providerConfigName,
					storeerr.ErrNotFound,
				)
			}
			return agentconfig.ResolvedModelSelection{}, err
		}
		effectiveModel, err := modelstore.EffectiveConfiguredModelForProjectGrant(
			providerConfig.APIFormat,
			configuredModel,
			grant,
		)
		if err != nil {
			return agentconfig.ResolvedModelSelection{}, err
		}
		supportsTools := effectiveModel.SupportsTools
		return agentconfig.ResolvedModelSelection{
			ConfiguredModelID: configuredModel.ID.String(),
			SupportsTools:     &supportsTools,
		}, nil
	}
	opts.ResolveMachineName = func(machineName string) (string, error) {
		machineID, err := s.store.Execution().ResolveAgentConfigMachineName(ctx, project.ID, machineName)
		if err != nil {
			return "", err
		}
		return publicID(publicid.KindMachine, machineID)
	}
	opts.ResolveMachinePoolName = func(machinePoolName string) (string, error) {
		machinePoolID, err := s.store.Execution().ResolveAgentConfigMachinePoolName(
			ctx,
			project.OrgID,
			project.ID,
			machinePoolName,
		)
		if err != nil {
			return "", err
		}
		return publicID(publicid.KindMachinePool, machinePoolID)
	}
	opts.ResolveSkillID = func(skillID string) (agentconfig.SkillResolution, error) {
		records, missing, err := s.skills.GetSkillsByIDsForCompile(ctx, skillstore.GetSkillsByIDsInput{
			OrgID:     project.OrgID,
			ProjectID: project.ID,
			IDs:       []string{skillID},
		})
		if err != nil {
			return agentconfig.SkillResolution{}, err
		}
		if len(missing) > 0 {
			return agentconfig.SkillResolution{}, fmt.Errorf("skill not found or not visible: %s", skillID)
		}
		if len(records) != 1 {
			return agentconfig.SkillResolution{}, fmt.Errorf("skill resolver returned %d records for %s", len(records), skillID)
		}
		rec := records[0]
		encoded, err := publicID(publicid.KindSkill, rec.ID)
		if err != nil {
			return agentconfig.SkillResolution{}, fmt.Errorf("encode skill public id: %w", err)
		}
		return agentconfig.SkillResolution{
			PublicID: encoded,
			Name:     rec.Name,
		}, nil
	}
	result, err := agentconfig.Compile(sourceFormat, []byte(source), opts)
	if err != nil {
		return compiledAgentConfigBody{}, err
	}
	resolvedConfiguredModelID, err := storage.ParseID(result.Compiled.Model.ConfiguredModelID)
	if err != nil || resolvedConfiguredModelID == storage.NilID {
		return compiledAgentConfigBody{}, fmt.Errorf(
			"model.provider_config and model.name must resolve to a configured project-granted model",
		)
	}
	if err := s.store.Execution().ValidateAgentConfigMachineSources(
		ctx,
		project.ID,
		json.RawMessage(result.CanonicalJSON),
		agentconfig.CompilerVersion,
		result.Hash,
	); err != nil {
		return compiledAgentConfigBody{}, err
	}
	return compiledAgentConfigBody{
		Definition:         json.RawMessage(result.CanonicalJSON),
		Source:             result.Source,
		SourceFormat:       string(result.SourceFormat),
		ConfiguredModelID:  resolvedConfiguredModelID,
		CompiledDefinition: json.RawMessage(result.CanonicalJSON),
		CompilerVersion:    agentconfig.CompilerVersion,
		DefinitionHash:     result.Hash,
	}, nil
}

const launchMachineProvisioningTimeout = 2 * time.Minute
