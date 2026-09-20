package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/httpapi/httpjson"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	integrationOAuthStateTTL     = 10 * time.Minute
	integrationOAuthTimeout      = 30 * time.Second
	integrationOAuthStateBytes   = 4096
	integrationOAuthCallbackPath = "/api/integrations/oauth/callback"
	integrationOAuthStatePurpose = "integration-oauth-state"
)

var errIntegrationOAuthStateTooLarge = errors.New("integration oauth state exceeds maximum size")

type integrationOAuthState struct {
	FlowID            uuid.UUID `json:"flow_id"`
	OrgID             uuid.UUID `json:"org_id"`
	ProjectID         uuid.UUID `json:"project_id"`
	AppID             uuid.UUID `json:"app_id"`
	SetupRevision     int64     `json:"setup_revision"`
	InstalledByUserID uuid.UUID `json:"installed_by_user_id"`
	Provider          string    `json:"provider"`
	ClientID          string    `json:"client_id"`
	ClientSecret      string    `json:"client_secret"`
	SigningSecret     string    `json:"signing_secret"`
	BotDisplayName    string    `json:"bot_display_name,omitempty"`
	ExpiresAt         time.Time `json:"expires_at"`
	ReturnTo          string    `json:"return_to,omitempty"`
}

func (s *Server) integrationOAuthCallbackRoute(w http.ResponseWriter, r *http.Request) {
	if s.store == nil || s.publicURL == "" || s.secretKeyWrapper == nil {
		apierror.Write(
			w,
			openapi.ErrorCodeServiceUnavailable,
			"integration oauth is not configured",
		)
		return
	}
	stateToken := strings.TrimSpace(r.URL.Query().Get("state"))
	if stateToken == "" {
		apierror.Write(w, openapi.ErrorCodeInvalidRequest, "state is required")
		return
	}
	principal, ok := principalFromContext(r.Context())
	if !ok || principal.Type != identitystore.PrincipalTypeUser {
		apierror.Write(w, openapi.ErrorCodeForbidden)
		return
	}
	if principal.BrowserSessionID == uuid.Nil {
		apierror.Write(w, openapi.ErrorCodeUnauthorized, "browser session required")
		return
	}
	state, err := s.decodeIntegrationOAuthState(r.Context(), stateToken)
	if err != nil {
		apierror.Write(w, openapi.ErrorCodeUnauthorized, "invalid oauth state")
		return
	}
	if err := validateIntegrationOAuthState(state, time.Now().UTC()); err != nil {
		apierror.Write(w, openapi.ErrorCodeUnauthorized, "invalid oauth state")
		return
	}
	if principal.ID != state.InstalledByUserID {
		apierror.Write(w, openapi.ErrorCodeForbidden, "oauth state belongs to another user")
		return
	}
	allowed, err := s.store.Identity().AuthorizeProject(
		r.Context(),
		identitystore.AuthorizeProjectInput{
			Principal: principal,
			OrgID:     state.OrgID,
			ProjectID: state.ProjectID,
			Action:    identitystore.ProjectActionManage,
		},
	)
	if err != nil {
		apierror.WriteError(w, authorizationAPIError(r.Context(), err))
		return
	}
	if !allowed {
		apierror.Write(w, openapi.ErrorCodeForbidden)
		return
	}
	app, err := s.store.Integrations().GetProjectApp(r.Context(), state.ProjectID, state.AppID)
	if storeerr.IsNotFound(err) {
		s.redirectOAuthOutcome(w, r, state.ReturnTo, url.Values{"integration_oauth_error": {"app_deleted"}})
		return
	}
	if err != nil {
		apierror.WriteError(w, apierror.ProjectScoped(err))
		return
	}
	if app.OrgID != state.OrgID || app.Provider != state.Provider {
		apierror.Write(w, openapi.ErrorCodeUnauthorized, "invalid oauth state")
		return
	}
	if app.SetupRevision != state.SetupRevision {
		s.redirectOAuthOutcome(w, r, state.ReturnTo, url.Values{"integration_oauth_error": {"app_setup_changed"}})
		return
	}
	if providerError := r.URL.Query().Get("error"); providerError != "" {
		s.redirectOAuthOutcome(
			w,
			r,
			state.ReturnTo,
			url.Values{"integration_oauth_error": []string{providerError}},
		)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		s.redirectOAuthOutcome(
			w,
			r,
			state.ReturnTo,
			url.Values{"integration_oauth_error": []string{"missing_code"}},
		)
		return
	}
	consumed, err := s.store.Integrations().IntegrationOAuthFlowConsumed(r.Context(), state.FlowID)
	if err != nil {
		logpkg.Error(r.Context(), fmt.Errorf("check integration oauth flow consumed: %w", err))
		apierror.Write(w, openapi.ErrorCodeInternalError)
		return
	}
	if consumed {
		s.redirectOAuthOutcome(w, r, state.ReturnTo, url.Values{"integration_oauth_error": {"flow_consumed"}})
		return
	}
	redirectURI := s.absolutePublicURL(integrationOAuthCallbackPath)
	outboundCtx, cancel := context.WithTimeout(r.Context(), integrationOAuthTimeout)
	defer cancel()
	providerInstall, err := s.completeIntegrationOAuth(outboundCtx, state, code, redirectURI)
	if err != nil {
		if errors.Is(err, errIntegrationOAuthMissingScope) {
			s.redirectOAuthOutcome(
				w,
				r,
				state.ReturnTo,
				url.Values{"integration_oauth_error": []string{"missing_scope"}},
			)
			return
		}
		logpkg.Error(r.Context(), fmt.Errorf("integration oauth code exchange failed: %w", err))
		s.redirectOAuthOutcome(
			w,
			r,
			state.ReturnTo,
			url.Values{"integration_oauth_error": []string{"exchange_failed"}},
		)
		return
	}
	var observed slack.InstallIdentity
	_ = json.Unmarshal(app.ProviderIdentity, &observed)
	verified, err := slack.ParseInstallIdentity(providerInstall.ProviderIdentity)
	if err != nil || (observed.BotUserID != "" && observed.BotUserID != verified.BotUserID) {
		s.redirectOAuthOutcome(
			w,
			r,
			state.ReturnTo,
			url.Values{"integration_oauth_error": {"setup_save_failed"}},
		)
		return
	}
	credentialSecret, err := s.createSlackIntegrationCredentialSecret(
		r.Context(),
		state.OrgID,
		state.ProjectID,
		principal,
		providerInstall.CredentialPayload,
	)
	if err != nil {
		logpkg.Error(
			r.Context(),
			fmt.Errorf("integration oauth credential secret save failed: %w", err),
		)
		s.redirectOAuthOutcome(
			w,
			r,
			state.ReturnTo,
			url.Values{"integration_oauth_error": []string{"secret_save_failed"}},
		)
		return
	}
	install, err := s.store.Integrations().
		ConfigureProjectApp(r.Context(), integrationstore.ConfigureProjectAppInput{
			OrgID:                    state.OrgID,
			ProjectID:                state.ProjectID,
			AppID:                    state.AppID,
			ExpectedSetupRevision:    state.SetupRevision,
			InstalledByUserID:        state.InstalledByUserID,
			Provider:                 app.Provider,
			ProviderTenantID:         providerInstall.ProviderTenantID,
			ProviderAccountRef:       providerInstall.ProviderAccountRef,
			ProviderAgentDisplayName: providerInstall.ProviderAgentDisplayName,
			CredentialSecretID:       credentialSecret.ID,
			CredentialVersionID:      credentialSecret.CurrentVersionID,
			ProviderIdentity:         providerInstall.ProviderIdentity,
			ProviderMetadata:         providerInstall.ProviderMetadata,
			OAuthFlowID:              state.FlowID,
		})
	if err != nil {
		s.cleanupIntegrationOAuthSecret(
			r.Context(),
			state.OrgID,
			principal,
			credentialSecret.ID,
		)
		var outcome string
		switch {
		case errors.Is(err, storeerr.ErrIntegrationOAuthFlowConsumed):
			outcome = "flow_consumed"
		case errors.Is(err, integrationstore.ErrProjectAppSetupChanged):
			outcome = "app_setup_changed"
		case storeerr.IsNotFound(err):
			// The app may have been deleted while Slack exchanged the code.
			_, appErr := s.store.Integrations().GetProjectApp(r.Context(), state.ProjectID, state.AppID)
			if storeerr.IsNotFound(appErr) {
				outcome = "app_deleted"
			}
		}
		if outcome != "" {
			s.redirectOAuthOutcome(w, r, state.ReturnTo, url.Values{"integration_oauth_error": {outcome}})
			return
		}
		// A concurrent setup, deletion, or identity mismatch rejects this flow.
		// Only its newly created credential is cleaned up; the saved app remains intact.
		logpkg.Error(r.Context(), fmt.Errorf("integration oauth setup save failed: %w", err))
		s.redirectOAuthOutcome(
			w,
			r,
			state.ReturnTo,
			url.Values{"integration_oauth_error": []string{"setup_save_failed"}},
		)
		return
	}
	appID, err := publicID(publicid.KindProjectApp, install.ID)
	if err != nil {
		apierror.Write(w, openapi.ErrorCodeInternalError)
		return
	}
	outcome := url.Values{"integration_oauth": {"success"}, "app_id": {appID}}
	s.redirectOAuthOutcome(w, r, state.ReturnTo, outcome)
}

