package openaichatcompletions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func (p protocol) ParseResponse(ctx context.Context, resp route.Response) (model.Response, error) {
	body := resp.Body
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return model.Response{}, classifyHTTPError(
			p.errorSource(), p.ModelAPIVariant(), resp.StatusCode, resp.Header, body,
		)
	}
	if err := model.ValidateProviderJSON(body); err != nil {
		return model.Response{}, p.invalidResponseError(resp, chatCompletionsResponse{}, err)
	}
	var decoded chatCompletionsResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return p.chatResponseEvidence(ctx, decoded), p.invalidResponseError(resp, decoded, err)
	}
	if decoded.Error.present() {
		return p.chatResponseEvidence(ctx, decoded), classifyProviderError(
			p.errorSource(),
			p.ModelAPIVariant(),
			resp.StatusCode,
			resp.Header,
			decoded.Error,
			"",
		)
	}
	if decoded.ID == "" {
		return p.chatResponseEvidence(ctx, decoded), p.invalidResponseError(
			resp,
			decoded,
			errors.New("openai chat completions response is missing id"),
		)
	}
	if len(decoded.Choices) != 1 {
		return p.chatResponseEvidence(ctx, decoded), p.invalidResponseError(
			resp,
			decoded,
			fmt.Errorf(
				"openai chat completions response must contain exactly one choice (got %d)",
				len(decoded.Choices),
			),
		)
	}
	if decoded.Choices[0].Index != 0 {
		return p.chatResponseEvidence(ctx, decoded), p.invalidResponseError(
			resp,
			decoded,
			fmt.Errorf(
				"openai chat completions response choice index must be 0 (got %d)",
				decoded.Choices[0].Index,
			),
		)
	}
	out := p.chatResponseEvidence(ctx, decoded)
	for _, choice := range decoded.Choices {
		if !choice.hasError() && strings.TrimSpace(choice.FinishReason) == "" {
			return out, p.invalidResponseError(
				resp,
				decoded,
				errors.New("openai chat completions choice is missing finish_reason"),
			)
		}
		if choice.hasError() {
			return out, classifyChoiceError(
				p.errorSource(),
				p.ModelAPIVariant(),
				resp.StatusCode,
				resp.Header,
				choice,
			)
		}
		truncated := choice.FinishReason == "length"
		text, err := textFromChatContent(choice.Message.Content)
		if err != nil {
			return out, p.invalidResponseError(resp, decoded, err)
		}
		if text == "" {
			text = choice.Message.Refusal
		}
		if reasoningText := reasoningTextFromChatMessage(choice.Message); reasoningText != "" {
			out.Content = append(out.Content, model.ResponsePart{
				Type: model.ResponsePartTypeReasoning,
				Text: reasoningText,
			})
		}
		if text != "" {
			out.Content = append(out.Content, model.ResponsePart{
				Type: model.ResponsePartTypeText,
				Text: text,
			})
		}
		if !truncated && len(choice.Message.ToolCalls) > 0 {
			for _, rawToolCall := range choice.Message.ToolCalls {
				var toolCall chatToolCall
				if err := json.Unmarshal(rawToolCall, &toolCall); err != nil {
					return out, p.invalidResponseError(resp, decoded, err)
				}
				out.Content = append(out.Content, model.ResponsePart{
					Type:           model.ResponsePartTypeToolCall,
					ProviderCallID: toolCall.ID,
					ToolName:       toolCall.Function.Name,
					ToolInput:      json.RawMessage(toolCall.Function.Arguments),
				})
			}
		}
		if !truncated && len(out.ProviderReplay) == 0 &&
			(chatMessageHasReasoningReplay(choice.Message) || len(choice.Message.ToolCalls) > 0) {
			replay, err := chatReplayForRequest(choice.Message)
			if err != nil {
				return out, p.invalidResponseError(resp, decoded, err)
			}
			out.ProviderReplay = replay
		}
		if truncated {
			out.StopReason = model.StopReasonMaxTokens
		} else if out.StopReason == "" {
			out.StopReason = mapFinishReason(choice.FinishReason)
		}
		if !truncated && choice.Message.Refusal != "" {
			out.StopReason = model.StopReasonRefusal
		}
	}
	out.StopReason = model.NormalizeStopReason(out.StopReason, out.HasToolCalls())
	return out, nil
}

