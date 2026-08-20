package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	integrationEventsPath             = "/api/integrations/slack/events"
	integrationEventHTTPTimeout       = 2 * time.Second
	integrationEventEnrichmentTimeout = 1500 * time.Millisecond
	contentBlockMetadataValueMaxRunes = 512
)

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
		if install.State == integrationstore.IntegrationInstallStateActive {
			applied, err := s.store.Integrations().DisableIntegrationInstall(
				r.Context(),
				integrationstore.DisableIntegrationInstallInput{
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
	if install.State != integrationstore.IntegrationInstallStateActive {
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
	route, ok := slack.InboundRouting(installIdentity.BotUserID, envelope.Event)
	if !ok {
		logent.IntegrationEvent(r.Context(), install, "ignored_event", envelope.Event.Type)
		writeJSON(w, http.StatusOK, map[string]string{"ok": "ignored"})
		return
	}
	credentials, err := s.integrationSlackCredentials(r.Context(), install)
	if err != nil {
		writeIntegrationProviderError(w, err)
		return
	}
	accepted, err := s.processIntegrationInboundEvent(
		r.Context(),
		install,
		credentials.BotToken,
		installIdentity,
		envelope,
		route,
	)
	if errors.Is(err, storeerr.ErrStateTransitionConflict) {
		logent.IntegrationEvent(r.Context(), install, "ignored_agent_state", envelope.Event.Type)
		writeJSON(w, http.StatusOK, map[string]string{"ok": "ignored"})
		return
	}
	if errors.Is(err, storeerr.ErrUnauthorized) {
		current, loadErr := s.store.Integrations().GetIntegrationInstall(
			r.Context(),
			install.ProjectID,
			install.ID,
		)
		if loadErr == nil && current.State == integrationstore.IntegrationInstallStateDisabled {
			logent.IntegrationEvent(r.Context(), install, "ignored_disabled", envelope.Event.Type)
			writeJSON(w, http.StatusOK, map[string]string{"ok": "ignored"})
			return
		}
	}
	if err != nil {
		writeIntegrationProviderError(w, err)
		return
	}
	if !accepted {
		logent.IntegrationEvent(r.Context(), install, "ignored_event", envelope.Event.Type)
		writeJSON(w, http.StatusOK, map[string]string{"ok": "ignored"})
		return
	}
	logent.IntegrationEvent(r.Context(), install, "accepted", envelope.Event.Type)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "accepted"})
}

func (s *Server) processIntegrationInboundEvent(
	ctx context.Context,
	install integrationstore.IntegrationInstallRecord,
	botToken string,
	installIdentity slack.InstallIdentity,
	envelope slack.EventsEnvelope,
	route slack.InboundRoute,
) (bool, error) {
	var integrationTarget integrationstore.IntegrationTargetRecord
	var launch executionstore.LaunchAgentResult
	newlyMapped := false
	existing, err := s.store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		install.ProjectID,
		install.ID,
		route.ProviderRef,
	)
	switch {
	case err == nil:
		if existing.ProviderRefKind != route.ProviderRefKind {
			return false, storeerr.ErrConflict
		}
		integrationTarget = existing
	case storeerr.IsNotFound(err) && route.AppendOnly:
		return false, nil
	case storeerr.IsNotFound(err):
		profile, err := s.store.Execution().GetAgentProfile(ctx, install.ProjectID, install.AgentProfileID)
		if err != nil {
			return false, err
		}
		if !agentConfigCanUseIntegrationSendTool(profile.CurrentConfig) {
			return false, storeerr.ErrStateTransitionConflict
		}
		mappedTarget, launchResult, err := s.integrations.GetOrCreateTarget(
			ctx,
			integration.GetOrCreateTargetInput{
				IntegrationInstallID: install.ID,
				ProviderRef:          route.ProviderRef,
				ProviderRefKind:      route.ProviderRefKind,
			},
		)
		if err != nil {
			return false, err
		}
		integrationTarget = mappedTarget
		launch = launchResult
		newlyMapped = mappedTarget.Created
		if launch.Agent.ID != storage.NilID {
			s.startLaunchMachineProvisioning(ctx, logpkg.LoggerFromContext(ctx), launch)
		}
	default:
		return false, err
	}
	currentEventKey, siblingEventKey := slack.InputIdempotencyKeyPair(envelope)
	if _, found, err := s.store.Execution().GetIntegrationTargetInputByIdempotency(
		ctx,
		executionstore.GetIntegrationTargetInputByIdempotencyInput{
			IntegrationInstallID: install.ID,
			IntegrationTargetID:  integrationTarget.ID,
			IdempotencyKey:       currentEventKey,
		},
	); err != nil {
		return false, err
	} else if found {
		if launch.Agent.ID != storage.NilID {
			s.startLaunchMachineProvisioning(ctx, logpkg.LoggerFromContext(ctx), launch)
		}
		return true, nil
	}
	siblingEventAlreadyAccepted := false
	if siblingEventKey != "" {
		if _, found, err := s.store.Execution().GetIntegrationTargetInputByIdempotency(
			ctx,
			executionstore.GetIntegrationTargetInputByIdempotencyInput{
				IntegrationInstallID: install.ID,
				IntegrationTargetID:  integrationTarget.ID,
				IdempotencyKey:       siblingEventKey,
			},
		); err != nil {
			return false, err
		} else if found {
			if len(envelope.Event.Files) == 0 {
				return true, nil
			}
			siblingEventAlreadyAccepted = true
		}
	}
	fileIngest, err := s.ingestIntegrationEventFiles(
		ctx,
		install,
		integrationTarget,
		envelope.Event.Files,
		botToken,
	)
	if err != nil {
		return true, err
	}
	enrichmentCtx, cancel := context.WithTimeout(ctx, integrationEventEnrichmentTimeout)
	defer cancel()
	historyMessages, historyStatus := s.integrationHistoryMessages(
		enrichmentCtx,
		botToken,
		envelope.Event,
		route,
		newlyMapped,
	)
	labels := s.integrationSlackLabels(
		enrichmentCtx,
		install,
		integrationTarget,
		botToken,
		installIdentity,
		envelope.Event,
		historyMessages,
	)
	historyText := ""
	if historyStatus == "fetched" {
		historyText = slack.FormatRecentContext(historyMessages, envelope.Event, labels)
		if historyText == "" {
			historyStatus = "empty"
		}
	}
	visibleEvent := envelope.Event
	if siblingEventAlreadyAccepted {
		visibleEvent.Text = slack.AttachmentOnlyMessageText
	}
	messageText, hiddenText := slack.ModelInputTextParts(
		visibleEvent,
		route,
		newlyMapped,
		historyText,
		labels,
	)
	displayText := labels.RenderDisplayText(strings.TrimSpace(visibleEvent.Text))
	skippedFileSummary := slack.SkippedFileSummary(fileIngest.Files)
	metadata, err := slack.InboundEventMetadata(
		integrationstore.IntegrationProviderSlack,
		envelope,
		route,
		historyStatus,
		fileIngest.Files,
	)
	if err != nil {
		return true, err
	}
	hiddenMetadata := map[string]any{"omnara_hidden": "true"}
	contentBlockPayload := make([]map[string]any, 0, 3+len(fileIngest.Blocks))
	contentBlockPayload = append(contentBlockPayload, map[string]any{
		"type":     "text",
		"text":     hiddenText,
		"metadata": hiddenMetadata,
	})
	if messageText != "" {
		block := map[string]any{"type": "text", "text": messageText}
		if siblingEventAlreadyAccepted {
			block["metadata"] = hiddenMetadata
		} else if metadata := displayTextMetadata(messageText, displayText); metadata != nil {
			block["metadata"] = metadata
		}
		contentBlockPayload = append(contentBlockPayload, block)
	}
	if skippedFileSummary != "" {
		text := "\n" + skippedFileSummary
		block := map[string]any{"type": "text", "text": text}
		if metadata := displayTextMetadata(text, skippedFileSummary); metadata != nil {
			block["metadata"] = metadata
		}
		contentBlockPayload = append(contentBlockPayload, block)
	}
	contentBlockPayload = append(contentBlockPayload, fileIngest.Blocks...)
	contentBlocks, err := marshalJSON(contentBlockPayload)
	if err != nil {
		return true, err
	}
	_, canceledInteractionIDs, err := s.store.Execution().CreateIntegrationTargetContentInput(
		ctx,
		executionstore.CreateIntegrationTargetContentInput{
			IntegrationInstallID:   install.ID,
			IntegrationTargetID:    integrationTarget.ID,
			ProviderTenantID:       install.ProviderTenantID,
			ProviderUserID:         envelope.Event.User,
			ActorDisplayName:       labels.StoredDisplayName(envelope.Event.User),
			ContentBlocks:          contentBlocks,
			Metadata:               metadata,
			DeliveryMode:           executionstore.DeliveryModeSteering,
			IdempotencyKey:         currentEventKey,
			CancelOpenInteractions: true,
		},
	)
	if err != nil {
		return true, err
	}
	if len(canceledInteractionIDs) > 0 {
		publicInteractionIDs, err := publicIDs(
			publicid.KindAgentInteraction,
			canceledInteractionIDs,
		)
		if err != nil {
			return true, err
		}
		go s.dismissSlackInteractionPrompts(
			context.WithoutCancel(ctx),
			botToken,
			envelope.Event,
			publicInteractionIDs,
		)
	}
	go s.addIntegrationInboundReaction(ctx, install, envelope.Event, botToken)
	return true, nil
}

