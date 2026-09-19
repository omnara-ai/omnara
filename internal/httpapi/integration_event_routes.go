package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const integrationEventsPath = "/api/integrations/slack/events"

func (s *Server) integrationEventsRoute(w http.ResponseWriter, r *http.Request) {
	raw, ok := readIntegrationCallbackBody(w, r, slack.EventBodyMaxBytes)
	if !ok {
		return
	}
	envelope, err := slack.DecodeEventsEnvelope(raw)
	if err != nil {
		apierror.Write(w, openapi.ErrorCodeInvalidRequest, "invalid slack event payload")
		return
	}
	if challenge, ok := slack.URLVerificationChallenge(envelope); ok {
		writeJSON(w, http.StatusOK, map[string]string{"challenge": challenge})
		return
	}
	install, ok := s.verifySignedSlackCallback(w, r, raw, envelope.APIAppID, envelope.TeamID)
	if !ok {
		return
	}
	if !slack.EventCallbackEnvelope(envelope) {
		logent.IntegrationEvent(r.Context(), install, "ignored_envelope", envelope.Type)
		writeJSON(w, http.StatusOK, map[string]string{"ok": "ignored"})
		return
	}
	installIdentity, err := slack.ParseInstallIdentity(install.ProviderIdentity)
	if err != nil {
		writeIntegrationProviderError(w, err)
		return
	}
	identity := slack.Identity{
		AppID:       install.ProviderAccountRef,
		WorkspaceID: install.ProviderTenantID,
		BotUserID:   installIdentity.BotUserID,
	}
	if !slack.ValidateEnvelopeIdentity(identity, envelope) {
		apierror.Write(w, openapi.ErrorCodeForbidden, "invalid slack event identity")
		return
	}
	if envelope.EventID == "" {
		apierror.Write(w, openapi.ErrorCodeInvalidRequest, "missing slack event id")
		return
	}
	if slack.DisabledInstallEvent(installIdentity.BotUserID, envelope.Event) {
		if install.State == integrationstore.IntegrationConnectionStateActive {
			applied, err := s.store.Integrations().DisableIntegrationConnection(
				r.Context(),
				integrationstore.DisableIntegrationConnectionInput{
					ProjectID:           install.ProjectID,
					ID:                  install.ID,
					ExpectedOAuthFlowID: &install.LastOAuthFlowID,
				},
			)
			if err != nil {
				writeIntegrationProviderError(w, err)
				return
			}
			if !applied {
				logent.IntegrationEvent(r.Context(), install, "ignored_stale_install", envelope.Event.Type)
				writeJSON(w, http.StatusOK, map[string]string{"ok": "ignored"})
				return
			}
		}
		logent.IntegrationEvent(r.Context(), install, "disabled", envelope.Event.Type)
		writeJSON(w, http.StatusOK, map[string]string{"ok": "disabled"})
		return
	}
	if slack.IgnoredLifecycleEvent(installIdentity.BotUserID, envelope.Event) {
		logent.IntegrationEvent(r.Context(), install, "ignored_event", envelope.Event.Type)
		writeJSON(w, http.StatusOK, map[string]string{"ok": "ignored"})
		return
	}
	if !slack.ValidateRuntimeBotAuthorization(identity, envelope) {
		apierror.Write(w, openapi.ErrorCodeForbidden, "invalid slack event identity")
		return
	}
	if install.State != integrationstore.IntegrationConnectionStateActive {
		logent.IntegrationEvent(r.Context(), install, "ignored_disabled", envelope.Event.Type)
		writeJSON(w, http.StatusOK, map[string]string{"ok": "ignored"})
		return
	}
	if update, ok := slack.EventNameUpdate(envelope); ok {
		if err := s.applyIntegrationNameUpdate(r.Context(), install, update); err != nil {
			writeIntegrationProviderError(w, err)
			return
		}
		logent.IntegrationEvent(r.Context(), install, "name_updated", envelope.Event.Type)
		writeJSON(w, http.StatusOK, map[string]string{"ok": "updated"})
		return
	}
	if slack.RemoteUserEvent(install.ProviderTenantID, envelope.Event) ||
		slack.BotOrSelfEvent(installIdentity.BotUserID, envelope.Event) {
		logent.IntegrationEvent(r.Context(), install, "ignored_event", envelope.Event.Type)
		writeJSON(w, http.StatusOK, map[string]string{"ok": "ignored"})
		return
	}
	// Capture eligible human messages before routing. App/listener policy and
	// message/app_mention semantic deduplication belong to the inbox consumer.
	event := envelope.Event
	if (event.Type != "message" && event.Type != "app_mention") ||
		(event.Subtype != "" && event.Subtype != "file_share") || event.Channel == "" || event.TS == "" {
		logent.IntegrationEvent(r.Context(), install, "ignored_event", envelope.Event.Type)
		writeJSON(w, http.StatusOK, map[string]string{"ok": "ignored"})
		return
	}
	_, _, err = s.store.Integrations().AcceptIntegrationReceipt(
		r.Context(),
		integrationstore.VerifiedIntegrationReceipt{
			ProjectID: install.ProjectID, ConnectionID: install.ID,
			ReceiptKey: "slack:" + envelope.EventID, Payload: raw,
		},
	)
	if err != nil {
		writeIntegrationProviderError(w, err)
		return
	}
	logent.IntegrationEvent(r.Context(), install, "received", envelope.Event.Type)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "received"})
}

func (s *Server) applyIntegrationNameUpdate(
	ctx context.Context,
	install integrationstore.IntegrationConnectionRecord,
	update slack.NameUpdate,
) error {
	if update.ConversationID != "" {
		return s.store.Integrations().UpdateIntegrationTargetDisplayNamesByProviderRefPrefix(
			ctx,
			install.ProjectID,
			install.ID,
			update.ConversationID,
			update.DisplayName,
		)
	}
	return s.store.Execution().UpdateActorDisplayName(
		ctx,
		executionstore.UpdateActorDisplayNameInput{
			ProjectID:        install.ProjectID,
			Provider:         install.Provider,
			ProviderTenantID: install.ProviderTenantID,
			ProviderUserID:   update.UserID,
			DisplayName:      update.DisplayName,
		},
	)
}

func (s *Server) integrationSlackCredentials(
	ctx context.Context,
	install integrationstore.IntegrationConnectionRecord,
) (slack.AppCredentials, error) {
	credential, err := s.store.Secrets().ReadProjectAvailableSecretPayload(
		ctx,
		secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID:     install.OrgID,
			ProjectID: install.ProjectID,
			SecretID:  install.CredentialSecretID,
			Kind:      secrets.KindSlackAppCredentials,
		},
	)
	if err != nil {
		return slack.AppCredentials{}, err
	}
	credentials, err := slack.AppCredentialsFromPayload(credential.Payload)
	if err != nil {
		return slack.AppCredentials{}, fmt.Errorf("read slack integration credentials: %w", err)
	}
	return credentials, nil
}

func writeIntegrationProviderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storeerr.ErrNotFound):
		apierror.Write(w, openapi.ErrorCodeNotFound)
	case errors.Is(err, storeerr.ErrUnauthorized):
		apierror.Write(w, openapi.ErrorCodeForbidden)
	case errors.Is(err, storeerr.ErrConflict), errors.Is(err, storeerr.ErrIdempotencyConflict):
		apierror.Write(w, openapi.ErrorCodeConflict)
	default:
		apierror.Write(w, openapi.ErrorCodeInternalError)
	}
}
