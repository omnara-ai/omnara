package integration

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (p *SlackAppInboxProvider) PublishScheduledRoot(
	ctx context.Context,
	app integrationstore.ProjectAppRecord,
	launch appdefinition.ScheduledThreadLaunch,
	_ uuid.UUID,
	authority func(context.Context) error,
) (appdefinition.Scope, error) {
	config, token, _, err := p.requestAccess(ctx, app)
	if err != nil {
		return appdefinition.Scope{}, err
	}
	config.HTTPClient = slack.WithRequestCheck(config.HTTPClient, authority)
	target := slack.MessageTarget{Channel: launch.ChannelID, BotToken: token}
	result, err := slack.PostPlainMessage(ctx, config, target, launch.OpeningMessage)
	if err != nil {
		return appdefinition.Scope{}, err
	}
	switch {
	case result.StatusCode >= 500 || result.TransientFailure || result.DeliveryUnknown:
		// Slack may have published before the timeout or server error; retrying could duplicate it.
		return appdefinition.Scope{}, fmt.Errorf(
			"%w: Slack opening publication could not be confirmed", ErrScheduledActionFailed,
		)
	case result.RateLimited:
		return appdefinition.Scope{}, fmt.Errorf("scheduled Slack opening was rate limited")
	case result.PermanentFailure:
		return appdefinition.Scope{}, fmt.Errorf("%w: Slack rejected the opening message", ErrScheduledActionFailed)
	}
	channel, ts, ok := strings.Cut(result.MessageID, ":")
	if !ok || channel != target.Channel || ts == "" {
		return appdefinition.Scope{}, fmt.Errorf("%w: unexpected Slack opening address", ErrScheduledActionFailed)
	}
	return appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: channel, ThreadTS: ts}}, nil
}

func (p *SlackAppInboxProvider) EnsureScheduledThread(
	ctx context.Context,
	_ integrationstore.ProjectAppRecord,
	_ appdefinition.Scope,
	authority func(context.Context) error,
) error {
	return authority(ctx)
}
