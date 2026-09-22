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
	"github.com/omnara-ai/omnara/internal/agentconfigcompile"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
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
		return nil, agentConfigCompileError(err)
	}
	config, err := s.server.store.Execution().CreateAgentConfig(ctx, compiled.CreateInput(project.ID))
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
	if principal.ID == uuid.Nil {
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
	if principal.ID == uuid.Nil {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden,
			"authenticated user principal is required to update an agent profile")
	}
	if strings.TrimSpace(request.Body.ExpectedCurrentConfigId) == "" {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "expected_current_config_id is required")
	}
	expectedCurrentConfigID, ok := parseOpenAPIPublicID(publicid.KindAgentConfig, request.Body.ExpectedCurrentConfigId)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid expected_current_config_id")
	}
	if strings.TrimSpace(request.Body.Config) == "" {
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

func (s strictOpenAPIServer) RenameAgentProfile(
	ctx context.Context,
	request openapi.RenameAgentProfileRequestObject,
) (openapi.RenameAgentProfileResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	profileID, ok := parseOpenAPIPublicID(publicid.KindAgentProfile, request.AgentProfileID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	if request.Body.Name == "" {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "name is required")
	}
	principal, _ := principalFromContext(ctx)
	if principal.ID == uuid.Nil {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden,
			"authenticated user principal is required to rename an agent profile")
	}
	profile, err := s.server.store.Execution().RenameAgentProfile(ctx, executionstore.RenameAgentProfileInput{
		ProjectID: scope.project.ID,
		ProfileID: profileID,
		Name:      request.Body.Name,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := s.server.agentProfileResponseFromRecord(ctx, profile)
	if err != nil {
		return nil, err
	}
	return openapi.RenameAgentProfile200JSONResponse(response), nil
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
	if err := s.server.validateIntegrationSendSetupConfig(ctx, profile.CurrentConfig); err != nil {
		return nil, err
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
	publicFlowID, err := publicID(publicid.KindIntegrationOAuthFlow, flowID)
	if err != nil {
		logpkg.Error(ctx, err)
		return nil, apierror.FromCode(openapi.ErrorCodeInternalError, "internal server error")
	}
	return openapi.CreateIntegrationOAuthSetup201JSONResponse(openapi.IntegrationOAuthSetup{
		Provider:    provider,
		FlowId:      publicFlowID,
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
	if err := s.server.validateIntegrationSendSetupConfig(ctx, profile.CurrentConfig); err != nil {
		return nil, err
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
	publicFlowID, err := publicID(publicid.KindIntegrationOAuthFlow, flowID)
	if err != nil {
		logpkg.Error(ctx, err)
		return nil, apierror.FromCode(openapi.ErrorCodeInternalError, "internal server error")
	}
	return openapi.CreateSlackSetup201JSONResponse(openapi.SlackSetup{
		Provider:    integrationstore.IntegrationProviderSlack,
		FlowId:      publicFlowID,
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
	if params.ParentAgentId != nil && *params.ParentAgentId != "" {
		parentAgentID, err := publicid.Decode(publicid.KindAgent, *params.ParentAgentId)
		if err != nil {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid parent agent filter")
		}
		filters.ParentAgentID = &parentAgentID
	}
	if params.IncludeSubagents != nil {
		filters.IncludeSubagents = *params.IncludeSubagents
	}
	if params.IncludeArchived != nil {
		filters.IncludeArchived = *params.IncludeArchived
	}
	extra := struct {
		AgentProfileID   *uuid.UUID
		ParentAgentID    *uuid.UUID
		IncludeSubagents bool
		IncludeArchived  bool
	}{filters.AgentProfileID, filters.ParentAgentID, filters.IncludeSubagents, filters.IncludeArchived}
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: params.Name, Sort: optionalString(params.Sort),
		Cursor: params.Cursor, ListKind: "agents",
		Scope: project.OrgID.String() + "/" + project.ID.String(), IDKind: publicid.KindAgent,
		AllowedSorts: defaultResourceSorts, Extra: extra,
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
		project.OrgID.String()+"/"+project.ID.String(), publicid.KindAgent, extra,
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
	data := make([]openapi.AgentProfileSummary, 0, len(page.Profiles))
	for _, profile := range page.Profiles {
		response, err := s.server.agentProfileSummaryFromRecord(ctx, profile)
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
	profileID := uuid.Nil
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
	result, err := s.server.store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      project.ID,
		ProfileID:      profileID,
		AgentConfigID:  configID,
		LaunchedBy:     principal,
		Name:           request.Body.Name,
		Message:        message,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	s.server.startLaunchMachineProvisioning(ctx, logpkg.LoggerFromContext(ctx), result)
	logent.Agent(ctx, result.Agent)
	if result.AgentInput.ID != uuid.Nil {
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
	response, err := currentAgentEnvelope(result.Agent)
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
		return nil, agentConfigCompileError(err)
	}
	expectedCurrentConfigID := uuid.Nil
	if request.Body.ExpectedCurrentConfigId != nil {
		expectedCurrentConfigID, ok = parseOpenAPIPublicID(
			publicid.KindAgentConfig,
			*request.Body.ExpectedCurrentConfigId,
		)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid expected_current_config_id")
		}
	}
	idempotencyKey := ""
	if request.Params.IdempotencyKey != nil {
		idempotencyKey = *request.Params.IdempotencyKey
	}
	result, err := s.server.store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput:  compiled.CreateInput(project.ID),
		AgentID:                 agent.ID,
		ExpectedCurrentConfigID: expectedCurrentConfigID,
		ActorType:               principal.Type,
		ActorID:                 principal.ID,
		Reason:                  "api",
		IdempotencyKey:          idempotencyKey,
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
	if s.machinePoolManager == nil {
		return
	}
	s.machinePoolManager.StartLaunchProvisioning(
		parent,
		logger,
		result.Agent.OrgID,
		result.ProvisionMachineIDs,
	)
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

func currentAgentEnvelope(record executionstore.AgentRecord) (openapi.CurrentAgentResponse, error) {
	agent, err := publicAgentResponseFromRecord(record)
	if err != nil {
		return openapi.CurrentAgentResponse{}, err
	}
	return openapi.CurrentAgentResponse{Agent: agent}, nil
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
	machineIDs := make([]openapi.MachineID, 0, len(records))
	seen := make(map[uuid.UUID]bool, len(records))
	for _, binding := range records {
		if binding.State != executionstore.AgentMachineBindingStateAttached || seen[binding.MachineID] {
			continue
		}
		seen[binding.MachineID] = true
		machineID, err := publicID(publicid.KindMachine, binding.MachineID)
		if err != nil {
			return openapi.GetAgentResponse{}, err
		}
		machineIDs = append(machineIDs, machineID)
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
		Agent:          agent,
		MachineIds:     machineIDs,
		McpConnections: mcpConnections,
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
	if result.AgentInput.ID != uuid.Nil {
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
	if err := publicSecretIDs(record.SecretEnvOverlay, &secretEnvOverlay); err != nil {
		return openapi.AgentMachineBinding{}, err
	}
	return openapi.AgentMachineBinding{
		Id:               id,
		ProjectId:        projectID,
		AgentId:          agentID,
		MachineId:        machineID,
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

func (s *Server) agentConfigResponseFromRecord(
	ctx context.Context,
	record executionstore.AgentConfigRecord,
) (openapi.AgentConfig, error) {
	summary, err := s.agentConfigSummaryFromRecord(ctx, record)
	if err != nil {
		return openapi.AgentConfig{}, err
	}
	return agentConfigDetailResponse(summary, record.CompiledDefinition)
}

func agentConfigDetailResponse(
	summary openapi.AgentConfigSummary,
	compiled json.RawMessage,
) (openapi.AgentConfig, error) {
	definition, err := publicCompiledDefinition(compiled)
	if err != nil {
		return openapi.AgentConfig{}, err
	}
	return openapi.AgentConfig{
		Id:                      summary.Id,
		OrgId:                   summary.OrgId,
		ProjectId:               summary.ProjectId,
		Source:                  summary.Source,
		SourceFormat:            (*openapi.AgentConfigSourceFormat)(summary.SourceFormat),
		EffectiveDefinitionHash: summary.EffectiveDefinitionHash,
		Model:                   summary.Model,
		InstructionHash:         summary.InstructionHash,
		CreatedAt:               summary.CreatedAt,
		CompiledDefinition:      definition,
	}, nil
}

func (s *Server) agentProfileResponseFromRecord(
	ctx context.Context,
	record executionstore.AgentProfileRecord,
) (openapi.AgentProfile, error) {
	summary, err := s.agentProfileSummaryFromRecord(ctx, record)
	if err != nil {
		return openapi.AgentProfile{}, err
	}
	config, err := agentConfigDetailResponse(summary.CurrentConfig, record.CurrentConfig.CompiledDefinition)
	if err != nil {
		return openapi.AgentProfile{}, err
	}
	return openapi.AgentProfile{
		Id: summary.Id, OrgId: summary.OrgId, ProjectId: summary.ProjectId,
		Name: summary.Name, CurrentConfigId: summary.CurrentConfigId,
		CurrentGeneration: summary.CurrentGeneration, CurrentConfig: config,
		CreatedAt: summary.CreatedAt, UpdatedAt: summary.UpdatedAt,
	}, nil
}

func (s *Server) agentProfileSummaryFromRecord(
	ctx context.Context,
	record executionstore.AgentProfileRecord,
) (openapi.AgentProfileSummary, error) {
	id, err := publicID(publicid.KindAgentProfile, record.ID)
	if err != nil {
		return openapi.AgentProfileSummary{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.AgentProfileSummary{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapi.AgentProfileSummary{}, err
	}
	currentConfigID, err := publicID(publicid.KindAgentConfig, record.CurrentConfigID)
	if err != nil {
		return openapi.AgentProfileSummary{}, err
	}
	currentConfig, err := s.agentConfigSummaryFromRecord(ctx, record.CurrentConfig)
	if err != nil {
		return openapi.AgentProfileSummary{}, err
	}
	return openapi.AgentProfileSummary{
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

func (s *Server) agentConfigSummaryFromRecord(
	ctx context.Context,
	record executionstore.AgentConfigRecord,
) (openapi.AgentConfigSummary, error) {
	id, err := publicID(publicid.KindAgentConfig, record.ID)
	if err != nil {
		return openapi.AgentConfigSummary{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.AgentConfigSummary{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapi.AgentConfigSummary{}, err
	}
	response := openapi.AgentConfigSummary{
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
		sourceFormat := openapi.AgentConfigSummarySourceFormat(record.SourceFormat)
		response.SourceFormat = &sourceFormat
	}
	contract, err := agentconfig.RuntimeContractFromCompiled(
		record.CompiledDefinition,
		record.EffectiveDefinitionHash,
	)
	if err != nil {
		return openapi.AgentConfigSummary{}, err
	}
	configuredModel, err := s.store.Models().GetConfiguredModelDisplay(ctx, record.OrgID, record.ConfiguredModelID)
	if err != nil {
		return openapi.AgentConfigSummary{}, err
	}
	revision, err := s.store.Models().GetConfiguredModelRevisionDisplay(
		ctx,
		record.OrgID,
		configuredModel.CurrentRevisionID,
	)
	if err != nil {
		return openapi.AgentConfigSummary{}, err
	}
	effectiveModel, err := s.agentConfigEffectiveModel(ctx, record, configuredModel, revision, contract.Model)
	if err != nil {
		return openapi.AgentConfigSummary{}, err
	}
	configuredModelID, err := publicID(publicid.KindConfiguredModel, record.ConfiguredModelID)
	if err != nil {
		return openapi.AgentConfigSummary{}, err
	}
	configuredModelRevisionID, err := publicID(publicid.KindConfiguredModelRevision, revision.ID)
	if err != nil {
		return openapi.AgentConfigSummary{}, err
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
		MaxOutputTokens:        nullableFromPtr(effectiveModel.MaxOutputTokens),
		DefaultMaxOutputTokens: nullableFromPtr(effectiveModel.DefaultMaxOutputTokens),
		DefaultCacheRetention: openapi.ModelCacheRetention(model.EffectiveCacheRetention(
			model.CacheRetention(effectiveModel.DefaultCacheRetention),
		)),
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
	compiledModel agentconfig.ModelCompiled,
) (modelstore.ConfiguredModelRevisionRecord, error) {
	options := compiledModel.Overrides()
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

func (s *Server) compileAgentConfigBodyForProject(
	ctx context.Context,
	project identitystore.ProjectRecord,
	sourceFormatRaw, source string,
) (agentconfigcompile.Body, error) {
	sourceFormat, err := agentConfigSourceFormatFromString(sourceFormatRaw)
	if err != nil {
		return agentconfigcompile.Body{}, err
	}
	body, err := agentconfigcompile.Compile(
		ctx,
		s.store,
		project.OrgID,
		project.ID,
		s.agentConfigOptions,
		sourceFormat,
		source,
	)
	if err != nil {
		return agentconfigcompile.Body{}, err
	}
	return body, nil
}

func agentConfigCompileError(err error) apierror.ResponseError {
	var validationErr *agentconfig.ValidationError
	if !errors.As(err, &validationErr) {
		return apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	issues := make([]openapi.AgentConfigErrorIssue, 0, len(validationErr.Issues))
	for _, issue := range validationErr.Issues {
		converted := openapi.AgentConfigErrorIssue{Path: issue.Path, Message: issue.Message}
		if issue.Line > 0 {
			line := issue.Line
			converted.Line = &line
		}
		if issue.Column > 0 {
			column := issue.Column
			converted.Column = &column
		}
		issues = append(issues, converted)
	}
	return apierror.WithIssues(openapi.ErrorCodeInvalidRequest, validationErr.Error(), issues)
}
