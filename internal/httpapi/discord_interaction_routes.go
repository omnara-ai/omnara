package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const discordInteractionsPath = "/api/integrations/discord/{connection_id}/interactions"

func (s *Server) discordInteractionsRoute(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), discord.InteractionTimeout)
	defer cancel()
	id, err := publicid.Decode(publicid.KindIntegrationConnection, r.PathValue("connection_id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	connection, err := s.store.Integrations().GetIntegrationConnectionByID(ctx, id)
	if err != nil || connection.Provider != appdefinition.ProviderDiscord ||
		connection.State != integrationstore.IntegrationConnectionStateActive {
		http.NotFound(w, r)
		return
	}
	handler, err := discord.NewInteractionHandler(
		connection.ProviderTenantID, integration.DiscordInteractionPublicKey(connection.ProviderConfig),
		func(ctx context.Context, input discord.Interaction) (discord.InteractionResponse, error) {
			return s.resolveDiscordInteraction(ctx, connection, input)
		})
	if err != nil {
		http.Error(w, "interaction handler unavailable", http.StatusServiceUnavailable)
		return
	}
	handler.ServeHTTP(w, r.WithContext(ctx))
}

// discordInteractionNotice acknowledges an invalid or stale action with an
// ephemeral response; it does not ask Discord to retry a rejected user answer.
func discordInteractionNotice(text string) (discord.InteractionResponse, error) {
	return discord.InteractionResponse{Type: 4, Data: &discord.InteractionResponseData{Content: text, Flags: 64}}, nil
}

func (s *Server) resolveDiscordInteraction(
	ctx context.Context, connection integrationstore.IntegrationConnectionRecord, input discord.Interaction,
) (discord.InteractionResponse, error) {
	if strings.HasPrefix(input.Data.CustomID, discord.ProfileChoiceCustomIDPrefix) {
		return s.discordProfileChoiceAction(ctx, connection, input)
	}
	// Only buttons and their modal submissions can address core interactions.
	if input.Type != 3 && input.Type != 5 {
		return discordInteractionNotice("Respond using an Omnara prompt.")
	}
	custom, err := discord.DecodeCustomID(input.Data.CustomID)
	if err != nil {
		return discordInteractionNotice("Invalid prompt.")
	}
	id, err := publicid.Decode(publicid.KindAgentInteraction, custom.InteractionID)
	if err != nil {
		return discordInteractionNotice("Invalid prompt.")
	}
	record, err := s.store.Execution().GetInteractionForHandlerCallback(ctx, connection.ProjectID, connection.ID, id)
	if errors.Is(err, storeerr.ErrNotFound) {
		return discordInteractionNotice("This prompt is unavailable.")
	}
	if err != nil {
		return discord.InteractionResponse{}, err
	}
	destination, err := record.CapturedDestination()
	if err != nil {
		return discord.InteractionResponse{}, err
	}
	if destination == nil || destination.HandlerDefinition != appdefinition.DiscordInteractions ||
		destination.ConnectionID != connection.ID {
		return discordInteractionNotice("This prompt is unavailable.")
	}
	var receipt integration.InteractionReceipt
	if json.Unmarshal(record.PresentationReceipt, &receipt) != nil || receipt.Provider != appdefinition.ProviderDiscord ||
		receipt.MessageID == "" ||
		input.Message == nil || input.Message.ID != receipt.MessageID || input.Message.ChannelID != receipt.ChannelID ||
		input.Message.Author.ID != connection.ProviderAccountRef ||
		input.ChannelID != receipt.ChannelID {
		return discordInteractionNotice("This prompt is unavailable. Respond in Omnara.")
	}
	channel := destination.Address.Ref
	if destination.Address.Kind == "thread" {
		_, channel, _ = strings.Cut(channel, ":")
	}
	if channel != input.ChannelID {
		return discordInteractionNotice("This prompt is unavailable.")
	}
	// Modal opening is read-only, but still requires the same live handler.
	_, err = s.store.Execution().GetAgentInteractionForPresentation(ctx, record.ProjectID, record.AgentID, record.ID)
	if err != nil {
		if errors.Is(err, storeerr.ErrUnauthorized) || errors.Is(err, storeerr.ErrNotFound) {
			return discordInteractionNotice("This prompt is unavailable. Respond in Omnara.")
		}
		return discord.InteractionResponse{}, err
	}
	latest, err := s.store.Integrations().GetIntegrationConnection(ctx, connection.ProjectID, connection.ID)
	if err != nil {
		return discord.InteractionResponse{}, err
	}
	if !latest.UpdatedAt.Equal(connection.UpdatedAt) {
		return discordInteractionNotice("This prompt is unavailable.")
	}
	if record.State != executionstore.AgentInteractionStateOpen {
		s.dismissInteractionAsync(ctx, record)
		return discordInteractionNotice("This prompt is already closed.")
	}
	form, err := record.Form()
	if err != nil {
		return discord.InteractionResponse{}, err
	}
	response, resolution, err := discord.ResolveInteractionForm(form, input)
	if err != nil {
		return discordInteractionNotice("Check your choices, or respond in Omnara.")
	}
	if resolution == nil {
		return response, nil
	}
	actor := input.Actor()
	name := actor.GlobalName
	if name == "" {
		name = actor.Username
	}
	resolve := executionstore.ResolveAgentInteractionFromHandlerInput{
		ConnectionID: connection.ID, SourceConnectionUpdatedAt: connection.UpdatedAt,
		HandlerDefinition: appdefinition.DiscordInteractions, Address: destination.Address,
		ResolveAgentInteractionInput: executionstore.ResolveAgentInteractionInput{
			ProjectID: record.ProjectID, AgentID: record.AgentID, ID: record.ID, Resolution: *resolution,
			Actor: &executionstore.ActorParams{
				Provider: appdefinition.ProviderDiscord, ProviderTenantID: connection.ProviderTenantID,
				ProviderUserID: actor.ID, DisplayName: &name},
		},
	}
	resolved, err := s.store.Execution().ResolveAgentInteractionFromHandler(ctx, resolve)
	if errors.Is(err, storeerr.ErrIdempotencyConflict) {
		return discordInteractionNotice("This prompt is already closed.")
	}
	if errors.Is(err, storeerr.ErrUnauthorized) || errors.Is(err, storeerr.ErrNotFound) {
		return discordInteractionNotice("This prompt is unavailable. Respond in Omnara.")
	}
	if err != nil {
		return discord.InteractionResponse{}, err
	}
	s.dismissInteractionAsync(ctx, resolved)
	return response, nil
}

func (s *Server) dismissInteractionAsync(ctx context.Context, record executionstore.AgentInteractionRecord) {
	if len(record.PresentationReceipt) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		presenter := integration.InteractionPresenter{Store: s.store, HTTPClient: s.slackOAuth.HTTPClient}
		if err := presenter.Dismiss(ctx, record); err != nil {
			s.log.Warn("interaction dismissal failed", "interaction_id", record.ID, "error", err)
		}
	}()
}
