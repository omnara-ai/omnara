package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

type CompleteToolCallInput struct {
	ProjectID          uuid.UUID
	AgentID            uuid.UUID
	ID                 uuid.UUID
	Outcome            ToolResultOutcome
	RuntimeLockID      uuid.UUID
	ResultContentParts json.RawMessage
}

type ToolCallCompletionInput struct {
	Outcome            ToolResultOutcome
	ResultContentParts json.RawMessage
}

type CompleteRuntimeToolCallInput struct {
	ProjectID          uuid.UUID
	AgentID            uuid.UUID
	ID                 uuid.UUID
	RuntimeLockID      uuid.UUID
	Outcome            ToolResultOutcome
	ResultContentParts json.RawMessage
}

type CompleteCustomToolCallInput struct {
	ProjectID     uuid.UUID
	AgentID       uuid.UUID
	ID            uuid.UUID
	Outcome       ToolResultOutcome
	ContentBlocks json.RawMessage
}

type CompleteCustomToolCallResult struct {
	ToolCall      ToolCallRecord
	Event         TypedAgentEventRecord
	ContentBlocks json.RawMessage
}

type MarkToolCallReadyInput struct {
	ProjectID     uuid.UUID
	AgentID       uuid.UUID
	ID            uuid.UUID
	RuntimeLockID uuid.UUID
}

type RequeueRuntimeToolCallInput struct {
	ProjectID     uuid.UUID
	AgentID       uuid.UUID
	ToolCallID    uuid.UUID
	RuntimeLockID uuid.UUID
}

type ListToolCallsInput struct {
	ProjectID uuid.UUID
	AgentIDs  []uuid.UUID
	State     ToolCallState
	Type      string
	Limit     int
	After     listing.KeysetCursor
}

type ListUnfinishedProjectToolCallsInput struct {
	ProjectID uuid.UUID
	State     ToolCallState
	Type      string
	Limit     int
	After     listing.KeysetCursor
}

type ListToolCallsResult struct {
	ToolCalls []ToolCallRecord
	HasMore   bool
}

type ToolCallState string

const (
	ToolCallStateAwaitingAuthorization ToolCallState = "awaiting_authorization"
	ToolCallStateAwaitingPermission    ToolCallState = "awaiting_permission"
	ToolCallStateReady                 ToolCallState = "ready"
	ToolCallStateRunning               ToolCallState = "running"
	ToolCallStateWaiting               ToolCallState = "waiting"
	ToolCallStateCompleted             ToolCallState = "completed"
)

type ToolResultOutcome string

const (
	ToolResultOutcomeSucceeded ToolResultOutcome = "succeeded"
	ToolResultOutcomeFailed    ToolResultOutcome = "failed"
	ToolResultOutcomeDenied    ToolResultOutcome = "denied"
	ToolResultOutcomeCanceled  ToolResultOutcome = "canceled"
)

func (outcome ToolResultOutcome) IsTerminal() bool {
	switch outcome {
	case ToolResultOutcomeSucceeded,
		ToolResultOutcomeFailed,
		ToolResultOutcomeDenied,
		ToolResultOutcomeCanceled:
		return true
	default:
		return false
	}
}

type ToolCallRecord struct {
	ID                 uuid.UUID       `json:"id"`
	ProjectID          uuid.UUID       `json:"project_id"`
	AgentID            uuid.UUID       `json:"agent_id"`
	TurnID             uuid.UUID       `json:"turn_id"`
	SourceEventID      uuid.UUID       `json:"source_event_id"`
	ModelCallContextID uuid.UUID       `json:"model_call_context_id"`
	ProviderCallID     string          `json:"provider_call_id"`
	Name               string          `json:"name"`
	Input              json.RawMessage `json:"input"`
	Type               string          `json:"type"`
	CreatedAt          time.Time       `json:"created_at"`

	State         ToolCallState     `json:"state"`
	Outcome       ToolResultOutcome `json:"outcome,omitempty"`
	RuntimeLockID uuid.UUID         `json:"-"`
	CompletedAt   *time.Time        `json:"completed_at,omitempty"`

	ToolCallResultID        uuid.UUID       `json:"tool_call_result_id,omitempty"`
	ToolResultEventID       uuid.UUID       `json:"tool_result_event_id,omitempty"`
	SourceEventSequence     int64           `json:"source_event_sequence,omitempty"`
	ToolResultEventSequence int64           `json:"tool_result_event_sequence,omitempty"`
	ResultContentParts      json.RawMessage `json:"result_content_parts"`
}

func (s *Store) CompleteToolCall(
	ctx context.Context,
	input CompleteToolCallInput,
) (ToolCallRecord, error) {
	parts, err := s.prepareToolResult(ctx, input.ProjectID, input.AgentID, input.ID, input.ResultContentParts)
	if err != nil {
		return ToolCallRecord{}, err
	}
	input.ResultContentParts = parts
	return agentexecution.Retry(ctx,
		"complete_tool_call",
		func(ctx context.Context) (ToolCallRecord, error) {
			unit, err := s.cell.Begin(ctx)
			if err != nil {
				return ToolCallRecord{}, err
			}
			defer func() { _ = unit.Rollback(ctx) }()

			record, err := completeExecutionTool(ctx, unit, input)
			if err != nil {
				return ToolCallRecord{}, err
			}
			if err := unit.Commit(ctx, "complete tool call"); err != nil {
				return ToolCallRecord{}, err
			}
			return record, nil
		})
}

