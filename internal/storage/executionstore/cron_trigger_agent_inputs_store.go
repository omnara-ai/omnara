package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
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
	if trigger.ProjectID == uuid.Nil || trigger.TriggerID == uuid.Nil || trigger.ClaimToken == uuid.Nil ||
		trigger.Target.ID == uuid.Nil || input.IdempotencyKey == "" {
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

	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin cron trigger input delivery: %w", err)
	}
	defer func() { _ = unit.Rollback(ctx) }()
	tx := unit.DB()

	qtx := dbsqlc.New(tx)
	// Input creation locks the agent before completion locks the trigger, matching
	// the lock order used when archiving an agent and deleting its cron triggers.
	if _, err := createAgentContentInputTx(ctx, unit, qtx, contentInput, contentBlocks); err != nil {
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
	return unit.Commit(ctx, "deliver cron trigger input")
}
