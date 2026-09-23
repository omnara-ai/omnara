package apps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/apps/discord"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
)

func (p *DiscordAppInboxProvider) PresentProfileChoice(
	ctx context.Context, appSetup appstore.ProjectAppRecord,
	choice appstore.AppProfileChoiceRecord, check func(context.Context) error,
) (string, string, error) {
	if DiscordInteractionPublicKey(appSetup.ProviderConfig) == "" {
		return "", "", fmt.Errorf("configure Discord's public key and interactions endpoint before offering multiple profiles: %w", ErrAppLaunchUnavailable)
	}
	var source AppEvent
	if err := json.Unmarshal(choice.Event, &source); err != nil || source.Event.Scope.Discord == nil {
		return "", "", fmt.Errorf("invalid Discord profile choice source")
	}
	var metadata DiscordEventMetadata
	if err := json.Unmarshal(source.Metadata, &metadata); err != nil {
		return "", "", err
	}
	scope := source.Event.Scope.Discord
	client, _, err := p.requestAccess(ctx, appSetup, check)
	if err != nil {
		return "", "", err
	}
	target := discord.Scope{GuildID: scope.GuildID, ChannelID: scope.ChannelID, ThreadID: scope.ThreadID}
	if metadata.ThreadStarter {
		_, err := client.EnsureThread(ctx, discord.Scope{GuildID: scope.GuildID, ChannelID: scope.ChannelID},
			metadata.MessageID, discordConversationName(appSetup))
		if err != nil {
			return "", "", err
		}
	}
	id, err := publicid.Encode(publicid.KindAppProfileChoice, choice.ID)
	if err != nil {
		return "", "", err
	}
	options := make([]discord.ProfileChoiceOption, 0, len(choice.Options))
	for _, option := range choice.Options {
		options = append(options, discord.ProfileChoiceOption{Key: option.Key, Name: option.Name})
	}
	text, rows, err := discord.ProfileChoicePrompt(id, options,
		profileChoiceExpiryText(choice.ExpiresAt))
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256([]byte(id))
	message, err := client.CreateMessage(ctx, target, discord.MessageArgs{
		Content: text, Components: rows, Nonce: hex.EncodeToString(digest[:12]),
	})
	if err != nil {
		return "", "", err
	}
	return message.ChannelID, message.ID, nil
}

func (p *DiscordAppInboxProvider) DismissProfileChoice(
	ctx context.Context, appSetup appstore.ProjectAppRecord,
	choice appstore.AppProfileChoiceRecord, text string,
) error {
	var source AppEvent
	if json.Unmarshal(choice.Event, &source) != nil || source.Event.Scope.Discord == nil {
		return fmt.Errorf("invalid Discord profile choice source")
	}
	client, _, err := p.requestAccess(ctx, appSetup, nil)
	if err != nil {
		return err
	}
	scope := source.Event.Scope.Discord
	_, err = client.EditMessage(ctx,
		discord.Scope{GuildID: scope.GuildID, ChannelID: scope.ChannelID, ThreadID: scope.ThreadID},
		choice.MessageID, text, nil)
	return err
}
