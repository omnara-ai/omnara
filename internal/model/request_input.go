package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func applyRequestInputMeasurement(prepared *PreparedRequest, client Client, bundle modelcontext.Bundle) {
	prepared.RequestInputIdentity = nil
	prepared.HasMeasuredInputPrefix = false
	projection, err := projectRequestInput(prepared.Body, client.APIFormat())
	if err != nil || prepared.InputRouteFingerprint == "" {
		return
	}
	identity, err := projection.identity(prepared.InputRouteFingerprint)
	if err != nil {
		return
	}
	prepared.RequestInputIdentity = &identity
	var source *modelcontext.Message
	for i := len(bundle.Messages) - 1; i >= 0; i-- {
		if bundle.Messages[i].Role == modelprotocol.RoleAssistant {
			source = &bundle.Messages[i]
			break
		}
	}
	if source == nil || source.RequestInputIdentity == nil ||
		source.ServedProviderModelSlug == "" || source.ServedProviderModelSlug != client.RequestedProviderModelSlug() ||
		!projection.matches(*source.RequestInputIdentity, prepared.InputRouteFingerprint) {
		return
	}
	usage := modelenvelope.NormalizeUsage(source.Usage)
	if usage.InputTokens <= 0 {
		return
	}
	suffix := projection.items[source.RequestInputIdentity.ItemCount:]
	if len(suffix) == 0 {
		return
	}
	// Reasoning retention can change across user turns despite identical wire history.
	// https://developers.openai.com/api/docs/guides/reasoning
	// https://platform.claude.com/docs/en/build-with-claude/thinking
	reasoning := inputHasReasoning(projection.items)
	outputTokens := 0
	if reasoning {
		if source.StopReason != modelenvelope.StopReasonToolUse || usage.OutputTokens <= 0 ||
			inputHasNewUser(suffix, client.APIFormat()) {
			return
		}
		replayed := completeMeasuredReplayItems(*source, suffix, client.APIFormat())
		if replayed == 0 {
			return
		}
		suffix = suffix[replayed:]
		outputTokens = usage.OutputTokens
	}
	raw, err := json.Marshal(suffix)
	if err != nil {
		return
	}
	newTokens := modelcontext.EstimatePreparedRequest(raw, prepared.RenderedMedia)
	if len(suffix) == 0 {
		newTokens = 0
	}
	if usage.InputTokens > math.MaxInt-outputTokens-newTokens {
		return
	}
	prepared.InputTokenEstimate = usage.InputTokens + outputTokens + newTokens
	prepared.HasMeasuredInputPrefix = true
}

