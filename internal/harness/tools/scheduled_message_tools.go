package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/cronschedule"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type createScheduledMessageRequest struct {
	Message      string  `json:"message"`
	Timestamp    *string `json:"timestamp,omitempty"`
	DelaySeconds *int64  `json:"delay_seconds,omitempty"`
	Cron         *string `json:"cron,omitempty"`
	Timezone     *string `json:"timezone,omitempty"`
}

type resolvedScheduledMessageRequest struct {
	message      string
	scheduledFor *time.Time
	delay        time.Duration
	cron         string
	timezone     string
}

type scheduleSummary struct {
	ID             string `json:"id"`
	Cron           string `json:"cron"`
	Timezone       string `json:"timezone"`
	Enabled        bool   `json:"enabled"`
	NextRunAt      string `json:"next_run_at,omitempty"`
	LastRunAt      string `json:"last_run_at,omitempty"`
	CreatedAt      string `json:"created_at"`
	Name           string `json:"name"`
	MessagePreview string `json:"message_preview"`
}

type deleteScheduledMessageRequest struct {
	ID string `json:"id"`
}

type scheduledMessageSummary struct {
	ID             string `json:"id"`
	ScheduledFor   string `json:"scheduled_for"`
	CreatedAt      string `json:"created_at"`
	MessagePreview string `json:"message_preview"`
}

func validateGetCurrentTimeInput(raw json.RawMessage) error {
	var input struct{}
	return decodeStrictToolRequest(toolcatalog.ToolNameGetCurrentTime, raw, &input)
}

func validateListScheduledMessagesInput(raw json.RawMessage) error {
	var input struct{}
	return decodeStrictToolRequest(toolcatalog.ToolNameListScheduledMessages, raw, &input)
}

func validateCreateScheduledMessageInput(raw json.RawMessage) error {
	_, err := resolveCreateScheduledMessageRequest(raw)
	return err
}

func validateDeleteScheduledMessageInput(raw json.RawMessage) error {
	_, err := resolveDeleteScheduledMessageRequest(raw)
	return err
}

func resolveCreateScheduledMessageRequest(raw json.RawMessage) (resolvedScheduledMessageRequest, error) {
	var input createScheduledMessageRequest
	if err := decodeStrictToolRequest(toolcatalog.ToolNameCreateScheduledMessage, raw, &input); err != nil {
		return resolvedScheduledMessageRequest{}, err
	}
	resolved := resolvedScheduledMessageRequest{message: strings.TrimSpace(input.Message)}
	if resolved.message == "" {
		return resolvedScheduledMessageRequest{}, errors.New("create_scheduled_message message is required")
	}
	times := 0
	for _, set := range []bool{input.Timestamp != nil, input.DelaySeconds != nil, input.Cron != nil} {
		if set {
			times++
		}
	}
	if times != 1 {
		return resolvedScheduledMessageRequest{}, errors.New(
			"create_scheduled_message requires exactly one of timestamp, delay_seconds, or cron",
		)
	}
	if input.Timezone != nil && input.Cron == nil {
		return resolvedScheduledMessageRequest{}, errors.New(
			"create_scheduled_message timezone is only valid with cron",
		)
	}
	if input.Cron != nil {
		resolved.cron = strings.TrimSpace(*input.Cron)
		resolved.timezone = "UTC"
		if input.Timezone != nil && strings.TrimSpace(*input.Timezone) != "" {
			resolved.timezone = strings.TrimSpace(*input.Timezone)
		}
		if err := cronschedule.Validate(resolved.cron, resolved.timezone); err != nil {
			return resolvedScheduledMessageRequest{}, fmt.Errorf("create_scheduled_message cron: %w", err)
		}
		if err := cronschedule.ValidateMessageTemplate(scheduleMessageTemplate(resolved)); err != nil {
			return resolvedScheduledMessageRequest{}, fmt.Errorf("create_scheduled_message message: %w", err)
		}
		return resolved, nil
	}
	if input.DelaySeconds != nil {
		if *input.DelaySeconds < 1 || *input.DelaySeconds > toolcatalog.ScheduledMessageMaxDelaySeconds {
			return resolvedScheduledMessageRequest{}, fmt.Errorf(
				"create_scheduled_message delay_seconds must be between 1 and %d",
				toolcatalog.ScheduledMessageMaxDelaySeconds,
			)
		}
		resolved.delay = time.Duration(*input.DelaySeconds) * time.Second
		return resolved, nil
	}
	scheduledFor, err := time.Parse(time.RFC3339, strings.TrimSpace(*input.Timestamp))
	if err != nil {
		return resolvedScheduledMessageRequest{}, errors.New(
			"create_scheduled_message timestamp must be RFC 3339 with a UTC offset, such as 2026-12-01T09:00:00Z",
		)
	}
	scheduledFor = scheduledFor.UTC()
	resolved.scheduledFor = &scheduledFor
	return resolved, nil
}

