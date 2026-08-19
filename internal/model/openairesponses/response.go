package openairesponses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/providererrors"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

func (p protocol) ParseResponse(ctx context.Context, resp route.Response) (model.Response, error) {
	_ = ctx
	body := resp.Body
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return model.Response{}, classifyHTTPError(p.errorSource(), resp.StatusCode, resp.Header, body)
	}
	if err := model.ValidateProviderJSON(body); err != nil {
		return model.Response{}, p.invalidResponseError(resp, responsesResponse{}, err)
	}
	var decoded responsesResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return responseEvidence(decoded), p.invalidResponseError(resp, decoded, err)
	}
	if decoded.Status == "" && decoded.Error.present() {
		return responseEvidence(decoded), responseFailureError(p.errorSource(), decoded, resp.StatusCode, resp.Header)
	}
	switch decoded.Status {
	case "failed":
		return responseEvidence(decoded), responseFailureError(p.errorSource(), decoded, resp.StatusCode, resp.Header)
	case "queued", "in_progress":
		return responseEvidence(decoded), model.AmbiguousProviderOutcome(model.ProviderError{
			Kind:       model.ErrorKindTransient,
			Source:     p.errorSource(),
			StatusCode: resp.StatusCode,
			Code:       decoded.Status,
			Message:    "openai-responses returned a nonterminal response status",
			RequestID:  model.RequestIDFromHeader(resp.Header),
			RetryAfter: model.RetryAfterFromHeader(resp.Header),
		})
	case "completed", "incomplete":
	default:
		return responseEvidence(decoded), p.invalidResponseError(
			resp,
			decoded,
			fmt.Errorf("openai-responses response has unexpected status %q", decoded.Status),
		)
	}
	if decoded.ID == "" {
		return responseEvidence(decoded), p.invalidResponseError(
			resp,
			decoded,
			errors.New("openai-responses response is missing id"),
		)
	}
	out := responseEvidence(decoded)
	out.StopReason = stopReasonFromResponse(decoded)
	truncated := out.StopReason == model.StopReasonMaxTokens
	responseReplayItems := make([]json.RawMessage, 0, len(decoded.Output))
	hasToolCall := false
	seenItemIDs := make(map[string]struct{}, len(decoded.Output))
	for _, rawItem := range decoded.Output {
		var item responsesOutputItem
		if err := json.Unmarshal(rawItem, &item); err != nil {
			return out, p.invalidResponseError(resp, decoded, err)
		}
		if item.ID != "" {
			if _, exists := seenItemIDs[item.ID]; exists {
				return out, p.invalidResponseError(
					resp,
					decoded,
					fmt.Errorf("openai-responses output reused item id %q", item.ID),
				)
			}
			seenItemIDs[item.ID] = struct{}{}
		}
		if !truncated {
			replayItem, err := responseOutputItemForReplay(rawItem, item)
			if err != nil {
				return out, p.invalidResponseError(resp, decoded, err)
			}
			responseReplayItems = append(responseReplayItems, replayItem)
		}
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				switch content.Type {
				case "output_text", "text":
					if content.Text != "" {
						out.Content = append(out.Content, model.ResponsePart{
							Type: model.ResponsePartTypeText,
							Text: content.Text,
						})
					}
				case "refusal":
					if content.Refusal != "" {
						out.Content = append(out.Content, model.ResponsePart{
							Type: model.ResponsePartTypeText,
							Text: content.Refusal,
						})
					}
				default:
					return out, p.invalidResponseError(
						resp,
						decoded,
						fmt.Errorf("openai-responses message has unsupported content type %q", content.Type),
					)
				}
			}
		case "reasoning":
			for _, summary := range item.Summary {
				if summary.Type != "summary_text" {
					return out, p.invalidResponseError(
						resp,
						decoded,
						fmt.Errorf("openai-responses reasoning has unsupported summary type %q", summary.Type),
					)
				}
			}
			if summary := reasoningSummaryText(item); summary != "" {
				out.Content = append(out.Content, model.ResponsePart{
					Type: model.ResponsePartTypeReasoning,
					Text: summary,
				})
			}
		case "function_call":
			if truncated {
				continue
			}
			if strings.TrimSpace(item.CallID) == "" || strings.TrimSpace(item.Name) == "" {
				return out, p.invalidResponseError(
					resp,
					decoded,
					errors.New("openai-responses function call is missing call_id or name"),
				)
			}
			arguments := json.RawMessage(item.Arguments)
			if err := modelenvelope.ValidateToolInput(arguments); err != nil {
				return out, p.invalidResponseError(
					resp,
					decoded,
					fmt.Errorf("openai-responses function call has invalid arguments: %w", err),
				)
			}
			out.Content = append(out.Content, model.ResponsePart{
				Type:           model.ResponsePartTypeToolCall,
				ProviderCallID: item.CallID,
				ToolName:       item.Name,
				ToolInput:      arguments,
			})
			hasToolCall = true
		default:
			if !validProviderOnlyResponseItem(item) {
				return out, p.invalidResponseError(
					resp,
					decoded,
					fmt.Errorf("openai-responses output has unsupported item type %q", item.Type),
				)
			}
		}
	}
	if !truncated && len(responseReplayItems) > 0 {
		items, err := json.Marshal(responseReplayItems)
		if err != nil {
			return out, p.invalidResponseError(resp, decoded, err)
		}
		out.ProviderReplay = items
	}
	out.StopReason = modelenvelope.NormalizeStopReason(out.StopReason, hasToolCall)
	return out, nil
}

