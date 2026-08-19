package modelcontext

import (
	"encoding/base64"
	"encoding/json"
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
	return len(projected)/4 + 1 + renderedMediaTokenEstimate(media)
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