func resolveDeleteScheduledMessageRequest(raw json.RawMessage) (deleteScheduledMessageRequest, error) {
	var input deleteScheduledMessageRequest
	if err := decodeStrictToolRequest(toolcatalog.ToolNameDeleteScheduledMessage, raw, &input); err != nil {
		return deleteScheduledMessageRequest{}, err
	}
	input.ID = strings.TrimSpace(input.ID)
	if input.ID == "" {
		return deleteScheduledMessageRequest{}, errors.New("delete_scheduled_message id is required")
	}
	return input, nil
}

func formatScheduledMessageTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func scheduledMessageNote(createdAt, scheduledFor time.Time) string {
	return fmt.Sprintf(
		"Scheduled message: you scheduled this message for yourself at %s, for delivery at %s.",
		formatScheduledMessageTime(createdAt), formatScheduledMessageTime(scheduledFor),
	)
}

const (
	scheduleMessageTemplateNote = "Scheduled message: your recurring schedule fired at {{.trigger.fired_at}}.\n\n"
	templateLiteralOpenBraces   = `{{"{{"}}`
)

func scheduleMessageTemplate(input resolvedScheduledMessageRequest) string {
	return scheduleMessageTemplateNote + strings.ReplaceAll(input.message, "{{", templateLiteralOpenBraces)
}

// scheduleMessageFromTemplate recovers the message the agent wrote from a template it created.
func scheduleMessageFromTemplate(template string) string {
	message, found := strings.CutPrefix(template, scheduleMessageTemplateNote)
	if !found {
		return template
	}
	return strings.ReplaceAll(message, templateLiteralOpenBraces, "{{")
}

func scheduledMessagePreview(message string) string {
	runes := []rune(message)
	if len(runes) <= toolcatalog.ScheduledMessagePreviewLength {
		return message
	}
	return string(runes[:toolcatalog.ScheduledMessagePreviewLength]) + "…"
}

func scheduleSummaryFromRecord(schedule executionstore.AgentSchedule) (scheduleSummary, error) {
	id, err := publicid.Encode(publicid.KindCronTrigger, schedule.ID)
	if err != nil {
		return scheduleSummary{}, fmt.Errorf("encode schedule id: %w", err)
	}
	summary := scheduleSummary{
		ID: id, Cron: schedule.CronExpression, Timezone: schedule.Timezone, Enabled: schedule.Enabled,
		CreatedAt: formatScheduledMessageTime(schedule.CreatedAt), Name: schedule.Name,
		MessagePreview: scheduledMessagePreview(scheduleMessageFromTemplate(schedule.MessageTemplate)),
	}
	if schedule.NextFireAfter != nil {
		summary.NextRunAt = formatScheduledMessageTime(*schedule.NextFireAfter)
	}
	if schedule.LastFiredAt != nil {
		summary.LastRunAt = formatScheduledMessageTime(*schedule.LastFiredAt)
	}
	return summary, nil
}

func scheduledMessageSummaryFromRecord(message executionstore.ScheduledMessage) (scheduledMessageSummary, error) {
	id, err := publicid.Encode(publicid.KindAgentInput, message.ID)
	if err != nil {
		return scheduledMessageSummary{}, fmt.Errorf("encode scheduled message id: %w", err)
	}
	return scheduledMessageSummary{
		ID:             id,
		ScheduledFor:   formatScheduledMessageTime(message.ScheduledFor),
		CreatedAt:      formatScheduledMessageTime(message.CreatedAt),
		MessagePreview: scheduledMessagePreview(message.Text),
	}, nil
}

func scheduledMessageFailureContent(code string, cause error) (toolResultContent, bool, error) {
	message := cause.Error()
	switch {
	case errors.Is(cause, storeerr.ErrAgentScheduledInputsFull):
		code = storeerr.AgentBacklogFullCode
		message = storeerr.ErrAgentScheduledInputsFull.Error()
	case errors.Is(cause, storeerr.ErrAgentBacklogFull):
		code = storeerr.AgentBacklogFullCode
		message = storeerr.AgentBacklogFullMessage
	case errors.Is(cause, storeerr.ErrNotFound):
		message = "No scheduled message or schedule of yours has that id. It may already have been delivered or " +
			"deleted; use list_scheduled_messages to list them."
	case errors.Is(cause, storeerr.ErrInvalidRequest), errors.Is(cause, storeerr.ErrStateTransitionConflict),
		errors.Is(cause, storeerr.ErrConflict):
	default:
		return toolResultContent{}, false, nil
	}
	content, err := toolFailureContent(code, message, false)
	return content, err == nil, err
}

func failScheduledMessageTransaction(code string, cause error) (transactionalPhaseResult, error) {
	content, handled, err := scheduledMessageFailureContent(code, cause)
	if err != nil {
		return nil, err
	}
	if !handled {
		return nil, cause
	}
	return failInTransaction(content, cause), nil
}

