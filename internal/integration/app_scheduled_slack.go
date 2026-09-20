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
	launch integrationstore.ScheduledAppLaunch,
	_ uuid.UUID,
	authority func(context.Context) error,
) (appdefinition.Scope, error) {
	config, token, _, err := p.requestAccess(ctx, app)
	if err != nil {
		return appdefinition.Scope{}, err
	}
	config.HTTPClient = slack.WithRequestCheck(config.HTTPClient, authority)
	scope, err := appdefinition.ResolveDestination(app.Provider, launch.Destination, nil)
	if err != nil {
		return appdefinition.Scope{}, err
	}
	target := slack.MessageTarget{Channel: scope.Slack.ChannelID, BotToken: token}
	result, err := slack.PostPlainMessage(ctx, config, target, launch.OpeningMessage)
	if err != nil {
		return appdefinition.Scope{}, err
	} // Request checks precede provider I/O.
	switch {
	case result.StatusCode >= 500 || result.TransientFailure || result.DeliveryUnknown:
		// A timeout or server error can follow publication. Fail this run rather
		// than deliberately repeat an unconfirmed post.
		return appdefinition.Scope{}, fmt.Errorf(
			"%w: Slack opening publication could not be confirmed", ErrScheduledLaunchFailed,
		)
	case result.RateLimited:
		return appdefinition.Scope{}, fmt.Errorf("scheduled Slack opening was rate limited")
	case result.PermanentFailure:
		return appdefinition.Scope{}, fmt.Errorf("%w: Slack rejected the opening message", ErrScheduledLaunchFailed)
	}
	channel, ts, ok := strings.Cut(result.MessageID, ":")
	if !ok || channel != target.Channel || ts == "" {
		return appdefinition.Scope{}, fmt.Errorf("%w: unexpected Slack opening address", ErrScheduledLaunchFailed)
	}
	return appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: channel, ThreadTS: ts}}, nil
}

func (p *SlackAppInboxProvider) EnsureScheduledThread(
	ctx context.Context,
	_ integrationstore.ProjectAppRecord,
	_ appdefinition.Scope,
	authority func(context.Context) error,
) error {
	// Slack's root is the thread. No extra provider mutation is needed.
	return authority(ctx)
}
