package integration

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (p *SlackIntegrationInboxProvider) PublishScheduledRoot(
	ctx context.Context,
	integration integrationstore.ProjectIntegrationRecord,
	launch integrationdefinition.ScheduledThreadLaunch,
	_ uuid.UUID,
	authority func(context.Context) error,
) (integrationdefinition.Scope, error) {
	config, token, _, err := p.requestAccess(ctx, integration)
	if err != nil {
		return integrationdefinition.Scope{}, err
	}
	config.HTTPClient = slack.WithRequestCheck(config.HTTPClient, authority)
	target := slack.MessageTarget{Channel: launch.ChannelID, BotToken: token}
	result, err := slack.PostPlainMessage(ctx, config, target, launch.OpeningMessage)
	if err != nil {
		return integrationdefinition.Scope{}, err
	}
	switch {
	case result.StatusCode >= 500 || result.TransientFailure || result.DeliveryUnknown:
		// Slack may have published before the timeout or server error; retrying could duplicate it.
		return integrationdefinition.Scope{}, fmt.Errorf(
			"%w: Slack opening publication could not be confirmed", ErrScheduledActionFailed,
		)
	case result.RateLimited:
		return integrationdefinition.Scope{}, fmt.Errorf("scheduled Slack opening: %w", &slack.APIError{Result: result})
	case result.PermanentFailure:
		return integrationdefinition.Scope{}, fmt.Errorf("%w: Slack rejected the opening message", ErrScheduledActionFailed)
	}
	channel, ts, ok := strings.Cut(result.MessageID, ":")
	if !ok || channel != target.Channel || ts == "" {
		return integrationdefinition.Scope{}, fmt.Errorf("%w: unexpected Slack opening address", ErrScheduledActionFailed)
	}
	return integrationdefinition.Scope{Slack: &integrationdefinition.SlackScope{ChannelID: channel, ThreadTS: ts}}, nil
}

func (p *SlackIntegrationInboxProvider) EnsureScheduledThread(
	ctx context.Context,
	_ integrationstore.ProjectIntegrationRecord,
	_ integrationdefinition.Scope,
	authority func(context.Context) error,
) error {
	return authority(ctx)
}