func getCurrentTime(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	now, err := call.Reader.DatabaseNow(ctx)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	content, err := structuredToolResultContent(map[string]any{
		"current_time": now.Format(time.RFC3339),
		"unix_seconds": now.Unix(),
		"weekday":      now.Weekday().String(),
		"timezone":     "UTC",
	})
	if err != nil {
		return nil, err
	}
	return completeInTransaction(content), nil
}

func listScheduledMessages(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	messages, err := call.Reader.ListOwnScheduledMessages(ctx)
	if err != nil {
		return nil, err
	}
	summaries := make([]scheduledMessageSummary, 0, len(messages))
	for _, message := range messages {
		summary, err := scheduledMessageSummaryFromRecord(message)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	schedules, err := call.Reader.ListAgentSchedules(ctx)
	if err != nil {
		return nil, err
	}
	scheduleSummaries := make([]scheduleSummary, 0, len(schedules))
	for _, schedule := range schedules {
		summary, err := scheduleSummaryFromRecord(schedule)
		if err != nil {
			return nil, err
		}
		scheduleSummaries = append(scheduleSummaries, summary)
	}
	content, err := structuredToolResultContent(map[string]any{
		"scheduled_messages": summaries, "schedules": scheduleSummaries,
	})
	if err != nil {
		return nil, err
	}
	return completeInTransaction(content), nil
}

func createScheduledMessage(_ context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	input, err := resolveCreateScheduledMessageRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	if input.cron != "" {
		return createSchedule(input), nil
	}
	command := executionstore.CreateScheduledMessageForToolCall(
		executionstore.CreateScheduledMessageInput{
			SourceScheduledFor: input.scheduledFor,
			Delay:              input.delay,
			Message:            input.message,
			Note:               scheduledMessageNote,
		},
		func(message executionstore.ScheduledMessage) (executionstore.ToolCallCompletionInput, error) {
			summary, err := scheduledMessageSummaryFromRecord(message)
			if err != nil {
				return executionstore.ToolCallCompletionInput{}, err
			}
			content, err := structuredToolResultContent(map[string]any{
				"id":            summary.ID,
				"scheduled_for": summary.ScheduledFor,
				"created_at":    summary.CreatedAt,
				"scheduled":     true,
			})
			if err != nil {
				return executionstore.ToolCallCompletionInput{}, err
			}
			return successfulToolCallCompletion(content)
		},
	)
	return executeInTransaction(command, func(err error) (transactionalPhaseResult, error) {
		return failScheduledMessageTransaction("create_scheduled_message_failed", err)
	}), nil
}

func deleteScheduledMessage(_ context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	input, err := resolveDeleteScheduledMessageRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"id": input.ID, "deleted": true}
	triggerID, triggerErr := publicid.Decode(publicid.KindCronTrigger, input.ID)
	inputID, inputErr := publicid.Decode(publicid.KindAgentInput, input.ID)
	if triggerErr != nil && inputErr != nil {
		return failScheduledMessageTransaction("delete_scheduled_message_failed", storeerr.ErrNotFound)
	}
	if triggerErr == nil {
		result["recurring"] = true
	}
	content, err := structuredToolResultContent(result)
	if err != nil {
		return nil, err
	}
	completion, err := successfulToolCallCompletion(content)
	if err != nil {
		return nil, err
	}
	command := executionstore.CancelScheduledMessageForToolCall(inputID, completion)
	if triggerErr == nil {
		command = executionstore.DeleteAgentScheduleForToolCall(triggerID, completion)
	}
	return executeInTransaction(command, func(err error) (transactionalPhaseResult, error) {
		return failScheduledMessageTransaction("delete_scheduled_message_failed", err)
	}), nil
}

func createSchedule(input resolvedScheduledMessageRequest) transactionalPhaseResult {
	command := executionstore.CreateAgentScheduleForToolCall(
		executionstore.CreateAgentScheduleInput{
			CronExpression: input.cron, Timezone: input.timezone, MessageTemplate: scheduleMessageTemplate(input),
		},
		func(schedule executionstore.AgentSchedule) (executionstore.ToolCallCompletionInput, error) {
			id, err := publicid.Encode(publicid.KindCronTrigger, schedule.ID)
			if err != nil {
				return executionstore.ToolCallCompletionInput{}, fmt.Errorf("encode schedule id: %w", err)
			}
			result := map[string]any{
				"id": id, "cron": schedule.CronExpression, "timezone": schedule.Timezone,
				"scheduled": true, "recurring": true,
			}
			if schedule.NextFireAfter != nil {
				result["next_run_at"] = formatScheduledMessageTime(*schedule.NextFireAfter)
			}
			content, err := structuredToolResultContent(result)
			if err != nil {
				return executionstore.ToolCallCompletionInput{}, err
			}
			return successfulToolCallCompletion(content)
		},
	)
	return executeInTransaction(command, func(err error) (transactionalPhaseResult, error) {
		return failScheduledMessageTransaction("create_scheduled_message_failed", err)
	})
}
