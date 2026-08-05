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

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/httpapi/httpjson"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
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
	FlowID            storage.ID `json:"flow_id"`
	OrgID             storage.ID `json:"org_id"`
	ProjectID         storage.ID `json:"project_id"`
	AgentProfileID    storage.ID `json:"agent_profile_id"`
	InstalledByUserID storage.ID `json:"installed_by_user_id"`
	Provider          string     `json:"provider"`
	ClientID          string     `json:"client_id"`
	ClientSecret      string     `json:"client_secret"`
	SigningSecret     string     `json:"signing_secret"`
	BotDisplayName    string     `json:"bot_display_name,omitempty"`
	ExpiresAt         time.Time  `json:"expires_at"`
	ReturnTo          string     `json:"return_to,omitempty"`
}

func (s *Server) integrationOAuthCallbackRoute(w http.ResponseWriter, r *http.Request) {
	if s.store == nil || s.publicURL == "" || s.secretKeyWrapper == nil {
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "integration oauth is not configured")
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
	if principal.BrowserSessionID == storage.NilID {
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
	if providerError := r.URL.Query().Get("error"); providerError != "" {
		s.redirectOAuthOutcome(w, r, state.ReturnTo, url.Values{"integration_oauth_error": []string{providerError}})
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		s.redirectOAuthOutcome(w, r, state.ReturnTo, url.Values{"integration_oauth_error": []string{"missing_code"}})
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
		logent.AuthorizationCheckFailed(r.Context(), err)
		apierror.Write(w, openapi.ErrorCodeForbidden)
		return
	}
	if !allowed {
		apierror.Write(w, openapi.ErrorCodeForbidden)
		return
	}
	consumed, err := s.store.Integrations().IntegrationOAuthFlowConsumed(r.Context(), state.FlowID)
	if err != nil {
		logpkg.Error(r.Context(), fmt.Errorf("check integration oauth flow consumed: %w", err))
		apierror.Write(w, openapi.ErrorCodeInternalError)
		return
	}
	if consumed {
		apierror.Write(w, openapi.ErrorCodeUnauthorized, "integration oauth state already redeemed")
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
		s.redirectOAuthOutcome(w, r, state.ReturnTo, url.Values{"integration_oauth_error": []string{"exchange_failed"}})
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
		logpkg.Error(r.Context(), fmt.Errorf("integration oauth credential secret save failed: %w", err))
		s.redirectOAuthOutcome(
			w,
			r,
			state.ReturnTo,
			url.Values{"integration_oauth_error": []string{"secret_save_failed"}},
		)
		return
	}
	install, err := s.store.Integrations().UpsertIntegrationInstall(
		r.Context(),
		integrationstore.UpsertIntegrationInstallInput{
			OrgID:                    state.OrgID,
			ProjectID:                state.ProjectID,
			AgentProfileID:           state.AgentProfileID,
			InstalledByUserID:        state.InstalledByUserID,
			Provider:                 state.Provider,
			IntegrationKind:          slack.IntegrationKindAgentProfile,
			ConnectionMode:           slack.ConnectionModeWebhook,
			State:                    integrationstore.IntegrationInstallStateActive,
			ProviderTenantID:         providerInstall.ProviderTenantID,
			ProviderAccountRef:       providerInstall.ProviderAccountRef,
			ProviderAgentDisplayName: providerInstall.ProviderAgentDisplayName,
			CredentialSecretID:       credentialSecret.ID,
			ProviderIdentity:         providerInstall.ProviderIdentity,
			ProviderMetadata:         providerInstall.ProviderMetadata,
			OAuthFlowID:              state.FlowID,
		},
	)
	if err != nil {
		s.cleanupIntegrationOAuthSecret(
			r.Context(),
			state.OrgID,
			principal,
			credentialSecret.ID,
		)
		if errors.Is(err, storeerr.ErrIntegrationOAuthFlowConsumed) {
			apierror.Write(w, openapi.ErrorCodeUnauthorized, "integration oauth state already redeemed")
			return
		}
		if errors.Is(err, storeerr.ErrConflict) {
			s.redirectOAuthOutcome(
				w,
				r,
				state.ReturnTo,
				url.Values{"integration_oauth_error": []string{"already_connected"}},
			)
			return
		}
		logpkg.Error(r.Context(), fmt.Errorf("integration oauth install save failed: %w", err))
		s.redirectOAuthOutcome(
			w,
			r,
			state.ReturnTo,
			url.Values{"integration_oauth_error": []string{"install_save_failed"}},
		)
		return
	}
	logent.IntegrationInstall(r.Context(), install)
	s.redirectOAuthOutcome(w, r, state.ReturnTo, url.Values{
		"integration_oauth": []string{"success"},
	})
}

func (s *Server) createSlackIntegrationCredentialSecret(
	ctx context.Context,
	orgID, projectID storage.ID,
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
	orgID storage.ID,
	actor identitystore.PrincipalRecord,
	secretID storage.ID,
) {
	if secretID == storage.NilID {
		return
	}
	if _, err := s.store.Secrets().DeleteSecret(
		ctx,
		secretstore.DeleteSecretInput{OrgID: orgID, SecretID: secretID, Actor: actor},
	); err != nil &&
		!storeerr.IsNotFound(err) {
		logpkg.Error(ctx, fmt.Errorf("delete failed integration oauth secret: %w", err))
	}
}

func agentConfigHasIntegrationSendTool(config executionstore.AgentConfigRecord) bool {
	contract, err := agentconfig.RuntimeContractFromCompiled(
		config.CompiledDefinition,
		config.CompilerVersion,
		config.EffectiveDefinitionHash,
	)
	if err != nil {
		return false
	}
	for _, tool := range contract.Tools {
		if tool.Name == toolcatalog.ToolNameSendIntegrationMessage {
			return true
		}
	}
	return false
}

func validateIntegrationOAuthState(state integrationOAuthState, now time.Time) error {
	if !supportedIntegrationOAuthProvider(state.Provider) || state.ClientID == "" || state.ClientSecret == "" ||
		state.SigningSecret == "" ||
		state.ExpiresAt.IsZero() ||
		now.After(state.ExpiresAt) {
		return errors.New("invalid oauth state")
	}
	if state.FlowID == storage.NilID || state.OrgID == storage.NilID || state.ProjectID == storage.NilID ||
		state.AgentProfileID == storage.NilID ||
		state.InstalledByUserID == storage.NilID {
		return errors.New("invalid oauth state")
	}
	return nil
}

func (s *Server) encodeIntegrationOAuthState(ctx context.Context, state integrationOAuthState) (string, error) {
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

func (s *Server) decodeIntegrationOAuthState(ctx context.Context, token string) (integrationOAuthState, error) {
	if s.secretKeyWrapper == nil {
		return integrationOAuthState{}, errors.New("secret key wrapper is required")
	}
	plaintext, err := secrets.OpenToken(ctx, s.secretKeyWrapper, integrationOAuthStatePurpose, token)
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
