package openaichatcompletions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/apivariantbody"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func (p protocol) BuildRequest(ctx context.Context, input model.PrepareInput) (json.RawMessage, error) {
	_ = ctx
	c := p.client
	apiVariant := c.ModelAPIVariant()
	providerModelSlug := c.RequestedProviderModelSlug()
	if providerModelSlug == "" {
		return nil, errors.New("openai-chat-completions provider model slug is required")
	}
	supportsTools := c.ModelCapabilities.SupportsTools
	if input.Policy.SupportsTools != nil {
		supportsTools = input.Policy.SupportsTools
	}
	if supportsTools != nil && !*supportsTools && len(input.Context.ToolSpecs) > 0 {
		return nil, errors.New("openai-chat-completions model does not support tools")
	}
	if err := validateToolResultProviderCallIDs(input.Context.ToolResults); err != nil {
		return nil, err
	}
	messages, err := buildMessages(
		input.Context,
		model.ProviderReplayIdentityForClient(c.ModelProviderConfigID, c),
		input.Policy,
	)
	if err != nil {
		return nil, err
	}
	payload := chatCompletionsRequest{
		Model:    providerModelSlug,
		Messages: messages,
		Tools:    buildTools(input.Context.ToolSpecs),
		N:        1,
	}
	if apiVariant != modelprotocol.APIVariantOpenRouter {
		store := false
		payload.Store = &store
	}
	if len(payload.Tools) > 0 {
		payload.ToolChoice = "auto"
	}
	reasoningOwned := chatCompletionsOwnsReasoning(c.ModelCapabilities, input.Policy)
	if reasoningOwned {
		if apiVariant == modelprotocol.APIVariantOpenRouter {
			payload.Reasoning = &chatReasoning{Effort: input.Policy.ReasoningEffort}
		} else {
			payload.ReasoningEffort = input.Policy.ReasoningEffort
		}
	}
	if input.Policy.MaxOutputTokens > 0 {
		payload.MaxCompletionTokens = input.Policy.MaxOutputTokens
	}
	plan := model.PlanPromptCache(
		model.PromptCacheRoute{
			APIFormat:         modelprotocol.APIFormatOpenAIChatCompletions,
			APIVariant:        apiVariant,
			BaseURL:           c.endpoint().ResolvedBaseURL(),
			ProviderModelSlug: providerModelSlug,
		},
		input.Context,
		input.Policy.CacheRetention,
	)
	if apivariantbody.Sets(c.APIVariantOptions, "session_id", "prompt_cache_key") {
		plan.Affinity = model.PromptCacheAffinityNone
	}
	applyPromptCachePlan(&payload, plan)
	return apivariantbody.MarshalWithAPIVariantOptions(
		c.APIVariantOptions,
		payload,
		chatCompletionsOwnedFields(reasoningOwned)...,
	)
}

func chatCompletionsOwnedFields(reasoningOwned bool) []string {
	fields := []string{
		"model",
		"stream",
		"messages",
		"tools",
		"tool_choice",
		"max_tokens",
		"max_completion_tokens",
		"n",
	}
	if reasoningOwned {
		fields = append(fields, "reasoning_effort", "reasoning")
	}
	return fields
}

func chatCompletionsOwnsReasoning(capabilities model.Capabilities, policy model.RequestPolicy) bool {
	supportsReasoning := capabilities.SupportsReasoning
	if policy.SupportsReasoning != nil {
		supportsReasoning = *policy.SupportsReasoning
	}
	return supportsReasoning && policy.ReasoningEffort != ""
}

func validateToolResultProviderCallIDs(results []modelcontext.ToolResultRef) error {
	for _, result := range results {
		if result.ProviderCallID == "" {
			return fmt.Errorf("tool result %s is missing provider call id", result.DurableID)
		}
	}
	return nil
}

