package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

type Response struct {
	ID                      string                   `json:"id"`
	ProviderRequestID       string                   `json:"-"`
	ServedProviderModelSlug string                   `json:"served_provider_model_slug,omitempty"`
	ProviderReplay          json.RawMessage          `json:"provider_replay,omitempty"`
	Content                 []ResponsePart           `json:"content_parts,omitempty"`
	StopReason              modelenvelope.StopReason `json:"stop_reason,omitempty"`
	Usage                   modelenvelope.Usage      `json:"usage,omitempty"`
}

func (r Response) ToolCalls() []ToolCall {
	return toolCallsFromContent(r.Content)
}

func (r Response) HasToolCalls() bool {
	for _, part := range r.Content {
		if part.Type == ResponsePartTypeToolCall {
			return true
		}
	}
	return false
}

func WithoutToolCallsOnMaxTokens(response Response) Response {
	if NormalizeStopReason(response.StopReason, response.HasToolCalls()) != StopReasonMaxTokens {
		return response
	}
	content := make([]ResponsePart, 0, len(response.Content))
	for _, part := range response.Content {
		if part.Type != ResponsePartTypeToolCall {
			content = append(content, part)
		}
	}
	response.Content = content
	response.ProviderReplay = nil
	return response
}

func (r Response) Text() string {
	var out strings.Builder
	for _, part := range r.Content {
		if part.Type != ResponsePartTypeText || part.Text == "" {
			continue
		}
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString(part.Text)
	}
	return out.String()
}

func ToolCallsFromEnvelope(envelope modelenvelope.ResponseEnvelope) []ToolCall {
	parts := envelope.Normalized.Content
	out := make([]ToolCall, 0, len(parts))
	for _, part := range parts {
		if part.Type != modelenvelope.ResponsePartTypeToolCall {
			continue
		}
		out = append(out, ToolCall{
			ID:    part.ProviderCallID,
			Name:  part.ToolName,
			Input: part.ToolInput,
		})
	}
	return out
}

func toolCallsFromContent(parts []ResponsePart) []ToolCall {
	out := make([]ToolCall, 0, len(parts))
	for _, part := range parts {
		if part.Type != ResponsePartTypeToolCall {
			continue
		}
		out = append(out, ToolCall{
			ID:    part.ProviderCallID,
			Name:  part.ToolName,
			Input: part.ToolInput,
		})
	}
	return out
}

type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type ResponsePartType = modelenvelope.ResponsePartType
type StopReason = modelenvelope.StopReason
type Usage = modelenvelope.Usage

type ResponsePart struct {
	Type           ResponsePartType `json:"type"`
	Text           string           `json:"text,omitempty"`
	ProviderCallID string           `json:"provider_call_id,omitempty"`
	ToolName       string           `json:"tool_name,omitempty"`
	ToolInput      json.RawMessage  `json:"tool_input,omitempty"`
}

const (
	ResponsePartTypeText      = modelenvelope.ResponsePartTypeText
	ResponsePartTypeToolCall  = modelenvelope.ResponsePartTypeToolCall
	ResponsePartTypeReasoning = modelenvelope.ResponsePartTypeReasoning
)

const (
	StopReasonEndTurn       = modelenvelope.StopReasonEndTurn
	StopReasonToolUse       = modelenvelope.StopReasonToolUse
	StopReasonMaxTokens     = modelenvelope.StopReasonMaxTokens
	StopReasonRefusal       = modelenvelope.StopReasonRefusal
	StopReasonContentFilter = modelenvelope.StopReasonContentFilter
	StopReasonPause         = modelenvelope.StopReasonPause
	StopReasonContextWindow = modelenvelope.StopReasonContextWindow
	StopReasonError         = modelenvelope.StopReasonError
	StopReasonUnknown       = modelenvelope.StopReasonUnknown
)

