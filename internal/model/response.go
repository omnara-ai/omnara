package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

type Response struct {
	ID                      string                                `json:"id"`
	ProviderRequestID       string                                `json:"-"`
	ServedProviderModelSlug string                                `json:"served_provider_model_slug,omitempty"`
	ProviderReportedCostUSD modelenvelope.ProviderReportedCostUSD `json:"provider_reported_cost_usd,omitempty"`
	ProviderMetadata        modelenvelope.ProviderMetadata        `json:"provider_metadata,omitzero"`
	ProviderReplay          json.RawMessage                       `json:"provider_replay,omitempty"`
	Content                 []ResponsePart                        `json:"content_parts,omitempty"`
	StopReason              modelenvelope.StopReason              `json:"stop_reason,omitempty"`
	Usage                   modelenvelope.Usage                   `json:"usage,omitempty"`
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
	ToolCallError  string           `json:"-"`
}

const IncompleteToolCallError = "The tool call was incomplete and was not executed. Retry with complete arguments, splitting large inputs into smaller calls."

const UnparseableToolCallName = "unparseable_tool_call"

// ToolArgumentString decodes provider arguments without losing the call identity
// when the value has the wrong JSON type. An invalid value becomes empty input,
// which NewToolCallPart rejects along with other malformed arguments.
type ToolArgumentString string

func (a *ToolArgumentString) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		value = ""
	}
	*a = ToolArgumentString(value)
	return nil
}

// NewToolCallPart keeps rejected attempts representable in provider history.
// Storage must record ToolCallError as a failed result in the same transaction
// as the call, before its placeholder input can be admitted for execution.
func NewToolCallPart(id, name string, input json.RawMessage) ResponsePart {
	part := ResponsePart{
		Type: ResponsePartTypeToolCall, ToolName: name,
	}
	if strings.TrimSpace(id) != "" {
		part.ProviderCallID = id
	}
	if strings.TrimSpace(name) == "" {
		part.ToolName = UnparseableToolCallName
		part.ToolCallError = "The tool call had no usable tool name and was not executed. Retry using an available tool name."
	}
	normalized, err := modelenvelope.NormalizeToolInput(input)
	inputError := "The tool arguments must be a complete JSON object and were not executed. " +
		"Retry with valid JSON, splitting large inputs into smaller calls."
	if err == nil {
		err = dbsafe.JSONStrings(normalized)
		inputError = "The tool arguments contain unsupported characters and were not executed. " +
			"Retry without null characters or invalid Unicode."
	}
	if err != nil {
		part.ToolInput = json.RawMessage(`{}`)
		if part.ToolCallError == "" {
			part.ToolCallError = inputError
		}
	} else {
		part.ToolInput = normalized
	}
	return part
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
				ToolCallError:  part.ToolCallError,
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
		ProviderReportedCostUSD:    response.ProviderReportedCostUSD,
		ProviderMetadata:           response.ProviderMetadata,
		ProviderReplay:             providerReplay,
		Normalized:                 normalized,
	}
	if err := envelope.Validate(); err != nil {
		return modelenvelope.ResponseEnvelope{}, err
	}
	return envelope, nil
}
