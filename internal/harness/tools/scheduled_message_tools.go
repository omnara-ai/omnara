package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type createScheduledMessageRequest struct {
	Message      string  `json:"message"`
	Timestamp    *string `json:"timestamp,omitempty"`
	DelaySeconds *int64  `json:"delay_seconds,omitempty"`
}

type resolvedScheduledMessageRequest struct {
	message      string
	scheduledFor *time.Time
	delay        time.Duration
}

type deleteScheduledMessageRequest struct {
	ID string `json:"id"`
}

type scheduledMessageSummary struct {
	ID           string `json:"id"`
	ScheduledFor string `json:"scheduled_for"`
	CreatedAt    string `json:"created_at"`
	Message      string `json:"message"`
}

func validateGetCurrentTimeInput(raw json.RawMessage) error {
	var input struct{}
	return decodeStrictToolRequest(toolcatalog.ToolNameGetCurrentTime, raw, &input)
}

func validateGetScheduledMessagesInput(raw json.RawMessage) error {
	var input struct{}
	return decodeStrictToolRequest(toolcatalog.ToolNameGetScheduledMessages, raw, &input)
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
	if (input.Timestamp == nil) == (input.DelaySeconds == nil) {
		return resolvedScheduledMessageRequest{}, errors.New(
			"create_scheduled_message requires exactly one of timestamp or delay_seconds",
		)
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

// scheduledMessageText is the delivered message: a preset note saying the
// agent scheduled it and when, followed by the agent's own text.
func scheduledMessageText(message string, createdAt, scheduledFor time.Time) string {
	return fmt.Sprintf(
		"Scheduled message: you scheduled this message for yourself at %s, for delivery at %s.\n\n%s",
		formatScheduledMessageTime(createdAt), formatScheduledMessageTime(scheduledFor), message,
	)
}

func scheduledMessageSummaryFromRecord(message executionstore.ScheduledMessage) (scheduledMessageSummary, error) {
	id, err := publicid.Encode(publicid.KindAgentInput, message.ID)
	if err != nil {
		return scheduledMessageSummary{}, fmt.Errorf("encode scheduled message id: %w", err)
	}
	return scheduledMessageSummary{
		ID:           id,
		ScheduledFor: formatScheduledMessageTime(message.ScheduledFor),
		CreatedAt:    formatScheduledMessageTime(message.CreatedAt),
		Message:      message.Text,
	}, nil
}

func failScheduledMessageTransaction(code string, cause error) (transactionalPhaseResult, error) {
	message := cause.Error()
	switch {
	case errors.Is(cause, storeerr.ErrAgentBacklogFull):
		code = storeerr.AgentBacklogFullCode
		message = storeerr.ErrAgentBacklogFull.Error()
	case errors.Is(cause, storeerr.ErrNotFound):
		message = "No scheduled message of yours has that id. It may already have been delivered or deleted; " +
			"use get_scheduled_messages to list them."
	case errors.Is(cause, storeerr.ErrInvalidRequest), errors.Is(cause, storeerr.ErrStateTransitionConflict):
	default:
		return nil, cause
	}
	content, err := toolFailureContent(code, message, false)
	if err != nil {
		return nil, err
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

func getScheduledMessages(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
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
	content, err := structuredToolResultContent(map[string]any{"scheduled_messages": summaries})
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
	command := executionstore.CreateScheduledMessageForToolCall(
		executionstore.CreateScheduledMessageInput{
			SourceScheduledFor: input.scheduledFor,
			Delay:              input.delay,
			Compose: func(createdAt, scheduledFor time.Time) string {
				return scheduledMessageText(input.message, createdAt, scheduledFor)
			},
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
	inputID, err := publicid.Decode(publicid.KindAgentInput, input.ID)
	if err != nil {
		return failScheduledMessageTransaction("delete_scheduled_message_failed", storeerr.ErrNotFound)
	}
	content, err := structuredToolResultContent(map[string]any{"id": input.ID, "deleted": true})
	if err != nil {
		return nil, err
	}
	completion, err := successfulToolCallCompletion(content)
	if err != nil {
		return nil, err
	}
	command := executionstore.CancelScheduledMessageForToolCall(inputID, completion)
	return executeInTransaction(command, func(err error) (transactionalPhaseResult, error) {
		return failScheduledMessageTransaction("delete_scheduled_message_failed", err)
	}), nil
}
