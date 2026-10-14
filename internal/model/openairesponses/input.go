package openairesponses

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type responsesRole string

const (
	responsesRoleUser      responsesRole = "user"
	responsesRoleAssistant responsesRole = "assistant"
)

func buildInput(
	bundle modelcontext.Bundle,
	replayIdentity modelenvelope.ProviderReplayIdentity,
	policy model.RequestPolicy,
) ([]any, error) {
	history, err := modelcontext.CanonicalHistory(bundle)
	if err != nil {
		return nil, fmt.Errorf("build openai-responses history: %w", err)
	}
	bundle.ResolvedMedia = renderableMedia(bundle.ResolvedMedia)
	capacity := len(bundle.Messages) + len(bundle.ToolResults)
	if bundle.ContextCheckpoint != nil {
		capacity++
	}
	items := make([]any, 0, capacity)
	clientToolSearch := modelcontext.DeferredToolsEnabled(bundle.ToolSpecs)
	if checkpoint := bundle.ContextCheckpoint; checkpoint != nil {
		items = append(
			items,
			map[string]any{
				"role":    responsesRoleUser,
				"content": modelcontext.ProjectedCheckpointContent(*checkpoint),
			},
		)
	}
	for _, entry := range history {
		switch entry.Message.Role {
		case modelprotocol.RoleAssistant:
			items, err = appendAssistantResponseEntry(
				items,
				entry.Message,
				entry.AssistantContent,
				replayIdentity,
				policy,
				clientToolSearch,
			)
			if err != nil {
				return nil, err
			}
		case modelprotocol.RoleUser:
			items = append(items, map[string]any{
				"role":    responsesRoleUser,
				"content": renderOpenAIInputContent(entry.Message.Content, bundle.ResolvedMedia),
			})
		default:
			return nil, fmt.Errorf("unsupported canonical message role %q", entry.Message.Role)
		}
		for _, result := range entry.ToolResults {
			if clientToolSearch && result.Name == toolcatalog.ToolNameToolSearch {
				var loaded []modelcontext.ToolSearchDefinition
				if search, ok := modelcontext.ToolSearchResultFromToolResult(result); ok {
					loaded = modelcontext.DeferredToolSearchDefinitions(bundle.ToolSpecs, search)
				}
				items = append(items, toolSearchOutputItem(result.ProviderCallID, loaded))
				if len(loaded) == 0 {
					items = append(items, map[string]any{
						"role":    responsesRoleUser,
						"content": toolResultOutput(result, bundle.ResolvedMedia),
					})
				}
				continue
			}
			items = append(
				items,
				map[string]any{
					"type":    "function_call_output",
					"call_id": result.ProviderCallID,
					"output":  toolResultOutput(result, bundle.ResolvedMedia),
				},
			)
		}
	}
	return items, nil
}

func toolSearchOutputItem(callID string, loaded []modelcontext.ToolSearchDefinition) map[string]any {
	tools := make([]responsesTool, 0, len(loaded))
	for _, definition := range loaded {
		tools = append(tools, discoveredToolDefinition(definition))
	}
	return map[string]any{
		"type":      "tool_search_output",
		"execution": "client",
		"call_id":   callID,
		"status":    "completed",
		"tools":     tools,
	}
}

func appendAssistantResponseEntry(
	items []any,
	source modelcontext.Message,
	content []modelcontext.AssistantContentEntry,
	replayIdentity modelenvelope.ProviderReplayIdentity,
	policy model.RequestPolicy,
	clientToolSearch bool,
) ([]any, error) {
	if policy.AllowsProviderReplay(source.Sequence) {
		if replayItems, ok := completeResponseReplay(source, content, replayIdentity, clientToolSearch); ok {
			for _, replayItem := range replayItems {
				items = append(items, replayItem)
			}
			return items, nil
		}
	}
	return appendCanonicalAssistantResponse(items, content, clientToolSearch)
}

