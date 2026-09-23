package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

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
	ctx, cancel := context.WithTimeout(r.Context(), integrationIntakeTimeout)
	defer cancel()
	r = r.WithContext(ctx)
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
	if s.store == nil {
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "store unavailable")
		return
	}
	response := ""
	var rejected error
	result, err := fanoutIntegrationApps(ctx, s.store.Integrations().ListProjectAppsForProviderEventVerification,
		integrationstore.IntegrationProviderSlack, envelope.TeamID, envelope.APIAppID,
		func(ctx context.Context, app integrationstore.ProjectAppRecord) (bool, error) {
			credentials, err := s.integrationSlackCredentials(ctx, app)
			if err != nil {
				return false, err
			}
			if !slack.ValidSignature(r.Header, raw, credentials.SigningSecret, time.Now().UTC()) {
				return false, nil
			}
			effect, err := s.acceptSlackEvent(ctx, app, envelope, raw)
			if err == nil {
				if response != "received" {
					response = effect
				}
			} else {
				rejected = err
			}
			return true, err
		})
	if err != nil {
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "Slack intake unavailable")
		return
	}
	if result.Verified == 0 {
		apierror.Write(w, openapi.ErrorCodeUnauthorized, "invalid signature")
		return
	}
	if response == "" {
		if errors.Is(rejected, storeerr.ErrInvalidRequest) {
			apierror.Write(w, openapi.ErrorCodeInvalidRequest, "invalid slack event")
		} else {
			apierror.Write(w, openapi.ErrorCodeForbidden, "invalid slack event identity")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": response})
}

func (s *Server) acceptSlackEvent(
	ctx context.Context, app integrationstore.ProjectAppRecord, envelope slack.EventsEnvelope, raw []byte,
) (string, error) {
	if !slack.EventCallbackEnvelope(envelope) {
		return "ignored", nil
	}
	identity, err := slack.ParseInstallIdentity(app.ProviderIdentity)
	if err != nil {
		return "", storeerr.InvalidRequest(err)
	}
	providerIdentity := slack.Identity{
		AppID: app.ProviderAccountRef, WorkspaceID: app.ProviderTenantID, BotUserID: identity.BotUserID,
	}
	if !slack.ValidateEnvelopeIdentity(providerIdentity, envelope) {
		return "", storeerr.ErrUnauthorized
	}
	if envelope.EventID == "" {
		return "", storeerr.InvalidRequest(errors.New("missing event id"))
	}
	if app.State != integrationstore.ProjectAppStateActive {
		return "ignored", nil
	}
	if slack.DisabledInstallEvent(identity.BotUserID, envelope.Event) {
		_, err := s.store.Integrations().DisconnectProjectApp(ctx, integrationstore.DisconnectProjectAppInput{
			ProjectID: app.ProjectID, AppID: app.ID, ExpectedSetupRevision: &app.SetupRevision,
		})
		return "disabled", err
	}
	if slack.IgnoredLifecycleEvent(identity.BotUserID, envelope.Event) {
		return "ignored", nil
	}
	if !slack.ValidateRuntimeBotAuthorization(providerIdentity, envelope) {
		return "", storeerr.ErrUnauthorized
	}
	if update, ok := slack.EventNameUpdate(envelope); ok {
		return "updated", s.applyIntegrationNameUpdate(ctx, app, update)
	}
	if slack.RemoteUserEvent(app.ProviderTenantID, envelope.Event) ||
		slack.BotOrSelfEvent(identity.BotUserID, envelope.Event) {
		return "ignored", nil
	}
	event := envelope.Event
	if !slack.ConversationalMessage(event) || event.Channel == "" || event.TS == "" {
		return "ignored", nil
	}
	_, _, err = s.store.Integrations().AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: app.ProjectID, AppID: app.ID,
		ReceiptKey: "slack:" + envelope.EventID, Payload: raw,
	})
	if err == nil {
		logent.IntegrationEvent(ctx, app, "received", envelope.Event.Type)
	}
	return "received", err
}

func (s *Server) applyIntegrationNameUpdate(
	ctx context.Context,
	install integrationstore.ProjectAppRecord,
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
	actor, err := executionstore.AppActorParams(install.ID, update.UserID, nil)
	if err != nil {
		return err
	}
	return s.store.Execution().UpdateActorDisplayName(
		ctx,
		executionstore.UpdateActorDisplayNameInput{
			ProjectID:        install.ProjectID,
			Provider:         actor.Provider,
			ProviderTenantID: actor.ProviderTenantID,
			ProviderUserID:   update.UserID,
			DisplayName:      update.DisplayName,
		},
	)
}

func (s *Server) integrationSlackCredentials(
	ctx context.Context,
	install integrationstore.ProjectAppRecord,
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
		return slack.AppCredentials{}, storeerr.InvalidRequest(fmt.Errorf("read slack app credentials: %w", err))
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