func (p protocol) chatResponseEvidence(
	ctx context.Context,
	response chatCompletionsResponse,
) model.Response {
	out := model.Response{
		ID:                      response.ID,
		ServedProviderModelSlug: response.Model,
		Usage:                   usageFromResponse(response.Usage),
	}
	if p.ModelAPIVariant() == modelprotocol.APIVariantOpenRouter {
		out.ProviderMetadata.OpenRouter.Provider = response.servedProvider()
		cost, issue := openRouterReportedCost(response.Usage)
		out.ProviderReportedCostUSD = cost
		switch issue {
		case openRouterCostIssueNone:
		case openRouterCostIssueInvalid:
			logent.ModelResponseProviderCostInvalid(ctx)
		case openRouterCostIssueBYOKStateMissing:
			logent.ModelResponseProviderCostBYOKStateMissing(ctx)
		case openRouterCostIssueBYOKStateInvalid:
			logent.ModelResponseProviderCostBYOKStateInvalid(ctx)
		case openRouterCostIssueBYOKComponentMissing:
			logent.ModelResponseProviderCostBYOKComponentMissing(ctx)
		}
	}
	return out
}

type chatCompletionsResponse struct {
	ID                 string              `json:"id"`
	Model              string              `json:"model"`
	Provider           string              `json:"provider,omitempty"`
	OpenRouterMetadata *openRouterMetadata `json:"openrouter_metadata,omitempty"`
	Choices            []chatChoice        `json:"choices"`
	Usage              chatUsage           `json:"usage"`
	Error              chatProviderError   `json:"error"`
}

type openRouterMetadata struct {
	Endpoints struct {
		Available []struct {
			Provider string `json:"provider"`
			Selected bool   `json:"selected"`
		} `json:"available"`
	} `json:"endpoints"`
}

func (r chatCompletionsResponse) servedProvider() string {
	if r.OpenRouterMetadata != nil {
		for _, endpoint := range r.OpenRouterMetadata.Endpoints.Available {
			if endpoint.Selected {
				return endpoint.Provider
			}
		}
	}
	return r.Provider
}

type chatChoice struct {
	Index        int                 `json:"index"`
	Message      chatResponseMessage `json:"message"`
	FinishReason string              `json:"finish_reason"`
	Error        chatProviderError   `json:"error"`
}

func (c chatChoice) hasError() bool {
	return strings.EqualFold(c.FinishReason, "error") || c.Error.present()
}

