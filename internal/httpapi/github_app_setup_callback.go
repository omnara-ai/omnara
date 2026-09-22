package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/httpjson"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
)

const githubManifestStatePurpose = "github-manifest-registration"

func (s *Server) decodeGitHubManifestState(ctx context.Context, token string) (githubManifestState, error) {
	var state githubManifestState
	if len(token) == 0 || len(token) > 12*1024 {
		return state, errors.New("invalid GitHub registration state")
	}
	body, err := secrets.OpenToken(ctx, s.secretKeyWrapper, githubManifestStatePurpose, token)
	if err != nil {
		return state, err
	}
	if len(body) > integrationOAuthStateBytes {
		return state, errors.New("GitHub registration state is too large")
	}
	if err := httpjson.DecodeStrictRequiredBytes(body, &state); err != nil {
		return state, err
	}
	return state, state.validate(time.Now().UTC())
}

func (s *Server) githubManifestCallbackRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if s.store == nil {
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "GitHub setup is unavailable")
		return
	}
	if err := s.validateGitHubGuidedSetup(); err != nil {
		apierror.WriteError(w, err)
		return
	}
	principal, ok := principalFromContext(r.Context())
	if !ok || principal.Type != identitystore.PrincipalTypeUser || principal.BrowserSessionID == uuid.Nil {
		apierror.Write(w, openapi.ErrorCodeUnauthorized, "browser session required")
		return
	}
	query := r.URL.Query()
	if len(query["state"]) != 1 {
		apierror.Write(w, openapi.ErrorCodeUnauthorized, "invalid GitHub registration state")
		return
	}
	state, err := s.decodeGitHubManifestState(r.Context(), query.Get("state"))
	if err != nil {
		apierror.Write(w, openapi.ErrorCodeUnauthorized, "invalid GitHub registration state")
		return
	}
	if principal.ID != state.UserID {
		apierror.Write(w, openapi.ErrorCodeForbidden, "GitHub registration belongs to another user")
		return
	}
	allowed, err := s.store.Identity().AuthorizeProject(r.Context(), identitystore.AuthorizeProjectInput{
		Principal: principal, OrgID: state.OrgID, ProjectID: state.ProjectID, Action: identitystore.ProjectActionManage,
	})
	if err != nil {
		apierror.WriteError(w, authorizationAPIError(r.Context(), err))
		return
	}
	if !allowed {
		apierror.Write(w, openapi.ErrorCodeForbidden)
		return
	}
	app, err := s.store.Integrations().GetProjectApp(r.Context(), state.ProjectID, state.AppID)
	if err != nil {
		apierror.WriteError(w, apierror.ProjectScoped(err))
		return
	}
	if app.OrgID != state.OrgID {
		apierror.Write(w, openapi.ErrorCodeForbidden)
		return
	}
	if app.AppType != appdefinition.GitHubPR {
		apierror.Write(w, openapi.ErrorCodeInvalidRequest, "this app does not support GitHub setup")
		return
	}
	path, err := githubAppReturnPath(state.ProjectID, state.AppID)
	if err != nil {
		apierror.Write(w, openapi.ErrorCodeInternalError)
		return
	}
	outcome := func(reason string) {
		s.redirectOAuthOutcome(w, r, path, url.Values{"github_setup_error": {reason}})
	}
	if query.Has("error") {
		outcome("registration_denied")
		return
	}
	if len(query["code"]) != 1 || query.Get("code") == "" {
		outcome("missing_code")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), integrationOAuthTimeout)
	defer cancel()
	// Do not retry this one-time manifest conversion.
	client, err := github.NewSetupClient(github.SetupConfig{
		APIURL: s.githubClientConfig.APIURL, HTTPClient: s.githubClientConfig.HTTPClient,
		BeforeRequest: s.githubClientConfig.BeforeRequest,
	})
	if err != nil {
		outcome("conversion_failed")
		return
	}
	converted, err := client.ConvertManifest(ctx, query.Get("code"))
	if err != nil {
		outcome("conversion_failed")
		return
	}
	credentials := converted.Credentials
	secret, _, err := s.store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: state.OrgID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: state.ProjectID,
		Name:  "github-" + strconv.FormatInt(credentials.AppID, 10) + "-" + state.FlowID.String(),
		Actor: principal,
		Material: secrets.GitHubAppCredentialsMaterial{
			AppID: strconv.FormatInt(credentials.AppID, 10), PrivateKey: credentials.PrivateKeyPEM,
			WebhookSecret: credentials.WebhookSecret,
		},
	})
	if err != nil {
		outcome("secret_save_failed")
		return
	}
	// Keep credentials through canceled or pending installations; manifest conversion is one-time.
	ref, err := publicid.Encode(publicid.KindSecret, secret.ID)
	if err != nil {
		apierror.Write(w, openapi.ErrorCodeInternalError)
		return
	}
	params := url.Values{
		"github_setup": {"credentials_saved"}, "credentials_secret_ref": {ref},
	}
	current, err := s.store.Integrations().GetProjectApp(ctx, state.ProjectID, state.AppID)
	if err != nil || current.SetupRevision != state.SetupRevision {
		params.Set("github_setup_error", "app_setup_changed")
	}
	s.redirectOAuthOutcome(w, r, path, params)
}
