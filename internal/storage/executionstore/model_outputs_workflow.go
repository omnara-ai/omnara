package executionstore

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
)

type RecordModelOutputAndCompleteContextInput struct {
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
	unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID)
	if err != nil {
		return events.Event{}, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	result, err := h.AcceptOutput(ctx, executionOutput(input, nil))
	if err != nil {
		return events.Event{}, err
	}
	if err = unit.Commit(ctx, "accept model output"); err != nil {
		return events.Event{}, err
	}
	return executionEvent(input.AgentID, result.Event, events.KindModelOutput), nil
}

func (s *Store) RecordToolCallSourceAndCompleteContext(
	ctx context.Context,
	input RecordToolCallSourceAndCompleteContextInput,
) (events.Event, []ToolCallRecord, error) {
	unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID)
	if err != nil {
		return events.Event{}, nil, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	result, err := h.AcceptOutput(ctx, executionOutput(RecordModelOutputAndCompleteContextInput{
		ProjectID: input.ProjectID, AgentID: input.AgentID, RuntimeLockID: input.RuntimeLockID,
		ModelCallContextID: input.ModelCallContextID, ProviderRequestID: input.ProviderRequestID,
		ProviderResponse: input.ProviderResponse}, input.ToolCallBindings))
	if err != nil {
		return events.Event{}, nil, err
	}
	records := make([]ToolCallRecord, 0, len(result.Tools))
	for _, call := range result.Tools {
		record, loadErr := getToolCallTx(ctx, unit.DB(), input.ProjectID, input.AgentID, call.ID)
		if loadErr != nil {
			return events.Event{}, nil, loadErr
		}
		records = append(records, record)
	}
	if err = unit.Commit(ctx, "accept model tool output"); err != nil {
		return events.Event{}, nil, err
	}
	return executionEvent(input.AgentID, result.Event, events.KindModelOutput), records, nil
}
