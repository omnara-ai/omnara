package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	integrationruntime "github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const unavailableProfileChoice = "This profile menu is no longer available. Mention the bot again to start a new request."

func profileChoiceText(choice integrationstore.IntegrationProfileChoiceRecord) string {
	for _, option := range choice.Options {
		if option.Key == choice.SelectedKey {
			return "Selected " + option.Name + ". The original request has been queued for the agent."
		}
	}
	return unavailableProfileChoice
}

func (s *Server) slackProfileChoiceAction(
	w http.ResponseWriter, r *http.Request,
	integration integrationstore.IntegrationRecord, envelope slack.ActionsEnvelope,
) bool {
	matched := false
	for _, action := range envelope.Actions {
		matched = matched || strings.HasPrefix(action.ActionID, slack.ProfileChoiceActionPrefix)
	}
	if !matched {
		return false
	}
	selection, err := slack.ProfileChoiceFromActions(envelope)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"ok": "invalid"})
		return true
	}
	id, err := publicid.Decode(publicid.KindIntegrationProfileChoice, selection.ChoiceID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"ok": "invalid"})
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	sourceChoice, err := s.store.Integrations().GetIntegrationProfileChoice(ctx, integration.ProjectID, integration.ID, id)
	if err != nil {
		writeIntegrationProviderError(w, err)
		return true
	}
	allowed, err := s.allowSlackProfileChoice(ctx, integration, sourceChoice, envelope)
	if err != nil {
		writeIntegrationProviderError(w, err)
		return true
	}
	if !allowed {
		writeJSON(w, http.StatusOK, map[string]string{"ok": "ignored"})
		return true
	}
	choice, err := integrationruntime.SelectChatIntegrationProfile(ctx, s.store.Integrations(), integration, id,
		selection.Key, envelope.User.ID, envelope.Channel.ID, envelope.Message.TS)
	text := profileChoiceText(choice)
	if err != nil {
		if !unavailableChoiceError(err) {
			writeIntegrationProviderError(w, err)
			return true
		}
		text = unavailableProfileChoice
	}
	retired := !choice.ExpiresAt.IsZero() && !time.Now().Before(choice.ExpiresAt)
	if (err == nil || retired) && choice.MessageID != "" && choice.MessageID == envelope.Message.TS &&
		choice.MessageChannelID == envelope.Channel.ID {
		s.dismissSlackProfileChoice(ctx, integration, choice, text)
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "recorded"})
	return true
}

// allowSlackProfileChoice checks both participants and retires requests that opt-out made unselectable.
func (s *Server) allowSlackProfileChoice(
	ctx context.Context, integration integrationstore.IntegrationRecord,
	choice integrationstore.IntegrationProfileChoiceRecord, envelope slack.ActionsEnvelope,
) (bool, error) {
	if choice.MessageChannelID != envelope.Channel.ID || choice.MessageID != envelope.Message.TS {
		return false, nil
	}
	source, err := slack.DecodeEventsEnvelope(choice.Payload)
	if err != nil {
		return false, err
	}
	allowExternal := integrationdefinition.SlackAllowsExternalParticipants(integration.Settings)
	if !slack.MessageParticipantAllowed(integration.ProviderTenantID, source, allowExternal) {
		// Retire an unselectable request so it does not reserve the conversation
		// until the menu's original expiry. A denied clicker alone must not retire it.
		if err := s.store.Integrations().ExpireIntegrationProfileChoice(
			ctx, integration.ProjectID, integration.ID, choice.ID); err != nil {
			return false, err
		}
		latest, err := s.store.Integrations().GetIntegrationProfileChoice(
			ctx, integration.ProjectID, integration.ID, choice.ID)
		if err != nil {
			return false, err
		}
		if latest.SelectedKey == "" {
			s.dismissSlackProfileChoice(ctx, integration, latest, unavailableProfileChoice)
		}
		return false, nil
	}
	return slack.ProfileChoiceParticipantAllowed(integration.ProviderTenantID, source, envelope, allowExternal), nil
}

func (s *Server) discordProfileChoiceAction(
	ctx context.Context, integration integrationstore.IntegrationRecord, input discord.Interaction,
) (discord.InteractionResponse, error) {
	selection, err := discord.ProfileChoiceFromInteraction(input)
	if err != nil || input.Message == nil || input.Message.Author.ID != integration.ProviderAccountRef ||
		input.Message.ChannelID != input.ChannelID {
		return discordInteractionNotice(unavailableProfileChoice)
	}
	id, err := publicid.Decode(publicid.KindIntegrationProfileChoice, selection.ChoiceID)
	if err != nil {
		return discordInteractionNotice(unavailableProfileChoice)
	}
	choice, err := integrationruntime.SelectChatIntegrationProfile(ctx, s.store.Integrations(), integration, id,
		selection.Key, input.Actor().ID, input.ChannelID, input.Message.ID)
	text := profileChoiceText(choice)
	if err != nil {
		if !unavailableChoiceError(err) {
			return discord.InteractionResponse{}, err
		}
		if choice.ExpiresAt.IsZero() || time.Now().Before(choice.ExpiresAt) ||
			choice.MessageID != input.Message.ID || choice.MessageChannelID != input.ChannelID {
			return discordInteractionNotice(unavailableProfileChoice)
		}
		text = unavailableProfileChoice
	}
	return discord.InteractionResponse{
		Type: discord.InteractionResponseUpdateMessage,
		Data: &discord.InteractionResponseData{Content: text, Components: []discord.ActionRow{}},
	}, nil
}

func unavailableChoiceError(err error) bool {
	return errors.Is(err, storeerr.ErrNotFound) || errors.Is(err, storeerr.ErrUnauthorized) ||
		errors.Is(err, storeerr.ErrStateTransitionConflict) || errors.Is(err, storeerr.ErrConflict)
}

func (s *Server) dismissSlackProfileChoice(
	ctx context.Context, integration integrationstore.IntegrationRecord,
	choice integrationstore.IntegrationProfileChoiceRecord, text string,
) {
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		provider := integrationruntime.NewSlackIntegrationInboxProvider(
			s.slackOAuth, s.store.Secrets(), s.store.Integrations(), s.store.Execution(),
		)
		if err := provider.DismissProfileChoice(ctx, integration, choice, text); err != nil {
			s.log.Warn("profile choice dismissal failed", "choice_id", choice.ID, "error", err)
		}
	}()
}
