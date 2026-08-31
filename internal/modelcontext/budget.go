package modelcontext

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

const (
	DefaultImageTokenEstimate          = 4_784
	DefaultBinaryDocumentTokenEstimate = 8_192
)

type ModelWindow struct {
	ContextTokens          int `json:"context_tokens"`
	RequestMaxOutputTokens int `json:"request_max_output_tokens,omitempty"`
	SafetyMarginTokens     int `json:"safety_margin_tokens"`
}

func (w ModelWindow) UsableInputTokens() int {
	usable := w.ContextTokens - w.RequestMaxOutputTokens - w.SafetyMarginTokens
	if usable < 0 {
		return 0
	}
	return usable
}

func DefaultSafetyMarginTokens(contextTokens int) int {
	if contextTokens <= 0 {
		return 0
	}
	margin := contextTokens / 20
	if contextTokens >= 4_096 && margin < 1_024 {
		margin = 1_024
	}
	if margin > 8_192 {
		margin = 8_192
	}
	return margin
}

// EstimatePreparedRequest replaces inline base64 with the adapter's media-token estimate.
func EstimatePreparedRequest(body json.RawMessage, media []RenderedMedia) int {
	projected := preparedRequestWithoutInlineMedia(body, media)
	return estimateSerializedTextTokens(projected) + EstimateRenderedMediaTokens(media)
}

func preparedRequestWithoutInlineMedia(body json.RawMessage, media []RenderedMedia) json.RawMessage {
	encodedMedia := map[string]struct{}{}
	for _, item := range media {
		if item.Representation != MediaRepresentationInline || len(item.Media.Data) == 0 {
			continue
		}
		encodedMedia[base64.StdEncoding.EncodeToString(item.Media.Data)] = struct{}{}
	}
	if len(encodedMedia) == 0 {
		return body
	}
	var request any
	if err := json.Unmarshal(body, &request); err != nil {
		return body
	}
	replaceInlineMediaFields(request, encodedMedia)
	projected, err := json.Marshal(request)
	if err != nil {
		return body
	}
	return projected
}

func replaceInlineMediaFields(value any, encodedMedia map[string]struct{}) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			replaceInlineMediaFields(item, encodedMedia)
		}
	case map[string]any:
		switch typed["type"] {
		case "input_image":
			replaceDataURLField(typed, "image_url", encodedMedia)
		case "input_file":
			replaceDataURLField(typed, "file_data", encodedMedia)
		case "file":
			if file, ok := typed["file"].(map[string]any); ok {
				replaceDataURLField(file, "file_data", encodedMedia)
			}
		case "image", "document":
			if source, ok := typed["source"].(map[string]any); ok && source["type"] == "base64" {
				replaceEncodedField(source, "data", encodedMedia)
			}
		case "image_url":
			if imageURL, ok := typed["image_url"].(map[string]any); ok {
				replaceDataURLField(imageURL, "url", encodedMedia)
			}
		}
		for _, item := range typed {
			replaceInlineMediaFields(item, encodedMedia)
		}
	}
}

func replaceDataURLField(object map[string]any, field string, encodedMedia map[string]struct{}) {
	value, ok := object[field].(string)
	if !ok {
		return
	}
	marker := ";base64,"
	index := strings.Index(value, marker)
	if index < 0 {
		return
	}
	if _, ok := encodedMedia[value[index+len(marker):]]; ok {
		object[field] = "<resolved-media>"
	}
}

func replaceEncodedField(object map[string]any, field string, encodedMedia map[string]struct{}) {
	value, ok := object[field].(string)
	if !ok {
		return
	}
	if _, ok := encodedMedia[value]; ok {
		object[field] = "<resolved-media>"
	}
}

func EstimateRenderedMediaTokens(media []RenderedMedia) int {
	estimate := 0
	for _, item := range media {
		if item.Representation == MediaRepresentationInlineText {
			continue
		}
		if item.TokenEstimate > 0 {
			estimate += item.TokenEstimate
		} else if item.Media.Kind == AttachmentKindImage {
			estimate += DefaultImageTokenEstimate
		} else {
			estimate += DefaultBinaryDocumentTokenEstimate
		}
	}
	return estimate
}

type ProviderUsageInputEstimate struct {
	AnchorInputEventSequence int64 `json:"anchor_input_event_sequence"`
	ProviderInputTokens      int   `json:"provider_input_tokens"`
	ProviderOutputTokens     int   `json:"provider_output_tokens"`
	NewTailTokens            int   `json:"new_tail_tokens"`
	EstimatedInputTokens     int   `json:"estimated_input_tokens"`
}

