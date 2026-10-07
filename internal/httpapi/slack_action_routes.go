package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	integrationruntime "github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var errInvalidSlackAction = errors.New("invalid integration action")

const slackActionsPath = "/api/integrations/slack/actions"

func (s *Server) slackActionsRoute(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), integrationIntakeTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	raw, ok := readIntegrationCallbackBody(w, r, slack.ActionBodyMaxBytes)
	if !ok {
		return
	}
	envelope, err := slack.DecodeActionsEnvelope(raw)
	if err != nil {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
		return
	}
	if s.store == nil {
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "store unavailable")
		return
	}
	ownerID, err := s.slackCallbackOwner(ctx, envelope)
	if err != nil {
		writeIntegrationProviderError(w, err)
		return
	}
	install, ok := s.verifySignedSlackCallback(w, r, raw, ownerID, envelope.APIAppID, envelope.Team.ID)
	if !ok {
		return
	}
	installIdentity, err := slack.ParseInstallIdentity(install.ProviderIdentity)
	if err != nil {
		writeIntegrationProviderError(w, err)
		return
	}
	if !slack.ValidateActionIdentity(
		slack.Identity{
			AppID:       install.ProviderAccountRef,
			WorkspaceID: install.ProviderTenantID,
			BotUserID:   installIdentity.BotUserID,
		},
		envelope,
	) {
		apierror.Write(w, openapi.ErrorCodeForbidden, "invalid slack action identity")
		return
	}
	if s.slackProfileChoiceAction(w, r, install, envelope) {
		return
	}
	result, err := s.resolveSlackInteractionAction(r, install, envelope)
	if err != nil {
		if errors.Is(err, storeerr.ErrNotFound) || errors.Is(err, storeerr.ErrUnauthorized) ||
			errors.Is(err, errInvalidSlackAction) {
			writeJSON(w, http.StatusOK, map[string]string{"ok": "ignored"})
			return
		}
		writeIntegrationProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) resolveSlackInteractionAction(
	r *http.Request,
	install integrationstore.IntegrationRecord,
	envelope slack.ActionsEnvelope,
) (map[string]any, error) {
	actionValue, err := slack.PromptActionFromActions(envelope)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errInvalidSlackAction, err)
	}
	agentID, err := publicid.Decode(publicid.KindAgent, actionValue.AgentID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid agent id", errInvalidSlackAction)
	}
	interactionID, err := publicid.Decode(publicid.KindAgentInteraction, actionValue.InteractionID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid interaction id", errInvalidSlackAction)
	}
	integrationTargetID, err := publicid.Decode(
		publicid.KindIntegrationTarget,
		actionValue.IntegrationTargetID,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid integration target id", errInvalidSlackAction)
	}
	existing, found, err := s.store.Execution().GetAgentInteraction(
		r.Context(),
		install.ProjectID,
		agentID,
		interactionID,
	)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, storeerr.ErrNotFound
	}
	destination, err := existing.CapturedDestination()
	if err != nil {
		return nil, err
	}
	if destination == nil || destination.IntegrationID != install.ID ||
		destination.IntegrationTargetID != integrationTargetID ||
		destination.IntegrationKind != install.IntegrationKind {
		return nil, storeerr.ErrUnauthorized
	}
	var receipt integrationruntime.InteractionReceipt
	if json.Unmarshal(existing.PresentationReceipt, &receipt) != nil ||
		receipt.Provider != integrationdefinition.ProviderSlack ||
		receipt.MessageID == "" ||
		envelope.Channel.ID != receipt.ChannelID ||
		envelope.Message.TS != receipt.MessageID {
		return nil, storeerr.ErrUnauthorized
	}
	channel, thread, err := slack.Destination(destination.Address.Kind, destination.Address.Ref)
	messageThread := envelope.Message.ThreadTS
	// A channel-root prompt gains thread_ts=ts when somebody replies to it.
	if thread == "" && messageThread == receipt.MessageID {
		messageThread = ""
	}
	if err != nil || channel != envelope.Channel.ID || thread != messageThread ||
		(envelope.Container.ChannelID != "" && envelope.Container.ChannelID != channel) ||
		(envelope.Container.MessageTS != "" && envelope.Container.MessageTS != receipt.MessageID) {
		return nil, storeerr.ErrUnauthorized
	}
	_, err = s.store.Execution().
		GetAgentInteractionForPresentation(r.Context(), install.ProjectID, agentID, interactionID)
	if err != nil {
		return nil, err
	}
	latest, err := s.store.Integrations().GetIntegration(r.Context(), install.ProjectID, install.ID)
	if err != nil {
		return nil, err
	}
	if latest.State != integrationstore.IntegrationStateActive || latest.SetupRevision != install.SetupRevision {
		return nil, storeerr.ErrUnauthorized
	}
	if existing.State != executionstore.AgentInteractionStateOpen {
		s.dismissInteractionAsync(r.Context(), existing)
		return map[string]any{"ok": "already_resolved", "text": "This prompt has already been resolved."}, nil
	}
	resolutionResult, err := slackInteractionResolution(existing, envelope)
	if err != nil {
		return nil, err
	}
	if resolutionResult.InvalidReason != "" {
		return map[string]any{"ok": "invalid", "text": resolutionResult.InvalidReason}, nil
	}
	resolution := resolutionResult.Resolution
	actor, err := executionstore.IntegrationActorParams(install, envelope.User.ID, nil)
	if err != nil {
		return nil, err
	}
	displayName := ""
	if names, err := s.store.Execution().ListActorDisplayNames(
		r.Context(),
		install.ProjectID,
		actor.Provider,
		actor.ProviderTenantID,
		[]string{envelope.User.ID},
	); err == nil {
		displayName = names[envelope.User.ID]
	}
	if displayName == "" {
		displayName = envelope.User.DisplayName()
	}
	actor.DisplayName = &displayName
	resolve := executionstore.ResolveAgentInteractionFromHandlerInput{
		IntegrationID: install.ID, SourceSetupRevision: install.SetupRevision,
		IntegrationKind: install.IntegrationKind, Address: destination.Address,
		ResolveAgentInteractionInput: executionstore.ResolveAgentInteractionInput{
			ProjectID:           install.ProjectID,
			AgentID:             agentID,
			ID:                  interactionID,
			Resolution:          resolution,
			Actor:               &actor,
			IntegrationTargetID: integrationTargetID,
		},
	}
	resolved, err := s.store.Execution().ResolveAgentInteractionFromHandler(r.Context(), resolve)
	if err != nil {
		if errors.Is(err, storeerr.ErrIdempotencyConflict) {
			return map[string]any{
				"ok":   "already_resolved",
				"text": "This prompt has already been resolved.",
			}, nil
		}
		return nil, err
	}
	text := integrationruntime.InteractionResolvedText(resolved)
	s.dismissInteractionAsync(r.Context(), resolved)
	return map[string]any{"ok": "resolved", "text": text}, nil
}

