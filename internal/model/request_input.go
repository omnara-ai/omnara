package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func applyRequestInputMeasurement(
	ctx context.Context,
	prepared *PreparedRequest,
	client Client,
	input PrepareForSendInput,
) {
	bundle := input.Context
	var source *modelcontext.Message
	for i := len(bundle.Messages) - 1; i >= 0; i-- {
		if bundle.Messages[i].Role == modelprotocol.RoleAssistant {
			source = &bundle.Messages[i]
			break
		}
	}
	if source == nil || source.InputEventSequence <= 0 || source.InputEventSequence >= source.Sequence ||
		source.InputFingerprint == "" ||
		bundle.AgentConfigID == uuid.Nil || source.AgentConfigID != bundle.AgentConfigID ||
		bundle.ConfiguredModelRevisionID == uuid.Nil || source.ConfiguredModelRevisionID != bundle.ConfiguredModelRevisionID {
		return
	}
	identity := source.ProviderReplaySource
	if identity.RequestedProviderModelSlug != client.RequestedProviderModelSlug() ||
		identity.APIFormat != client.APIFormat() || identity.APIVariant != client.ModelAPIVariant() {
		return
	}
	if checkpoint := bundle.ContextCheckpoint; checkpoint != nil &&
		(checkpoint.Sequence == 0 || checkpoint.Sequence > source.InputEventSequence) {
		return
	}
	usage := modelenvelope.NormalizeUsage(source.Usage)
	if usage.InputTokens <= 0 {
		return
	}

	prefix, suffix := bundle, bundle
	prefix.InputEventSequence = source.InputEventSequence
	prefix.Messages = nil
	prefix.ToolResults = nil
	suffix.SystemPrompt = ""
	suffix.ContextCheckpoint = nil
	suffix.Messages = nil
	suffix.ToolResults = nil
	for _, message := range bundle.Messages {
		if message.Sequence > source.InputEventSequence {
			suffix.Messages = append(suffix.Messages, message)
		} else {
			prefix.Messages = append(prefix.Messages, message)
		}
	}
	for _, result := range bundle.ToolResults {
		if result.ResultEventSequence <= source.InputEventSequence {
			prefix.ToolResults = append(prefix.ToolResults, result)
			continue
		}
		if result.SourceEventSequence <= source.InputEventSequence {
			return
		}
		suffix.ToolResults = append(suffix.ToolResults, result)
	}
	previous, err := client.Prepare(ctx, PrepareInput{Context: prefix, Policy: input.Policy})
	if err != nil {
		return
	}
	if previous.InputFingerprint == "" {
		previous.InputFingerprint = RequestInputFingerprint(previous.Body, "")
	}
	if previous.InputFingerprint != source.InputFingerprint {
		return
	}
	rendered, err := client.Prepare(ctx, PrepareInput{Context: suffix, Policy: input.Policy})
	if err != nil {
		return
	}
	var body struct {
		Messages []json.RawMessage `json:"messages"`
		Input    []json.RawMessage `json:"input"`
		Tools    []json.RawMessage `json:"tools"`
	}
	if json.Unmarshal(rendered.Body, &body) != nil {
		return
	}
	items := body.Messages
	if client.APIFormat() == modelprotocol.APIFormatOpenAIResponses {
		items = body.Input
	}
	if len(items) == 0 {
		return
	}
	outputTokens := 0
	if inputHasReasoning(items) {
		if usage.OutputTokens <= 0 {
			return
		}
		outputTokens = usage.OutputTokens
	}
	var newItems []json.RawMessage
	for _, raw := range items {
		var item struct {
			Role string `json:"role"`
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return
		}
		if item.Role == "system" || item.Role == "developer" {
			continue
		}
		if outputTokens > 0 && (item.Role == "assistant" || item.Type == "reasoning" || item.Type == "function_call") {
			continue
		}
		newItems = append(newItems, raw)
	}
	newTokens := 0
	if len(newItems) > 0 {
		added := struct {
			Messages []json.RawMessage `json:"messages"`
			Tools    []json.RawMessage `json:"tools,omitempty"`
		}{Messages: newItems}
		for _, raw := range body.Tools {
			var tool struct {
				Deferred bool `json:"defer_loading"`
			}
			if json.Unmarshal(raw, &tool) == nil && tool.Deferred {
				added.Tools = append(added.Tools, raw)
			}
		}
		raw, err := json.Marshal(added)
		if err != nil {
			return
		}
		newTokens = modelcontext.EstimatePreparedRequest(raw, rendered.RenderedMedia)
	}
	if outputTokens > math.MaxInt-newTokens || usage.InputTokens > math.MaxInt-outputTokens-newTokens {
		return
	}
	prepared.InputTokenEstimate = usage.InputTokens + outputTokens + newTokens
	prepared.UsesProviderInputUsage = true
}

func inputHasReasoning(items []json.RawMessage) bool {
	for _, raw := range items {
		var item struct {
			Role             string            `json:"role"`
			Type             string            `json:"type"`
			Content          json.RawMessage   `json:"content"`
			Reasoning        string            `json:"reasoning"`
			ReasoningContent string            `json:"reasoning_content"`
			ReasoningDetails []json.RawMessage `json:"reasoning_details"`
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		if item.Type == "reasoning" {
			return true
		}
		if item.Role != "assistant" {
			continue
		}
		if item.Reasoning != "" || item.ReasoningContent != "" || len(item.ReasoningDetails) > 0 {
			return true
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

func RequestInputFingerprint(body json.RawMessage, endpoint string) string {
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil || request == nil {
		return ""
	}
	delete(request, "max_tokens")
	delete(request, "max_output_tokens")
	delete(request, "max_completion_tokens")
	encoded, err := json.Marshal(request)
	if err != nil {
		return ""
	}
	canonical, err := jsoncanonical.Normalize(encoded)
	if err != nil {
		return ""
	}
	digest := sha256.New()
	digest.Write([]byte(endpoint))
	digest.Write([]byte{0})
	digest.Write(canonical)
	return hex.EncodeToString(digest.Sum(nil))
}
