package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const integrationPermissionPromptCopyTimeout = 30 * time.Second

func (e Executor) postIntegrationPrompt(
	ctx context.Context,
	turn Turn,
	interaction executionstore.AgentInteractionRecord,
) error {
	target, ok, err := e.resolveIntegrationPromptTarget(ctx, turn, interaction)
	if err != nil || !ok {
		return err
	}
	return e.postIntegrationPromptToTarget(
		ctx,
		turn,
		target,
		interaction,
		func(ctx context.Context) error {
			return e.ensureIntegrationPostOwnership(ctx, turn)
		},
	)
}

func (e Executor) enqueueIntegrationPromptCopy(
	turn Turn,
	interaction executionstore.AgentInteractionRecord,
) {
	if e.BackgroundRunner == nil {
		return
	}
	if e.BackgroundRunner.TrySubmit(
		"integration_permission_prompt_copy",
		func(ctx context.Context) error {
			deliveryCtx, cancel := context.WithTimeout(ctx, integrationPermissionPromptCopyTimeout)
			defer cancel()
			if err := e.copyPermissionPromptToIntegration(
				deliveryCtx,
				turn,
				interaction.ID,
			); err != nil &&
				!(errors.Is(err, context.Canceled) && ctx.Err() != nil) {
				slog.WarnContext(
					deliveryCtx,
					"integration permission prompt copy failed",
					"agent_id",
					turn.AgentID,
					"interaction_id",
					interaction.ID,
					"error",
					err,
				)
			}
			return nil
		},
	) {
		return
	}
	slog.Warn(
		"integration permission prompt copy dropped",
		"agent_id",
		turn.AgentID,
		"interaction_id",
		interaction.ID,
	)
}

func (e Executor) copyPermissionPromptToIntegration(
	ctx context.Context,
	turn Turn,
	interactionID storage.ID,
) error {
	current, found, err := e.Store.Execution().GetAgentInteraction(
		ctx,
		turn.ProjectID,
		turn.AgentID,
		interactionID,
	)
	if err != nil {
		return err
	}
	if !found || current.State != executionstore.AgentInteractionStateOpen {
		return nil
	}
	target, ok, err := e.resolveIntegrationPromptTarget(ctx, turn, current)
	if err != nil || !ok {
		return err
	}
	if err := e.postIntegrationPromptToTarget(
		ctx,
		turn,
		target,
		current,
		nil,
	); err != nil {
		return fmt.Errorf("copy permission interaction to integration: %w", err)
	}
	return nil
}

func (e Executor) resolveIntegrationPromptTarget(
	ctx context.Context,
	turn Turn,
	interaction executionstore.AgentInteractionRecord,
) (integrationToolTarget, bool, error) {
	if interaction.State != executionstore.AgentInteractionStateOpen {
		return integrationToolTarget{}, false, nil
	}
	target, err := e.currentIntegrationToolTarget(ctx, turn)
	if err != nil {
		if errors.Is(err, errMissingIntegrationTarget) || errors.Is(err, errIntegrationDisabled) {
			return integrationToolTarget{}, false, nil
		}
		return integrationToolTarget{}, false, err
	}
	return target, true, nil
}

func (e Executor) postIntegrationPromptToTarget(
	ctx context.Context,
	turn Turn,
	target integrationToolTarget,
	interaction executionstore.AgentInteractionRecord,
	beforeAttempt func(context.Context) error,
) error {
	payload, err := integrationInteractionPromptPayload(target, turn.AgentID, interaction)
	if err != nil {
		return err
	}
	interactionID, err := publicid.Encode(publicid.KindAgentInteraction, interaction.ID)
	if err != nil {
		return err
	}
	slackTarget, err := slackMessageTarget(target)
	if err != nil {
		return err
	}
	return e.deliverIntegrationPrompt(
		ctx,
		slackTarget,
		payload,
		interactionID,
		beforeAttempt,
	)
}

