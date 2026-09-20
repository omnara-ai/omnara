package integration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (c *AppInboxConsumer) notifySlackLaunchFailure(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	appSetup integrationstore.ProjectAppRecord,
	payload []byte,
	provider *SlackAppInboxProvider,
	failure error,
) {
	failed := appLaunchFailureForNotice(failure)
	if failed == nil {
		return
	}
	// One normalized Slack message has one conversation. Its first failed launch
	// owns the notice even when N recipients independently fail.
	claimed := false
	err := c.inbox.WithIntegrationInboxLease(ctx, lease, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		var err error
		claimed, err = work.ClaimLaunchFailureNotice(ctx, failed.Slot)
		return err
	})
	if err != nil || !claimed {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	envelope, _, ok, err := slackInboxEnvelope(appSetup, payload)
	if err != nil || !ok {
		return
	}
	config, token, _, err := provider.requestAccess(ctx, appSetup)
	if err == nil {
		thread := envelope.Event.ThreadTS
		if thread == "" && envelope.Event.ChannelType != "im" {
			thread = envelope.Event.TS
		}
		var result slack.APIResult
		result, err = slack.PostPlainMessage(
			ctx,
			config,
			slack.MessageTarget{Channel: envelope.Event.Channel, ThreadTS: thread, BotToken: token},
			slack.AgentRequestFailureMessage,
		)
		err = slackFeedbackError(result, err)
	}
	if err != nil {
		slog.WarnContext(
			ctx,
			"Slack launch failure notice failed",
			"receipt_id",
			lease.ReceiptID,
			"app_id",
			appSetup.ID,
			"error",
			err,
		)
	}
}

// Joined admission failures preserve slot order; skip input or unrelated failures
// when selecting the one actionable launch failure for this message.
func appLaunchFailureForNotice(err error) *AppSlotAdmissionError {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if result := appLaunchFailureForNotice(child); result != nil {
				return result
			}
		}
		return nil
	}
	var failed *AppSlotAdmissionError
	if errors.As(err, &failed) && failed.Launch &&
		(errors.Is(failed.Err, storeerr.ErrStateTransitionConflict) ||
			errors.Is(failed.Err, storeerr.ErrManagedWorkAdmissionDenied)) {
		return failed
	}
	return nil
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
