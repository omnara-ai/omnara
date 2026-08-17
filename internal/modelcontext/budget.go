package modelcontext

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"strings"
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
	return estimateTokensFromBytes(len(projected)) + renderedMediaTokenEstimate(media)
}

func estimateTokensFromBytes(bytes int) int {
	return bytes/4 + 1
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

func renderedMediaTokenEstimate(media []RenderedMedia) int {
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

// ProviderUsageInputFloor carries forward the latest provider measurement when
// it is compatible, then estimates only the model-visible suffix added since
// that call. A newer unusable measurement is a barrier; older anchors are not
// reused. The result is a conservative floor for the full request estimate.
func ProviderUsageInputFloor(
	bundle Bundle,
	target ModelRequestIdentity,
	suppressProviderReplay bool,
) (int, bool) {
	if suppressProviderReplay || !completeModelRequestIdentity(target) {
		return 0, false
	}
	var anchor *ProviderUsageAnchor
	anchorSequence := int64(0)
	for index := len(bundle.Messages) - 1; index >= 0; index-- {
		if bundle.Messages[index].UsageAnchor != nil {
			anchor = bundle.Messages[index].UsageAnchor
			anchorSequence = bundle.Messages[index].Sequence
			break
		}
	}
	if anchor == nil || anchor.Usage.InputTokens <= 0 ||
		anchorSequence <= 0 || anchor.Identity != target {
		return 0, false
	}
	if checkpoint := bundle.ContextCheckpoint; checkpoint != nil &&
		(checkpoint.PublishedEventSequence <= 0 || anchorSequence <= checkpoint.PublishedEventSequence) {
		return 0, false
	}
	suffix := estimateModelVisibleSuffix(bundle, anchorSequence)
	if anchor.Usage.InputTokens > math.MaxInt-anchor.Usage.OutputTokens ||
		anchor.Usage.InputTokens+anchor.Usage.OutputTokens > math.MaxInt-suffix {
		return 0, false
	}
	return anchor.Usage.InputTokens + anchor.Usage.OutputTokens + suffix, true
}

func completeModelRequestIdentity(identity ModelRequestIdentity) bool {
	return strings.TrimSpace(identity.AgentConfigID) != "" &&
		strings.TrimSpace(identity.ConfiguredModelRevisionID) != "" &&
		strings.TrimSpace(identity.RequestedModelSlug) != "" &&
		identity.APIFormat != "" && identity.APIVariant != ""
}

func estimateModelVisibleSuffix(bundle Bundle, afterSequence int64) int {
	type suffixMessage struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
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
				Role:    string(message.Role),
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
	estimate := estimateTokensFromBytes(len(body))
	for _, rendered := range bundle.RenderedMedia {
		switch rendered.Occurrence.ownerKind {
		case mediaOccurrenceOwnerUnknown:
		case mediaOccurrenceOwnerMessage:
			if rendered.Occurrence.ownerIndex >= 0 &&
				rendered.Occurrence.ownerIndex < len(bundle.Messages) &&
				bundle.Messages[rendered.Occurrence.ownerIndex].Sequence > afterSequence {
				estimate += renderedMediaTokenEstimate([]RenderedMedia{rendered})
			}
		case mediaOccurrenceOwnerToolResult:
			if rendered.Occurrence.ownerIndex >= 0 &&
				rendered.Occurrence.ownerIndex < len(bundle.ToolResults) &&
				bundle.ToolResults[rendered.Occurrence.ownerIndex].ResultEventSequence > afterSequence {
				estimate += renderedMediaTokenEstimate([]RenderedMedia{rendered})
			}
		}
	}
	return estimate
}
