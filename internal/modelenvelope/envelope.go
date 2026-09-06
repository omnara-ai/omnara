package modelenvelope

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

type ResponseEnvelope struct {
	RequestedProviderModelSlug string                   `json:"requested_provider_model_slug"`
	ServedProviderModelSlug    string                   `json:"served_provider_model_slug,omitempty"`
	APIFormat                  modelprotocol.APIFormat  `json:"api_format"`
	APIVariant                 modelprotocol.APIVariant `json:"api_variant"`
	ProviderReportedCostUSD    ProviderReportedCostUSD  `json:"provider_reported_cost_usd,omitempty"`
	ProviderMetadata           ProviderMetadata         `json:"provider_metadata,omitzero"`
	ProviderReplay             json.RawMessage          `json:"provider_replay,omitempty"`
	Normalized                 ResponseNormalized       `json:"normalized"`
}

type ResponseNormalized struct {
	ID         string         `json:"id"`
	Content    []ResponsePart `json:"content_parts"`
	StopReason StopReason     `json:"stop_reason,omitempty"`
	Usage      Usage          `json:"usage,omitempty"`
}

type ResponsePartType string

const (
	ResponsePartTypeText      ResponsePartType = "text"
	ResponsePartTypeError     ResponsePartType = "error"
	ResponsePartTypeToolCall  ResponsePartType = "tool_call"
	ResponsePartTypeReasoning ResponsePartType = "reasoning"
)

type ResponsePart struct {
	Type           ResponsePartType `json:"type"`
	Text           string           `json:"text,omitempty"`
	ProviderCallID string           `json:"provider_call_id,omitempty"`
	ToolName       string           `json:"tool_name,omitempty"`
	ToolInput      json.RawMessage  `json:"tool_input,omitempty"`
}

func NormalizeToolInput(input json.RawMessage) (json.RawMessage, error) {
	input = bytes.TrimSpace(input)
	if err := ValidateToolInput(input); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), input...), nil
}

