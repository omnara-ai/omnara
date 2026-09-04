package anthropicmessages

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/apivariantbody"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (p protocol) BuildRequest(ctx context.Context, input model.PrepareInput) (json.RawMessage, error) {
	_ = ctx
	c := p.client
	providerModelSlug := c.RequestedProviderModelSlug()
	if providerModelSlug == "" {
		return nil, errors.New("anthropic-messages provider model slug is required")
	}
	supportsTools := c.ModelCapabilities.SupportsTools
	if input.Policy.SupportsTools != nil {
		supportsTools = input.Policy.SupportsTools
	}
	if supportsTools != nil && !*supportsTools && len(input.Context.ToolSpecs) > 0 {
		return nil, errors.New("anthropic-messages model does not support tools")
	}
	maxTokens := input.Policy.MaxOutputTokens
	if maxTokens <= 0 {
		return nil, errors.New("anthropic-messages max output tokens are required")
	}
	if err := validateToolNames(input.Context.ToolSpecs); err != nil {
		return nil, err
	}
	control := CacheControlFor(model.PlanPromptCache(
		model.ProviderRoute{
			APIFormat:         modelprotocol.APIFormatAnthropicMessages,
			APIVariant:        c.APIVariant,
			ProviderModelSlug: providerModelSlug,
		},
		input.Context,
		input.Policy.CacheRetention,
	))
	messages, err := buildMessages(
		input.Context,
		input.Policy,
		model.ProviderReplayIdentityForClient(c.ModelProviderConfigID, c),
		control,
	)
	if err != nil {
		return nil, err
	}
	payload := messagesRequest{
		Model:     providerModelSlug,
		MaxTokens: maxTokens,
		System:    systemContent(input.Context, control),
		Messages:  messages,
		Tools:     buildTools(input.Context.ToolSpecs, control),
	}
	if len(payload.Tools) > 0 {
		payload.ToolChoice = map[string]string{"type": "auto"}
	}
	return apivariantbody.MarshalWithAPIVariantOptions(
		c.APIVariantOptions,
		payload,
		anthropicMessagesOwnedFields()...,
	)
}

func anthropicMessagesOwnedFields() []string {
	return []string{
		"model",
		"stream",
		"max_tokens",
		"system",
		"messages",
		"tools",
		"tool_choice",
	}
}

type messagesRequest struct {
	Model      string            `json:"model"`
	Stream     bool              `json:"stream"`
	MaxTokens  int               `json:"max_tokens"`
	System     any               `json:"system,omitempty"`
	Messages   []message         `json:"messages"`
	Tools      []toolDefinition  `json:"tools,omitempty"`
	ToolChoice map[string]string `json:"tool_choice,omitempty"`
}

type anthropicRole string

const (
	anthropicRoleUser      anthropicRole = "user"
	anthropicRoleAssistant anthropicRole = "assistant"
)

type message struct {
	Role    anthropicRole `json:"role"`
	Content any           `json:"content"`
}

type textBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

type toolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type toolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   any    `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

type toolDefinition struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl *CacheControl   `json:"cache_control,omitempty"`
}

func validateToolNames(specs []modelcontext.ToolSpec) error {
	for _, spec := range specs {
		if !toolNamePattern.MatchString(spec.Name) {
			return fmt.Errorf("tool name %q is invalid for anthropic-messages", spec.Name)
		}
	}
	return nil
}

func systemContent(bundle modelcontext.Bundle, control *CacheControl) any {
	blocks := []textBlock{{Type: "text", Text: modelcontext.ProjectedSystemPrompt(bundle)}}
	if modelcontext.MachinePoolContextEnabled(bundle.ToolSpecs) {
		blocks = append(blocks, textBlock{
			Type: "text",
			Text: modelcontext.AvailableMachinePoolsContent(
				bundle.AvailableMachinePools,
			),
		})
	}
	if modelcontext.IntegrationTargetContextEnabled(bundle.ToolSpecs) {
		blocks = append(blocks, textBlock{
			Type: "text",
			Text: modelcontext.IntegrationTargetsContent(
				bundle.IntegrationTargets,
			),
		})
	}
	if control != nil {
		blocks[len(blocks)-1].CacheControl = control
	}
	if len(blocks) == 1 && blocks[0].CacheControl == nil {
		return blocks[0].Text
	}
	return blocks
}

func buildMessages(
	bundle modelcontext.Bundle,
	policy model.RequestPolicy,
	replayIdentity modelenvelope.ProviderReplayIdentity,
	control *CacheControl,
) ([]message, error) {
	history, err := modelcontext.CanonicalHistory(bundle)
	if err != nil {
		return nil, fmt.Errorf("build anthropic-messages history: %w", err)
	}
	bundle.ResolvedMedia = renderableMedia(bundle.ResolvedMedia)
	messageCapacity := len(bundle.Messages) + len(bundle.ToolResults)*2
	if bundle.ContextCheckpoint != nil {
		messageCapacity++
	}
	messages := make([]message, 0, messageCapacity)
	if checkpoint := bundle.ContextCheckpoint; checkpoint != nil {
		block := textBlock{
			Type:         "text",
			Text:         modelcontext.ProjectedCheckpointContent(*checkpoint),
			CacheControl: control,
		}
		messages = appendMessageBlocks(messages, anthropicRoleUser, []any{block})
	}
	usedIDs := map[string]bool{}
	historyAdded := false
	for _, entry := range history {
		if entry.Message.Role == modelprotocol.RoleAssistant {
			assistantBlocks, toolUseIDByCallID, buildErr := assistantTurnForEntry(
				entry.Message,
				entry.AssistantContent,
				entry.ToolResults,
				replayIdentity,
				policy,
				usedIDs,
			)
			if buildErr != nil {
				return nil, buildErr
			}
			if len(assistantBlocks) > 0 {
				historyAdded = true
				messages = appendMessageBlocks(
					messages,
					anthropicRoleAssistant,
					assistantBlocks,
				)
			}
			if len(entry.ToolResults) == 0 {
				continue
			}
			resultContent := make([]any, 0, len(entry.ToolResults))
			for _, result := range entry.ToolResults {
				resultContent = append(resultContent, toolResultBlock{
					Type:      "tool_result",
					ToolUseID: toolUseIDByCallID[result.ProviderCallID],
					Content:   toolResultContent(result, bundle.ResolvedMedia),
					IsError:   result.Outcome == executionstore.ToolResultOutcomeFailed,
				})
			}
			messages = appendMessageBlocks(messages, anthropicRoleUser, resultContent)
			historyAdded = true
			continue
		}
		blocks := messageBlocksFromParts(entry.Message.Content, bundle.ResolvedMedia)
		if len(blocks) > 0 {
			messages = appendMessageBlocks(messages, anthropicRoleUser, blocks)
			historyAdded = true
		}
	}
	if historyAdded {
		messages = markLastMessageCacheBreakpoint(messages, control)
	}
	if len(messages) > 0 && messages[0].Role == anthropicRoleAssistant {
		messages = append(
			[]message{{Role: anthropicRoleUser, Content: []any{textBlock{Type: "text", Text: "Continue."}}}},
			messages...)
	}
	if len(messages) == 0 {
		messages = append(
			messages,
			message{Role: anthropicRoleUser, Content: []any{textBlock{Type: "text", Text: "Continue."}}},
		)
	}
	return messages, nil
}

func markLastMessageCacheBreakpoint(messages []message, control *CacheControl) []message {
	if control == nil {
		return messages
	}
	for index := len(messages) - 1; index >= 0; index-- {
		blocks, ok := messages[index].Content.([]any)
		if !ok {
			continue
		}
		for blockIndex := len(blocks) - 1; blockIndex >= 0; blockIndex-- {
			block, ok := cacheableBlock(blocks[blockIndex])
			if !ok {
				continue
			}
			block["cache_control"] = control
			blocks[blockIndex] = block
			messages[index].Content = blocks
			return messages
		}
	}
	return messages
}

func cacheableBlock(value any) (map[string]any, bool) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var block map[string]any
	if decoder.Decode(&block) != nil || block == nil {
		return nil, false
	}
	switch block["type"] {
	case "text":
		text, _ := block["text"].(string)
		return block, strings.TrimSpace(text) != ""
	case "tool_use", "tool_result", "image", "document":
		return block, true
	}
	return nil, false
}

func appendMessageBlocks(messages []message, role anthropicRole, blocks []any) []message {
	if len(messages) > 0 && messages[len(messages)-1].Role == role {
		if existing, ok := messages[len(messages)-1].Content.([]any); ok {
			messages[len(messages)-1].Content = append(existing, blocks...)
			return messages
		}
	}
	return append(messages, message{Role: role, Content: blocks})
}

func messageBlocksFromParts(raw json.RawMessage, media map[string]modelcontext.ResolvedMedia) []any {
	return renderAnthropicContent(raw, media)
}

func toolResultContent(result modelcontext.ToolResultRef, media map[string]modelcontext.ResolvedMedia) any {
	return renderAnthropicContent(result.ContentParts, media)
}

func renderAnthropicContent(
	raw json.RawMessage,
	media map[string]modelcontext.ResolvedMedia,
) []any {
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil
	}
	blocks := make([]any, 0, len(parts))
	for _, part := range parts {
		partType := jsonFieldText(part["type"])
		if partType == "media_ref" {
			artifactID := jsonFieldText(part["artifact_id"])
			if resolved, ok := media[artifactID]; ok {
				if block, ok := anthropicMediaBlock(resolved); ok {
					if ref := modelcontext.ArtifactPublicID(artifactID); ref != "" {
						blocks = append(blocks, textBlock{Type: "text", Text: "artifact_id: " + ref})
					}
					blocks = append(blocks, block)
				}
				continue
			}
			if text := modelcontext.MediaRefText(part); text != "" {
				blocks = append(blocks, textBlock{Type: "text", Text: text})
			}
			continue
		}
		if partType == "reasoning" {
			continue
		}
		if text := jsonFieldText(part["text"]); strings.TrimSpace(text) != "" {
			blocks = append(blocks, textBlock{Type: "text", Text: text})
			continue
		}
		if partType == "structured_data" {
			if value := part["value"]; len(value) != 0 {
				blocks = append(blocks, textBlock{Type: "text", Text: string(value)})
			}
		}
	}
	return blocks
}

func assistantTurnForEntry(
	source modelcontext.Message,
	content []modelcontext.AssistantContentEntry,
	group []modelcontext.ToolResultRef,
	replayIdentity modelenvelope.ProviderReplayIdentity,
	policy model.RequestPolicy,
	usedIDs map[string]bool,
) ([]any, map[string]string, error) {
	if policy.AllowsProviderReplay(source.Sequence) {
		if blocks, toolUseIDs, ok := completeAnthropicReplay(
			source,
			content,
			replayIdentity,
			usedIDs,
		); ok {
			return blocks, toolUseIDs, nil
		}
	}
	blocks := make([]any, 0, len(content))
	toolUseIDs := make(map[string]string, len(group))
	for _, entry := range content {
		switch entry := entry.(type) {
		case modelcontext.AssistantToolCallEntry:
			id := anthropicToolUseID(entry.ToolCall, usedIDs)
			blocks = append(blocks, rebuiltToolUse(entry.ToolCall, id))
			toolUseIDs[entry.ToolCall.ProviderCallID] = id
		case modelcontext.AssistantBlockEntry:
			raw, err := json.Marshal([]json.RawMessage{entry.Content})
			if err != nil {
				return nil, nil, err
			}
			blocks = append(blocks, messageBlocksFromParts(raw, nil)...)
		default:
			return nil, nil, fmt.Errorf("unsupported assistant content entry %T", entry)
		}
	}
	return blocks, toolUseIDs, nil
}

func completeAnthropicReplay(
	source modelcontext.Message,
	content []modelcontext.AssistantContentEntry,
	target modelenvelope.ProviderReplayIdentity,
	usedIDs map[string]bool,
) ([]any, map[string]string, bool) {
	if !source.ProviderReplaySource.Matches(target) {
		return nil, nil, false
	}
	var rawBlocks []json.RawMessage
	if err := json.Unmarshal(source.ProviderReplay, &rawBlocks); err != nil || len(rawBlocks) == 0 {
		return nil, nil, false
	}
	replayed, replayToolIDs, ok := anthropicReplaySemantics(rawBlocks, usedIDs)
	if !ok {
		return nil, nil, false
	}
	canonical, ok := canonicalAnthropicSemantics(content)
	if !ok || !sameAnthropicSemantics(replayed, canonical) {
		return nil, nil, false
	}
	blocks := make([]any, 0, len(rawBlocks))
	for _, raw := range rawBlocks {
		blocks = append(blocks, raw)
	}
	for id := range replayToolIDs {
		usedIDs[id] = true
	}
	return blocks, replayToolIDs, true
}

type anthropicReplaySemantic struct {
	kind   string
	text   string
	callID string
	name   string
	input  json.RawMessage
}

func anthropicReplaySemantics(
	rawBlocks []json.RawMessage,
	usedIDs map[string]bool,
) ([]anthropicReplaySemantic, map[string]string, bool) {
	semantics := make([]anthropicReplaySemantic, 0, len(rawBlocks))
	toolUseIDs := make(map[string]string)
	for _, raw := range rawBlocks {
		var block contentBlock
		if json.Unmarshal(raw, &block) != nil {
			return nil, nil, false
		}
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				semantics = append(semantics, anthropicReplaySemantic{
					kind: "text",
					text: block.Text,
				})
			}
		case "thinking":
			if block.Signature == "" {
				return nil, nil, false
			}
			if block.Thinking != "" {
				semantics = append(semantics, anthropicReplaySemantic{
					kind: "reasoning",
					text: block.Thinking,
				})
			}
		case "redacted_thinking":
			if block.Data == "" {
				return nil, nil, false
			}
		case "tool_use":
			if block.ID == "" || block.Name == "" || usedIDs[block.ID] ||
				modelenvelope.ValidateToolInput(block.Input) != nil {
				return nil, nil, false
			}
			if _, duplicate := toolUseIDs[block.ID]; duplicate {
				return nil, nil, false
			}
			toolUseIDs[block.ID] = block.ID
			semantics = append(semantics, anthropicReplaySemantic{
				kind:   "tool_call",
				callID: block.ID,
				name:   block.Name,
				input:  block.Input,
			})
		default:
			return nil, nil, false
		}
	}
	return semantics, toolUseIDs, true
}

func canonicalAnthropicSemantics(
	content []modelcontext.AssistantContentEntry,
) ([]anthropicReplaySemantic, bool) {
	semantics := make([]anthropicReplaySemantic, 0, len(content))
	for _, entry := range content {
		switch entry := entry.(type) {
		case modelcontext.AssistantToolCallEntry:
			input := entry.ToolCall.Input
			if modelenvelope.ValidateToolInput(input) != nil {
				return nil, false
			}
			semantics = append(semantics, anthropicReplaySemantic{
				kind:   "tool_call",
				callID: entry.ToolCall.ProviderCallID,
				name:   entry.ToolCall.Name,
				input:  input,
			})
		case modelcontext.AssistantBlockEntry:
			var part map[string]json.RawMessage
			if json.Unmarshal(entry.Content, &part) != nil {
				return nil, false
			}
			switch jsonFieldText(part["type"]) {
			case "text":
				semantics = append(semantics, anthropicReplaySemantic{
					kind: "text",
					text: jsonFieldText(part["text"]),
				})
			case "reasoning":
				semantics = append(semantics, anthropicReplaySemantic{
					kind: "reasoning",
					text: jsonFieldText(part["text"]),
				})
			case "media_ref":
				semantics = append(semantics, anthropicReplaySemantic{kind: "media"})
			default:
				return nil, false
			}
		default:
			return nil, false
		}
	}
	return semantics, true
}

func sameAnthropicSemantics(left, right []anthropicReplaySemantic) bool {
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
			!jsoncanonical.Equal(left[index].input, right[index].input) {
			return false
		}
	}
	return true
}

func jsonFieldText(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func rebuiltToolUse(result modelcontext.ToolResultRef, id string) toolUseBlock {
	return toolUseBlock{
		Type:  "tool_use",
		ID:    id,
		Name:  result.Name,
		Input: result.Input,
	}
}

func anthropicToolUseID(result modelcontext.ToolResultRef, used map[string]bool) string {
	providerCallID := result.ProviderCallID
	base := sanitizeID(result.ModelCallContextID + "_" + providerCallID)
	if base == "" {
		base = sanitizeID(result.ToolCallID)
	}
	if base == "" {
		base = "toolu"
	}
	sum := sha256.Sum256([]byte(
		result.ModelCallContextID + ":" +
			result.ToolCallID + ":" +
			providerCallID,
	))
	suffix := hex.EncodeToString(sum[:])[:8]
	maxBase := 64 - len(suffix) - 1
	if len(base) > maxBase {
		base = base[:maxBase]
	}
	id := base + "_" + suffix
	for index := 1; used[id]; index++ {
		extra := fmt.Sprintf("_%d", index)
		maxBase = 64 - len(suffix) - len(extra) - 1
		if len(base) > maxBase {
			base = base[:maxBase]
		}
		id = base + "_" + suffix + extra
	}
	used[id] = true
	return id
}

func sanitizeID(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func buildTools(specs []modelcontext.ToolSpec, control *CacheControl) []toolDefinition {
	tools := make([]toolDefinition, 0, len(specs))
	for index, spec := range specs {
		schema := spec.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		def := toolDefinition{Name: spec.Name, Description: spec.Description, InputSchema: schema}
		if index == len(specs)-1 && control != nil {
			def.CacheControl = control
		}
		tools = append(tools, def)
	}
	return tools
}
