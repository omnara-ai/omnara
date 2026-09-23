package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/apps/slack"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
)

func (s strictOpenAPIServer) CreateProjectAppOAuthSetup(
	ctx context.Context,
	request openapi.CreateProjectAppOAuthSetupRequestObject,
) (openapi.CreateProjectAppOAuthSetupResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.createAppOAuthSetup(ctx, scope, request.AppID, request.Body)
	if err != nil {
		return nil, err
	}
	return openapi.CreateProjectAppOAuthSetup201JSONResponse(*result), nil
}

func (s strictOpenAPIServer) createAppOAuthSetup(
	ctx context.Context,
	scope projectScopeRecord,
	appRef string,
	body *openapi.CreateAppOAuthSetupRequest,
) (*openapi.AppOAuthSetup, error) {
	if s.server.publicURL == "" || s.server.secretKeyWrapper == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable,
			"app OAuth setup requires a configured public URL and secret encryption keys")
	}
	app, err := s.projectAppForSetup(ctx, scope, appRef)
	if err != nil {
		return nil, err
	}
	if app.Provider != appstore.AppProviderSlack {
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"this app does not support Slack OAuth setup",
		)
	}
	principal, principalErr := userPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	if body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	provider := app.Provider
	clientID := strings.TrimSpace(body.ClientId)
	clientSecret := strings.TrimSpace(body.ClientSecret)
	signingSecret := strings.TrimSpace(body.SigningSecret)
	if err := validateSlackSetupPublicURL(s.server.publicURL); err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, err.Error())
	}
	if clientID == "" || clientSecret == "" || signingSecret == "" {
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"client_id, client_secret, and signing_secret are required",
		)
	}
	now := time.Now().UTC()
	flowID, err := uuid.NewV7()
	if err != nil {
		logpkg.Error(ctx, fmt.Errorf("generate app oauth flow id: %w", err))
		return nil, fmt.Errorf("internal server error")
	}
	expiresAt := now.Add(appOAuthStateTTL)
	returnTo := ""
	if body.ReturnTo != nil {
		returnTo = *body.ReturnTo
	}
	stateToken, err := s.server.encodeAppOAuthState(ctx, appOAuthState{
		FlowID:            flowID,
		OrgID:             scope.project.OrgID,
		ProjectID:         scope.project.ID,
		AppID:             app.ID,
		SetupRevision:     app.SetupRevision,
		InstalledByUserID: principal.ID,
		Provider:          provider,
		ClientID:          clientID,
		ClientSecret:      clientSecret,
		SigningSecret:     signingSecret,
		ExpiresAt:         expiresAt,
		ReturnTo:          returnTo,
	})
	if err != nil {
		if errors.Is(err, errAppOAuthStateTooLarge) {
			return nil, apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				"request fields are too large for the OAuth state parameter",
			)
		}
		logpkg.Error(ctx, fmt.Errorf("start app oauth flow: %w", err))
		return nil, fmt.Errorf("state generation failed")
	}
	redirectURI := s.server.absolutePublicURL(appOAuthCallbackPath)
	eventsURL := s.server.absolutePublicURL(appEventsPath)
	actionsURL := s.server.absolutePublicURL(appActionsPath)
	installURL, err := s.server.appOAuthAuthorizeURL(
		provider,
		clientID,
		redirectURI,
		stateToken,
	)
	if err != nil {
		if errors.Is(err, errAppOAuthStateTooLarge) {
			return nil, apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				"request fields are too large for the oauth state parameter",
			)
		}
		logpkg.Error(ctx, fmt.Errorf("build app oauth authorization URL: %w", err))
		return nil, fmt.Errorf("internal server error")
	}
	publicFlowID, err := publicID(publicid.KindAppOAuthFlow, flowID)
	if err != nil {
		logpkg.Error(ctx, err)
		return nil, apierror.FromCode(openapi.ErrorCodeInternalError, "internal server error")
	}
	return &openapi.AppOAuthSetup{
		AppId:         appRef,
		SetupRevision: app.SetupRevision,
		Provider:      provider,
		FlowId:        publicFlowID,
		OauthUrl:      installURL,
		RedirectUri:   redirectURI,
		EventsUrl:     eventsURL,
		ActionsUrl:    actionsURL,
		ExpiresAt:     expiresAt,
	}, nil
}

func (s strictOpenAPIServer) CreateProjectAppSlackSetup(
	ctx context.Context,
	request openapi.CreateProjectAppSlackSetupRequestObject,
) (openapi.CreateProjectAppSlackSetupResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.createSlackSetup(ctx, scope, request.AppID, request.Body)
	if err != nil {
		return nil, err
	}
	return openapi.CreateProjectAppSlackSetup201JSONResponse(*result), nil
}

