package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/apps"
	"github.com/omnara-ai/omnara/internal/apps/slack"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var errInvalidAppAction = errors.New("invalid app action")

const appActionsPath = "/api/integrations/slack/actions"

func (s *Server) appActionsRoute(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), appIntakeTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	raw, ok := readAppCallbackBody(w, r, slack.ActionBodyMaxBytes)
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
		writeAppProviderError(w, err)
		return
	}
	install, ok := s.verifySignedSlackCallback(w, r, raw, ownerID, envelope.APIAppID, envelope.Team.ID)
	if !ok {
		return
	}
	installIdentity, err := slack.ParseInstallIdentity(install.ProviderIdentity)
	if err != nil {
		writeAppProviderError(w, err)
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
	result, err := s.resolveAppInteractionAction(r, install, envelope)
	if err != nil {
		if errors.Is(err, storeerr.ErrNotFound) || errors.Is(err, storeerr.ErrUnauthorized) ||
			errors.Is(err, errInvalidAppAction) {
			writeJSON(w, http.StatusOK, map[string]string{"ok": "ignored"})
			return
		}
		writeAppProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) resolveAppInteractionAction(
	r *http.Request,
	install appstore.ProjectAppRecord,
	envelope slack.ActionsEnvelope,
) (map[string]any, error) {
	actionValue, err := slack.PromptActionFromActions(envelope)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errInvalidAppAction, err)
	}
	agentID, err := publicid.Decode(publicid.KindAgent, actionValue.AgentID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid agent id", errInvalidAppAction)
	}
	interactionID, err := publicid.Decode(publicid.KindAgentInteraction, actionValue.InteractionID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid interaction id", errInvalidAppAction)
	}
	appTargetID, err := publicid.Decode(
		publicid.KindAppTarget,
		actionValue.AppTargetID,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid app target id", errInvalidAppAction)
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
	if destination == nil || destination.AppID != install.ID ||
		destination.AppTargetID != appTargetID ||
		destination.AppType != install.AppType {
		return nil, storeerr.ErrUnauthorized
	}
	var receipt apps.InteractionReceipt
	if json.Unmarshal(existing.PresentationReceipt, &receipt) != nil || receipt.Provider != appdefinition.ProviderSlack ||
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
	latest, err := s.store.Apps().GetProjectApp(r.Context(), install.ProjectID, install.ID)
	if err != nil {
		return nil, err
	}
	if latest.State != appstore.ProjectAppStateActive || latest.SetupRevision != install.SetupRevision {
		return nil, storeerr.ErrUnauthorized
	}
	if existing.State != executionstore.AgentInteractionStateOpen {
		s.dismissInteractionAsync(r.Context(), existing)
		return map[string]any{"ok": "already_resolved", "text": "This prompt has already been resolved."}, nil
	}
	resolutionResult, err := appInteractionResolution(existing, envelope)
	if err != nil {
		return nil, err
	}
	if resolutionResult.InvalidReason != "" {
		return map[string]any{"ok": "invalid", "text": resolutionResult.InvalidReason}, nil
	}
	resolution := resolutionResult.Resolution
	actor, err := executionstore.AppActorParams(install.ID, envelope.User.ID, nil)
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
		AppID: install.ID, SourceSetupRevision: install.SetupRevision,
		AppType: install.AppType, Address: destination.Address,
		ResolveAgentInteractionInput: executionstore.ResolveAgentInteractionInput{
			ProjectID:   install.ProjectID,
			AgentID:     agentID,
			ID:          interactionID,
			Resolution:  resolution,
			Actor:       &actor,
			AppTargetID: appTargetID,
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
	text := apps.InteractionResolvedText(resolved)
	s.dismissInteractionAsync(r.Context(), resolved)
	return map[string]any{"ok": "resolved", "text": text}, nil
}

type appInteractionResolutionResult struct {
	Resolution    interactionform.Resolution
	InvalidReason string
}

func appInteractionResolution(
	existing executionstore.AgentInteractionRecord,
	envelope slack.ActionsEnvelope,
) (appInteractionResolutionResult, error) {
	value, err := existing.Form()
	if err != nil {
		return appInteractionResolutionResult{},
			fmt.Errorf("parse stored interaction form: %w", err)
	}
	responseResult := slack.ResolveInteractionForm(value, envelope.State)
	if responseResult.InvalidReason != "" {
		return appInteractionResolutionResult{InvalidReason: responseResult.InvalidReason}, nil
	}
	return appInteractionResolutionResult{Resolution: responseResult.Resolution}, nil
}
