package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (s strictOpenAPIServer) CreateIntegrationOAuthSetup(
	ctx context.Context,
	request openapi.CreateIntegrationOAuthSetupRequestObject,
) (openapi.CreateIntegrationOAuthSetupResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.createIntegrationOAuthSetup(ctx, scope.project, &request.AgentProfileID, request.Body)
	if err != nil {
		return nil, err
	}
	return openapi.CreateIntegrationOAuthSetup201JSONResponse(*result), nil
}

func (s strictOpenAPIServer) createIntegrationOAuthSetup(
	ctx context.Context,
	project identitystore.ProjectRecord,
	profileRef *string,
	body *openapi.CreateIntegrationOAuthSetupRequest,
) (*openapi.IntegrationOAuthSetup, error) {
	if s.server.publicURL == "" || s.server.secretKeyWrapper == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable,
			"integration oauth requires a configured public URL and secret encryption keys")
	}
	var agentProfileID uuid.UUID
	if profileRef != nil {
		var ok bool
		agentProfileID, ok = parseOpenAPIPublicID(publicid.KindAgentProfile, *profileRef)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		}
	}
	if body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	principal, _ := principalFromContext(ctx)
	provider := integrationstore.IntegrationProviderSlack
	if body.Provider != nil {
		provider = strings.TrimSpace(*body.Provider)
	}
	if provider == "" {
		provider = integrationstore.IntegrationProviderSlack
	}
	clientID := strings.TrimSpace(body.ClientId)
	clientSecret := strings.TrimSpace(body.ClientSecret)
	signingSecret := strings.TrimSpace(body.SigningSecret)
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
	if profileRef != nil {
		profile, err := s.server.store.Execution().GetAgentProfile(ctx, project.ID, agentProfileID)
		if err != nil {
			return nil, apierror.ProjectScoped(err)
		}
		if err := s.server.validateSlackAppSetupConfig(ctx, profile.CurrentConfig); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	flowID, err := uuid.NewV7()
	if err != nil {
		logpkg.Error(ctx, fmt.Errorf("generate integration oauth flow id: %w", err))
		return nil, fmt.Errorf("internal server error")
	}
	expiresAt := now.Add(integrationOAuthStateTTL)
	returnTo := ""
	if body.ReturnTo != nil {
		returnTo = *body.ReturnTo
	}
	stateToken, err := s.server.encodeIntegrationOAuthState(ctx, integrationOAuthState{
		FlowID:            flowID,
		OrgID:             project.OrgID,
		ProjectID:         project.ID,
		AgentProfileID:    agentProfileID,
		ConnectionOnly:    profileRef == nil,
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
	return &openapi.IntegrationOAuthSetup{
		Provider:    provider,
		FlowId:      publicFlowID,
		OauthUrl:    installURL,
		RedirectUri: redirectURI,
		EventsUrl:   eventsURL,
		ActionsUrl:  actionsURL,
		ExpiresAt:   expiresAt,
	}, nil
}

func (s strictOpenAPIServer) CreateSlackSetup(
	ctx context.Context,
	request openapi.CreateSlackSetupRequestObject,
) (openapi.CreateSlackSetupResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.createSlackSetup(ctx, scope.project, &request.AgentProfileID, request.Body)
	if err != nil {
		return nil, err
	}
	return openapi.CreateSlackSetup201JSONResponse(*result), nil
}

func (s strictOpenAPIServer) createSlackSetup(
	ctx context.Context,
	project identitystore.ProjectRecord,
	profileRef *string,
	body *openapi.CreateSlackSetupRequest,
) (*openapi.SlackSetup, error) {
	if s.server.publicURL == "" || s.server.secretKeyWrapper == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable,
			"slack setup requires a configured public URL and secret encryption keys")
	}
	if err := validateSlackSetupPublicURL(s.server.publicURL); err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, err.Error())
	}
	var agentProfileID uuid.UUID
	if profileRef != nil {
		var ok bool
		agentProfileID, ok = parseOpenAPIPublicID(publicid.KindAgentProfile, *profileRef)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		}
	}
	if body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	principal, _ := principalFromContext(ctx)
	appName := strings.TrimSpace(body.AppName)
	appConfigurationToken := strings.TrimSpace(body.AppConfigurationToken)
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
	if profileRef != nil {
		profile, err := s.server.store.Execution().GetAgentProfile(ctx, project.ID, agentProfileID)
		if err != nil {
			return nil, apierror.ProjectScoped(err)
		}
		if err := s.server.validateSlackAppSetupConfig(ctx, profile.CurrentConfig); err != nil {
			return nil, err
		}
	}
	appIcon, err := slackSetupAppIcon(*body)
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
	if body.ReturnTo != nil {
		returnTo = *body.ReturnTo
	}
	stateToken, err := s.server.encodeIntegrationOAuthState(ctx, integrationOAuthState{
		FlowID:            flowID,
		OrgID:             project.OrgID,
		ProjectID:         project.ID,
		AgentProfileID:    agentProfileID,
		ConnectionOnly:    profileRef == nil,
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
	return &openapi.SlackSetup{
		Provider:    integrationstore.IntegrationProviderSlack,
		FlowId:      publicFlowID,
		SlackAppId:  app.AppID,
		OauthUrl:    installURL,
		RedirectUri: redirectURI,
		EventsUrl:   eventsURL,
		ActionsUrl:  actionsURL,
		ExpiresAt:   expiresAt,
	}, nil
}

func (s strictOpenAPIServer) CreateProjectIntegrationOAuthSetup(
	ctx context.Context,
	request openapi.CreateProjectIntegrationOAuthSetupRequestObject,
) (openapi.CreateProjectIntegrationOAuthSetupResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.createIntegrationOAuthSetup(ctx, scope.project, nil, request.Body)
	if err != nil {
		return nil, err
	}
	return openapi.CreateProjectIntegrationOAuthSetup201JSONResponse(*result), nil
}

func (s strictOpenAPIServer) CreateProjectSlackSetup(
	ctx context.Context,
	request openapi.CreateProjectSlackSetupRequestObject,
) (openapi.CreateProjectSlackSetupResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.createSlackSetup(ctx, scope.project, nil, request.Body)
	if err != nil {
		return nil, err
	}
	return openapi.CreateProjectSlackSetup201JSONResponse(*result), nil
}
