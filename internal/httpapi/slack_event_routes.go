package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/omnara-ai/omnara/internal/integrationdefinition"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const slackEventsPath = "/api/integrations/slack/events"

func (s *Server) slackEventsRoute(w http.ResponseWriter, r *http.Request) {
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
	result, err := fanoutIntegrations(ctx, s.store.Integrations().ListIntegrationsForProviderEventVerification,
		integrationdefinition.ProviderSlack, envelope.TeamID, envelope.APIAppID,
		func(ctx context.Context, integration integrationstore.IntegrationRecord) (bool, error) {
			credentials, err := s.slackCredentials(ctx, integration)
			if err != nil {
				return false, err
			}
			if !slack.ValidSignature(r.Header, raw, credentials.SigningSecret, time.Now().UTC()) {
				return false, nil
			}
			effect, err := s.acceptSlackEvent(ctx, integration, envelope, raw)
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
	ctx context.Context, integration integrationstore.IntegrationRecord, envelope slack.EventsEnvelope, raw []byte,
) (string, error) {
	if !slack.EventCallbackEnvelope(envelope) {
		return "ignored", nil
	}
	identity, err := slack.ParseInstallIdentity(integration.ProviderIdentity)
	if err != nil {
		return "", storeerr.InvalidRequest(err)
	}
	providerIdentity := slack.Identity{
		AppID: integration.ProviderAccountRef, WorkspaceID: integration.ProviderTenantID, BotUserID: identity.BotUserID,
	}
	if !slack.ValidateEnvelopeIdentity(providerIdentity, envelope) {
		return "", storeerr.ErrUnauthorized
	}
	if envelope.EventID == "" {
		return "", storeerr.InvalidRequest(errors.New("missing event id"))
	}
	if integration.State != integrationstore.IntegrationStateActive {
		return "ignored", nil
	}
	if slack.DisabledInstallEvent(identity.BotUserID, envelope.Event) {
		_, err := s.store.Integrations().DisconnectIntegration(ctx, integrationstore.DisconnectIntegrationInput{
			ProjectID: integration.ProjectID, IntegrationID: integration.ID, ExpectedSetupRevision: &integration.SetupRevision,
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
		return "updated", s.applySlackNameUpdate(ctx, integration, update)
	}
	event := envelope.Event
	if slack.RemoteUserEvent(integration.ProviderTenantID, event) ||
		slack.BotOrSelfEvent(identity.BotUserID, event) ||
		!slack.ConversationalMessage(event) || event.Channel == "" || event.TS == "" {
		s.integrationInboxIntake.Record("slack", metrics.IntegrationInboxIntakeOutcomeFiltered)
		return "ignored", nil
	}
	_, created, err := s.store.Integrations().AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: integration.ProjectID, IntegrationID: integration.ID,
		ReceiptKey: "slack:" + envelope.EventID, Payload: raw,
	})
	if err != nil {
		s.integrationInboxIntake.Record("slack", metrics.IntegrationInboxIntakeOutcomeError)
		return "received", err
	}
	outcome := metrics.IntegrationInboxIntakeOutcomeAccepted
	if !created {
		outcome = metrics.IntegrationInboxIntakeOutcomeDuplicate
	}
	s.integrationInboxIntake.Record("slack", outcome)
	logent.IntegrationEvent(ctx, integration, "received", envelope.Event.Type)
	return "received", nil
}

func (s *Server) applySlackNameUpdate(
	ctx context.Context,
	install integrationstore.IntegrationRecord,
	update slack.NameUpdate,
) error {
	if update.ConversationID != "" {
		return s.store.Integrations().UpdateIntegrationTargetDisplayNamesByScopeRefPrefix(
			ctx,
			install.ProjectID,
			install.ID,
			update.ConversationID,
			update.DisplayName,
		)
	}
	actor, err := executionstore.IntegrationActorParams(install, update.UserID, nil)
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

func (s *Server) slackCredentials(
	ctx context.Context,
	install integrationstore.IntegrationRecord,
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
		return slack.AppCredentials{}, storeerr.InvalidRequest(fmt.Errorf("read Slack app credentials: %w", err))
	}
	return credentials, nil
}