func (e Executor) deliverIntegrationPrompt(
	ctx context.Context,
	target slack.MessageTarget,
	payload json.RawMessage,
	interactionID string,
	beforeAttempt func(context.Context) error,
) error {
	var rateLimitSlept time.Duration
	for attempt := 1; attempt <= integrationMessageSendAttempts; attempt++ {
		if beforeAttempt != nil {
			if err := beforeAttempt(ctx); err != nil {
				return err
			}
		}
		result, err := slack.PostPrompt(ctx, e.IntegrationHTTPClient, target, payload)
		if err != nil {
			return err
		}
		switch {
		case !result.RateLimited && !result.TransientFailure &&
			!result.PermanentFailure && !result.DeliveryUnknown:
			return nil
		case result.RateLimited:
			if slept, err := sleepForIntegrationRateLimit(
				ctx,
				result.RetryAfter,
				&rateLimitSlept,
				attempt,
			); err != nil {
				return err
			} else if slept {
				continue
			}
			return errors.New("integration provider rate limited prompt delivery")
		case result.TransientFailure || result.DeliveryUnknown:
			found, err := e.reconcileIntegrationPrompt(ctx, target, interactionID)
			if err != nil {
				return err
			}
			if found {
				return nil
			}
			if attempt < integrationMessageSendAttempts {
				continue
			}
			return errors.New("integration prompt delivery outcome is unknown")
		default:
			message := result.Message
			if message == "" {
				message = "integration prompt could not be delivered"
			}
			return errors.New(message)
		}
	}
	return errors.New("integration prompt delivery outcome is unknown")
}

func (e Executor) reconcileIntegrationPrompt(
	ctx context.Context,
	target slack.MessageTarget,
	interactionID string,
) (bool, error) {
	found, result, err := slack.ReconcilePrompt(
		ctx,
		e.IntegrationHTTPClient,
		target,
		interactionID,
	)
	if err == nil && !result.RateLimited && !result.TransientFailure &&
		!result.PermanentFailure && !result.DeliveryUnknown {
		return found, nil
	}
	message := "integration prompt delivery outcome is unknown"
	if err != nil {
		message += ": " + err.Error()
	} else if result.Message != "" {
		message += ": " + result.Message
	}
	return false, errors.New(message)
}

func (e Executor) postIntegrationPayloadToTarget(
	ctx context.Context,
	turn Turn,
	target integrationToolTarget,
	payload json.RawMessage,
	failureMessage string,
) error {
	slackTarget, err := slackMessageTarget(target)
	if err != nil {
		return err
	}
	var rateLimitSlept time.Duration
	for attempt := 1; attempt <= integrationMessageSendAttempts; attempt++ {
		if err := e.ensureIntegrationPostOwnership(ctx, turn); err != nil {
			return err
		}
		result, err := slack.PostPrompt(ctx, e.IntegrationHTTPClient, slackTarget, payload)
		if err != nil {
			return err
		}
		switch {
		case result.MessageID != "" ||
			(!result.RateLimited && !result.TransientFailure &&
				!result.PermanentFailure && !result.DeliveryUnknown):
			return nil
		case result.RateLimited:
			if slept, err := sleepForIntegrationRateLimit(ctx, result.RetryAfter, &rateLimitSlept, attempt); err != nil {
				return err
			} else if slept {
				continue
			}
			return errors.New(failureMessage)
		case result.TransientFailure || result.DeliveryUnknown:
			if attempt < integrationMessageSendAttempts {
				continue
			}
			return errors.New(failureMessage)
		default:
			return errors.New(failureMessage)
		}
	}
	return errors.New(failureMessage)
}

func integrationInteractionPromptPayload(
	target integrationToolTarget,
	agentID storage.ID,
	interaction executionstore.AgentInteractionRecord,
) (json.RawMessage, error) {
	interactionID, err := publicid.Encode(publicid.KindAgentInteraction, interaction.ID)
	if err != nil {
		return nil, err
	}
	agentPublicID, err := publicid.Encode(publicid.KindAgent, agentID)
	if err != nil {
		return nil, err
	}
	actionBase := slack.PromptActionValue{
		Type:                slack.PromptType,
		InteractionID:       interactionID,
		AgentID:             agentPublicID,
		IntegrationTargetID: target.PublicID,
	}
	text, blocks, err := integrationInteractionPromptBlocks(interaction, actionBase)
	if err != nil {
		return nil, err
	}
	slackTarget, err := slackMessageTarget(target)
	if err != nil {
		return nil, err
	}
	return slack.PromptPayload(slackTarget, text, blocks)
}

func integrationInteractionPromptBlocks(
	interaction executionstore.AgentInteractionRecord,
	actionBase slack.PromptActionValue,
) (string, []map[string]any, error) {
	value, err := interaction.Form()
	if err != nil {
		return "", nil, err
	}
	label, blocks := slack.InteractionFormPromptBlocks(value, actionBase)
	return label, blocks, nil
}
