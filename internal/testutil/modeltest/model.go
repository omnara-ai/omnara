package modeltest

import "github.com/omnara-ai/omnara/internal/model"

func ResponsePartsForToolCalls(calls []model.ToolCall) []model.ResponsePart {
	parts := make([]model.ResponsePart, 0, len(calls))
	for _, call := range calls {
		parts = append(parts, model.ResponsePart{
			Type:           model.ResponsePartTypeToolCall,
			ProviderCallID: call.ID,
			ToolName:       call.Name,
			ToolInput:      call.Input,
		})
	}
	return parts
}