func inputHasReasoning(items []json.RawMessage) bool {
	for _, raw := range items {
		var item struct {
			Role             string          `json:"role"`
			Type             string          `json:"type"`
			Content          json.RawMessage `json:"content"`
			ReasoningContent json.RawMessage `json:"reasoning_content"`
			ReasoningDetails json.RawMessage `json:"reasoning_details"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return true
		}
		if item.Type == "reasoning" {
			return true
		}
		if item.Role != "assistant" {
			continue
		}
		for _, value := range []json.RawMessage{item.ReasoningContent, item.ReasoningDetails} {
			if len(value) > 0 && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return true
			}
		}
		var blocks []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(item.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			if block.Type == "thinking" || block.Type == "redacted_thinking" || block.Type == "reasoning" {
				return true
			}
		}
	}
	return false
}

func inputHasNewUser(items []json.RawMessage, format modelprotocol.APIFormat) bool {
	for _, raw := range items {
		var item struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return true
		}
		if item.Role != "user" {
			continue
		}
		if format != modelprotocol.APIFormatAnthropicMessages {
			return true
		}
		var blocks []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(item.Content, &blocks) != nil || len(blocks) == 0 {
			return true
		}
		for _, block := range blocks {
			if block.Type != "tool_result" {
				return true
			}
		}
	}
	return false
}

func completeMeasuredReplayItems(
	source modelcontext.Message,
	suffix []json.RawMessage,
	format modelprotocol.APIFormat,
) int {
	var replay []json.RawMessage
	if json.Unmarshal(source.ProviderReplay, &replay) != nil || len(replay) == 0 {
		return 0
	}
	switch format {
	case modelprotocol.APIFormatOpenAIChatCompletions:
		return 0
	case modelprotocol.APIFormatAnthropicMessages:
		var message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if len(suffix) == 0 || json.Unmarshal(suffix[0], &message) != nil || message.Role != "assistant" {
			return 0
		}
		for _, raw := range replay {
			var block struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(raw, &block) != nil {
				return 0
			}
			switch block.Type {
			case "text", "thinking", "redacted_thinking", "tool_use":
			default:
				return 0
			}
		}
		if sameInputJSON(stripBlockCacheControls(source.ProviderReplay), stripBlockCacheControls(message.Content)) {
			return 1
		}
	case modelprotocol.APIFormatOpenAIResponses:
		if len(replay) > len(suffix) {
			return 0
		}
		for i, raw := range replay {
			var item struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(raw, &item) != nil {
				return 0
			}
			switch item.Type {
			case "message", "reasoning", "function_call":
			default:
				return 0
			}
			if !sameInputJSON(raw, suffix[i]) {
				return 0
			}
		}
		return len(replay)
	}
	return 0
}

func sameInputJSON(a, b json.RawMessage) bool {
	left, err := fingerprintRequestInput(a)
	if err != nil {
		return false
	}
	right, err := fingerprintRequestInput(b)
	return err == nil && left == right
}

type requestInputProjection struct {
	static json.RawMessage
	items  []json.RawMessage
}

const requestInputFingerprintVersion = 1

func (p requestInputProjection) identity(routeFingerprint string) (modelenvelope.RequestInputIdentity, error) {
	fingerprint, err := p.fingerprint(routeFingerprint, len(p.items))
	if err != nil {
		return modelenvelope.RequestInputIdentity{}, err
	}
	identity := modelenvelope.RequestInputIdentity{
		Fingerprint: fingerprint,
		ItemCount:   len(p.items),
	}
	return identity, identity.Validate()
}

func (p requestInputProjection) matches(identity modelenvelope.RequestInputIdentity, routeFingerprint string) bool {
	if identity.Validate() != nil || identity.ItemCount > len(p.items) {
		return false
	}
	fingerprint, err := p.fingerprint(routeFingerprint, identity.ItemCount)
	return err == nil && fingerprint == identity.Fingerprint
}

func (p requestInputProjection) fingerprint(routeFingerprint string, count int) (string, error) {
	if count <= 0 || count > len(p.items) {
		return "", errors.New("request input prefix is out of range")
	}
	if _, err := hex.DecodeString(routeFingerprint); err != nil || len(routeFingerprint) != 64 {
		return "", errors.New("request input identity requires a SHA-256 route fingerprint")
	}
	body, err := json.Marshal(struct {
		Version int               `json:"version"`
		Route   string            `json:"route"`
		Static  json.RawMessage   `json:"static"`
		Items   []json.RawMessage `json:"items"`
	}{requestInputFingerprintVersion, routeFingerprint, p.static, p.items[:count]})
	if err != nil {
		return "", err
	}
	return fingerprintRequestInput(body)
}

func fingerprintRequestInput(raw json.RawMessage) (string, error) {
	canonical, err := jsoncanonical.Normalize(raw)
	if err != nil {
		return "", fmt.Errorf("canonicalize request input: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func projectRequestInput(body json.RawMessage, format modelprotocol.APIFormat) (requestInputProjection, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return requestInputProjection{}, errors.New("prepared request must be an object")
	}
	key := "messages"
	switch format {
	case modelprotocol.APIFormatOpenAIChatCompletions:
		delete(object, "max_tokens")
		delete(object, "max_completion_tokens")
	case modelprotocol.APIFormatOpenAIResponses:
		key = "input"
		delete(object, "max_output_tokens")
		for _, field := range []string{"previous_response_id", "conversation"} {
			if value, ok := object[field]; ok && string(value) != "null" {
				return requestInputProjection{}, errors.New("server-managed conversation input is not reusable")
			}
		}
	case modelprotocol.APIFormatAnthropicMessages:
		delete(object, "max_tokens")
	default:
		return requestInputProjection{}, errors.New("unsupported request input format")
	}
	delete(object, "stream")
	delete(object, "stream_options")
	if value, ok := object["truncation"]; ok && string(value) != `"disabled"` && string(value) != "null" {
		return requestInputProjection{}, errors.New("automatic input truncation is not reusable")
	}
	if value, ok := object["context_management"]; ok && string(value) != "null" {
		return requestInputProjection{}, errors.New("server-managed context editing is not reusable")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(object[key], &items); err != nil || len(items) == 0 {
		return requestInputProjection{}, errors.New("prepared request has no conversation items")
	}
	delete(object, key)
	if format == modelprotocol.APIFormatAnthropicMessages {
		for _, field := range []string{"system", "tools"} {
			if value, ok := object[field]; ok {
				object[field] = stripBlockCacheControls(value)
			}
		}
	}
	for index, item := range items {
		items[index] = stripMessageCacheControls(item, format)
	}

	prefixEnd, suffixStart := 0, len(items)
	if format != modelprotocol.APIFormatAnthropicMessages {
		for prefixEnd < suffixStart && isSystemInputItem(items[prefixEnd]) {
			prefixEnd++
		}
		for suffixStart > prefixEnd && isSystemInputItem(items[suffixStart-1]) {
			suffixStart--
		}
	}
	for _, item := range items[prefixEnd:suffixStart] {
		if isSystemInputItem(item) {
			return requestInputProjection{}, errors.New("system input inside the conversation cannot be reused")
		}
	}
	if prefixEnd == suffixStart {
		return requestInputProjection{}, errors.New("prepared request contains only static input")
	}
	static, err := json.Marshal(struct {
		Format       modelprotocol.APIFormat    `json:"format"`
		Options      map[string]json.RawMessage `json:"options"`
		SystemPrefix []json.RawMessage          `json:"system_prefix,omitempty"`
		SystemSuffix []json.RawMessage          `json:"system_suffix,omitempty"`
		LoadedTools  []string                   `json:"loaded_tools,omitempty"`
	}{format, object, items[:prefixEnd], items[suffixStart:], loadedInputTools(items, format)})
	if err != nil {
		return requestInputProjection{}, err
	}
	return requestInputProjection{static: static, items: items[prefixEnd:suffixStart]}, nil
}

func loadedInputTools(items []json.RawMessage, format modelprotocol.APIFormat) []string {
	names := map[string]struct{}{}
	for _, raw := range items {
		var item struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
			Tools   []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		if format == modelprotocol.APIFormatOpenAIResponses && item.Type == "tool_search_output" {
			for _, tool := range item.Tools {
				if tool.Name != "" {
					names[tool.Name] = struct{}{}
				}
			}
		}
		if format != modelprotocol.APIFormatAnthropicMessages {
			continue
		}
		var blocks []struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(item.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			if block.Type != "tool_result" {
				continue
			}
			var references []struct {
				Type     string `json:"type"`
				ToolName string `json:"tool_name"`
			}
			if json.Unmarshal(block.Content, &references) != nil {
				continue
			}
			for _, reference := range references {
				if reference.Type == "tool_reference" && reference.ToolName != "" {
					names[reference.ToolName] = struct{}{}
				}
			}
		}
	}
	loaded := make([]string, 0, len(names))
	for name := range names {
		loaded = append(loaded, name)
	}
	slices.Sort(loaded)
	return loaded
}

func isSystemInputItem(raw json.RawMessage) bool {
	var item struct {
		Role string `json:"role"`
	}
	return json.Unmarshal(raw, &item) == nil && (item.Role == "system" || item.Role == "developer")
}

func stripBlockCacheControls(raw json.RawMessage) json.RawMessage {
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil || blocks == nil {
		return raw
	}
	for _, block := range blocks {
		delete(block, "cache_control")
		var kind string
		_ = json.Unmarshal(block["type"], &kind)
		if kind == "tool_result" {
			if content, ok := block["content"]; ok {
				block["content"] = stripBlockCacheControls(content)
			}
		}
	}
	encoded, err := json.Marshal(blocks)
	if err != nil {
		return raw
	}
	return encoded
}

func stripMessageCacheControls(raw json.RawMessage, format modelprotocol.APIFormat) json.RawMessage {
	if format == modelprotocol.APIFormatOpenAIResponses {
		return raw
	}
	var item map[string]json.RawMessage
	if json.Unmarshal(raw, &item) != nil || item == nil {
		return raw
	}
	if content, ok := item["content"]; ok {
		item["content"] = stripBlockCacheControls(content)
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		return raw
	}
	return encoded
}
