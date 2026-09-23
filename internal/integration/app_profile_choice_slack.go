package integration

import (
	"context"
	"fmt"

	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (p *SlackAppInboxProvider) PresentProfileChoice(
	ctx context.Context, appSetup integrationstore.ProjectAppRecord,
	choice integrationstore.AppProfileChoiceRecord, check func(context.Context) error,
) (string, string, error) {
	config, token, _, err := p.requestAccess(ctx, appSetup)
	if err != nil {
		return "", "", err
	}
	config.HTTPClient = slack.WithRequestCheck(config.HTTPClient, check)
	channel, thread, err := slack.Destination(choice.Address.Kind, choice.Address.Ref)
	if err != nil {
		return "", "", err
	}
	id, err := publicid.Encode(publicid.KindAppProfileChoice, choice.ID)
	if err != nil {
		return "", "", err
	}
	options := make([]slack.ProfileChoiceOption, 0, len(choice.Options))
	for _, option := range choice.Options {
		options = append(options, slack.ProfileChoiceOption{Key: option.Key, Name: option.Name})
	}
	message, result, err := slack.PostProfileChoice(ctx, config,
		slack.MessageTarget{Channel: channel, ThreadTS: thread, BotToken: token}, id, options,
		profileChoiceExpiryText(choice.ExpiresAt))
	if err != nil {
		return "", "", err
	}
	if result.RateLimited || result.TransientFailure || result.PermanentFailure || result.DeliveryUnknown {
		return "", "", fmt.Errorf("post Slack profile choice: %w", &slack.APIError{Result: result})
	}
	return channel, message, nil
}

func (p *SlackAppInboxProvider) DismissProfileChoice(
	ctx context.Context, appSetup integrationstore.ProjectAppRecord,
	choice integrationstore.AppProfileChoiceRecord, text string,
) error {
	config, token, _, err := p.requestAccess(ctx, appSetup)
	if err != nil {
		return err
	}
	result, err := slack.UpdateProfileChoice(ctx, config,
		slack.MessageTarget{Channel: choice.MessageChannelID, BotToken: token}, choice.MessageID, text)
	if err != nil {
		return err
	}
	if result.RateLimited || result.TransientFailure || result.PermanentFailure || result.DeliveryUnknown {
		return fmt.Errorf("update Slack profile choice: %w", &slack.APIError{Result: result})
	}
	return nil
}