type chatResponseMessage struct {
	Role             chatRole          `json:"role"`
	Content          json.RawMessage   `json:"content,omitempty"`
	Refusal          string            `json:"refusal,omitempty"`
	Reasoning        string            `json:"reasoning,omitempty"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	ReasoningDetails []json.RawMessage `json:"reasoning_details,omitempty"`
	ToolCalls        []json.RawMessage `json:"tool_calls,omitempty"`
}

func chatReplayForRequest(
	message chatResponseMessage,
) (json.RawMessage, error) {
	role := message.Role
	if role == "" {
		role = chatRoleAssistant
	}
	if role != chatRoleAssistant {
		return nil, fmt.Errorf("chat replay has role %q", role)
	}
	content := message.Content
	if len(content) == 0 {
		content = json.RawMessage(`null`)
	}
	toolCalls := make([]json.RawMessage, 0, len(message.ToolCalls))
	for _, raw := range message.ToolCalls {
		var call chatToolCall
		if err := json.Unmarshal(raw, &call); err != nil {
			return nil, err
		}
		if call.Type != "function" || call.ID == "" || call.Function.Name == "" {
			return nil, errors.New("chat replay has an invalid function call")
		}
		input, err := modelenvelope.NormalizeToolInput(
			json.RawMessage(call.Function.Arguments),
		)
		if err != nil {
			return nil, err
		}
		normalized, err := json.Marshal(chatToolCall{
			ID:   call.ID,
			Type: "function",
			Function: chatFunction{
				Name:      call.Function.Name,
				Arguments: string(input),
			},
		})
		if err != nil {
			return nil, err
		}
		toolCalls = append(toolCalls, normalized)
	}
	return json.Marshal(chatResponseMessage{
		Role:             role,
		Content:          content,
		Refusal:          message.Refusal,
		Reasoning:        message.Reasoning,
		ReasoningContent: message.ReasoningContent,
		ReasoningDetails: message.ReasoningDetails,
		ToolCalls:        toolCalls,
	})
}

func chatMessageHasReasoningReplay(message chatResponseMessage) bool {
	return strings.TrimSpace(message.Reasoning) != "" ||
		strings.TrimSpace(message.ReasoningContent) != "" ||
		len(message.ReasoningDetails) > 0
}

func reasoningTextFromChatMessage(message chatResponseMessage) string {
	if text := reasoningTextFromDetails(message.ReasoningDetails); text != "" {
		return text
	}
	if text := strings.TrimSpace(message.Reasoning); text != "" {
		return text
	}
	return strings.TrimSpace(message.ReasoningContent)
}

func reasoningTextFromDetails(details []json.RawMessage) string {
	var b strings.Builder
	for _, raw := range details {
		var detail struct {
			Type    string `json:"type"`
			Summary string `json:"summary"`
			Text    string `json:"text"`
		}
		if err := json.Unmarshal(raw, &detail); err != nil {
			continue
		}
		text := strings.TrimSpace(detail.Summary)
		if text == "" {
			text = strings.TrimSpace(detail.Text)
		}
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(text)
	}
	return b.String()
}

type chatUsage struct {
	PromptTokens            int              `json:"prompt_tokens"`
	PromptCacheHitTokens    int              `json:"prompt_cache_hit_tokens"`
	CachedTokens            int              `json:"cached_tokens"`
	CompletionTokens        int              `json:"completion_tokens"`
	OpenRouterCost          json.RawMessage  `json:"cost,omitempty"`
	OpenRouterCostDetails   json.RawMessage  `json:"cost_details,omitempty"`
	OpenRouterIsBYOK        json.RawMessage  `json:"is_byok,omitempty"`
	PromptTokensDetails     chatTokenDetails `json:"prompt_tokens_details"`
	CompletionTokensDetails chatTokenDetails `json:"completion_tokens_details"`
}

type chatTokenDetails struct {
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
}

func textFromChatContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var parts []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", errors.New("openai chat completions message content must be a string, null, or supported text-part array")
	}
	if len(parts) == 0 {
		return "", errors.New("openai chat completions message content array is empty")
	}
	var b strings.Builder
	for index, part := range parts {
		var text string
		switch part.Type {
		case "text":
			text = part.Text
		case "refusal":
			text = part.Refusal
		default:
			return "", fmt.Errorf(
				"openai chat completions message content part %d has unsupported type %q",
				index,
				part.Type,
			)
		}
		if text == "" {
			return "", fmt.Errorf(
				"openai chat completions message content part %d is missing text",
				index,
			)
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
	}
	return b.String(), nil
}

func usageFromResponse(usage chatUsage) model.Usage {
	if usage.PromptTokensDetails.CachedTokens == 0 {
		usage.PromptTokensDetails.CachedTokens = max(usage.PromptCacheHitTokens, usage.CachedTokens)
	}
	if usage.PromptTokens < 0 ||
		usage.PromptTokensDetails.CachedTokens < 0 ||
		usage.PromptTokensDetails.CacheWriteTokens < 0 ||
		usage.CompletionTokens < 0 ||
		usage.CompletionTokensDetails.ReasoningTokens < 0 ||
		usage.PromptTokensDetails.CachedTokens > usage.PromptTokens ||
		usage.PromptTokensDetails.CacheWriteTokens > usage.PromptTokens-usage.PromptTokensDetails.CachedTokens ||
		usage.CompletionTokensDetails.ReasoningTokens > usage.CompletionTokens {
		return model.Usage{}
	}
	uncachedInputTokens := usage.PromptTokens -
		usage.PromptTokensDetails.CachedTokens -
		usage.PromptTokensDetails.CacheWriteTokens
	return model.Usage{
		InputTokens:         usage.PromptTokens,
		UncachedInputTokens: uncachedInputTokens,
		OutputTokens:        usage.CompletionTokens,
		ReasoningTokens:     usage.CompletionTokensDetails.ReasoningTokens,
		CacheReadTokens:     usage.PromptTokensDetails.CachedTokens,
		CacheWriteTokens:    usage.PromptTokensDetails.CacheWriteTokens,
	}
}

func mapFinishReason(reason string) model.StopReason {
	switch reason {
	case "stop":
		return model.StopReasonEndTurn
	case "tool_calls", "function_call":
		return model.StopReasonToolUse
	case "length":
		return model.StopReasonMaxTokens
	case "content_filter":
		return model.StopReasonContentFilter
	default:
		return model.StopReasonUnknown
	}
}
