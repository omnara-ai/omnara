package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

const githubManifestCallbackPath = "/api/integrations/github/manifest/callback"

// Match GitHub's one-hour manifest conversion window.
const githubManifestStateTTL = time.Hour

var githubOrganizationLogin = regexp.MustCompile(`^[A-Za-z0-9]+(-[A-Za-z0-9]+)*$`)

func (s strictOpenAPIServer) CreateProjectIntegrationGitHubSetup(
	ctx context.Context,
	request openapi.CreateProjectIntegrationGitHubSetupRequestObject,
) (openapi.CreateProjectIntegrationGitHubSetupResponseObject, error) {
	if err := authorizeOperationPrincipal(ctx, principalKindBrowserSession); err != nil {
		return nil, err
	}
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	integration, err := s.projectIntegrationForSetup(ctx, scope, request.IntegrationID)
	if err != nil {
		return nil, err
	}
	if err := s.server.validateGitHubGuidedSetup(); err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	if err := validateGitHubRegistrationApp(integration, request.Body.ExpectedSetupRevision); err != nil {
		return nil, err
	}
	name := integration.Name
	if request.Body.AppName != nil {
		name = *request.Body.AppName
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 34 || strings.ContainsFunc(name, unicode.IsControl) {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "GitHub App name must contain 1 to 34 characters")
	}
	organization := strings.TrimSpace(stringValue(request.Body.Organization))
	if request.Body.Organization != nil && (len(organization) > 39 || !githubOrganizationLogin.MatchString(organization)) {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid GitHub organization login")
	}
	principal, _ := principalFromContext(ctx)
	flowID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	expires := time.Now().UTC().Add(githubManifestStateTTL)
	state := githubManifestState{
		FlowID: flowID, OrgID: integration.OrgID, ProjectID: integration.ProjectID, IntegrationID: integration.ID,
		UserID: principal.ID, SetupRevision: integration.SetupRevision, ExpiresAt: expires,
	}
	token, err := s.server.encodeGitHubManifestState(ctx, state)
	if err != nil {
		return nil, err
	}
	path, err := githubIntegrationReturnPath(integration.ProjectID, integration.ID)
	if err != nil {
		return nil, err
	}
	registration := "https://github.com/settings/apps/new"
	if organization != "" {
		registration = "https://github.com/organizations/" + organization + "/settings/apps/new"
	}
	webhookURL, err := s.server.githubManifestWebhookURL()
	if err != nil {
		return nil, err
	}
	manifest := map[string]any{
		"name":                     name,
		"url":                      s.server.absolutePublicURL(path),
		"hook_attributes":          map[string]any{"url": webhookURL, "active": true},
		"redirect_url":             s.server.absolutePublicURL(githubManifestCallbackPath),
		"setup_url":                s.server.absolutePublicURL(path),
		"public":                   false,
		"request_oauth_on_install": false,
		"setup_on_update":          false,
		"default_permissions":      map[string]string{"pull_requests": "write", "issues": "read"},
		"default_events": []string{
			"pull_request",
			"issue_comment",
			"pull_request_review",
			"pull_request_review_comment",
		},
	}
	return openapi.CreateProjectIntegrationGitHubSetup201JSONResponse(openapi.GitHubSetup{
		IntegrationId: request.IntegrationID, SetupRevision: integration.SetupRevision,
		RegistrationUrl: registration + "?" + url.Values{"state": {token}}.Encode(),
		Manifest:        manifest, ExpiresAt: expires,
	}), nil
}

func (s *Server) githubManifestWebhookURL() (string, error) {
	base := s.publicAPIURL
	if base == "" {
		base = s.publicURL
	}
	if validateSlackSetupPublicURL(base) != nil {
		return "", apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "GitHub webhooks require a public HTTPS API URL")
	}
	origin, err := parseConfiguredOrigin(base)
	if err != nil {
		return "", err
	}
	return origin.url + GitHubSharedEventsPath, nil
}

func (s *Server) validateGitHubGuidedSetup() error {
	if s.secretKeyWrapper == nil || validateSlackSetupPublicURL(s.publicURL) != nil {
		return apierror.FromCode(
			openapi.ErrorCodeServiceUnavailable,
			"GitHub setup requires a public HTTPS URL and secret encryption keys",
		)
	}
	origin, err := url.Parse(s.publicURL)
	if err != nil || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" ||
		(origin.Path != "" && origin.Path != "/") {
		return apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "GitHub setup requires a public HTTPS origin")
	}
	if api := strings.TrimRight(s.githubClientConfig.APIURL, "/"); api != "" && api != github.APIURL {
		return apierror.FromCode(
			openapi.ErrorCodeServiceUnavailable,
			"guided GitHub setup supports github.com only",
		)
	}
	return nil
}

func validateGitHubRegistrationApp(integration integrationstore.ProjectIntegrationRecord, revision int64) error {
	if integration.IntegrationType != integrationdefinition.GitHubPR {
		return apierror.FromCode(openapi.ErrorCodeInvalidRequest, "this integration does not support GitHub setup")
	}
	if integration.SetupRevision != revision {
		return apierror.ProjectScoped(integrationstore.ErrProjectIntegrationSetupChanged)
	}
	if integration.ProviderTenantID != "" || integration.State == integrationstore.ProjectIntegrationStateActive {
		return apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"use existing GitHub App credentials to reconnect this integration",
		)
	}
	return nil
}

func githubIntegrationReturnPath(projectID, integrationID uuid.UUID) (string, error) {
	project, err := publicid.Encode(publicid.KindProject, projectID)
	if err != nil {
		return "", err
	}
	integration, err := publicid.Encode(publicid.KindProjectIntegration, integrationID)
	if err != nil {
		return "", err
	}
	return "/projects/" + project + "/integrations/" + integration, nil
}

type githubManifestState struct {
	FlowID        uuid.UUID `json:"flow_id"`
	OrgID         uuid.UUID `json:"org_id"`
	ProjectID     uuid.UUID `json:"project_id"`
	IntegrationID uuid.UUID `json:"integration_id"`
	UserID        uuid.UUID `json:"user_id"`
	SetupRevision int64     `json:"setup_revision"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func (state githubManifestState) validate(now time.Time) error {
	if state.FlowID == uuid.Nil || state.OrgID == uuid.Nil || state.ProjectID == uuid.Nil ||
		state.IntegrationID == uuid.Nil || state.UserID == uuid.Nil || state.SetupRevision < 1 ||
		!now.Before(state.ExpiresAt) || state.ExpiresAt.After(now.Add(githubManifestStateTTL)) {
		return errors.New("invalid GitHub registration state")
	}
	return nil
}

func (s *Server) encodeGitHubManifestState(ctx context.Context, state githubManifestState) (string, error) {
	body, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	if len(body) > integrationOAuthStateBytes {
		return "", errors.New("GitHub registration state is too large")
	}
	return secrets.SealToken(ctx, s.secretKeyWrapper, githubManifestStatePurpose, body)
}
