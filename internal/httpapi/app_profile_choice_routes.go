package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/apps"
	"github.com/omnara-ai/omnara/internal/apps/discord"
	"github.com/omnara-ai/omnara/internal/apps/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const unavailableProfileChoice = "This profile menu is no longer available. Mention the bot again to start a new request."

func profileChoiceText(choice appstore.AppProfileChoiceRecord) string {
	for _, option := range choice.Options {
		if option.Key == choice.SelectedKey {
			return "Selected " + option.Name + ". The original request has been queued for the agent."
		}
	}
	return unavailableProfileChoice
}

func (s *Server) slackProfileChoiceAction(
	w http.ResponseWriter, r *http.Request,
	app appstore.ProjectAppRecord, envelope slack.ActionsEnvelope,
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
	id, err := publicid.Decode(publicid.KindAppProfileChoice, selection.ChoiceID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"ok": "invalid"})
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	choice, err := apps.SelectChatAppProfile(ctx, s.store.Apps(), app, id,
		selection.Key, envelope.User.ID, envelope.Channel.ID, envelope.Message.TS)
	text := profileChoiceText(choice)
	if err != nil {
		if !unavailableChoiceError(err) {
			writeAppProviderError(w, err)
			return true
		}
		text = unavailableProfileChoice
	}
	retired := !choice.ExpiresAt.IsZero() && !time.Now().Before(choice.ExpiresAt)
	if (err == nil || retired) && choice.MessageID != "" && choice.MessageID == envelope.Message.TS &&
		choice.MessageChannelID == envelope.Channel.ID {
		s.dismissSlackProfileChoice(ctx, app, choice, text)
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "recorded"})
	return true
}

func (s *Server) discordProfileChoiceAction(
	ctx context.Context, app appstore.ProjectAppRecord, input discord.Interaction,
) (discord.InteractionResponse, error) {
	selection, err := discord.ProfileChoiceFromInteraction(input)
	if err != nil || input.Message == nil || input.Message.Author.ID != app.ProviderAccountRef ||
		input.Message.ChannelID != input.ChannelID {
		return discordInteractionNotice(unavailableProfileChoice)
	}
	id, err := publicid.Decode(publicid.KindAppProfileChoice, selection.ChoiceID)
	if err != nil {
		return discordInteractionNotice(unavailableProfileChoice)
	}
	choice, err := apps.SelectChatAppProfile(ctx, s.store.Apps(), app, id,
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
	ctx context.Context, app appstore.ProjectAppRecord,
	choice appstore.AppProfileChoiceRecord, text string,
) {
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		provider := apps.NewSlackAppInboxProvider(
			s.slackOAuth, s.store.Secrets(), s.store.Apps(), s.store.Execution(),
		)
		if err := provider.DismissProfileChoice(ctx, app, choice, text); err != nil {
			s.log.Warn("profile choice dismissal failed", "choice_id", choice.ID, "error", err)
		}
	}()
}
