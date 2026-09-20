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
	receiptID uuid.UUID,
	preparation integrationstore.ScheduledLaunchPreparation,
	fresh bool,
	authority func(context.Context) error,
) (appdefinition.Scope, bool, error) {
	config, token, _, err := p.requestAccess(ctx, app)
	if err != nil {
		return appdefinition.Scope{}, true, err
	}
	config.HTTPClient = slack.WithRequestCheck(config.HTTPClient, authority)
	scope, err := appdefinition.ResolveDestination(app.Provider, launch.Destination, nil)
	if err != nil {
		return appdefinition.Scope{}, true, err
	}
	target := slack.MessageTarget{Channel: scope.Slack.ChannelID, BotToken: token}
	identity, err := slack.ParseInstallIdentity(app.ProviderIdentity)
	if err != nil {
		return appdefinition.Scope{}, true, err
	}
	var messageID string
	if fresh {
		result, err := slack.PostScheduledMessage(ctx, config, target, receiptID.String(), launch.OpeningMessage)
		if err != nil {
			return appdefinition.Scope{}, true, err
		} // request checks precede provider I/O
		switch {
		case result.MessageID != "":
			messageID = result.MessageID
		case result.StatusCode < 500 && result.RateLimited:
			return appdefinition.Scope{}, true, fmt.Errorf("scheduled Slack opening was rate limited")
		case result.StatusCode < 500 && result.PermanentFailure:
			return appdefinition.Scope{}, true, fmt.Errorf("%w: Slack rejected the opening message", ErrScheduledLaunchFailed)
		}
	}
	if messageID == "" {
		// Timeouts, 5xx and missing acknowledgements never justify another POST.
		var found bool
		var result slack.APIResult
		messageID, found, result, err = slack.ReconcileScheduledMessage(
			ctx, config, target, receiptID.String(), identity.BotUserID, *preparation.AttemptedAt,
		)
		if err != nil {
			return appdefinition.Scope{}, false, err
		}
		if result.RateLimited || result.TransientFailure || result.DeliveryUnknown {
			return appdefinition.Scope{}, false, fmt.Errorf("scheduled Slack opening reconciliation unavailable")
		}
		if !found {
			// Slack may commit the post after a timed-out response. Later attempts
			// only read back; the inbox's existing retry budget bounds this wait.
			return appdefinition.Scope{}, false, fmt.Errorf("opening publication not yet confirmed; no replacement sent")
		}
	}
	channel, ts, ok := strings.Cut(messageID, ":")
	if !ok || channel != target.Channel || ts == "" {
		return appdefinition.Scope{}, false, fmt.Errorf("%w: unexpected Slack opening address", ErrScheduledLaunchFailed)
	}
	return appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: channel, ThreadTS: ts}}, false, nil
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
