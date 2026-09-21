package httpapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const discordInteractionsPath = "/api/integrations/discord/{application_id}/interactions"

func (s *Server) discordInteractionsRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), discord.InteractionTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	if s.store == nil {
		http.Error(w, "interaction handler unavailable", http.StatusServiceUnavailable)
		return
	}
	raw, ok := readIntegrationCallbackBody(w, r, discord.InteractionMaxBytes)
	if !ok {
		return
	}
	var hint discord.Interaction
	applicationID := r.PathValue("application_id")
	if json.Unmarshal(raw, &hint) != nil || applicationID == "" || hint.ApplicationID != applicationID {
		http.Error(w, "invalid interaction", http.StatusBadRequest)
		return
	}
	var app integrationstore.ProjectAppRecord
	var err error
	if hint.Type == 1 {
		app, err = s.discordPingApp(ctx, applicationID, r.Header, raw)
	} else {
		var ownerID uuid.UUID
		ownerID, err = s.discordCallbackOwner(ctx, hint)
		if err == nil {
			app, err = s.store.Integrations().GetProjectAppByID(ctx, ownerID)
		}
	}
	if err != nil {
		if hint.Type == 1 && permanentIntegrationIngressError(err) {
			http.Error(w, "invalid interaction signature", http.StatusUnauthorized)
		} else if permanentIntegrationIngressError(err) {
			http.Error(w, "interaction owner unavailable", http.StatusNotFound)
		} else {
			http.Error(w, "interaction handler unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	if app.Provider != appdefinition.ProviderDiscord || app.ProviderTenantID != applicationID ||
		app.State != integrationstore.ProjectAppStateActive {
		http.Error(w, "invalid interaction owner", http.StatusForbidden)
		return
	}
	handler, err := discord.NewInteractionHandler(
		applicationID, integration.DiscordInteractionPublicKey(app.ProviderConfig),
		func(ctx context.Context, input discord.Interaction) (discord.InteractionResponse, error) {
			return s.resolveDiscordInteraction(ctx, app, input)
		})
	if err != nil {
		http.Error(w, "interaction handler unavailable", http.StatusServiceUnavailable)
		return
	}
	// The provider handler performs final signature, timestamp and payload
	// validation on these exact bytes before dispatching to the captured owner.
	r.Body = io.NopCloser(bytes.NewReader(raw))
	handler.ServeHTTP(w, r)
}

func (s *Server) discordCallbackOwner(ctx context.Context, input discord.Interaction) (uuid.UUID, error) {
	if strings.HasPrefix(input.Data.CustomID, discord.ProfileChoiceCustomIDPrefix) {
		choice, err := discord.ProfileChoiceFromInteraction(input)
		if err != nil {
			return uuid.Nil, storeerr.ErrNotFound
		}
		id, err := publicid.Decode(publicid.KindAppProfileChoice, choice.ChoiceID)
		if err != nil {
			return uuid.Nil, storeerr.ErrNotFound
		}
		return s.store.Integrations().GetAppProfileChoiceAppID(ctx, id)
	}
	custom, err := discord.DecodeCustomID(input.Data.CustomID)
	if err != nil {
		return uuid.Nil, storeerr.ErrNotFound
	}
	id, err := publicid.Decode(publicid.KindAgentInteraction, custom.InteractionID)
	if err != nil {
		return uuid.Nil, storeerr.ErrNotFound
	}
	return s.store.Execution().GetInteractionCallbackAppID(ctx, id)
}

func (s *Server) discordPingApp(
	ctx context.Context, applicationID string, header http.Header, raw []byte,
) (integrationstore.ProjectAppRecord, error) {
	after := uuid.Nil
	var retryErr error
	for {
		if err := ctx.Err(); err != nil {
			return integrationstore.ProjectAppRecord{}, err
		}
		apps, err := s.store.Integrations().ListProjectAppsByProviderTenant(
			ctx, appdefinition.ProviderDiscord, applicationID, after, integrationIngressPageSize,
		)
		if err != nil {
			return integrationstore.ProjectAppRecord{}, err
		}
		if len(apps) > integrationIngressPageSize {
			return integrationstore.ProjectAppRecord{}, fmt.Errorf("app ingress page exceeds limit")
		}
		for _, app := range apps {
			if app.ID == uuid.Nil || app.ID.String() <= after.String() {
				return integrationstore.ProjectAppRecord{}, fmt.Errorf("app ingress cursor did not advance")
			}
			after = app.ID
			if app.Provider != appdefinition.ProviderDiscord || app.ProviderTenantID != applicationID ||
				app.State != integrationstore.ProjectAppStateActive {
				continue
			}
			if !discordPingKeyMatches(app, header, raw) {
				continue
			}
			_, err := s.store.Secrets().
				ReadProjectAvailableSecretPayload(ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
					OrgID: app.OrgID, ProjectID: app.ProjectID, SecretID: app.CredentialSecretID, Kind: secrets.KindGeneric,
				})
			if err == nil {
				return app, nil
			}
			if !permanentIntegrationIngressError(err) {
				retryErr = err
			}
		}
		if len(apps) < integrationIngressPageSize {
			if retryErr != nil {
				return integrationstore.ProjectAppRecord{}, retryErr
			}
			return integrationstore.ProjectAppRecord{}, storeerr.ErrNotFound
		}
	}
}

// Select a candidate key only; Discord's handler remains responsible for full
// authentication, including timestamp age and protocol validation. PING has no
// captured owner and may be verified by any matching active app.
func discordPingKeyMatches(app integrationstore.ProjectAppRecord, header http.Header, raw []byte) bool {
	key, err := hex.DecodeString(integration.DiscordInteractionPublicKey(app.ProviderConfig))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return false
	}
	signature, err := hex.DecodeString(header.Get("X-Signature-Ed25519"))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return false
	}
	timestamp := header.Get("X-Signature-Timestamp")
	if len(timestamp) == 0 || len(timestamp) > 20 {
		return false
	}
	return ed25519.Verify(key, append([]byte(timestamp), raw...), signature)
}