func EstimateInputFromProviderUsage(
	bundle Bundle,
	target ModelRequestIdentity,
	providerReplayCutoffEventSequence int64,
) (ProviderUsageInputEstimate, bool) {
	if !modelRequestIdentityComplete(target) {
		return ProviderUsageInputEstimate{}, false
	}
	for index := len(bundle.Messages) - 1; index >= 0; index-- {
		message := bundle.Messages[index]
		if message.Role != modelprotocol.RoleAssistant {
			continue
		}
		anchor := message.ProviderUsageAnchor
		if anchor == nil || !modelRequestIdentityMatches(anchor.Identity, target) ||
			anchor.InputEventSequence <= 0 || anchor.InputEventSequence > bundle.InputEventSequence ||
			anchor.InputTokens <= 0 || anchor.OutputTokens < 0 ||
			(providerReplayCutoffEventSequence > 0 &&
				anchor.InputEventSequence < providerReplayCutoffEventSequence) ||
			!anchorIncludesCheckpoint(bundle.ContextCheckpoint, anchor.InputEventSequence) {
			return ProviderUsageInputEstimate{}, false
		}
		tail := estimateModelVisibleSuffix(bundle, message.Sequence)
		estimate, ok := checkedTokenSum(anchor.InputTokens, anchor.OutputTokens, tail)
		if !ok {
			return ProviderUsageInputEstimate{}, false
		}
		return ProviderUsageInputEstimate{
			AnchorInputEventSequence: anchor.InputEventSequence,
			ProviderInputTokens:      anchor.InputTokens,
			ProviderOutputTokens:     anchor.OutputTokens,
			NewTailTokens:            tail,
			EstimatedInputTokens:     estimate,
		}, true
	}
	return ProviderUsageInputEstimate{}, false
}

func modelRequestIdentityComplete(identity ModelRequestIdentity) bool {
	provider := identity.ProviderRequestIdentity
	return strings.TrimSpace(identity.AgentConfigID) != "" &&
		strings.TrimSpace(identity.ConfiguredModelRevisionID) != "" &&
		strings.TrimSpace(provider.ModelProviderConfigID) != "" &&
		strings.TrimSpace(provider.RequestedProviderModelSlug) != "" &&
		strings.TrimSpace(string(provider.APIFormat)) != "" &&
		strings.TrimSpace(string(provider.APIVariant)) != ""
}

func modelRequestIdentityMatches(left, right ModelRequestIdentity) bool {
	return left.AgentConfigID == right.AgentConfigID &&
		left.ConfiguredModelRevisionID == right.ConfiguredModelRevisionID &&
		left.ProviderRequestIdentity.Matches(right.ProviderRequestIdentity)
}

func anchorIncludesCheckpoint(checkpoint *CheckpointRef, inputEventSequence int64) bool {
	if checkpoint == nil {
		return true
	}
	return checkpoint.EventSequence > 0 && inputEventSequence >= checkpoint.EventSequence
}

func estimateModelVisibleSuffix(bundle Bundle, afterSequence int64) int {
	type suffixMessage struct {
		Role    modelprotocol.MessageRole `json:"role"`
		Content json.RawMessage           `json:"content"`
	}
	type suffixToolResult struct {
		Name         string          `json:"name"`
		ContentParts json.RawMessage `json:"content_parts"`
	}
	projection := struct {
		Messages    []suffixMessage    `json:"messages,omitempty"`
		ToolResults []suffixToolResult `json:"tool_results,omitempty"`
	}{}
	for _, message := range bundle.Messages {
		if message.Sequence > afterSequence {
			projection.Messages = append(projection.Messages, suffixMessage{
				Role:    message.Role,
				Content: message.Content,
			})
		}
	}
	for _, result := range bundle.ToolResults {
		if result.ResultEventSequence > afterSequence {
			projection.ToolResults = append(projection.ToolResults, suffixToolResult{
				Name:         result.Name,
				ContentParts: result.ContentParts,
			})
		}
	}
	body, err := json.Marshal(projection)
	if err != nil {
		return 0
	}
	estimate := 0
	if len(projection.Messages) > 0 || len(projection.ToolResults) > 0 {
		estimate = estimateSerializedTextTokens(body)
	}
	for _, rendered := range bundle.RenderedMedia {
		included := false
		switch rendered.Occurrence.ownerKind {
		case mediaOccurrenceOwnerUnknown:
		case mediaOccurrenceOwnerMessage:
			included = rendered.Occurrence.ownerIndex >= 0 &&
				rendered.Occurrence.ownerIndex < len(bundle.Messages) &&
				bundle.Messages[rendered.Occurrence.ownerIndex].Sequence > afterSequence
		case mediaOccurrenceOwnerToolResult:
			included = rendered.Occurrence.ownerIndex >= 0 &&
				rendered.Occurrence.ownerIndex < len(bundle.ToolResults) &&
				bundle.ToolResults[rendered.Occurrence.ownerIndex].ResultEventSequence > afterSequence
		}
		if !included {
			continue
		}
		if rendered.Representation == MediaRepresentationInlineText {
			estimate += estimateSerializedTextTokens(rendered.Media.Data)
		} else {
			estimate += EstimateRenderedMediaTokens([]RenderedMedia{rendered})
		}
	}
	return estimate
}

func checkedTokenSum(values ...int) (int, bool) {
	total := 0
	for _, value := range values {
		if value < 0 || total > math.MaxInt-value {
			return 0, false
		}
		total += value
	}
	return total, true
}

func estimateSerializedTextTokens(value []byte) int {
	denseBytes := 0
	denseRunes := 0
	for index := 0; index < len(value); {
		if value[index] < utf8.RuneSelf {
			index++
			continue
		}
		r, size := utf8.DecodeRune(value[index:])
		if unicode.In(r, unicode.Han, unicode.Hangul, unicode.Hiragana, unicode.Katakana) {
			denseBytes += size
			denseRunes++
		}
		index += size
	}
	return ceilDiv(len(value)-denseBytes, 4) + denseRunes
}

func ceilDiv(value, divisor int) int {
	return (value + divisor - 1) / divisor
}
