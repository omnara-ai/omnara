package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/apps"
	"github.com/omnara-ai/omnara/internal/apps/github"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const GitHubEventsPath = "/api/integrations/github/{app_id}/events"
const GitHubSharedEventsPath = "/api/integrations/github/events"

// Leave headroom under GitHub's 10s deadline:
// https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks#respond-within-10-seconds
const githubIntakeTimeout = 5 * time.Second
const githubWebhookCredentialLimit = appstore.GitHubWebhookCredentialLimit

type githubIntakeStore interface {
	ListProjectAppsByProviderIdentity(
		context.Context, string, string, string, uuid.UUID, int,
	) ([]appstore.ProjectAppRecord, error)
	AcceptAppReceipt(context.Context, appstore.VerifiedAppReceipt) (
		appstore.AppInboxRecord, bool, error,
	)
}

type githubIntakeSecrets interface {
	ReadProjectAvailableSecretPayload(context.Context, secretstore.ReadProjectAvailableSecretPayloadInput) (
		secretstore.SecretPayloadRecord, error,
	)
}

type GitHubWebhookCredentialApps func(
	context.Context, string, int,
) ([]appstore.ProjectAppRecord, error)

func (s *Server) GitHubEventsHandler() http.Handler {
	if s.store == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "GitHub intake unavailable")
		})
	}
	return &githubIntakeHandler{
		store: s.store.Apps(), secrets: s.store.Secrets(),
		credentialApps: s.store.Apps().ListGitHubWebhookCredentialApps,
	}
}

type githubIntakeHandler struct {
	store          githubIntakeStore
	secrets        githubIntakeSecrets
	credentialApps GitHubWebhookCredentialApps
}

