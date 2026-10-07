package integration

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const launchUnavailableMessage = "The Omnara agent is unavailable. Please contact the integration owner."
const launchNotSetUpMessage = "This bot isn't available for requests. Ask its owner for help."

var errIntegrationMentionUnrouted = errors.New("mention matched no agent or launcher")

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
	message := launchUnavailableMessage
	if errors.Is(cause, errIntegrationMentionUnrouted) {
		message = launchNotSetUpMessage
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := provider.NotifyLaunchUnavailable(ctx, input, message); err != nil {
		log.WarnContext(ctx, "notify unavailable integration launch", "integration_id", input.Integration.ID, "error", err)
	}
}