func appendCanonicalAssistantResponse(
	items []any,
	content []modelcontext.AssistantContentEntry,
	clientToolSearch bool,
) ([]any, error) {
	pending := make([]json.RawMessage, 0, len(content))
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		raw, err := json.Marshal(pending)
		if err != nil {
			return err
		}
		rendered := renderOpenAIAssistantContent(raw)
		if len(rendered) > 0 {
			items = append(items, map[string]any{
				"role":    responsesRoleAssistant,
				"content": rendered,
			})
		}
		pending = pending[:0]
		return nil
	}
	for _, entry := range content {
		switch entry := entry.(type) {
		case modelcontext.AssistantBlockEntry:
			pending = append(pending, entry.Content)
		case modelcontext.AssistantToolCallEntry:
			if err := flush(); err != nil {
				return nil, err
			}
			if clientToolSearch && entry.ToolCall.Name == toolcatalog.ToolNameToolSearch {
				items = append(items, map[string]any{
					"type":      "tool_search_call",
					"execution": "client",
					"call_id":   entry.ToolCall.ProviderCallID,
					"status":    "completed",
					"arguments": entry.ToolCall.Input,
				})
				continue
			}
			items = append(items, map[string]any{
				"type":      "function_call",
				"call_id":   entry.ToolCall.ProviderCallID,
				"name":      entry.ToolCall.Name,
				"arguments": toolArguments(entry.ToolCall),
			})
		default:
			return nil, fmt.Errorf("unsupported assistant content entry %T", entry)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return items, nil
}

type responseReplaySemantic struct {
	kind           string
	text           string
	callID         string
	name           string
	arguments      json.RawMessage
	toolSearchItem bool
}

func completeResponseReplay(
	source modelcontext.Message,
	content []modelcontext.AssistantContentEntry,
	target modelenvelope.ProviderReplayIdentity,
	clientToolSearch bool,
) ([]json.RawMessage, bool) {
	if !source.ProviderReplaySource.Matches(target) {
		return nil, false
	}
	var items []json.RawMessage
	if json.Unmarshal(source.ProviderReplay, &items) != nil || len(items) == 0 {
		return nil, false
	}
	replayed, ok := responseReplaySemantics(items)
	if !ok || !replayMatchesToolSearchMode(replayed, clientToolSearch) {
		return nil, false
	}
	canonical, ok := canonicalResponseSemantics(content)
	if !ok || !sameResponseSemantics(replayed, canonical) {
		return nil, false
	}
	return items, true
}

func replayMatchesToolSearchMode(replayed []responseReplaySemantic, clientToolSearch bool) bool {
	for _, semantic := range replayed {
		if semantic.name == toolcatalog.ToolNameToolSearch && semantic.toolSearchItem != clientToolSearch {
			return false
		}
	}
	return true
}

func responseReplaySemantics(items []json.RawMessage) ([]responseReplaySemantic, bool) {
	semantics := make([]responseReplaySemantic, 0, len(items))
	reasoningNeedsContinuation := false
	for _, raw := range items {
		var item responsesOutputItem
		if json.Unmarshal(raw, &item) != nil {
			return nil, false
		}
		switch item.Type {
		case "message":
			reasoningNeedsContinuation = false
			for _, content := range item.Content {
				switch content.Type {
				case "output_text", "text":
					if content.Text != "" {
						semantics = append(semantics, responseReplaySemantic{
							kind: "text",
							text: content.Text,
						})
					}
				case "refusal":
					if content.Refusal != "" {
						semantics = append(semantics, responseReplaySemantic{
							kind: "text",
							text: content.Refusal,
						})
					}
				default:
					return nil, false
				}
			}
		case "reasoning":
			if item.EncryptedContent == "" {
				return nil, false
			}
			reasoningNeedsContinuation = true
			if summary := reasoningSummaryText(item); summary != "" {
				semantics = append(semantics, responseReplaySemantic{
					kind: "reasoning",
					text: summary,
				})
			}
		case "function_call":
			if strings.TrimSpace(item.CallID) == "" || strings.TrimSpace(item.Name) == "" ||
				modelenvelope.ValidateToolInput(json.RawMessage(item.Arguments)) != nil {
				return nil, false
			}
			reasoningNeedsContinuation = false
			semantics = append(semantics, responseReplaySemantic{
				kind:      "tool_call",
				callID:    item.CallID,
				name:      item.Name,
				arguments: json.RawMessage(item.Arguments),
			})
		case "tool_search_call":
			if strings.TrimSpace(item.CallID) == "" {
				if !validProviderOnlyResponseItem(item) {
					return nil, false
				}
				reasoningNeedsContinuation = false
				continue
			}
			arguments := toolSearchCallArguments(raw)
			if modelenvelope.ValidateToolInput(arguments) != nil {
				return nil, false
			}
			reasoningNeedsContinuation = false
			semantics = append(semantics, responseReplaySemantic{
				kind:           "tool_call",
				callID:         item.CallID,
				name:           toolcatalog.ToolNameToolSearch,
				arguments:      arguments,
				toolSearchItem: true,
			})
		default:
			if !validProviderOnlyResponseItem(item) {
				return nil, false
			}
			reasoningNeedsContinuation = false
		}
	}
	if reasoningNeedsContinuation {
		return nil, false
	}
	return semantics, true
}

func validProviderOnlyResponseItem(item responsesOutputItem) bool {
	if item.ID == "" {
		return false
	}
	switch item.Type {
	case "file_search_call",
		"function_call_output",
		"web_search_call",
		"computer_call",
		"computer_call_output",
		"tool_search_call",
		"tool_search_output",
		"compaction",
		"code_interpreter_call",
		"local_shell_call",
		"local_shell_call_output",
		"shell_call",
		"shell_call_output",
		"apply_patch_call",
		"apply_patch_call_output",
		"mcp_call",
		"mcp_list_tools",
		"mcp_approval_request",
		"mcp_approval_response",
		"custom_tool_call",
		"custom_tool_call_output":
		return true
	default:
		return false
	}
}

func canonicalResponseSemantics(
	content []modelcontext.AssistantContentEntry,
) ([]responseReplaySemantic, bool) {
	semantics := make([]responseReplaySemantic, 0, len(content))
	for _, entry := range content {
		switch entry := entry.(type) {
		case modelcontext.AssistantToolCallEntry:
			arguments := json.RawMessage(toolArguments(entry.ToolCall))
			if modelenvelope.ValidateToolInput(arguments) != nil {
				return nil, false
			}
			semantics = append(semantics, responseReplaySemantic{
				kind:      "tool_call",
				callID:    entry.ToolCall.ProviderCallID,
				name:      entry.ToolCall.Name,
				arguments: arguments,
			})
		case modelcontext.AssistantBlockEntry:
			var part map[string]json.RawMessage
			if json.Unmarshal(entry.Content, &part) != nil {
				return nil, false
			}
			switch jsonFieldText(part["type"]) {
			case "text":
				semantics = append(semantics, responseReplaySemantic{
					kind: "text",
					text: jsonFieldText(part["text"]),
				})
			case "reasoning":
				semantics = append(semantics, responseReplaySemantic{
					kind: "reasoning",
					text: jsonFieldText(part["text"]),
				})
			case "media_ref":
				return nil, false
			default:
				return nil, false
			}
		default:
			return nil, false
		}
	}
	return semantics, true
}

func sameResponseSemantics(left, right []responseReplaySemantic) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].kind != right[index].kind ||
			left[index].text != right[index].text ||
			left[index].callID != right[index].callID ||
			left[index].name != right[index].name {
			return false
		}
		if left[index].kind == "tool_call" &&
			!jsoncanonical.Equal(left[index].arguments, right[index].arguments) {
			return false
		}
	}
	return true
}

func toolArguments(result modelcontext.ToolResultRef) string {
	return string(result.Input)
}

func toolSearchCallArguments(raw json.RawMessage) json.RawMessage {
	var item struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(raw, &item) != nil || len(item.Arguments) == 0 {
		return json.RawMessage(`{}`)
	}
	var encoded string
	if json.Unmarshal(item.Arguments, &encoded) == nil {
		return json.RawMessage(encoded)
	}
	return item.Arguments
}
