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
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	integrationruntime "github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const GitHubEventsPath = "/api/integrations/github/{app_id}/events"
const GitHubSharedEventsPath = "/api/integrations/github/events"

// Leave headroom under GitHub's 10s deadline:
// https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks#respond-within-10-seconds
const githubIntakeTimeout = 5 * time.Second
const githubWebhookCredentialLimit = integrationstore.GitHubWebhookCredentialLimit

type githubIntakeStore interface {
	ListProjectIntegrationsByProviderIdentity(
		context.Context, string, string, string, uuid.UUID, int,
	) ([]integrationstore.ProjectIntegrationRecord, error)
	AcceptIntegrationReceipt(context.Context, integrationstore.VerifiedIntegrationReceipt) (
		integrationstore.IntegrationInboxRecord, bool, error,
	)
}

type githubIntakeSecrets interface {
	ReadProjectAvailableSecretPayload(context.Context, secretstore.ReadProjectAvailableSecretPayloadInput) (
		secretstore.SecretPayloadRecord, error,
	)
}

type GitHubWebhookCredentialIntegrations func(
	context.Context, string, int,
) ([]integrationstore.ProjectIntegrationRecord, error)

func (s *Server) GitHubEventsHandler() http.Handler {
	if s.store == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "GitHub intake unavailable")
		})
	}
	return &githubIntakeHandler{
		store: s.store.Integrations(), secrets: s.store.Secrets(),
		credentialIntegrations: s.store.Integrations().ListGitHubWebhookCredentialIntegrations,
	}
}

type githubIntakeHandler struct {
	store                  githubIntakeStore
	secrets                githubIntakeSecrets
	credentialIntegrations GitHubWebhookCredentialIntegrations
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
	raw, ok := readIntegrationCallbackBody(w, r, integrationstore.IntegrationInboxMaxPayloadBytes)
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
	var result integrationFanoutResult
	var invalidWebhook bool
	if hint.Installation.ID > 0 {
		result, err = fanoutIntegrations(ctx, h.store.ListProjectIntegrationsByProviderIdentity,
			integrationstore.IntegrationProviderGitHub, appID, strconv.FormatInt(hint.Installation.ID, 10),
			func(ctx context.Context, integration integrationstore.ProjectIntegrationRecord) (bool, error) {
				event, verified, err := h.verifyIntegrationCredential(ctx, r.Header, raw, appID, integration)
				if verified && err != nil {
					invalidWebhook = true
					return true, storeerr.InvalidRequest(err)
				}
				if err != nil || !verified {
					return verified, err
				}
				if !integrationruntime.GitHubWebhookInstallationMatches(integration, event) {
					return true, storeerr.ErrUnauthorized
				}
				_, _, err = h.store.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
					ProjectID: integration.ProjectID, IntegrationID: integration.ID,
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
	event, verified, err := h.verifyIntegrationWebhook(ctx, r.Header, raw, appID)
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

func (h *githubIntakeHandler) verifyIntegrationCredential(
	ctx context.Context,
	header http.Header,
	raw []byte,
	appID string,
	integration integrationstore.ProjectIntegrationRecord,
) (github.Webhook, bool, error) {
	if integration.Provider != integrationstore.IntegrationProviderGitHub || integration.ProviderTenantID != appID ||
		integration.CredentialSecretID == uuid.Nil {
		return github.Webhook{}, false, nil
	}
	credential, err := h.secrets.ReadProjectAvailableSecretPayload(ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
		OrgID:     integration.OrgID,
		ProjectID: integration.ProjectID,
		SecretID:  integration.CredentialSecretID,
		Kind:      secrets.KindGitHubAppCredentials,
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

func (h *githubIntakeHandler) verifyIntegrationWebhook(
	ctx context.Context, header http.Header, raw []byte, appID string,
) (github.Webhook, bool, error) {
	if h.credentialIntegrations == nil {
		return github.Webhook{}, false, fmt.Errorf("github App credential resolver is required")
	}
	candidates, err := h.credentialIntegrations(ctx, appID, githubWebhookCredentialLimit)
	if err != nil {
		return github.Webhook{}, false, err
	}
	if len(candidates) > githubWebhookCredentialLimit {
		return github.Webhook{}, false, fmt.Errorf("github App credential candidate limit exceeded")
	}
	var retryErr error
	for _, integration := range candidates {
		event, verified, err := h.verifyIntegrationCredential(ctx, header, raw, appID, integration)
		if verified {
			return event, true, err
		}
		if err != nil && !permanentIntegrationIngressError(err) {
			retryErr = err
		}
	}
	return github.Webhook{}, false, retryErr
}
