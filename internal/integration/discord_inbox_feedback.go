package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (p *DiscordIntegrationInboxProvider) NotifyInboxFailure(ctx context.Context,
	integration integrationstore.ProjectIntegrationRecord, receipt integrationstore.IntegrationInboxRecord, text string,
) error {
	if err := checkInboxFailureReceipt(integration, receipt, integrationdefinition.ProviderDiscord); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, discord.OperationTimeout)
	defer cancel()
	var scope discord.Scope
	var root bool
	var source *discord.MessageEvent
	switch {
	case receipt.Source == integrationstore.IntegrationInboxSourceScheduled:
		known, ok, err := scheduledInboxFailureScope(receipt, integrationdefinition.ProviderDiscord)
		if err != nil || !ok {
			return err
		}
		if known.Discord.ThreadID == "" || known.Discord.GuildID == "" {
			return nil
		}
		scope = discord.Scope(*known.Discord)
		root, text = true, scheduledInboxFailureMessage
	case receipt.Source != integrationstore.IntegrationInboxSourceProvider:
		return nil
	case len(receipt.Events) != 0:
		event, err := inboxFailureSelectedEvent(receipt, integrationdefinition.ProviderDiscord)
		if err != nil {
			return err
		}
		var metadata DiscordEventMetadata
		if json.Unmarshal(event.Metadata, &metadata) != nil {
			return fmt.Errorf("invalid Discord failure source metadata")
		}
		scope = discord.Scope(*event.Event.Scope.Discord)
		text = selectedInboxFailureMessage
		root = metadata.ThreadStarter
		if scope.GuildID == "" || scope.ThreadID == "" ||
			(root && (metadata.SourceChannelID != scope.ChannelID || metadata.MessageID != scope.ThreadID)) ||
			(!root && metadata.SourceChannelID != scope.ThreadID) {
			return fmt.Errorf("discord failure source differs from its conversation")
		}
	default:
		message, ok, err := discordInboxMessage(integration, receipt.Payload)
		if err != nil || !ok {
			return err
		}
		if len(receipt.Plan) == 0 && !message.MentionsBot {
			return nil
		}
		source = &message
	}
	client, _, err := p.requestAccess(ctx, integration, nil)
	if err != nil {
		return err
	}
	if source != nil {
		channel, err := client.GetChannel(ctx, source.Message.ChannelID)
		if err != nil {
			return err
		}
		scope, err = discordInboxMessageScope(source.Message, channel)
		if err != nil {
			return err
		}
		root = scope.ThreadID == ""
		if root {
			scope.ThreadID = source.Message.ID
		}
	}
	if root {
		if _, err := client.GetScopedChannel(ctx, scope); err != nil {
			var apiErr *discord.APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
				return err
			}
			messageID := scope.ThreadID
			scope.ThreadID = ""
			if _, err := client.GetMessage(ctx, scope, messageID); err != nil {
				return err
			}
		}
	}
	nonce := "f_" + base64.RawURLEncoding.EncodeToString(receipt.ID[:])
	_, err = client.CreateMessage(ctx, scope, discord.MessageArgs{Content: text, Nonce: nonce})
	return err
}