type chatCompletionsRequest struct {
	Model                string               `json:"model"`
	Stream               bool                 `json:"stream"`
	Messages             []chatMessage        `json:"messages"`
	Tools                []chatToolDefinition `json:"tools,omitempty"`
	ToolChoice           string               `json:"tool_choice,omitempty"`
	MaxCompletionTokens  int                  `json:"max_completion_tokens,omitempty"`
	N                    int                  `json:"n"`
	PromptCacheKey       string               `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string               `json:"prompt_cache_retention,omitempty"`
	SessionID            string               `json:"session_id,omitempty"`
	ReasoningEffort      string               `json:"reasoning_effort,omitempty"`
	Reasoning            *chatReasoning       `json:"reasoning,omitempty"`
	Store                *bool                `json:"store,omitempty"`
}

type chatReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type chatRole string

const (
	chatRoleSystem    chatRole = "system"
	chatRoleUser      chatRole = "user"
	chatRoleAssistant chatRole = "assistant"
	chatRoleTool      chatRole = "tool"
)

type chatMessage struct {
	Role           chatRole        `json:"role"`
	Content        any             `json:"content"`
	ToolCalls      []chatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID     string          `json:"tool_call_id,omitempty"`
	ProviderReplay json.RawMessage `json:"-"`
}

func (m chatMessage) MarshalJSON() ([]byte, error) {
	if len(m.ProviderReplay) != 0 {
		return m.ProviderReplay, nil
	}
	type wireMessage chatMessage
	return json.Marshal(wireMessage(m))
}

type chatToolDefinition struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Arguments   string          `json:"arguments,omitempty"`
}

func buildMessages(
	bundle modelcontext.Bundle,
	replayIdentity modelenvelope.ProviderReplayIdentity,
	policy model.RequestPolicy,
) ([]chatMessage, error) {
	history, err := modelcontext.CanonicalHistory(bundle)
	if err != nil {
		return nil, fmt.Errorf("build openai-chat-completions history: %w", err)
	}
	bundle.ResolvedMedia = renderableMedia(bundle.ResolvedMedia)
	capacity := 1 + len(bundle.Messages) + len(bundle.ToolResults)*2
	if bundle.ContextCheckpoint != nil {
		capacity++
	}
	if modelcontext.MachinePoolContextEnabled(bundle.ToolSpecs) {
		capacity++
	}
	if modelcontext.IntegrationTargetContextEnabled(bundle.ToolSpecs) {
		capacity++
	}
	messages := make([]chatMessage, 0, capacity)
	if systemPrompt := modelcontext.ProjectedSystemPrompt(bundle); strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, chatMessage{Role: chatRoleSystem, Content: systemPrompt})
	}
	if checkpoint := bundle.ContextCheckpoint; checkpoint != nil {
		messages = append(messages, chatMessage{
			Role:    chatRoleUser,
			Content: modelcontext.ProjectedCheckpointContent(*checkpoint),
		})
	}
	for _, entry := range history {
		if entry.Message.Role == modelprotocol.RoleAssistant {
			assistantMessages, buildErr := assistantMessagesForEntry(
				entry.Message,
				entry.AssistantContent,
				entry.ToolResults,
				bundle.ResolvedMedia,
				replayIdentity,
				policy,
			)
			if buildErr != nil {
				return nil, buildErr
			}
			messages = append(messages, assistantMessages...)
			continue
		}
		message, ok := messageFromContext(entry.Message, bundle.ResolvedMedia)
		if ok {
			messages = append(messages, message)
		}
	}
	if modelcontext.MachinePoolContextEnabled(bundle.ToolSpecs) {
		messages = append(messages, chatMessage{
			Role:    chatRoleSystem,
			Content: modelcontext.AvailableMachinePoolsContent(bundle.AvailableMachinePools),
		})
	}
	if modelcontext.IntegrationTargetContextEnabled(bundle.ToolSpecs) {
		messages = append(messages, chatMessage{
			Role:    chatRoleSystem,
			Content: modelcontext.IntegrationTargetsContent(bundle.IntegrationTargets),
		})
	}
	if len(messages) == 0 {
		return []chatMessage{{Role: chatRoleUser, Content: "Continue."}}, nil
	}
	switch messages[0].Role {
	case chatRoleSystem, chatRoleUser:
		return messages, nil
	default:
		return append([]chatMessage{{Role: chatRoleUser, Content: "Continue."}}, messages...), nil
	}
}

func messageFromContext(
	message modelcontext.Message,
	media map[string]modelcontext.ResolvedMedia,
) (chatMessage, bool) {
	switch message.Role {
	case modelprotocol.RoleAssistant:
		text := textContentFromParts(message.Content, nil)
		if strings.TrimSpace(text) == "" {
			return chatMessage{}, false
		}
		return chatMessage{Role: chatRoleAssistant, Content: text}, true
	case modelprotocol.RoleUser:
		content := userChatContentFromParts(message.Content, media)
		if content == nil {
			return chatMessage{}, false
		}
		return chatMessage{Role: chatRoleUser, Content: content}, true
	default:
		return chatMessage{}, false
	}
}

func buildTools(specs []modelcontext.ToolSpec) []chatToolDefinition {
	tools := make([]chatToolDefinition, 0, len(specs))
	for _, spec := range specs {
		parameters := spec.InputSchema
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, chatToolDefinition{
			Type: "function",
			Function: chatFunction{
				Name:        spec.Name,
				Description: spec.Description,
				Parameters:  parameters,
			},
		})
	}
	return tools
}

func assistantMessagesForEntry(
	source modelcontext.Message,
	content []modelcontext.AssistantContentEntry,
	group []modelcontext.ToolResultRef,
	media map[string]modelcontext.ResolvedMedia,
	replayIdentity modelenvelope.ProviderReplayIdentity,
	policy model.RequestPolicy,
) ([]chatMessage, error) {
	if policy.AllowsProviderReplay(source.Sequence) {
		if replay, ok := completeChatReplay(source, content, replayIdentity); ok {
			return appendToolResultMessages([]chatMessage{replay}, group, media), nil
		}
	}
	contentParts := make([]json.RawMessage, 0, len(content))
	for _, entry := range content {
		switch entry := entry.(type) {
		case modelcontext.AssistantBlockEntry:
			contentParts = append(contentParts, entry.Content)
		case modelcontext.AssistantToolCallEntry:
		default:
			return nil, fmt.Errorf("unsupported assistant content entry %T", entry)
		}
	}
	rawContent, err := json.Marshal(contentParts)
	if err != nil {
		return nil, err
	}
	calls := make([]chatToolCall, 0, len(group))
	for _, result := range group {
		calls = append(calls, chatToolCall{
			ID:   result.ProviderCallID,
			Type: "function",
			Function: chatFunction{
				Name:      result.Name,
				Arguments: toolArguments(result),
			},
		})
	}
	assistant := chatMessage{
		Role:      chatRoleAssistant,
		Content:   textContentFromParts(rawContent, nil),
		ToolCalls: calls,
	}
	if assistant.Content == "" && len(assistant.ToolCalls) == 0 {
		return nil, nil
	}
	return appendToolResultMessages([]chatMessage{assistant}, group, media), nil
}

func appendToolResultMessages(
	messages []chatMessage,
	results []modelcontext.ToolResultRef,
	media map[string]modelcontext.ResolvedMedia,
) []chatMessage {
	var mediaContent []any
	for _, result := range results {
		messages = append(messages, chatMessage{
			Role:       chatRoleTool,
			ToolCallID: result.ProviderCallID,
			Content:    toolResultOutput(result, media),
		})
		mediaContent = append(mediaContent, toolResultMediaContent(result, media)...)
	}
	if len(mediaContent) > 0 {
		messages = append(messages, chatMessage{Role: chatRoleUser, Content: mediaContent})
	}
	return messages
}

func completeChatReplay(
	source modelcontext.Message,
	content []modelcontext.AssistantContentEntry,
	target modelenvelope.ProviderReplayIdentity,
) (chatMessage, bool) {
	if !source.ProviderReplaySource.Matches(target) {
		return chatMessage{}, false
	}
	var message chatResponseMessage
	if err := json.Unmarshal(source.ProviderReplay, &message); err != nil {
		return chatMessage{}, false
	}
	replayed, ok := chatReplaySemantics(message)
	if !ok {
		return chatMessage{}, false
	}
	canonical, ok := canonicalChatSemantics(content)
	if !ok || !sameChatSemantics(replayed, canonical) {
		return chatMessage{}, false
	}
	normalized, err := chatReplayForRequest(message)
	if err != nil {
		return chatMessage{}, false
	}
	return chatMessage{Role: chatRoleAssistant, ProviderReplay: normalized}, true
}

type chatReplaySemantic struct {
	kind      string
	text      string
	callID    string
	name      string
	arguments json.RawMessage
}

func chatReplaySemantics(replay chatResponseMessage) ([]chatReplaySemantic, bool) {
	if replay.Role != chatRoleAssistant {
		return nil, false
	}
	semantics := make([]chatReplaySemantic, 0, len(replay.ToolCalls)+2)
	if reasoning := reasoningTextFromChatMessage(replay); reasoning != "" {
		semantics = append(semantics, chatReplaySemantic{kind: "reasoning", text: reasoning})
	}
	text, err := textFromChatContent(replay.Content)
	if err != nil {
		return nil, false
	}
	if text == "" {
		text = replay.Refusal
	}
	if text != "" {
		semantics = append(semantics, chatReplaySemantic{kind: "text", text: text})
	}
	for _, raw := range replay.ToolCalls {
		var call chatToolCall
		if err := json.Unmarshal(raw, &call); err != nil ||
			call.Type != "function" ||
			call.ID == "" || call.Function.Name == "" ||
			modelenvelope.ValidateToolInput(
				json.RawMessage(call.Function.Arguments),
			) != nil {
			return nil, false
		}
		semantics = append(semantics, chatReplaySemantic{
			kind:      "tool_call",
			callID:    call.ID,
			name:      call.Function.Name,
			arguments: json.RawMessage(call.Function.Arguments),
		})
	}
	return semantics, true
}

func canonicalChatSemantics(
	content []modelcontext.AssistantContentEntry,
) ([]chatReplaySemantic, bool) {
	semantics := make([]chatReplaySemantic, 0, len(content))
	for _, entry := range content {
		switch entry := entry.(type) {
		case modelcontext.AssistantToolCallEntry:
			arguments := json.RawMessage(toolArguments(entry.ToolCall))
			if modelenvelope.ValidateToolInput(arguments) != nil {
				return nil, false
			}
			semantics = append(semantics, chatReplaySemantic{
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
				semantics = append(semantics, chatReplaySemantic{
					kind: "text",
					text: jsonFieldText(part["text"]),
				})
			case "reasoning":
				semantics = append(semantics, chatReplaySemantic{
					kind: "reasoning",
					text: jsonFieldText(part["text"]),
				})
			case "media_ref":
				semantics = append(semantics, chatReplaySemantic{kind: "media"})
			default:
				return nil, false
			}
		default:
			return nil, false
		}
	}
	return semantics, true
}

func sameChatSemantics(left, right []chatReplaySemantic) bool {
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