type slackInteractionResolutionResult struct {
	Resolution    interactionform.Resolution
	InvalidReason string
}

func slackInteractionResolution(
	existing executionstore.AgentInteractionRecord,
	envelope slack.ActionsEnvelope,
) (slackInteractionResolutionResult, error) {
	value, err := existing.Form()
	if err != nil {
		return slackInteractionResolutionResult{},
			fmt.Errorf("parse stored interaction form: %w", err)
	}
	responseResult := slack.ResolveInteractionForm(value, envelope.State)
	if responseResult.InvalidReason != "" {
		return slackInteractionResolutionResult{InvalidReason: responseResult.InvalidReason}, nil
	}
	return slackInteractionResolutionResult{Resolution: responseResult.Resolution}, nil
}

func (s *Server) verifySignedSlackCallback(
	w http.ResponseWriter,
	r *http.Request,
	raw []byte,
	ownerID uuid.UUID, appID, workspaceID string,
) (integrationstore.IntegrationRecord, bool) {
	if s.store == nil {
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "store unavailable")
		return integrationstore.IntegrationRecord{}, false
	}
	if appID == "" || workspaceID == "" {
		apierror.Write(w, openapi.ErrorCodeForbidden, "invalid slack callback identity")
		return integrationstore.IntegrationRecord{}, false
	}
	install, err := s.store.Integrations().GetIntegrationByID(r.Context(), ownerID)
	if err != nil {
		writeIntegrationProviderError(w, err)
		return integrationstore.IntegrationRecord{}, false
	}
	if install.State != integrationstore.IntegrationStateActive ||
		install.Provider != integrationdefinition.ProviderSlack ||
		install.ProviderTenantID != workspaceID || install.ProviderAccountRef != appID {
		apierror.Write(w, openapi.ErrorCodeForbidden, "invalid slack callback owner")
		return integrationstore.IntegrationRecord{}, false
	}
	credentials, err := s.slackCredentials(r.Context(), install)
	if err != nil {
		writeIntegrationProviderError(w, err)
		return integrationstore.IntegrationRecord{}, false
	}
	if !slack.ValidSignature(r.Header, raw, credentials.SigningSecret, time.Now().UTC()) {
		apierror.Write(w, openapi.ErrorCodeUnauthorized, "invalid signature")
		return integrationstore.IntegrationRecord{}, false
	}
	return install, true
}

func (s *Server) slackCallbackOwner(ctx context.Context, envelope slack.ActionsEnvelope) (uuid.UUID, error) {
	for _, action := range envelope.Actions {
		if value, ok := strings.CutPrefix(action.ActionID, slack.ProfileChoiceActionPrefix); ok {
			id, err := publicid.Decode(publicid.KindIntegrationProfileChoice, value)
			if err != nil {
				return uuid.Nil, storeerr.ErrNotFound
			}
			return s.store.Integrations().GetIntegrationProfileChoiceIntegrationID(ctx, id)
		}
	}
	interactionID, err := slack.PromptCallbackInteractionID(envelope)
	if err != nil {
		return uuid.Nil, storeerr.ErrNotFound
	}
	id, err := publicid.Decode(publicid.KindAgentInteraction, interactionID)
	if err != nil {
		return uuid.Nil, storeerr.ErrNotFound
	}
	return s.store.Execution().GetInteractionCallbackIntegrationID(ctx, id)
}