func NormalizeStopReason(reason StopReason, hasToolCalls bool) StopReason {
	return modelenvelope.NormalizeStopReason(reason, hasToolCalls)
}

func NewResponseEnvelopeForStorage(
	requestedProviderModelSlug string,
	apiFormat modelprotocol.APIFormat,
	apiVariant modelprotocol.APIVariant,
	response Response,
) (modelenvelope.ResponseEnvelope, error) {
	response = WithoutToolCallsOnMaxTokens(response)
	if err := ValidateProviderResponse(response); err != nil {
		return modelenvelope.ResponseEnvelope{}, err
	}
	response.StopReason = modelenvelope.NormalizeStopReason(response.StopReason, response.HasToolCalls())
	requestedProviderModelSlug = strings.TrimSpace(requestedProviderModelSlug)
	apiFormat = modelprotocol.APIFormat(strings.TrimSpace(string(apiFormat)))
	apiVariant = modelprotocol.APIVariant(strings.TrimSpace(string(apiVariant)))
	if requestedProviderModelSlug == "" || apiFormat == "" || apiVariant == "" {
		return modelenvelope.ResponseEnvelope{}, errors.New(
			"requested provider model slug, API format, and API variant are required",
		)
	}
	normalized := modelenvelope.ResponseNormalized{
		ID:         response.ID,
		Content:    make([]modelenvelope.ResponsePart, 0, len(response.Content)),
		StopReason: response.StopReason,
		Usage:      modelenvelope.NormalizeUsage(response.Usage),
	}
	for index, part := range response.Content {
		switch part.Type {
		case ResponsePartTypeText:
			normalized.Content = append(normalized.Content, modelenvelope.ResponsePart{
				Type: modelenvelope.ResponsePartTypeText,
				Text: part.Text,
			})
		case ResponsePartTypeReasoning:
			if part.ProviderCallID != "" || part.ToolName != "" ||
				len(part.ToolInput) != 0 {
				return modelenvelope.ResponseEnvelope{}, fmt.Errorf(
					"response content reasoning part %d must not carry media or tool_call fields",
					index,
				)
			}
			normalized.Content = append(normalized.Content, modelenvelope.ResponsePart{
				Type: modelenvelope.ResponsePartTypeReasoning,
				Text: part.Text,
			})
		case ResponsePartTypeToolCall:
			if part.ProviderCallID == "" || part.ToolName == "" {
				return modelenvelope.ResponseEnvelope{}, fmt.Errorf(
					"response content tool_call part %d is missing provider_call_id or tool_name",
					index,
				)
			}
			toolInput, err := modelenvelope.NormalizeToolInput(part.ToolInput)
			if err != nil {
				return modelenvelope.ResponseEnvelope{}, fmt.Errorf(
					"response content tool_call part %d: %w",
					index,
					err,
				)
			}
			normalized.Content = append(normalized.Content, modelenvelope.ResponsePart{
				Type:           modelenvelope.ResponsePartTypeToolCall,
				ProviderCallID: part.ProviderCallID,
				ToolName:       part.ToolName,
				ToolInput:      toolInput,
			})
		default:
			return modelenvelope.ResponseEnvelope{}, fmt.Errorf(
				"response content part %d has unsupported type %q",
				index,
				part.Type,
			)
		}
	}
	providerReplay := response.ProviderReplay
	if len(providerReplay) == 0 || string(providerReplay) == "null" {
		providerReplay = nil
	}
	envelope := modelenvelope.ResponseEnvelope{
		RequestedProviderModelSlug: requestedProviderModelSlug,
		ServedProviderModelSlug:    strings.TrimSpace(response.ServedProviderModelSlug),
		APIFormat:                  apiFormat,
		APIVariant:                 apiVariant,
		ProviderReplay:             providerReplay,
		Normalized:                 normalized,
	}
	if err := envelope.Validate(); err != nil {
		return modelenvelope.ResponseEnvelope{}, err
	}
	return envelope, nil
}