func (s strictOpenAPIServer) createSlackSetup(
	ctx context.Context,
	scope projectScopeRecord,
	appRef string,
	body *openapi.CreateSlackSetupRequest,
) (*openapi.SlackSetup, error) {
	if s.server.publicURL == "" || s.server.secretKeyWrapper == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable,
			"slack setup requires a configured public URL and secret encryption keys")
	}
	if err := validateSlackSetupPublicURL(s.server.publicURL); err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, err.Error())
	}
	app, err := s.projectAppForSetup(ctx, scope, appRef)
	if err != nil {
		return nil, err
	}
	if app.Provider != appstore.AppProviderSlack {
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"this app does not support Slack OAuth setup",
		)
	}
	principal, principalErr := userPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	if body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	if app.ProviderAccountRef != "" {
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"this app already has a Slack identity; reconnect through OAuth setup",
		)
	}
	appName := strings.TrimSpace(body.AppName)
	appConfigurationToken := strings.TrimSpace(body.AppConfigurationToken)
	if appName == "" || appConfigurationToken == "" {
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"app_name and app_configuration_token are required",
		)
	}
	if utf8.RuneCountInString(appName) > slack.AppNameMaxRunes {
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"app_name must be 35 characters or fewer",
		)
	}
	if strings.EqualFold(appName, "slackbot") {
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"Slack reserves this app name. Choose a different name.",
		)
	}
	appIcon, err := slackSetupAppIcon(*body)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	redirectURI := s.server.absolutePublicURL(appOAuthCallbackPath)
	eventsURL := s.server.absolutePublicURL(appEventsPath)
	actionsURL := s.server.absolutePublicURL(appActionsPath)
	outboundCtx, cancel := context.WithTimeout(ctx, appOAuthTimeout)
	defer cancel()
	manifestApp, err := slack.CreateManifestApp(
		outboundCtx,
		s.server.slackOAuth,
		appConfigurationToken,
		slack.BuildAppManifest(appName, eventsURL, actionsURL, redirectURI),
	)
	if err != nil {
		logpkg.Error(ctx, fmt.Errorf("create slack app manifest: %w", err))
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"slack app creation failed: "+err.Error(),
		)
	}
	iconCtx, iconCancel := context.WithTimeout(outboundCtx, 3*time.Second)
	defer iconCancel()
	if err := slack.SetAppIcon(
		iconCtx,
		s.server.slackOAuth,
		appConfigurationToken,
		manifestApp.AppID,
		appIcon,
	); err != nil {
		logpkg.Error(ctx, fmt.Errorf("set slack app icon: %w", err))
	}
	now := time.Now().UTC()
	flowID, err := uuid.NewV7()
	if err != nil {
		logpkg.Error(ctx, fmt.Errorf("generate slack oauth flow id: %w", err))
		return nil, fmt.Errorf("internal server error")
	}
	expiresAt := now.Add(appOAuthStateTTL)
	returnTo := ""
	if body.ReturnTo != nil {
		returnTo = *body.ReturnTo
	}
	stateToken, err := s.server.encodeAppOAuthState(ctx, appOAuthState{
		FlowID:            flowID,
		OrgID:             scope.project.OrgID,
		ProjectID:         scope.project.ID,
		AppID:             app.ID,
		SetupRevision:     app.SetupRevision,
		InstalledByUserID: principal.ID,
		Provider:          appstore.AppProviderSlack,
		ClientID:          manifestApp.ClientID,
		ClientSecret:      manifestApp.ClientSecret,
		SigningSecret:     manifestApp.SigningSecret,
		BotDisplayName:    appName,
		ExpiresAt:         expiresAt,
		ReturnTo:          returnTo,
	})
	if err != nil {
		if errors.Is(err, errAppOAuthStateTooLarge) {
			return nil, apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				"request fields are too large for the oauth state parameter",
			)
		}
		logpkg.Error(ctx, fmt.Errorf("start slack oauth flow: %w", err))
		return nil, fmt.Errorf("state generation failed")
	}
	installURL, err := s.server.appOAuthAuthorizeURL(
		appstore.AppProviderSlack,
		manifestApp.ClientID,
		redirectURI,
		stateToken,
	)
	if err != nil {
		if errors.Is(err, errAppOAuthStateTooLarge) {
			return nil, apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				"request fields are too large for the oauth state parameter",
			)
		}
		logpkg.Error(ctx, fmt.Errorf("build slack oauth authorization URL: %w", err))
		return nil, fmt.Errorf("internal server error")
	}
	publicFlowID, err := publicID(publicid.KindAppOAuthFlow, flowID)
	if err != nil {
		logpkg.Error(ctx, err)
		return nil, apierror.FromCode(openapi.ErrorCodeInternalError, "internal server error")
	}
	return &openapi.SlackSetup{
		AppId:         appRef,
		SetupRevision: app.SetupRevision,
		Provider:      appstore.AppProviderSlack,
		FlowId:        publicFlowID,
		SlackAppId:    manifestApp.AppID,
		OauthUrl:      installURL,
		RedirectUri:   redirectURI,
		EventsUrl:     eventsURL,
		ActionsUrl:    actionsURL,
		ExpiresAt:     expiresAt,
	}, nil
}