func (h *githubIntakeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), githubIntakeTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	appID := r.PathValue("app_id")
	if appID == "" && r.URL.Path == GitHubSharedEventsPath {
		values := r.Header.Values("X-Github-Hook-Installation-Target-Id")
		if len(values) != 1 {
			apierror.Write(w, openapi.ErrorCodeInvalidRequest, "GitHub App lookup hint is required")
			return
		}
		appID = values[0]
	}
	parsedAppID, err := strconv.ParseInt(appID, 10, 64)
	if err != nil || parsedAppID <= 0 || strconv.FormatInt(parsedAppID, 10) != appID {
		apierror.Write(w, openapi.ErrorCodeNotFound)
		return
	}
	raw, ok := readAppCallbackBody(w, r, appstore.AppInboxMaxPayloadBytes)
	if !ok {
		return
	}
	var hint github.Webhook
	if !utf8.Valid(raw) || !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") || json.Unmarshal(raw, &hint) != nil {
		apierror.Write(w, openapi.ErrorCodeInvalidRequest, "invalid GitHub webhook")
		return
	}
	if hint.Installation.ID < 0 || (hint.Installation.AppID != 0 && hint.Installation.AppID != parsedAppID) {
		apierror.Write(w, openapi.ErrorCodeForbidden, "GitHub installation identity mismatch")
		return
	}
	var result appFanoutResult
	var invalidWebhook bool
	if hint.Installation.ID > 0 {
		result, err = fanoutApps(ctx, h.store.ListProjectAppsByProviderIdentity,
			appstore.AppProviderGitHub, appID, strconv.FormatInt(hint.Installation.ID, 10),
			func(ctx context.Context, app appstore.ProjectAppRecord) (bool, error) {
				event, verified, err := h.verifyAppCredential(ctx, r.Header, raw, appID, app)
				if verified && err != nil {
					invalidWebhook = true
					return true, storeerr.InvalidRequest(err)
				}
				if err != nil || !verified {
					return verified, err
				}
				if !apps.GitHubWebhookInstallationMatches(app, event) {
					return true, storeerr.ErrUnauthorized
				}
				_, _, err = h.store.AcceptAppReceipt(ctx, appstore.VerifiedAppReceipt{
					ProjectID: app.ProjectID, AppID: app.ID,
					ReceiptKey: "github:" + event.EventType + ":" + event.DeliveryID, Payload: raw,
				})
				return true, err
			})
		if err != nil {
			apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "GitHub intake unavailable")
			return
		}
	}
	if invalidWebhook {
		apierror.Write(w, openapi.ErrorCodeInvalidRequest, "invalid GitHub webhook")
		return
	}
	if result.Matched > 0 {
		// Fallback would authorize a known installation through another project's credential grant.
		if result.Verified == 0 {
			apierror.Write(w, openapi.ErrorCodeUnauthorized, "invalid GitHub callback")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	event, verified, err := h.verifyAppWebhook(ctx, r.Header, raw, appID)
	if err != nil {
		if verified {
			apierror.Write(w, openapi.ErrorCodeInvalidRequest, "invalid GitHub webhook")
		} else {
			apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "GitHub credentials unavailable")
		}
		return
	}
	if !verified {
		apierror.Write(w, openapi.ErrorCodeUnauthorized, "invalid GitHub callback")
		return
	}
	if event.Installation.ID == 0 {
		var ping struct {
			Zen  string `json:"zen"`
			Hook struct {
				ID int64 `json:"id"`
			} `json:"hook"`
		}
		if json.Unmarshal(raw, &ping) != nil || event.EventType != "ping" || ping.Zen == "" || ping.Hook.ID <= 0 ||
			event.Action != "" || event.Repository.ID != 0 ||
			event.PullRequest != nil || event.Issue != nil || event.Comment != nil {
			apierror.Write(w, openapi.ErrorCodeInvalidRequest, "GitHub event requires an installation")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *githubIntakeHandler) verifyAppCredential(
	ctx context.Context, header http.Header, raw []byte, appID string, app appstore.ProjectAppRecord,
) (github.Webhook, bool, error) {
	if app.Provider != appstore.AppProviderGitHub || app.ProviderTenantID != appID ||
		app.CredentialSecretID == uuid.Nil {
		return github.Webhook{}, false, nil
	}
	credential, err := h.secrets.ReadProjectAvailableSecretPayload(ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
		OrgID: app.OrgID, ProjectID: app.ProjectID, SecretID: app.CredentialSecretID, Kind: secrets.KindGitHubAppCredentials,
	})
	if err != nil {
		return github.Webhook{}, false, err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(credential.Payload[secrets.KeyAppID]), 10, 64)
	if err != nil {
		return github.Webhook{}, false, storeerr.InvalidRequest(fmt.Errorf("invalid GitHub credential App ID: %w", err))
	}
	if strconv.FormatInt(id, 10) != appID ||
		!github.ValidSignature(header, raw, credential.Payload[secrets.KeyWebhookSecret]) {
		return github.Webhook{}, false, nil
	}
	event, err := github.DecodeWebhook(header, raw, credential.Payload[secrets.KeyWebhookSecret])
	return event, true, err
}

func (h *githubIntakeHandler) verifyAppWebhook(
	ctx context.Context, header http.Header, raw []byte, appID string,
) (github.Webhook, bool, error) {
	if h.credentialApps == nil {
		return github.Webhook{}, false, fmt.Errorf("github App credential resolver is required")
	}
	candidates, err := h.credentialApps(ctx, appID, githubWebhookCredentialLimit)
	if err != nil {
		return github.Webhook{}, false, err
	}
	if len(candidates) > githubWebhookCredentialLimit {
		return github.Webhook{}, false, fmt.Errorf("github App credential candidate limit exceeded")
	}
	var retryErr error
	for _, app := range candidates {
		event, verified, err := h.verifyAppCredential(ctx, header, raw, appID, app)
		if verified {
			return event, true, err
		}
		if err != nil && !permanentAppIngressError(err) {
			retryErr = err
		}
	}
	return github.Webhook{}, false, retryErr
}