func (s *Server) createSlackIntegrationCredentialSecret(
	ctx context.Context,
	orgID, projectID uuid.UUID,
	actor identitystore.PrincipalRecord,
	payload secrets.Payload,
) (secretstore.SecretRecord, error) {
	suffix, err := httpauth.RandomURLToken(6)
	if err != nil {
		return secretstore.SecretRecord{}, err
	}
	secret, _, err := s.store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          orgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: projectID,
		Name:           "slack-credentials-" + suffix,
		Material:       secrets.SlackAppCredentialsMaterialFromPayload(payload),
		Actor:          actor,
	})
	return secret, err
}

func (s *Server) cleanupIntegrationOAuthSecret(
	ctx context.Context,
	orgID uuid.UUID,
	actor identitystore.PrincipalRecord,
	secretID uuid.UUID,
) {
	if secretID == uuid.Nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := s.store.Secrets().DeleteSecret(
		cleanupCtx,
		secretstore.DeleteSecretInput{OrgID: orgID, SecretID: secretID, Actor: actor},
	); err != nil &&
		!storeerr.IsNotFound(err) {
		logpkg.Error(ctx, fmt.Errorf("delete failed integration oauth secret: %w", err))
	}
}

func validateIntegrationOAuthState(state integrationOAuthState, now time.Time) error {
	if state.AppID == uuid.Nil || state.SetupRevision < 1 {
		return errors.New("invalid oauth app setup scope")
	}
	if !supportedIntegrationOAuthProvider(state.Provider) || state.ClientID == "" ||
		state.ClientSecret == "" ||
		state.SigningSecret == "" ||
		state.ExpiresAt.IsZero() ||
		!now.Before(state.ExpiresAt) {
		return errors.New("invalid oauth state")
	}
	if state.FlowID == uuid.Nil || state.OrgID == uuid.Nil || state.ProjectID == uuid.Nil ||
		state.InstalledByUserID == uuid.Nil {
		return errors.New("invalid oauth state")
	}
	return nil
}