func responseOutputItemForReplay(
	raw json.RawMessage,
	item responsesOutputItem,
) (json.RawMessage, error) {
	if item.Type != "function_call" {
		return raw, nil
	}
	return responseFunctionCallForReplay(raw, item)
}

func responseFunctionCallForReplay(
	raw json.RawMessage,
	item responsesOutputItem,
) (json.RawMessage, error) {
	input := json.RawMessage(item.Arguments)
	if err := modelenvelope.ValidateToolInput(input); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	arguments, err := json.Marshal(string(input))
	if err != nil {
		return nil, err
	}
	fields["arguments"] = arguments
	return json.Marshal(fields)
}

func responseEvidence(response responsesResponse) model.Response {
	return model.Response{
		ID:                      response.ID,
		ServedProviderModelSlug: response.Model,
		Usage:                   usageFromResponse(response.Usage),
	}
}

type responsesResponse struct {
	ID                string                     `json:"id"`
	Model             string                     `json:"model"`
	Status            string                     `json:"status"`
	ErrorType         string                     `json:"error_type"`
	Error             responsesError             `json:"error"`
	IncompleteDetails responsesIncompleteDetails `json:"incomplete_details"`
	Output            []json.RawMessage          `json:"output"`
	Usage             responsesUsage             `json:"usage"`
}

type responsesError struct {
	Code    any    `json:"code"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e responsesError) present() bool {
	return e.codeText() != "" || e.Type != "" || e.Message != ""
}

func (e responsesError) codeText() string {
	return providererrors.CodeText(e.Code)
}

type responsesIncompleteDetails struct {
	Reason string `json:"reason"`
}

type responsesUsage struct {
	InputTokens         int                   `json:"input_tokens"`
	OutputTokens        int                   `json:"output_tokens"`
	OutputTokensDetails responsesTokenDetails `json:"output_tokens_details"`
	InputTokensDetails  responsesTokenDetails `json:"input_tokens_details"`
}

type responsesTokenDetails struct {
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
}

type responsesOutputItem struct {
	ID               string                 `json:"id"`
	Type             string                 `json:"type"`
	CallID           string                 `json:"call_id"`
	Name             string                 `json:"name"`
	Arguments        string                 `json:"arguments"`
	Content          []responsesContentPart `json:"content"`
	Summary          []responsesSummaryPart `json:"summary"`
	EncryptedContent string                 `json:"encrypted_content"`
}

type responsesContentPart struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type responsesSummaryPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func reasoningSummaryText(item responsesOutputItem) string {
	var b strings.Builder
	for _, summary := range item.Summary {
		text := strings.TrimSpace(summary.Text)
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

func usageFromResponse(usage responsesUsage) modelenvelope.Usage {
	if usage.InputTokens < 0 ||
		usage.InputTokensDetails.CachedTokens < 0 ||
		usage.InputTokensDetails.CacheWriteTokens < 0 ||
		usage.OutputTokens < 0 ||
		usage.OutputTokensDetails.ReasoningTokens < 0 ||
		usage.InputTokensDetails.CachedTokens > usage.InputTokens ||
		usage.InputTokensDetails.CacheWriteTokens >
			usage.InputTokens-usage.InputTokensDetails.CachedTokens ||
		usage.OutputTokensDetails.ReasoningTokens > usage.OutputTokens {
		return modelenvelope.Usage{}
	}
	uncachedInputTokens := usage.InputTokens -
		usage.InputTokensDetails.CachedTokens -
		usage.InputTokensDetails.CacheWriteTokens
	return modelenvelope.Usage{
		InputTokens:         usage.InputTokens,
		UncachedInputTokens: uncachedInputTokens,
		OutputTokens:        usage.OutputTokens,
		ReasoningTokens:     usage.OutputTokensDetails.ReasoningTokens,
		CacheReadTokens:     usage.InputTokensDetails.CachedTokens,
		CacheWriteTokens:    usage.InputTokensDetails.CacheWriteTokens,
	}
}

func stopReasonFromResponse(response responsesResponse) modelenvelope.StopReason {
	if response.Status == "failed" {
		return modelenvelope.StopReasonUnknown
	}
	if response.Status == "incomplete" {
		switch response.IncompleteDetails.Reason {
		case "max_output_tokens":
			return modelenvelope.StopReasonMaxTokens
		case "content_filter":
			return modelenvelope.StopReasonContentFilter
		default:
			return modelenvelope.StopReasonUnknown
		}
	}
	if response.Status != "" && response.Status != "completed" {
		return modelenvelope.StopReasonUnknown
	}
	for _, rawItem := range response.Output {
		var item responsesOutputItem
		if err := json.Unmarshal(rawItem, &item); err != nil {
			continue
		}
		for _, content := range item.Content {
			if content.Type == "refusal" {
				return modelenvelope.StopReasonRefusal
			}
		}
	}
	return modelenvelope.StopReasonEndTurn
}
