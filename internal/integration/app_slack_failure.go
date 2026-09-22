package integration

import (
	"context"
	"fmt"
	"time"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (p *SlackAppInboxProvider) NotifyInboxFailure(ctx context.Context,
	app integrationstore.ProjectAppRecord, receipt integrationstore.IntegrationInboxRecord, text string,
) error {
	if err := checkInboxFailureReceipt(app, receipt, appdefinition.ProviderSlack); err != nil {
		return err
	}
	var scope appdefinition.Scope
	switch {
	case receipt.Source == integrationstore.IntegrationInboxSourceScheduled:
		var ok bool
		var err error
		scope, ok, err = scheduledInboxFailureScope(receipt, appdefinition.ProviderSlack)
		if err != nil || !ok {
			return err
		}
		if scope.Slack.ThreadTS == "" {
			return nil
		}
		text = scheduledInboxFailureMessage
	case receipt.Source != integrationstore.IntegrationInboxSourceProvider:
		return nil
	case len(receipt.Events) != 0:
		event, err := inboxFailureSelectedEvent(receipt, appdefinition.ProviderSlack)
		if err != nil {
			return err
		}
		scope = event.Event.Scope
		text = selectedInboxFailureMessage
	default:
		event, ok, err := NormalizeSlackAppEvent(app, receipt.Payload)
		if err != nil || !ok {
			return err
		}
		// Without a frozen recipient, only a direct request warrants feedback.
		// Slack normalization treats a DM as directly addressing the bot too.
		if len(receipt.Plan) == 0 && !event.Event.Mentioned {
			return nil
		}
		if event.Event.Mentioned && event.Event.Scope.Slack.ThreadTS != "" {
			// Slack sends both message and app_mention callbacks for a mention.
			// Only app_mention owns feedback; DMs and ordinary replies have no sibling.
			envelope, err := slack.DecodeEventsEnvelope(receipt.Payload)
			if err != nil || envelope.Event.Type != "app_mention" {
				return err
			}
		}
		scope = event.Event.Scope
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	config, token, _, err := p.requestAccess(ctx, app)
	if err != nil {
		return err
	}
	result, err := slack.PostPlainMessage(ctx, config, slack.MessageTarget{
		Channel: scope.Slack.ChannelID, ThreadTS: scope.Slack.ThreadTS, BotToken: token,
	}, text)
	return slackFeedbackError(result, err)
}

func slackFeedbackError(result slack.APIResult, err error) error {
	if err != nil {
		return err
	}
	if result.RateLimited || result.TransientFailure || result.PermanentFailure || result.DeliveryUnknown {
		return fmt.Errorf("slack feedback rejected: %s (%s)", result.Code, result.ProviderCode)
	}
	return nil
}
