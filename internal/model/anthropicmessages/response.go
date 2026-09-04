package anthropicmessages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
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
		return model.Response{}, p.invalidResponseError(resp, messagesResponse{}, err)
	}
	var decoded messagesResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return anthropicResponseEvidence(decoded), p.invalidResponseError(resp, decoded, err)
	}
	if decoded.Type == "error" || decoded.Error.present() {
		return anthropicResponseEvidence(decoded), anthropicProviderError(
			p.errorSource(),
			resp.StatusCode,
			resp.Header,
			anthropicErrorEnvelope{
				Type:      decoded.Type,
				Error:     decoded.Error,
				RequestID: decoded.RequestID,
			},
			"anthropic messages request failed",
		)
	}
	if decoded.ID == "" {
		return anthropicResponseEvidence(decoded), p.invalidResponseError(
			resp,
			decoded,
			errors.New("anthropic messages response is missing id"),
		)
	}
	if strings.TrimSpace(decoded.StopReason) == "" {
		return anthropicResponseEvidence(decoded), p.invalidResponseError(
			resp,
			decoded,
			errors.New("anthropic messages response is missing stop_reason"),
		)
	}
	out := anthropicResponseEvidence(decoded)
	out.StopReason = mapStopReason(decoded.StopReason)
	replayBlocks := make([]json.RawMessage, 0, len(decoded.Content))
	hasToolCall := false
	for _, rawBlock := range decoded.Content {
		var block contentBlock
		if err := json.Unmarshal(rawBlock, &block); err != nil {
			return out, p.invalidResponseError(resp, decoded, err)
		}
		replayBlock, err := anthropicContentBlockForReplay(rawBlock, block)
		if err != nil {
			replayBlock = rawBlock
		}
		replayBlocks = append(replayBlocks, replayBlock)
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) == "" {
				continue
			}
			out.Content = append(out.Content, model.ResponsePart{
				Type: model.ResponsePartTypeText,
				Text: block.Text,
			})
		case "thinking", "redacted_thinking":
			if block.Type == "thinking" && block.Thinking != "" {
				out.Content = append(out.Content, model.ResponsePart{
					Type: model.ResponsePartTypeReasoning,
					Text: block.Thinking,
				})
			}
		case "tool_use":
			out.Content = append(out.Content, model.ResponsePart{
				Type:           model.ResponsePartTypeToolCall,
				ProviderCallID: block.ID,
				ToolName:       block.Name,
				ToolInput:      block.Input,
			})
			hasToolCall = true
		default:
			return out, p.invalidResponseError(
				resp,
				decoded,
				fmt.Errorf("anthropic messages response contains unsupported content block type %q", block.Type),
			)
		}
	}
	if len(replayBlocks) > 0 {
		batch, err := json.Marshal(replayBlocks)
		if err != nil {
			return out, p.invalidResponseError(resp, decoded, err)
		}
		out.ProviderReplay = batch
	}
	out.StopReason = modelenvelope.NormalizeStopReason(out.StopReason, hasToolCall)
	return out, nil
}

func anthropicContentBlockForReplay(
	raw json.RawMessage,
	block contentBlock,
) (json.RawMessage, error) {
	if block.Type != "tool_use" {
		return raw, nil
	}
	input, err := modelenvelope.NormalizeToolInput(block.Input)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	fields["input"] = input
	return json.Marshal(fields)
}

func anthropicResponseEvidence(response messagesResponse) model.Response {
	return model.Response{
		ID:                      response.ID,
		ServedProviderModelSlug: response.Model,
		Usage:                   usageFromResponse(response.Usage),
	}
}

type messagesResponse struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Model      string             `json:"model"`
	Content    []json.RawMessage  `json:"content"`
	StopReason string             `json:"stop_reason"`
	Usage      usage              `json:"usage"`
	Error      anthropicErrorBody `json:"error"`
	RequestID  string             `json:"request_id"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	Data      string          `json:"data"`
}

type usage struct {
	InputTokens              int                                  `json:"input_tokens"`
	OutputTokens             int                                  `json:"output_tokens"`
	CacheCreationInputTokens int                                  `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}
func mapStopReason(reason string) modelenvelope.StopReason {
	switch reason {
	case "end_turn":
		return modelenvelope.StopReasonEndTurn
	case "tool_use":
		return modelenvelope.StopReasonToolUse
	case "stop_sequence":
		return modelenvelope.StopReasonEndTurn
	case "max_tokens":
		return modelenvelope.StopReasonMaxTokens
	case "refusal":
		return modelenvelope.StopReasonRefusal
	case "pause_turn":
		return modelenvelope.StopReasonPause
	case "model_context_window_exceeded":
		return modelenvelope.StopReasonContextWindow
	default:
		return modelenvelope.StopReasonUnknown
	}
}

func usageFromResponse(value usage) modelenvelope.Usage {
	if value.InputTokens < 0 ||
		value.OutputTokens < 0 ||
		value.CacheCreationInputTokens < 0 ||
		value.CacheReadInputTokens < 0 {
		return modelenvelope.Usage{}
	}
	remaining := math.MaxInt - value.InputTokens
	if value.CacheCreationInputTokens > remaining {
		return modelenvelope.Usage{}
	}
	remaining -= value.CacheCreationInputTokens
	if value.CacheReadInputTokens > remaining {
		return modelenvelope.Usage{}
	}
	inputTokens := value.InputTokens + value.CacheCreationInputTokens + value.CacheReadInputTokens
	return modelenvelope.Usage{
		InputTokens:         inputTokens,
		UncachedInputTokens: value.InputTokens,
		OutputTokens:        value.OutputTokens,
		CacheWriteTokens:    value.CacheCreationInputTokens,
		CacheReadTokens:     value.CacheReadInputTokens,
	}
}
