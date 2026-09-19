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
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const GitHubEventsPath = "/api/integrations/github/{app_id}/events"

const githubIntakeTimeout = 5 * time.Second
const githubWebhookCredentialLimit = integrationstore.GitHubWebhookCredentialLimit

type githubIntakeStore interface {
	GetIntegrationConnectionByProviderAccount(context.Context, string, string, string) (
		integrationstore.IntegrationConnectionRecord, error,
	)
	AcceptIntegrationReceipt(context.Context, integrationstore.VerifiedIntegrationReceipt) (
		integrationstore.IntegrationInboxRecord, bool, error,
	)
}

type githubIntakeSecrets interface {
	ReadProjectAvailableSecretPayload(context.Context, secretstore.ReadProjectAvailableSecretPayloadInput) (
		secretstore.SecretPayloadRecord, error,
	)
}

// GitHubWebhookCredentialConnections reads bounded credential candidates from
// existing connections for an App, without tying its URL to one installation.
// The store should deduplicate credential references and select representatives
// with live project secret access. Disabled installations may supply credentials
// for App-level verification; they never receive input. Return at most limit.
type GitHubWebhookCredentialConnections func(
	context.Context, string, int,
) ([]integrationstore.IntegrationConnectionRecord, error)

// GitHubEventsHandler serves the provider-signed POST GitHubEventsPath. Known
// active installation events require durable acceptance. Verified App pings and
// unmanaged/disabled installation events acknowledge without agent input.
// Ping/bootstrap verification resolves credentials from existing connections;
// normal events resolve directly by (github, App ID, installation ID).
func (s *Server) GitHubEventsHandler() http.Handler {
	if s.store == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "GitHub intake unavailable")
		})
	}
	return &githubIntakeHandler{
		store: s.store.Integrations(), secrets: s.store.Secrets(),
		appConnections: s.store.Integrations().ListGitHubWebhookCredentialConnections,
	}
}

type githubIntakeHandler struct {
	store          githubIntakeStore
	secrets        githubIntakeSecrets
	appConnections GitHubWebhookCredentialConnections
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
	parsedAppID, err := strconv.ParseInt(appID, 10, 64)
	if err != nil || parsedAppID <= 0 || strconv.FormatInt(parsedAppID, 10) != appID {
		apierror.Write(w, openapi.ErrorCodeNotFound)
		return
	}
	raw, ok := readIntegrationCallbackBody(w, r, integrationstore.IntegrationInboxMaxPayloadBytes)
	if !ok {
		return
	}
	// Body identity is only a bounded credential-lookup hint until HMAC passes.
	var hint github.Webhook
	if !utf8.Valid(raw) || !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") || json.Unmarshal(raw, &hint) != nil {
		apierror.Write(w, openapi.ErrorCodeInvalidRequest, "invalid GitHub webhook")
		return
	}
	if hint.Installation.ID < 0 || (hint.Installation.AppID != 0 && hint.Installation.AppID != parsedAppID) {
		apierror.Write(w, openapi.ErrorCodeForbidden, "GitHub installation identity mismatch")
		return
	}
	var connection integrationstore.IntegrationConnectionRecord
	known := false
	if hint.Installation.ID > 0 {
		connection, err = h.store.GetIntegrationConnectionByProviderAccount(ctx,
			integrationstore.IntegrationProviderGitHub, appID, strconv.FormatInt(hint.Installation.ID, 10))
		if err != nil && !storeerr.IsNotFound(err) {
			apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "GitHub intake unavailable")
			return
		}
		known = err == nil
		if known && !integration.GitHubWebhookInstallationMatches(connection, hint) {
			apierror.Write(w, openapi.ErrorCodeForbidden, "GitHub installation identity mismatch")
			return
		}
	}
	event, verified, err := h.verifyAppWebhook(ctx, r.Header, raw, appID, connection, known)
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
		// Ping is an App health probe, not a project receipt. Header relabeling
		// cannot discard a routable payload: its signed installation is present.
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
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !known || connection.State != integrationstore.IntegrationConnectionStateActive {
		// A physical App may have installations not connected here. Verification
		// does not provision one or forward its events to a different project.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Headers are diagnostic receipt identity only. Normalization/semantic
	// deduplication use signed body facts, so relabeling a replay grants nothing.
	_, _, err = h.store.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: connection.ProjectID, ConnectionID: connection.ID,
		ReceiptKey: "github:" + event.EventType + ":" + event.DeliveryID,
		Payload:    raw,
	})
	if err != nil {
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "GitHub receipt was not accepted")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *githubIntakeHandler) verifyAppWebhook(
	ctx context.Context, header http.Header, raw []byte, appID string,
	connection integrationstore.IntegrationConnectionRecord, known bool,
) (github.Webhook, bool, error) {
	candidates := []integrationstore.IntegrationConnectionRecord{connection}
	if !known {
		if h.appConnections == nil {
			return github.Webhook{}, false, fmt.Errorf("github App credential resolver is required")
		}
		var err error
		candidates, err = h.appConnections(ctx, appID, githubWebhookCredentialLimit)
		if err != nil {
			return github.Webhook{}, false, err
		}
		if len(candidates) > githubWebhookCredentialLimit {
			return github.Webhook{}, false, fmt.Errorf("github App credential candidate limit exceeded")
		}
	}
	seen := map[uuid.UUID]bool{}
	var accessErr error
	for _, candidate := range candidates {
		if candidate.Provider != integrationstore.IntegrationProviderGitHub || candidate.ProviderTenantID != appID ||
			candidate.CredentialSecretID == uuid.Nil || seen[candidate.CredentialSecretID] {
			continue
		}
		input := secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID: candidate.OrgID, ProjectID: candidate.ProjectID, SecretID: candidate.CredentialSecretID,
			Kind: secrets.KindGitHubAppCredentials,
		}
		credential, err := h.secrets.ReadProjectAvailableSecretPayload(ctx, input)
		if err != nil {
			accessErr = err
			continue
		}
		seen[candidate.CredentialSecretID] = true
		id, err := strconv.ParseInt(strings.TrimSpace(credential.Payload[secrets.KeyAppID]), 10, 64)
		if err != nil || strconv.FormatInt(id, 10) != appID ||
			!github.ValidSignature(header, raw, credential.Payload[secrets.KeyWebhookSecret]) {
			continue
		}
		event, err := github.DecodeWebhook(header, raw, credential.Payload[secrets.KeyWebhookSecret])
		if err != nil {
			// Distinguish a verified body with malformed transport headers from
			// an invalid signature without exposing credential/store errors.
			return github.Webhook{}, true, err
		}
		return event, true, nil
	}
	return github.Webhook{}, false, accessErr
}
