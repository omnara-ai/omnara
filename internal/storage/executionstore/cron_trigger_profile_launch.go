package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type LaunchCronTriggerAgentInput struct {
	Trigger        ClaimedCronTrigger
	AgentConfigID  uuid.UUID
	Message        string
	Actor          *ActorParams
	IdempotencyKey string
}

// LaunchCronTriggerAgent commits launch, explicit grants, initial input and
// firing completion together. Lost claims roll back all product effects. The
// in-memory claim carries the accepted configuration just like its message;
// an expired-lease retry reads the current schedule rather than a durable copy.
func (s *Store) LaunchCronTriggerAgent(
	ctx context.Context, input LaunchCronTriggerAgentInput,
) (LaunchAgentResult, error) {
	trigger := input.Trigger
	if trigger.ProjectID == uuid.Nil || trigger.TriggerID == uuid.Nil || trigger.ClaimToken == uuid.Nil ||
		trigger.Target.ID == uuid.Nil || input.IdempotencyKey == "" {
		return LaunchAgentResult{}, errors.New("project, trigger, claim, profile and idempotency key are required")
	}
	if trigger.Target.Kind != CronTriggerTargetAgentProfile {
		return LaunchAgentResult{}, storeerr.InvalidRequest(errors.New("cron launch requires a profile target"))
	}
	launchInput, err := validateLaunchAgentInput(LaunchAgentInput{
		ProjectID: trigger.ProjectID, ProfileID: trigger.Target.ID, AgentConfigID: input.AgentConfigID,
		LaunchedBy: identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeSystem, ID: trigger.TriggerID},
		Message:    input.Message, MessageActor: input.Actor, IdempotencyKey: input.IdempotencyKey,
		ChannelBindings: trigger.ChannelBindings,
	})
	if err != nil {
		return LaunchAgentResult{}, err
	}
	return storeutil.RetryTransaction(ctx, "launch_cron_trigger_agent", func() (LaunchAgentResult, error) {
		txNotifications := s.newTxNotifications()
		tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return LaunchAgentResult{}, fmt.Errorf("begin cron profile launch: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		q := s.q.WithTx(tx)
		result, err := s.launchAgentTx(ctx, tx, q, txNotifications, launchInput)
		if err != nil {
			return LaunchAgentResult{}, err
		}
		// Profile/agent/channel locks precede the cron row, matching their deletion
		// and configuration paths. A lost completion fence rolls back the launch.
		if err := completeCronTriggerFiringTx(ctx, q, CompleteCronTriggerFiringInput{
			ProjectID: trigger.ProjectID, TriggerID: trigger.TriggerID, ClaimToken: trigger.ClaimToken, Fired: true,
		}); err != nil {
			return LaunchAgentResult{}, err
		}
		if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "launch cron profile agent"); err != nil {
			return LaunchAgentResult{}, err
		}
		return result, nil
	})
}
