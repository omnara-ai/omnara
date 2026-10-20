package executionstore

import (
	"context"
	"errors"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
)

type RecordModelOutputAndCompleteContextInput struct {
	RootAgentID        uuid.UUID
	FinishClaim        bool
	ProjectID          uuid.UUID
	AgentID            uuid.UUID
	RuntimeLockID      uuid.UUID
	ModelCallContextID uuid.UUID
	ProviderRequestID  string
	ProviderResponse   modelenvelope.ResponseEnvelope
}

type ToolCallBindingInput struct {
	ID             uuid.UUID
	ProviderCallID string
	Type           string
}

type RecordToolCallSourceAndCompleteContextInput struct {
	RootAgentID        uuid.UUID
	FinishClaim        bool
	ProjectID          uuid.UUID
	AgentID            uuid.UUID
	RuntimeLockID      uuid.UUID
	ModelCallContextID uuid.UUID
	ProviderRequestID  string
	ProviderResponse   modelenvelope.ResponseEnvelope
	ToolCallBindings   []ToolCallBindingInput
}

func executionOutput(
	input RecordModelOutputAndCompleteContextInput,
	bindings []ToolCallBindingInput,
) agentexecution.AcceptOutputInput {
	tools := make([]agentexecution.ToolProposal, len(bindings))
	for i, b := range bindings {
		tools[i] = agentexecution.ToolProposal{ID: b.ID, ProviderCallID: b.ProviderCallID, Type: b.Type}
	}
	return agentexecution.AcceptOutputInput{
		FinishClaim:   input.FinishClaim,
		RuntimeLockID: input.RuntimeLockID,
		ContextID:     input.ModelCallContextID,
		RequestID:     input.ProviderRequestID,
		Response:      input.ProviderResponse,
		Tools:         tools,
	}
}

func executionEvent(agentID uuid.UUID, event agentexecution.ExecutionEvent, kind events.Kind) events.Event {
	return events.Event{ID: event.ID, AgentID: agentID, Sequence: event.Sequence, Kind: kind, At: event.Time}
}

func (s *Store) RecordModelOutputAndCompleteContext(
	ctx context.Context,
	input RecordModelOutputAndCompleteContextInput,
) (events.Event, error) {
	return agentexecution.Retry(ctx,
		"record_model_output_and_complete_context",
		func(ctx context.Context) (events.Event,
			error) {
			unit, h, err := beginStepExecution(ctx, s, input.ProjectID, input.AgentID, input.RootAgentID,
				agentexecution.ParentEffectKind(input.ProviderResponse) != "", input.RuntimeLockID)
			if err != nil {
				return events.Event{}, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			result, err := h.AcceptOutput(ctx, executionOutput(input, nil))
			if err != nil {
				return events.Event{}, err
			}
			if result.Created {
				if err = unit.Commit(ctx, "accept model output"); err != nil {
					if errors.Is(err, storeerr.ErrCommitOutcomeUnknown) {
						resolved, ok, resolveErr := s.resolveOutput(ctx, input, nil)
						if resolveErr == nil && ok {
							return executionEvent(input.AgentID, resolved.Event, events.KindModelOutput), nil
						}
						err = errors.Join(err, resolveErr)
					}
					return events.Event{}, err
				}
			}
			return executionEvent(input.AgentID, result.Event, events.KindModelOutput), nil
		})
}

func (s *Store) RecordToolCallSourceAndCompleteContext(
	ctx context.Context,
	input RecordToolCallSourceAndCompleteContextInput,
) (events.Event, []ToolCallRecord, error) {
	type executionResult struct {
		event events.Event
		tools []ToolCallRecord
	}
	result,
		err := agentexecution.Retry(ctx,
		"record_tool_call_source_and_complete_context",
		func(ctx context.Context) (executionResult,
			error) {
			unit, h, err := beginStepExecution(ctx, s, input.ProjectID, input.AgentID, input.RootAgentID,
				agentexecution.ParentEffectKind(input.ProviderResponse) != "", input.RuntimeLockID)
			if err != nil {
				return executionResult{events.Event{}, nil}, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			result, err := h.AcceptOutput(ctx, executionOutput(RecordModelOutputAndCompleteContextInput{
				FinishClaim: input.FinishClaim,
				ProjectID:   input.ProjectID, AgentID: input.AgentID, RuntimeLockID: input.RuntimeLockID,
				ModelCallContextID: input.ModelCallContextID, ProviderRequestID: input.ProviderRequestID,
				ProviderResponse: input.ProviderResponse}, input.ToolCallBindings))
			if err != nil {
				return executionResult{events.Event{}, nil}, err
			}
			records := make([]ToolCallRecord, 0, len(result.Tools))
			for _, call := range result.Tools {
				record, loadErr := getToolCallTx(ctx, unit.DB(), input.ProjectID, input.AgentID, call.ID)
				if loadErr != nil {
					return executionResult{events.Event{}, nil}, loadErr
				}
				records = append(records, record)
			}
			if result.Created {
				if err = unit.Commit(ctx, "accept model tool output"); err != nil {
					if errors.Is(err, storeerr.ErrCommitOutcomeUnknown) {
						resolved, ok, resolveErr := s.resolveOutput(ctx, RecordModelOutputAndCompleteContextInput{
							ProjectID: input.ProjectID, AgentID: input.AgentID, RuntimeLockID: input.RuntimeLockID,
							ModelCallContextID: input.ModelCallContextID,
						}, input.ToolCallBindings)
						if resolveErr == nil && ok {
							return executionResult{executionEvent(input.AgentID, resolved.Event, events.KindModelOutput), records}, nil
						}
						err = errors.Join(err, resolveErr)
					}
					return executionResult{}, err
				}
			}
			return executionResult{executionEvent(input.AgentID, result.Event, events.KindModelOutput), records}, nil
		})
	return result.event, result.tools, err
}

func (s *Store) resolveOutput(
	ctx context.Context, input RecordModelOutputAndCompleteContextInput, bindings []ToolCallBindingInput,
) (agentexecution.AcceptedOutput, bool, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	type resolution struct {
		output agentexecution.AcceptedOutput
		found  bool
	}
	result, err := agentexecution.Retry(ctx, "resolve_model_output", func(ctx context.Context) (resolution, error) {
		unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID, false, uuid.Nil)
		if err != nil {
			return resolution{}, err
		}
		defer func() { _ = unit.Rollback(ctx) }()
		output, found, err := h.ResolveOutput(ctx, executionOutput(input, bindings))
		return resolution{output, found}, err
	})
	return result.output, result.found, err
}
