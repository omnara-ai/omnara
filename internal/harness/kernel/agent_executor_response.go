package kernel

import (
	"context"
	"fmt"

	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func (e AgentExecutor) recordToolCallSourceEvent(
	ctx context.Context,
	input ModelWorkExecution,
	contextRow executionstore.ModelCallContextRecord,
	providerRequestID string,
	envelope modelenvelope.ResponseEnvelope,
	specs []modelcontext.ToolSpec,
	streamedToolCallIDs map[string]storage.ID,
) (events.Event, error) {
	toolCalls := model.ToolCallsFromEnvelope(envelope)
	bindings := make([]executionstore.ToolCallBindingInput, 0, len(toolCalls))
	toolsByName := toolSpecSet(specs)
	for _, call := range toolCalls {
		toolType := toolcatalog.ToolTypeBuiltIn
		if toolcatalog.IsMCPRuntimeToolName(call.Name) {
			toolType = toolcatalog.ToolTypeMCP
		}
		if spec, ok := toolsByName[call.Name]; ok && spec.Type != "" {
			toolType = spec.Type
		}
		bindings = append(bindings, executionstore.ToolCallBindingInput{
			ID:             streamedToolCallIDs[call.ID],
			ProviderCallID: call.ID,
			Type:           toolType,
		})
	}
	event, _, err := e.Store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID:          input.ProjectID,
			AgentID:            input.AgentID,
			RuntimeLockID:      input.RuntimeLockID,
			ModelCallContextID: contextRow.ID,
			ProviderRequestID:  providerRequestID,
			ProviderResponse:   envelope,
			ToolCallBindings:   bindings,
		},
	)
	return event, err
}

func invalidModelToolCallResponse(
	errorSource string,
	calls []model.ToolCall,
) (error, bool) {
	seenIDs := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if call.ID == "" {
			return malformedToolCallResponse(
				errorSource,
				"The model response contains a tool call without an ID.",
			), true
		}
		if _, exists := seenIDs[call.ID]; exists {
			return malformedToolCallResponse(
				errorSource,
				fmt.Sprintf("The model response contains duplicate tool call ID %q.", call.ID),
			), true
		}
		seenIDs[call.ID] = struct{}{}
		if call.Name == "" {
			return malformedToolCallResponse(
				errorSource,
				"The model response contains a tool call without a name.",
			), true
		}
		if err := modelenvelope.ValidateToolInput(call.Input); err != nil {
			return malformedToolCallResponse(
				errorSource,
				fmt.Sprintf(
					"The model response tool call %q has invalid input: %v",
					call.ID,
					err,
				),
			), true
		}
	}
	return nil, false
}

func malformedToolCallResponse(errorSource, message string) error {
	return model.MalformedProviderSuccess(
		errorSource,
		"malformed_tool_call",
		message,
		nil,
	)
}
