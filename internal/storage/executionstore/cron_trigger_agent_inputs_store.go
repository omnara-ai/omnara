package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type CreateCronTriggerAgentInputInput struct {
	Trigger        ClaimedCronTrigger
	Actor          *ActorParams
	ContentBlocks  json.RawMessage
	IdempotencyKey string
}

// CreateCronTriggerAgentInput commits delivery and firing completion together.
// A failed completion, including a lost claim, rolls back the input as well.
func (s *Store) CreateCronTriggerAgentInput(ctx context.Context, input CreateCronTriggerAgentInputInput) error {
	trigger := input.Trigger
	if isNilID(trigger.ProjectID) || isNilID(trigger.TriggerID) || isNilID(trigger.ClaimToken) ||
		isNilID(trigger.Target.ID) || input.IdempotencyKey == "" {
		return errors.New("project, cron trigger, claim token, target agent, and idempotency key are required")
	}
	if trigger.Target.Kind != CronTriggerTargetAgent {
		return storeerr.InvalidRequest(errors.New("cron input delivery requires an agent target"))
	}
	if err := validateCronTriggerDeliveryMode(trigger.Target.Kind, trigger.Target.DeliveryMode); err != nil {
		return err
	}
	contentInput, err := prepareCreateAgentContentInput(CreateAgentContentInputInput{
		ProjectID:      trigger.ProjectID,
		AgentID:        trigger.Target.ID,
		Actor:          input.Actor,
		DeliveryMode:   AgentInputDeliveryMode(trigger.Target.DeliveryMode),
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return err
	}
	contentBlocks, err := parseAgentInputContentBlocks(input.ContentBlocks)
	if err != nil {
		return err
	}
	contentInput.ContentBlocks, err = marshalAgentInputContentBlocks(contentBlocks)
	if err != nil {
		return err
	}

	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin cron trigger input delivery: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	agent, err := loadAgentInProjectTx(ctx, tx, trigger.ProjectID, trigger.Target.ID)
	if err != nil {
		return err
	}
	// Input creation locks the agent before completion locks the trigger, matching
	// the lock order used when archiving an agent and deleting its cron triggers.
	if _, err := createAgentContentInputTx(ctx, txNotifications, tx, qtx, agent, contentInput, contentBlocks); err != nil {
		return err
	}
	if err := completeCronTriggerFiringTx(ctx, qtx, CompleteCronTriggerFiringInput{
		ProjectID:  trigger.ProjectID,
		TriggerID:  trigger.TriggerID,
		ClaimToken: trigger.ClaimToken,
		Fired:      true,
	}); err != nil {
		return err
	}
	return s.commitTxWithNotifications(ctx, tx, txNotifications, "deliver cron trigger input")
}