func ValidateToolInput(input json.RawMessage) error {
	input = bytes.TrimSpace(input)
	if len(input) == 0 || bytes.Equal(input, []byte("null")) {
		return errors.New("tool input must be a JSON object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(input, &object); err != nil || object == nil {
		return errors.New("tool input must be a JSON object")
	}
	return nil
}

type ProviderReplayIdentity struct {
	ModelProviderConfigID      string
	RequestedProviderModelSlug string
	APIFormat                  modelprotocol.APIFormat
	APIVariant                 modelprotocol.APIVariant
}

func (i ProviderReplayIdentity) Matches(other ProviderReplayIdentity) bool {
	return i.valid() && other.valid() && i == other
}

func (i ProviderReplayIdentity) valid() bool {
	return strings.TrimSpace(i.ModelProviderConfigID) != "" &&
		strings.TrimSpace(i.RequestedProviderModelSlug) != "" &&
		strings.TrimSpace(string(i.APIFormat)) != "" &&
		strings.TrimSpace(string(i.APIVariant)) != ""
}

func (e ResponseEnvelope) Validate() error {
	if strings.TrimSpace(e.RequestedProviderModelSlug) == "" ||
		strings.TrimSpace(string(e.APIFormat)) == "" ||
		strings.TrimSpace(string(e.APIVariant)) == "" {
		return errors.New(
			"provider response envelope requested provider model slug, API format, and API variant are required",
		)
	}
	if e.Normalized.StopReason == "" {
		return errors.New("provider response envelope normalized stop reason is required")
	}
	if len(e.ProviderReplay) != 0 && !validReplayPayload(e.ProviderReplay) {
		return errors.New("provider replay must be valid non-null JSON")
	}
	if err := ValidateProviderReportedCostUSD(e.ProviderReportedCostUSD); err != nil {
		return fmt.Errorf("provider-reported cost: %w", err)
	}
	for index, part := range e.Normalized.Content {
		if err := validateResponsePart(part); err != nil {
			return fmt.Errorf("response content part %d: %w", index, err)
		}
	}
	return nil
}

func validateResponsePart(part ResponsePart) error {
	switch part.Type {
	case ResponsePartTypeText, ResponsePartTypeError:
		if part.ProviderCallID != "" ||
			part.ToolName != "" || len(part.ToolInput) != 0 {
			return fmt.Errorf("%s part carries fields from another content type", part.Type)
		}
	case ResponsePartTypeReasoning:
		if part.ProviderCallID != "" ||
			part.ToolName != "" || len(part.ToolInput) != 0 {
			return errors.New("reasoning part carries fields from another content type")
		}
	case ResponsePartTypeToolCall:
		if part.Text != "" ||
			part.ProviderCallID == "" || part.ToolName == "" {
			return errors.New("tool_call part is invalid or missing provider_call_id or tool_name")
		}
		if err := ValidateToolInput(part.ToolInput); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported type %q", part.Type)
	}
	return nil
}

func validReplayPayload(raw json.RawMessage) bool {
	if len(raw) == 0 || !json.Valid(raw) {
		return false
	}
	var value any
	return json.Unmarshal(raw, &value) == nil && value != nil
}

func (e ResponseEnvelope) HasToolCalls() bool {
	return HasToolCallParts(e.Normalized.Content)
}

func HasToolCallParts(parts []ResponsePart) bool {
	for _, part := range parts {
		if part.Type == ResponsePartTypeToolCall {
			return true
		}
	}
	return false
}

func (e ResponseEnvelope) StripToolCallParts() []ResponsePart {
	out := make([]ResponsePart, 0, len(e.Normalized.Content))
	for _, part := range e.Normalized.Content {
		if part.Type == ResponsePartTypeToolCall {
			continue
		}
		out = append(out, part)
	}
	return out
}

func (e ResponseEnvelope) Text() string {
	return TextFromParts(e.Normalized.Content)
}

func TextFromParts(parts []ResponsePart) string {
	var out strings.Builder
	for _, part := range parts {
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

type StopReason string

const (
	StopReasonEndTurn       StopReason = "end_turn"
	StopReasonToolUse       StopReason = "tool_use"
	StopReasonMaxTokens     StopReason = "max_tokens"
	StopReasonRefusal       StopReason = "refusal"
	StopReasonContentFilter StopReason = "content_filter"
	StopReasonPause         StopReason = "pause"
	StopReasonContextWindow StopReason = "context_window"
	StopReasonError         StopReason = "error"
	StopReasonUnknown       StopReason = "unknown"
)

func IsDurableModelOutputStopReason(reason StopReason) bool {
	switch reason {
	case StopReasonEndTurn,
		StopReasonToolUse,
		StopReasonMaxTokens,
		StopReasonRefusal,
		StopReasonContentFilter,
		StopReasonError:
		return true
	default:
		return false
	}
}

func NormalizeStopReason(reason StopReason, hasToolCalls bool) StopReason {
	// A response that proposes tool calls is a tool-use turn regardless of
	// how the provider labels the stop (OpenAI reports end_turn).
	if reason == "" || reason == StopReasonEndTurn {
		if hasToolCalls {
			return StopReasonToolUse
		}
		if reason == "" {
			return StopReasonEndTurn
		}
	}
	switch reason {
	case StopReasonEndTurn,
		StopReasonToolUse,
		StopReasonMaxTokens,
		StopReasonRefusal,
		StopReasonContentFilter,
		StopReasonPause,
		StopReasonContextWindow,
		StopReasonError,
		StopReasonUnknown:
		return reason
	default:
		return StopReasonUnknown
	}
}

type Usage struct {
	// InputTokens includes uncached, cache-read, and cache-write input tokens.
	InputTokens         int `json:"input_tokens_total,omitempty"`
	UncachedInputTokens int `json:"uncached_input_tokens,omitempty"`
	// OutputTokens includes visible output and reasoning tokens.
	OutputTokens     int `json:"output_tokens_total,omitempty"`
	ReasoningTokens  int `json:"reasoning_output_tokens,omitempty"`
	CacheReadTokens  int `json:"cache_read_input_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_input_tokens,omitempty"`
}

func OptionalCount(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}

// NormalizeUsage rejects usage that violates the token-accounting
// invariants (negative counts, cache reads/writes exceeding input, or an
// inconsistent uncached-input derivation) by zeroing it, and derives
// UncachedInputTokens when the provider omitted it.
func NormalizeUsage(usage Usage) Usage {
	if usage.InputTokens < 0 ||
		usage.UncachedInputTokens < 0 ||
		usage.OutputTokens < 0 ||
		usage.ReasoningTokens < 0 ||
		usage.CacheReadTokens < 0 ||
		usage.CacheWriteTokens < 0 ||
		usage.ReasoningTokens > usage.OutputTokens {
		return Usage{}
	}
	if usage.CacheReadTokens > usage.InputTokens ||
		usage.CacheWriteTokens > usage.InputTokens-usage.CacheReadTokens {
		return Usage{}
	}
	if usage.InputTokens > 0 && usage.UncachedInputTokens == 0 {
		usage.UncachedInputTokens = usage.InputTokens - usage.CacheReadTokens - usage.CacheWriteTokens
	}
	if usage.UncachedInputTokens != usage.InputTokens-usage.CacheReadTokens-usage.CacheWriteTokens {
		return Usage{}
	}
	return usage
}
