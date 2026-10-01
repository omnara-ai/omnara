package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integration/slack"
)

const launchUnavailableMessage = "The Omnara agent is unavailable. Please contact the integration owner."

var errDiscordProfileChoiceSetup = errors.New("discord profile choices require interaction setup")

type integrationLaunchFeedback struct {
	input IntegrationLaunchContext
	cause error
}

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
	if err := provider.NotifyLaunchUnavailable(ctx, input, launchUnavailableMessage); err != nil {
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