func (s *Store) MarkToolCallReady(ctx context.Context, input MarkToolCallReadyInput) (ToolCallRecord, error) {
	return agentexecution.Retry(ctx,
		"mark_tool_call_ready",
		func(ctx context.Context) (ToolCallRecord, error) {
			unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID, false, input.RuntimeLockID)
			if err != nil {
				return ToolCallRecord{}, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			if _, err = h.AuthorizeTool(ctx,
				agentexecution.ToolRef{ID: input.ID,
					RuntimeLockID: input.RuntimeLockID}); err != nil {
				return ToolCallRecord{}, err
			}
			record, err := getToolCallTx(ctx, unit.DB(), input.ProjectID, input.AgentID, input.ID)
			if err != nil {
				return ToolCallRecord{}, err
			}
			if err = unit.Commit(ctx, "authorize tool"); err != nil {
				return ToolCallRecord{}, err
			}
			return record, nil
		})
}

func (s *Store) RequeueRuntimeToolCall(ctx context.Context, input RequeueRuntimeToolCallInput) error {
	_, err := agentexecution.Retry(ctx,
		"requeue_runtime_tool_call",
		func(ctx context.Context) (struct{}, error) {
			unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID, false, input.RuntimeLockID)
			if err != nil {
				return struct{}{}, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			if _, err = h.RequeueTool(ctx,
				agentexecution.ToolRef{ID: input.ToolCallID,
					RuntimeLockID: input.RuntimeLockID}); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, unit.Commit(ctx, "requeue tool")
		})
	return err
}

func completeExecutionTool(
	ctx context.Context,
	unit *agentexecution.Unit,
	input CompleteToolCallInput,
) (ToolCallRecord, error) {
	h, err := lockExecution(ctx, unit, input.ProjectID, input.AgentID, false, input.RuntimeLockID)
	if err != nil {
		return ToolCallRecord{}, err
	}
	blocks, err := parseToolResultContentBlocks(input.ResultContentParts)
	if err != nil {
		return ToolCallRecord{}, err
	}
	parts, err := executionContent(blocks)
	if err != nil {
		return ToolCallRecord{}, err
	}
	_, err = h.CompleteTool(ctx, agentexecution.ToolCompletion{ToolRef: agentexecution.ToolRef{ID: input.ID,
		RuntimeLockID: input.RuntimeLockID}, Outcome: string(input.Outcome), Content: parts})
	if err != nil {
		return ToolCallRecord{}, err
	}
	return getToolCallTx(ctx, unit.DB(), input.ProjectID, input.AgentID, input.ID)
}

func (s *Store) CompleteCustomToolCall(
	ctx context.Context,
	input CompleteCustomToolCallInput,
) (CompleteCustomToolCallResult, error) {
	parts, err := s.prepareToolResult(ctx, input.ProjectID, input.AgentID, input.ID, input.ContentBlocks)
	if err != nil {
		return CompleteCustomToolCallResult{}, err
	}
	input.ContentBlocks = parts
	return agentexecution.Retry(ctx,
		"complete_custom_tool_call",
		func(ctx context.Context) (CompleteCustomToolCallResult, error) {
			unit, err := s.cell.Begin(ctx)
			if err != nil {
				return CompleteCustomToolCallResult{}, err
			}
			defer func() { _ = unit.Rollback(ctx) }()

			result, err := completeCustomToolCallTx(ctx, unit, input)
			if err != nil {
				return CompleteCustomToolCallResult{}, err
			}
			if err := unit.Commit(ctx, "complete custom tool call"); err != nil {
				return CompleteCustomToolCallResult{}, err
			}
			return result, nil
		})
}

func completeCustomToolCallTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	input CompleteCustomToolCallInput,
) (CompleteCustomToolCallResult, error) {
	h, err := lockExecution(ctx, unit, input.ProjectID, input.AgentID, false, uuid.Nil)
	if err != nil {
		return CompleteCustomToolCallResult{}, err
	}
	blocks, err := parseToolResultContentBlocks(input.ContentBlocks)
	if err != nil {
		return CompleteCustomToolCallResult{}, err
	}
	parts, err := executionContent(blocks)
	if err != nil {
		return CompleteCustomToolCallResult{}, err
	}
	completed, err := h.CompleteCustomTool(ctx, input.ID, string(input.Outcome), parts)
	if err != nil {
		return CompleteCustomToolCallResult{}, err
	}
	record, err := getToolCallTx(ctx, unit.DB(), input.ProjectID, input.AgentID, input.ID)
	if err != nil {
		return CompleteCustomToolCallResult{}, err
	}
	return CompleteCustomToolCallResult{ToolCall: record, Event: TypedAgentEventRecord{
		Event: executionEvent(
			input.AgentID,
			completed.Event,
			events.KindToolResult,
		), TurnID: completed.Event.TurnID,
		ToolCallResultID: completed.ResultID}, ContentBlocks: record.ResultContentParts}, nil
}

func (s *Store) CompleteRuntimeToolCall(
	ctx context.Context, input CompleteRuntimeToolCallInput,
) (ToolCallRecord, error) {
	if !input.Outcome.IsTerminal() || input.Outcome == ToolResultOutcomeDenied {
		return ToolCallRecord{}, errors.New("invalid runtime tool outcome")
	}
	return s.CompleteToolCall(ctx, CompleteToolCallInput{ProjectID: input.ProjectID, AgentID: input.AgentID,
		ID: input.ID, RuntimeLockID: input.RuntimeLockID, Outcome: input.Outcome,
		ResultContentParts: input.ResultContentParts})
}
