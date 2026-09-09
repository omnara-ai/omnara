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

func invalidModelResponse(errorSource string, reason model.StopReason, calls []model.ToolCall) error {
	seenIDs := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if _, exists := seenIDs[call.ID]; exists {
			return model.MalformedProviderSuccess(
				errorSource,
				"malformed_tool_call",
				fmt.Sprintf("The model response contains duplicate tool call ID %q.", call.ID),
				nil,
			)
		}
		seenIDs[call.ID] = struct{}{}
	}
	switch reason {
	case model.StopReasonToolUse:
		if len(calls) == 0 {
			return model.MalformedProviderSuccess(
				errorSource,
				string(reason),
				"The model stopped for tool use without returning a supported tool call.",
				nil,
			)
		}
	case model.StopReasonEndTurn, model.StopReasonMaxTokens:
	case model.StopReasonRefusal, model.StopReasonContentFilter:
		if len(calls) > 0 {
			return model.MalformedProviderSuccess(
				errorSource,
				"contradictory_stop_reason",
				fmt.Sprintf("The model returned stop reason %q together with tool calls.", reason),
				nil,
			)
		}
	case model.StopReasonContextWindow:
		return model.ProviderError{
			Kind:    model.ErrorKindContextWindow,
			Source:  errorSource,
			Code:    string(reason),
			Message: "The model provider reported that the context window was exceeded.",
		}
	case model.StopReasonPause:
		return model.ProviderError{
			Kind:    model.ErrorKindInvalidRequest,
			Source:  errorSource,
			Code:    string(reason),
			Message: fmt.Sprintf("The model returned unsupported stop reason %q.", reason),
		}
	default:
		return model.MalformedProviderSuccess(
			errorSource,
			string(reason),
			fmt.Sprintf("The model returned unsupported stop reason %q.", reason),
			nil,
		)
	}
	return nil
}
