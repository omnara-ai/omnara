package executionstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/executionpolicy"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	scheduledMessageIdempotencyScope = "scheduled_message"
	MaxScheduledMessageDelay         = 24 * time.Hour
)

type ScheduledMessage struct {
	ID           uuid.UUID
	Text         string
	CreatedAt    time.Time
	ScheduledFor time.Time
}

type CreateScheduledMessageInput struct {
	SourceScheduledFor *time.Time
	Delay              time.Duration
	Compose            func(createdAt, scheduledFor time.Time) string
}

func (r *ToolCallReader) DatabaseNow(ctx context.Context) (time.Time, error) {
	now, err := r.transaction.q.DBNow(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("read database time: %w", err)
	}
	return now, nil
}

func (r *ToolCallReader) ListOwnScheduledMessages(ctx context.Context) ([]ScheduledMessage, error) {
	t := r.transaction
	actorID, found, err := t.ownActorID(ctx)
	if err != nil || !found {
		return []ScheduledMessage{}, err
	}
	rows, err := t.q.ListScheduledAgentInputsByActor(ctx, dbsqlc.ListScheduledAgentInputsByActorParams{
		ProjectID: t.input.ProjectID,
		AgentID:   t.input.AgentID,
		ActorID:   actorID,
		RowLimit:  executionpolicy.MaxPendingAgentContentInputs,
	})
	if err != nil {
		return nil, fmt.Errorf("list scheduled messages: %w", err)
	}
	messages := make([]ScheduledMessage, 0, len(rows))
	inputIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		if row.ScheduledFor == nil {
			return nil, fmt.Errorf("scheduled input %s has no scheduled time", row.ID)
		}
		messages = append(messages, ScheduledMessage{ID: row.ID, CreatedAt: row.QueuedAt, ScheduledFor: *row.ScheduledFor})
		inputIDs = append(inputIDs, row.ID)
	}
	if len(inputIDs) == 0 {
		return messages, nil
	}
	blocks, err := t.q.ListContentBlocksForAgentInputs(ctx, dbsqlc.ListContentBlocksForAgentInputsParams{
		ProjectID: t.input.ProjectID, AgentID: t.input.AgentID, AgentInputIds: inputIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("list scheduled message content: %w", err)
	}
	texts := make(map[uuid.UUID][]string, len(inputIDs))
	for _, block := range blocks {
		if block.OwnerAgentInputID != nil && block.BlockKind == string(ContentBlockKindText) {
			texts[*block.OwnerAgentInputID] = append(texts[*block.OwnerAgentInputID], block.TextContent)
		}
	}
	for index := range messages {
		messages[index].Text = strings.Join(texts[messages[index].ID], "\n")
	}
	return messages, nil
}

func CreateScheduledMessageForToolCall(
	input CreateScheduledMessageInput,
	completion ToolCallCompletionBuilder[ScheduledMessage],
) ToolCallCommand {
	return toolCallCommandFunc(func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		if completion == nil {
			return nil, errors.New("scheduled message completion builder is required")
		}
		if err := tx.lockForMutation(ctx); err != nil {
			return nil, err
		}
		message, err := tx.createScheduledMessage(ctx, input)
		if err != nil {
			return nil, err
		}
		toolCompletion, err := completion(message)
		if err != nil {
			return nil, err
		}
		if _, err := tx.completeToolCall(ctx, toolCompletion); err != nil {
			return nil, err
		}
		return message, nil
	})
}

func CancelScheduledMessageForToolCall(inputID uuid.UUID, completion ToolCallCompletionInput) ToolCallCommand {
	return toolCallCommandFunc(func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		if err := tx.lockForMutation(ctx); err != nil {
			return nil, err
		}
		if err := tx.cancelOwnScheduledMessage(ctx, inputID); err != nil {
			return nil, err
		}
		if _, err := tx.completeToolCall(ctx, completion); err != nil {
			return nil, err
		}
		return nil, nil
	})
}

func (t *toolCallTransaction) ownActorParams(ctx context.Context) (*ActorParams, error) {
	agent, err := loadAgentInProjectTx(ctx, t.tx, t.input.ProjectID, t.input.AgentID)
	if err != nil {
		return nil, err
	}
	return SubagentActorParams(agent.OrgID, agent)
}