func displayTextMetadata(text, displayText string) map[string]any {
	if text == displayText || utf8.RuneCountInString(displayText) > contentBlockMetadataValueMaxRunes {
		return nil
	}
	return map[string]any{"omnara_display_text": displayText}
}

func (s *Server) dismissSlackInteractionPrompts(
	ctx context.Context,
	token string,
	event slack.Event,
	interactionIDs []string,
) {
	result, err := slack.DismissInteractionPrompts(
		ctx,
		s.slackOAuth,
		token,
		event,
		interactionIDs,
	)
	if err != nil || result.RateLimited || result.TransientFailure || result.PermanentFailure ||
		result.DeliveryUnknown {
		s.log.Warn("slack interaction prompt update failed", "error", err, "message", result.Message)
	}
}

func (s *Server) applyIntegrationNameUpdate(
	ctx context.Context,
	install integrationstore.IntegrationInstallRecord,
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

func (s *Server) integrationHistoryMessages(
	ctx context.Context,
	token string,
	event slack.Event,
	route slack.InboundRoute,
	newlyMapped bool,
) ([]slack.HistoryMessage, string) {
	messages, status, err := slack.FetchRecentContextMessages(
		ctx,
		s.slackOAuth,
		token,
		event,
		route,
		newlyMapped,
	)
	if err != nil {
		return nil, "failed"
	}
	return messages, status
}

func (s *Server) integrationSlackLabels(
	ctx context.Context,
	install integrationstore.IntegrationInstallRecord,
	integrationTarget integrationstore.IntegrationTargetRecord,
	token string,
	installIdentity slack.InstallIdentity,
	event slack.Event,
	historyMessages []slack.HistoryMessage,
) slack.DisplayLabels {
	userIDs := slack.ReferencedUserIDs(event, historyMessages)
	storedUserDisplayNames := map[string]string{}
	if len(userIDs) > 0 {
		names, err := s.store.Execution().ListActorDisplayNames(
			ctx,
			install.ProjectID,
			install.Provider,
			install.ProviderTenantID,
			userIDs,
		)
		if err != nil {
			logpkg.LoggerFromContext(ctx).Debug(
				"slack stored display name read failed",
				"integration_install_id",
				install.ID,
				"error",
				err,
			)
		} else {
			storedUserDisplayNames = names
		}
	}
	input := slack.DisplayLabelInput{
		Event:                  event,
		HistoryMessages:        historyMessages,
		BotUserID:              installIdentity.BotUserID,
		BotDisplayName:         install.ProviderAgentDisplayName,
		ChannelDisplayName:     integrationTarget.DisplayName,
		StoredUserDisplayNames: storedUserDisplayNames,
	}
	labels, resolvedChannels, err := slack.ResolveDisplayLabels(ctx, s.slackOAuth, token, input)
	if err != nil {
		logpkg.LoggerFromContext(ctx).Debug(
			"slack display name lookup failed",
			"integration_install_id",
			install.ID,
			"error",
			err,
		)
	}
	if name := resolvedChannels[event.Channel]; name != "" && event.ChannelType != "im" {
		if err := s.store.Integrations().UpdateIntegrationTargetDisplayNamesByProviderRefPrefix(
			ctx,
			integrationTarget.ProjectID,
			integrationTarget.IntegrationInstallID,
			event.Channel,
			name,
		); err != nil {
			logpkg.LoggerFromContext(ctx).Warn(
				"slack conversation display name write failed",
				"integration_target_id",
				integrationTarget.ID,
				"error",
				err,
			)
		}
	}
	return labels
}

func (s *Server) integrationSlackCredentials(
	ctx context.Context,
	install integrationstore.IntegrationInstallRecord,
) (slack.AppCredentials, error) {
	payload, err := s.store.Secrets().GetProjectOwnedSecretPayload(
		ctx,
		install.OrgID,
		install.ProjectID,
		install.CredentialSecretID,
	)
	if err != nil {
		return slack.AppCredentials{}, err
	}
	credentials, err := slack.AppCredentialsFromPayload(payload)
	if err != nil {
		return slack.AppCredentials{}, fmt.Errorf("read slack integration credentials: %w", err)
	}
	return credentials, nil
}

func (s *Server) addIntegrationInboundReaction(
	parent context.Context,
	install integrationstore.IntegrationInstallRecord,
	event slack.Event,
	token string,
) {
	if event.Channel == "" || event.TS == "" {
		return
	}
	logger := logpkg.LoggerFromContext(parent)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), integrationEventHTTPTimeout)
	defer cancel()
	result, err := slack.AddReaction(
		ctx,
		s.slackOAuth,
		token,
		event.Channel,
		event.TS,
		slack.InboundReaction,
	)
	if err != nil {
		logger.Warn(
			"slack inbound reaction failed",
			"integration_install_id",
			install.ID,
			"channel",
			event.Channel,
			"message_ts",
			event.TS,
			"error",
			err,
		)
		return
	}
	if result != (slack.APIResult{}) {
		logger.Warn(
			"slack inbound reaction rejected",
			"integration_install_id",
			install.ID,
			"channel",
			event.Channel,
			"message_ts",
			event.TS,
			"code",
			result.Code,
			"message",
			result.Message,
			"rate_limited",
			result.RateLimited,
		)
	}
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
