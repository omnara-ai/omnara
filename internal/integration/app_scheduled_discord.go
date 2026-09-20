package integration

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (p *DiscordAppInboxProvider) PublishScheduledRoot(
	ctx context.Context,
	app integrationstore.ProjectAppRecord,
	launch integrationstore.ScheduledAppLaunch,
	receiptID uuid.UUID,
	_ integrationstore.ScheduledLaunchPreparation,
	fresh bool,
	authority func(context.Context) error,
) (appdefinition.Scope, bool, error) {
	// The consumer reuses saved roots without calling this method. Without one,
	// a resumed attempt cannot recover from history: Discord's nonce is ephemeral.
	if !fresh {
		return appdefinition.Scope{}, false, fmt.Errorf(
			"%w: opening publication outcome is unknown; no replacement sent", ErrScheduledLaunchFailed,
		)
	}
	client, _, err := p.requestAccess(ctx, app, authority)
	if err != nil {
		return appdefinition.Scope{}, true, err
	}
	destination, err := appdefinition.ResolveDestination(app.Provider, launch.Destination, nil)
	if err != nil {
		return appdefinition.Scope{}, true, err
	}
	parent := discord.Scope{GuildID: destination.Discord.GuildID, ChannelID: destination.Discord.ChannelID}
	channel, err := client.GetScopedChannel(ctx, parent)
	if err != nil {
		return appdefinition.Scope{}, true, scheduledDiscordError(err)
	}
	if channel.GuildID == "" || (channel.Type != 0 && channel.Type != 5) {
		return appdefinition.Scope{}, true, fmt.Errorf(
			"%w: Discord requires a server text or announcement channel", ErrScheduledLaunchFailed,
		)
	}
	parent.GuildID = channel.GuildID
	nonce := base64.RawURLEncoding.EncodeToString(receiptID[:])
	// CreateMessage bounds same-nonce retries to its initial operation. An
	// uncertain outcome after that operation must not trigger another send.
	root, err := client.CreateMessage(ctx, parent, discord.MessageArgs{Content: launch.OpeningMessage, Nonce: nonce})
	if err != nil {
		var apiErr *discord.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != discord.DeliveryUnknown {
			return appdefinition.Scope{}, true, scheduledDiscordError(err)
		}
	}
	if err != nil || root.ID == "" {
		return appdefinition.Scope{}, false, fmt.Errorf(
			"%w: opening publication outcome is unknown; no replacement sent", ErrScheduledLaunchFailed,
		)
	}
	return appdefinition.Scope{Discord: &appdefinition.DiscordScope{
		GuildID: channel.GuildID, ChannelID: channel.ID, ThreadID: root.ID,
	}}, false, nil
}

func (p *DiscordAppInboxProvider) EnsureScheduledThread(
	ctx context.Context,
	app integrationstore.ProjectAppRecord,
	scope appdefinition.Scope,
	authority func(context.Context) error,
) error {
	if err := scope.Validate(appdefinition.ProviderDiscord); err != nil {
		return err
	}
	client, _, err := p.requestAccess(ctx, app, authority)
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
		return fmt.Errorf("%w: Discord rejected the destination or request", ErrScheduledLaunchFailed)
	}
	return err
}