func (t *toolCallTransaction) ownActorID(ctx context.Context) (uuid.UUID, bool, error) {
	params, err := t.ownActorParams(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}
	actor, err := t.q.GetActorByIdentity(ctx, dbsqlc.GetActorByIdentityParams{
		ProjectID:        t.input.ProjectID,
		Provider:         string(params.Provider),
		ProviderTenantID: &params.ProviderTenantID,
		ProviderUserID:   params.ProviderUserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("load agent actor: %w", err)
	}
	return actor.ID, true, nil
}

func (t *toolCallTransaction) createScheduledMessage(
	ctx context.Context,
	input CreateScheduledMessageInput,
) (ScheduledMessage, error) {
	if input.Compose == nil {
		return ScheduledMessage{}, errors.New("scheduled message composer is required")
	}
	if (input.SourceScheduledFor == nil) == (input.Delay == 0) {
		return ScheduledMessage{}, storeerr.InvalidRequest(errors.New("provide exactly one of timestamp or delay_seconds"))
	}
	now, err := t.q.DBNow(ctx)
	if err != nil {
		return ScheduledMessage{}, fmt.Errorf("read database time: %w", err)
	}
	scheduledFor := now.Add(input.Delay)
	if input.SourceScheduledFor != nil {
		scheduledFor = *input.SourceScheduledFor
	}
	if !scheduledFor.After(now) {
		return ScheduledMessage{}, storeerr.InvalidRequest(errors.New("the scheduled time must be in the future"))
	}
	if scheduledFor.After(now.Add(MaxScheduledMessageDelay)) {
		return ScheduledMessage{}, storeerr.InvalidRequest(errors.New("the scheduled time is more than 24 hours away"))
	}
	actor, err := t.ownActorParams(ctx)
	if err != nil {
		return ScheduledMessage{}, err
	}
	text := input.Compose(now, scheduledFor)
	contentBlocks, contentBlocksJSON, err := textInputContentBlocks(text)
	if err != nil {
		return ScheduledMessage{}, err
	}
	created, err := createAgentContentInputTx(ctx, t.unit, t.q, CreateAgentContentInputInput{
		ProjectID:          t.input.ProjectID,
		AgentID:            t.input.AgentID,
		Actor:              actor,
		ContentBlocks:      contentBlocksJSON,
		DeliveryMode:       DeliveryModeQueued,
		IdempotencyScope:   scheduledMessageIdempotencyScope,
		IdempotencyKey:     "tool_call:" + t.input.ToolCallID.String(),
		SourceScheduledFor: &scheduledFor,
	}, contentBlocks)
	if err != nil {
		return ScheduledMessage{}, fmt.Errorf("schedule message: %w", err)
	}
	record := created.agentInput
	if record.ScheduledFor == nil {
		return ScheduledMessage{}, errors.New("scheduled message has no scheduled time")
	}
	return ScheduledMessage{
		ID: record.ID, Text: text, CreatedAt: record.QueuedAt, ScheduledFor: *record.ScheduledFor,
	}, nil
}

func (t *toolCallTransaction) cancelOwnScheduledMessage(ctx context.Context, inputID uuid.UUID) error {
	actorID, found, err := t.ownActorID(ctx)
	if err != nil {
		return err
	}
	if !found {
		return storeerr.ErrNotFound
	}
	row, err := t.q.GetAgentInput(ctx, dbsqlc.GetAgentInputParams{
		ProjectID: t.input.ProjectID, AgentID: t.input.AgentID, ID: inputID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load scheduled message: %w", err)
	}
	if row.State != AgentInputStateScheduled || row.ActorID == nil || *row.ActorID != actorID {
		return storeerr.ErrNotFound
	}
	h, err := t.unit.Handle(t.input.ProjectID, t.input.AgentID)
	if err != nil {
		return err
	}
	canceled, err := h.ChangeBacklog(ctx, agentexecution.BacklogChange{
		ID: inputID, DeliveryMode: string(DeliveryModeQueued), Cancel: true,
	})
	if err != nil {
		return err
	}
	if !canceled {
		return storeerr.ErrNotFound
	}
	return nil
}

const (
	MaxAgentSchedules       = 32
	MaxAgentSchedulesListed = 100
)

type AgentSchedule struct {
	ID              uuid.UUID
	Name            string
	CronExpression  string
	Timezone        string
	MessageTemplate string
	Enabled         bool
	LastFiredAt     *time.Time
	NextFireAfter   *time.Time
	CreatedAt       time.Time
}

func (r *ToolCallReader) ListAgentSchedules(ctx context.Context) ([]AgentSchedule, error) {
	t := r.transaction
	rows, err := t.q.ListCronTriggersForAgent(ctx, dbsqlc.ListCronTriggersForAgentParams{
		ProjectID: t.input.ProjectID, AgentID: t.input.AgentID, RowLimit: MaxAgentSchedulesListed,
	})
	if err != nil {
		return nil, fmt.Errorf("list agent schedules: %w", err)
	}
	schedules := make([]AgentSchedule, 0, len(rows))
	for _, row := range rows {
		schedules = append(schedules, AgentSchedule{
			ID: row.ID, Name: row.Name, CronExpression: row.CronExpression, Timezone: row.Timezone,
			MessageTemplate: row.MessageTemplate, Enabled: row.Enabled, LastFiredAt: row.LastFiredAt,
			NextFireAfter: row.NextFireAfter, CreatedAt: row.CreatedAt,
		})
	}
	return schedules, nil
}

func (s *Store) DeleteCronTriggerForAgent(ctx context.Context, projectID, agentID, id uuid.UUID) error {
	if projectID == uuid.Nil || agentID == uuid.Nil || id == uuid.Nil {
		return errors.New("project, agent, and cron trigger are required")
	}
	rows, err := s.q.DeleteCronTriggerForAgent(ctx, dbsqlc.DeleteCronTriggerForAgentParams{
		ProjectID: projectID, AgentID: agentID, ID: id,
	})
	if err != nil {
		return fmt.Errorf("delete agent cron trigger: %w", err)
	}
	if rows == 0 {
		return storeerr.ErrNotFound
	}
	return nil
}
