package modelcontext

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

type HistoryEntry struct {
	Sequence         int64
	Message          Message
	AssistantContent []AssistantContentEntry
	ToolResults      []ToolResultRef
}

type AssistantContentEntry interface {
	assistantContentEntry()
}

type AssistantBlockEntry struct {
	Content json.RawMessage
}

type AssistantToolCallEntry struct {
	ToolCall ToolResultRef
}

func (AssistantBlockEntry) assistantContentEntry()    {}
func (AssistantToolCallEntry) assistantContentEntry() {}

func CanonicalHistory(bundle Bundle) ([]HistoryEntry, error) {
	resultsByContext := make(map[string][]ToolResultRef)
	for _, result := range bundle.ToolResults {
		if result.ModelCallContextID == "" {
			return nil, errors.New("tool result is missing its model call context")
		}
		resultsByContext[result.ModelCallContextID] = append(
			resultsByContext[result.ModelCallContextID],
			result,
		)
	}

	entries := make([]HistoryEntry, 0, len(bundle.Messages))
	seenContexts := make(map[string]struct{}, len(bundle.Messages))
	for index := range bundle.Messages {
		message := bundle.Messages[index]
		if message.Sequence <= 0 {
			return nil, errors.New("model context message event sequence is required")
		}
		if err := validateMessageContentBlocks(message.Role, message.Content); err != nil {
			return nil, fmt.Errorf("message %s content: %w", message.ID, err)
		}
		entry := HistoryEntry{Sequence: message.Sequence, Message: message}
		contextID := message.ModelCallContextID
		if contextID != "" {
			if _, duplicate := seenContexts[contextID]; duplicate {
				return nil, errors.New("model context has duplicate model call context messages")
			}
			seenContexts[contextID] = struct{}{}
		}
		results := resultsByContext[contextID]
		if message.Role != modelprotocol.RoleAssistant {
			if len(results) != 0 {
				return nil, errors.New("tool results are linked to a non-assistant message")
			}
			entries = append(entries, entry)
			continue
		}
		content, orderedResults, err := canonicalAssistantContent(message.Content, results)
		if err != nil {
			return nil, fmt.Errorf("assemble assistant message %s: %w", message.ID, err)
		}
		entry.AssistantContent = content
		entry.ToolResults = orderedResults
		delete(resultsByContext, contextID)
		entries = append(entries, entry)
	}
	if len(resultsByContext) != 0 {
		return nil, errors.New("tool results are missing their source model output")
	}
	return entries, nil
}

func canonicalAssistantContent(
	raw json.RawMessage,
	results []ToolResultRef,
) ([]AssistantContentEntry, []ToolResultRef, error) {
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil || blocks == nil {
		if err == nil {
			err = errors.New("content must be a JSON array")
		}
		return nil, nil, err
	}
	resultIndexByCallID := make(map[string]int, len(results))
	for index := range results {
		callID := results[index].ToolCallID
		if callID == "" {
			return nil, nil, errors.New("tool result is missing its tool call id")
		}
		if _, duplicate := resultIndexByCallID[callID]; duplicate {
			return nil, nil, fmt.Errorf("duplicate tool result for tool call %s", callID)
		}
		resultIndexByCallID[callID] = index
	}

	content := make([]AssistantContentEntry, 0, len(blocks))
	orderedResults := make([]ToolResultRef, 0, len(results))
	usedResults := make(map[int]struct{}, len(results))
	for index, block := range blocks {
		var discriminator struct {
			Type canonicalContentBlockType `json:"type"`
		}
		if err := json.Unmarshal(block, &discriminator); err != nil {
			return nil, nil, fmt.Errorf("decode content block %d: %w", index, err)
		}
		if discriminator.Type != canonicalContentBlockToolCall {
			content = append(content, AssistantBlockEntry{Content: block})
			continue
		}
		var link struct {
			ToolCallID string `json:"tool_call_id"`
		}
		if err := json.Unmarshal(block, &link); err != nil || link.ToolCallID == "" {
			return nil, nil, fmt.Errorf("content block %d has an invalid tool call link", index)
		}
		resultIndex, ok := resultIndexByCallID[link.ToolCallID]
		if !ok {
			return nil, nil, fmt.Errorf(
				"tool call %s has no completed result",
				link.ToolCallID,
			)
		}
		if _, duplicate := usedResults[resultIndex]; duplicate {
			return nil, nil, fmt.Errorf(
				"tool call %s appears more than once",
				link.ToolCallID,
			)
		}
		usedResults[resultIndex] = struct{}{}
		content = append(content, AssistantToolCallEntry{
			ToolCall: results[resultIndex],
		})
		orderedResults = append(orderedResults, results[resultIndex])
	}
	if len(usedResults) != len(results) {
		return nil, nil, errors.New("tool result has no canonical tool call block")
	}
	return content, orderedResults, nil
}
