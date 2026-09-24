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

	"github.com/google/uuid"
	integrationruntime "github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
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
	var integration integrationstore.ProjectIntegrationRecord
	var err error
	if hint.Type == discord.InteractionTypePing {
		integration, err = s.discordPingIntegration(ctx, applicationID, r.Header, raw)
	} else {
		var ownerID uuid.UUID
		ownerID, err = s.discordCallbackOwner(ctx, hint)
		if err == nil {
			integration, err = s.store.Integrations().GetProjectIntegrationByID(ctx, ownerID)
		}
	}
	if err != nil {
		if hint.Type == discord.InteractionTypePing && permanentIntegrationIngressError(err) {
			http.Error(w, "invalid interaction signature", http.StatusUnauthorized)
		} else if permanentIntegrationIngressError(err) {
			http.Error(w, "interaction owner unavailable", http.StatusNotFound)
		} else {
			http.Error(w, "interaction handler unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	if integration.Provider != integrationdefinition.ProviderDiscord || integration.ProviderTenantID != applicationID ||
		integration.State != integrationstore.ProjectIntegrationStateActive {
		http.Error(w, "invalid interaction owner", http.StatusForbidden)
		return
	}
	handler, err := discord.NewInteractionHandler(
		applicationID, integrationruntime.DiscordInteractionPublicKey(integration.ProviderConfig),
		func(ctx context.Context, input discord.Interaction) (discord.InteractionResponse, error) {
			return s.resolveDiscordInteraction(ctx, integration, input)
		})
	if err != nil {
		http.Error(w, "interaction handler unavailable", http.StatusServiceUnavailable)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	handler.ServeHTTP(w, r)
}

func (s *Server) discordCallbackOwner(ctx context.Context, input discord.Interaction) (uuid.UUID, error) {
	if strings.HasPrefix(input.Data.CustomID, discord.ProfileChoiceCustomIDPrefix) {
		choice, err := discord.ProfileChoiceFromInteraction(input)
		if err != nil {
			return uuid.Nil, storeerr.ErrNotFound
		}
		id, err := publicid.Decode(publicid.KindIntegrationProfileChoice, choice.ChoiceID)
		if err != nil {
			return uuid.Nil, storeerr.ErrNotFound
		}
		return s.store.Integrations().GetIntegrationProfileChoiceIntegrationID(ctx, id)
	}
	custom, err := discord.DecodeCustomID(input.Data.CustomID)
	if err != nil {
		return uuid.Nil, storeerr.ErrNotFound
	}
	id, err := publicid.Decode(publicid.KindAgentInteraction, custom.InteractionID)
	if err != nil {
		return uuid.Nil, storeerr.ErrNotFound
	}
	return s.store.Execution().GetInteractionCallbackIntegrationID(ctx, id)
}

func (s *Server) discordPingIntegration(
	ctx context.Context, applicationID string, header http.Header, raw []byte,
) (integrationstore.ProjectIntegrationRecord, error) {
	after := uuid.Nil
	var retryErr error
	for {
		if err := ctx.Err(); err != nil {
			return integrationstore.ProjectIntegrationRecord{}, err
		}
		page, err := s.store.Integrations().ListProjectIntegrationsByProviderTenant(
			ctx, integrationdefinition.ProviderDiscord, applicationID, after, integrationIngressPageSize,
		)
		if err != nil {
			return integrationstore.ProjectIntegrationRecord{}, err
		}
		if len(page) > integrationIngressPageSize {
			return integrationstore.ProjectIntegrationRecord{}, fmt.Errorf("integration ingress page exceeds limit")
		}
		for _, integration := range page {
			if integration.ID == uuid.Nil || integration.ID.String() <= after.String() {
				return integrationstore.ProjectIntegrationRecord{}, fmt.Errorf("integration ingress cursor did not advance")
			}
			after = integration.ID
			if integration.Provider != integrationdefinition.ProviderDiscord || integration.ProviderTenantID != applicationID ||
				integration.State != integrationstore.ProjectIntegrationStateActive {
				continue
			}
			if !discordPingKeyMatches(integration, header, raw) {
				continue
			}
			_, err := s.store.Secrets().
				ReadProjectAvailableSecretPayload(ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
					OrgID:     integration.OrgID,
					ProjectID: integration.ProjectID,
					SecretID:  integration.CredentialSecretID,
					Kind:      secrets.KindGeneric,
				})
			if err == nil {
				return integration, nil
			}
			if !permanentIntegrationIngressError(err) {
				retryErr = err
			}
		}
		if len(page) < integrationIngressPageSize {
			if retryErr != nil {
				return integrationstore.ProjectIntegrationRecord{}, retryErr
			}
			return integrationstore.ProjectIntegrationRecord{}, storeerr.ErrNotFound
		}
	}
}

// This only selects a PING key; the handler must still validate timestamp age and protocol.
func discordPingKeyMatches(integration integrationstore.ProjectIntegrationRecord, header http.Header, raw []byte) bool {
	key, err := hex.DecodeString(integrationruntime.DiscordInteractionPublicKey(integration.ProviderConfig))
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

func discordInteractionNotice(text string) (discord.InteractionResponse, error) {
	return discord.InteractionResponse{
		Type: discord.InteractionResponseChannelMessageWithSource,
		Data: &discord.InteractionResponseData{Content: text, Flags: discord.MessageFlagEphemeral},
	}, nil
}

func (s *Server) resolveDiscordInteraction(
	ctx context.Context, integration integrationstore.ProjectIntegrationRecord, input discord.Interaction,
) (discord.InteractionResponse, error) {
	if strings.HasPrefix(input.Data.CustomID, discord.ProfileChoiceCustomIDPrefix) {
		return s.discordProfileChoiceAction(ctx, integration, input)
	}
	if input.Type != discord.InteractionTypeMessageComponent && input.Type != discord.InteractionTypeModalSubmit {
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
	record, err := s.store.Execution().GetInteractionForHandlerCallback(ctx, integration.ProjectID, integration.ID, id)
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
	if destination == nil || destination.IntegrationType != integration.IntegrationType ||
		destination.IntegrationID != integration.ID {
		return discordInteractionNotice("This prompt is unavailable.")
	}
	var receipt integrationruntime.InteractionReceipt
	if json.Unmarshal(record.PresentationReceipt, &receipt) != nil ||
		receipt.Provider != integrationdefinition.ProviderDiscord ||
		receipt.MessageID == "" ||
		input.Message == nil || input.Message.ID != receipt.MessageID || input.Message.ChannelID != receipt.ChannelID ||
		input.Message.Author.ID != integration.ProviderAccountRef ||
		input.ChannelID != receipt.ChannelID {
		return discordInteractionNotice("This prompt is unavailable. Respond in Omnara.")
	}
	scope, err := integrationruntime.DiscordInteractionScope(*destination)
	if err != nil {
		return discord.InteractionResponse{}, err
	}
	channel := scope.ChannelID
	if scope.ThreadID != "" {
		channel = scope.ThreadID
	}
	if channel != input.ChannelID {
		return discordInteractionNotice("This prompt is unavailable.")
	}
	_, err = s.store.Execution().GetAgentInteractionForPresentation(ctx, record.ProjectID, record.AgentID, record.ID)
	if err != nil {
		if errors.Is(err, storeerr.ErrUnauthorized) || errors.Is(err, storeerr.ErrNotFound) {
			return discordInteractionNotice("This prompt is unavailable. Respond in Omnara.")
		}
		return discord.InteractionResponse{}, err
	}
	latest, err := s.store.Integrations().GetProjectIntegration(ctx, integration.ProjectID, integration.ID)
	if err != nil {
		return discord.InteractionResponse{}, err
	}
	if latest.State != integrationstore.ProjectIntegrationStateActive ||
		latest.SetupRevision != integration.SetupRevision {
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
	integrationActor, err := executionstore.IntegrationActorParams(integration.ID, actor.ID, &name)
	if err != nil {
		return discord.InteractionResponse{}, err
	}
	resolve := executionstore.ResolveAgentInteractionFromHandlerInput{
		IntegrationID: integration.ID, SourceSetupRevision: integration.SetupRevision,
		IntegrationType: integration.IntegrationType, Address: destination.Address,
		ResolveAgentInteractionInput: executionstore.ResolveAgentInteractionInput{
			ProjectID: record.ProjectID, AgentID: record.AgentID, ID: record.ID, Resolution: *resolution,
			Actor: &integrationActor,
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