func (s *Server) encodeIntegrationOAuthState(
	ctx context.Context,
	state integrationOAuthState,
) (string, error) {
	if s.secretKeyWrapper == nil {
		return "", errors.New("secret key wrapper is required")
	}
	body, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	if len(body) > integrationOAuthStateBytes {
		return "", errIntegrationOAuthStateTooLarge
	}
	return secrets.SealToken(ctx, s.secretKeyWrapper, integrationOAuthStatePurpose, body)
}

func (s *Server) decodeIntegrationOAuthState(
	ctx context.Context,
	token string,
) (integrationOAuthState, error) {
	if s.secretKeyWrapper == nil {
		return integrationOAuthState{}, errors.New("secret key wrapper is required")
	}
	plaintext, err := secrets.OpenToken(
		ctx,
		s.secretKeyWrapper,
		integrationOAuthStatePurpose,
		token,
	)
	if err != nil {
		return integrationOAuthState{}, err
	}
	var state integrationOAuthState
	if err := httpjson.DecodeStrictRequiredBytes(plaintext, &state); err != nil {
		return integrationOAuthState{}, err
	}
	return state, nil
}

var errSlackSetupPublicURL = errors.New(
	"slack setup requires OMNARA_PUBLIC_URL to be a public HTTPS URL",
)

func validateSlackSetupPublicURL(raw string) error {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errSlackSetupPublicURL
	}
	if strings.ToLower(parsed.Scheme) != "https" {
		return errSlackSetupPublicURL
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if hostname == "" ||
		hostname == "localhost" ||
		strings.HasSuffix(hostname, ".localhost") ||
		strings.HasSuffix(hostname, ".local") {
		return errSlackSetupPublicURL
	}
	if ip := net.ParseIP(hostname); ip != nil && !ssrf.IsAllowedIP(ip, false) {
		return errSlackSetupPublicURL
	}
	return nil
}
