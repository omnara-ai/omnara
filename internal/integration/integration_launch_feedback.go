package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integration/slack"
)

const (
	launchUnavailableMessage = "I couldn't start an agent because a configured profile is unavailable. " +
		"Ask an integration administrator to update the launch profiles."
	launchCapabilitiesUnavailableMessage = "I couldn't start an agent because a configured profile's capabilities " +
		"refer to a different integration. Ask an integration administrator to update the profile."
	discordProfileChoiceSetupMessage = "I couldn't offer a profile choice " +
		"because Discord interactions aren't configured. " +
		"Ask an integration administrator to configure the public key and interactions endpoint."
)

var errDiscordProfileChoiceSetup = errors.New("discord profile choices require interaction setup")

func (w *IntegrationLaunchWorkflow) launchUnavailable(
	ctx context.Context, input IntegrationLaunchContext, cause error,
) {
	log := w.Log
	if log == nil {
		log = slog.Default()
	}
	log.WarnContext(ctx, "integration launch unavailable",
		"integration_id", input.Integration.ID, "receipt_id", input.Receipt.ID, "error", cause)
	if !input.Event.Event.Mentioned {
		return
	}
	provider, ok := w.providers[input.Integration.Provider].(interface {
		NotifyLaunchUnavailable(context.Context, IntegrationLaunchContext, string) error
	})
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	message := launchUnavailableMessage
	switch {
	case errors.Is(cause, errDiscordProfileChoiceSetup):
		message = discordProfileChoiceSetupMessage
	case errors.Is(cause, agentconfig.ErrIntegrationCapabilityUnavailable):
		message = launchCapabilitiesUnavailableMessage
	}
	if err := provider.NotifyLaunchUnavailable(ctx, input, message); err != nil {
		log.WarnContext(ctx, "notify unavailable integration launch", "integration_id", input.Integration.ID, "error", err)
	}
}

func (p *SlackIntegrationInboxProvider) NotifyLaunchUnavailable(
	ctx context.Context, input IntegrationLaunchContext, message string,
) error {
	envelope, err := slack.DecodeEventsEnvelope(input.Receipt.Payload)
	if err != nil {
		return err
	}
	scope := input.Event.Event.Scope.Slack
	if scope == nil {
		return nil
	}
	// Channel mentions have sibling message callbacks; DMs only have message callbacks.
	if scope.ThreadTS != "" && envelope.Event.Type != "app_mention" {
		return nil
	}
	config, token, _, err := p.requestAccess(ctx, input.Integration)
	if err != nil {
		return err
	}
	result, err := slack.PostPlainMessage(ctx, config, slack.MessageTarget{
		Channel: scope.ChannelID, ThreadTS: scope.ThreadTS, BotToken: token,
	}, message)
	return slackFeedbackError(result, err)
}

func (p *DiscordIntegrationInboxProvider) NotifyLaunchUnavailable(
	ctx context.Context, input IntegrationLaunchContext, message string,
) error {
	scope := input.Event.Event.Scope.Discord
	if scope == nil {
		return nil
	}
	var metadata DiscordEventMetadata
	if err := json.Unmarshal(input.Event.Metadata, &metadata); err != nil {
		return err
	}
	client, _, err := p.requestAccess(ctx, input.Integration, nil)
	if err != nil {
		return err
	}
	target := discord.Scope{GuildID: scope.GuildID, ChannelID: scope.ChannelID, ThreadID: scope.ThreadID}
	// A missing profile must not create a conversation just to report the failure.
	if metadata.ThreadStarter {
		target.ThreadID = ""
	}
	_, err = client.CreateMessage(ctx, target, discord.MessageArgs{
		Content: message, Nonce: "u_" + base64.RawURLEncoding.EncodeToString(input.Receipt.ID[:]),
	})
	return err
}

func (p GitHubIntegrationInboxProvider) NotifyLaunchUnavailable(
	ctx context.Context, input IntegrationLaunchContext, message string,
) error {
	scope := input.Event.Event.Scope.GitHub
	if scope == nil || !input.Event.Event.Mentioned {
		return nil
	}
	client, err := p.requestAccess(ctx, input.Integration)
	if err != nil {
		return err
	}
	_, err = client.CreateDiscussionComment(ctx, github.Scope{
		RepositoryID: scope.RepositoryID, PullRequest: scope.PullRequest,
	}, message)
	return err
}