// discordInteractionNotice acknowledges an invalid or stale action with an
// ephemeral response; it does not ask Discord to retry a rejected user answer.
func discordInteractionNotice(text string) (discord.InteractionResponse, error) {
	return discord.InteractionResponse{Type: 4, Data: &discord.InteractionResponseData{Content: text, Flags: 64}}, nil
}

func (s *Server) resolveDiscordInteraction(
	ctx context.Context, app integrationstore.ProjectAppRecord, input discord.Interaction,
) (discord.InteractionResponse, error) {
	if strings.HasPrefix(input.Data.CustomID, discord.ProfileChoiceCustomIDPrefix) {
		return s.discordProfileChoiceAction(ctx, app, input)
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
	record, err := s.store.Execution().GetInteractionForHandlerCallback(ctx, app.ProjectID, app.ID, id)
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
	if destination == nil || destination.AppType != app.AppType ||
		destination.AppID != app.ID {
		return discordInteractionNotice("This prompt is unavailable.")
	}
	var receipt integration.InteractionReceipt
	if json.Unmarshal(record.PresentationReceipt, &receipt) != nil || receipt.Provider != appdefinition.ProviderDiscord ||
		receipt.MessageID == "" ||
		input.Message == nil || input.Message.ID != receipt.MessageID || input.Message.ChannelID != receipt.ChannelID ||
		input.Message.Author.ID != app.ProviderAccountRef ||
		input.ChannelID != receipt.ChannelID {
		return discordInteractionNotice("This prompt is unavailable. Respond in Omnara.")
	}
	scope, err := integration.DiscordInteractionScope(*destination)
	if err != nil {
		return discord.InteractionResponse{}, err
	}
	channel := scope.ChannelID
	if scope.ThreadID != "" {
		channel = scope.ThreadID
	}
	if channel != input.ChannelID || (scope.GuildID != "" && scope.GuildID != input.GuildID) {
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
	latest, err := s.store.Integrations().GetProjectApp(ctx, app.ProjectID, app.ID)
	if err != nil {
		return discord.InteractionResponse{}, err
	}
	if latest.State != integrationstore.ProjectAppStateActive || latest.SetupRevision != app.SetupRevision {
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
	appActor, err := executionstore.AppActorParams(app.ID, actor.ID, &name)
	if err != nil {
		return discord.InteractionResponse{}, err
	}
	resolve := executionstore.ResolveAgentInteractionFromHandlerInput{
		AppID: app.ID, SourceSetupRevision: app.SetupRevision,
		AppType: app.AppType, Address: destination.Address,
		ResolveAgentInteractionInput: executionstore.ResolveAgentInteractionInput{
			ProjectID: record.ProjectID, AgentID: record.AgentID, ID: record.ID, Resolution: *resolution,
			Actor: &appActor,
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
