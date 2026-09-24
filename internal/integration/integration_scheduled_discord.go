package integration

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (p *DiscordIntegrationInboxProvider) PublishScheduledRoot(
	ctx context.Context,
	integration integrationstore.ProjectIntegrationRecord,
	launch integrationdefinition.ScheduledThreadLaunch,
	receiptID uuid.UUID,
	authority func(context.Context) error,
) (integrationdefinition.Scope, error) {
	client, _, err := p.requestAccess(ctx, integration, authority)
	if err != nil {
		return integrationdefinition.Scope{}, err
	}
	parent := discord.Scope{ChannelID: launch.ChannelID}
	channel, err := client.GetScopedChannel(ctx, parent)
	if err != nil {
		return integrationdefinition.Scope{}, scheduledDiscordError(err)
	}
	if channel.GuildID == "" || (channel.Type != 0 && channel.Type != 5) {
		return integrationdefinition.Scope{}, fmt.Errorf(
			"%w: Discord requires a server text or announcement channel", ErrScheduledActionFailed,
		)
	}
	parent.GuildID = channel.GuildID
	nonce := base64.RawURLEncoding.EncodeToString(receiptID[:])
	root, err := client.CreateMessage(ctx, parent, discord.MessageArgs{Content: launch.OpeningMessage, Nonce: nonce})
	if err != nil {
		var apiErr *discord.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != discord.DeliveryUnknown {
			return integrationdefinition.Scope{}, scheduledDiscordError(err)
		}
	}
	if err != nil || root.ID == "" {
		return integrationdefinition.Scope{}, fmt.Errorf(
			"%w: opening publication outcome is unknown; no replacement sent", ErrScheduledActionFailed,
		)
	}
	return integrationdefinition.Scope{Discord: &integrationdefinition.DiscordScope{
		GuildID: channel.GuildID, ChannelID: channel.ID, ThreadID: root.ID,
	}}, nil
}

func (p *DiscordIntegrationInboxProvider) EnsureScheduledThread(
	ctx context.Context,
	integration integrationstore.ProjectIntegrationRecord,
	scope integrationdefinition.Scope,
	authority func(context.Context) error,
) error {
	if err := scope.Validate(integrationdefinition.ProviderDiscord); err != nil {
		return err
	}
	client, _, err := p.requestAccess(ctx, integration, authority)
	if err != nil {
		return err
	}
	_, err = client.EnsureThread(
		ctx,
		discord.Scope{GuildID: scope.Discord.GuildID, ChannelID: scope.Discord.ChannelID},
		scope.Discord.ThreadID,
		"Omnara scheduled task",
	)
	return scheduledDiscordError(err)
}

func scheduledDiscordError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *discord.APIError
	if errors.As(err, &apiErr) && (apiErr.Code == discord.PermanentFailure || apiErr.Code == discord.ScopeMismatch) {
		return fmt.Errorf("%w: Discord rejected the destination or request", ErrScheduledActionFailed)
	}
	return err
}
