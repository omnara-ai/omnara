package modelcontext

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	DefaultImageTokenEstimate          = 4_784
	DefaultBinaryDocumentTokenEstimate = 8_192
)

type ModelWindow struct {
	ContextTokens       int `json:"context_tokens"`
	OutputReserveTokens int `json:"output_reserve_tokens,omitempty"`
	SafetyMarginTokens  int `json:"safety_margin_tokens"`
}

func (w ModelWindow) UsableInputTokens() int {
	usable := w.ContextTokens - w.OutputReserveTokens - w.SafetyMarginTokens
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

// EstimatePreparedRequest replaces inline base64 with the adapter's media-token estimate
// and drops deferred tool definitions that no tool_reference has loaded into context.
func EstimatePreparedRequest(body json.RawMessage, media []RenderedMedia) int {
	projected, mediaTokens := projectPreparedRequest(body, media)
	return estimateSerializedTextTokens(projected) + mediaTokens
}

func estimateSerializedTextTokens(value []byte) int {
	denseBytes, denseRunes := 0, 0
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
	return (len(value)-denseBytes+3)/4 + denseRunes
}

func projectPreparedRequest(body json.RawMessage, media []RenderedMedia) (json.RawMessage, int) {
	encodedMedia := map[string][]int{}
	for _, item := range media {
		if item.Representation != MediaRepresentationInline || len(item.Media.Data) == 0 {
			continue
		}
		tokens := item.TokenEstimate
		if tokens <= 0 {
			tokens = DefaultBinaryDocumentTokenEstimate
			if item.Media.Kind == AttachmentKindImage {
				tokens = DefaultImageTokenEstimate
			}
		}
		encoded := base64.StdEncoding.EncodeToString(item.Media.Data)
		encodedMedia[encoded] = append(encodedMedia[encoded], tokens)
	}
	hasDeferredTools := bytes.Contains(body, []byte(`"defer_loading"`))
	if len(encodedMedia) == 0 && !hasDeferredTools {
		return body, 0
	}
	var request any
	if err := json.Unmarshal(body, &request); err != nil {
		return body, 0
	}
	mediaTokens := replaceRequestMedia(request, encodedMedia)
	if hasDeferredTools {
		removeUnloadedDeferredTools(request)
	}
	projected, err := json.Marshal(request)
	if err != nil {
		return body, 0
	}
	return projected, mediaTokens
}

func removeUnloadedDeferredTools(request any) {
	object, ok := request.(map[string]any)
	if !ok {
		return
	}
	tools, ok := object["tools"].([]any)
	if !ok {
		return
	}
	loaded := map[string]struct{}{}
	collectToolReferenceNames(object, loaded)
	kept := make([]any, 0, len(tools))
	for _, tool := range tools {
		definition, ok := tool.(map[string]any)
		if ok && definition["defer_loading"] == true {
			name, _ := definition["name"].(string)
			if _, referenced := loaded[name]; !referenced {
				continue
			}
		}
		kept = append(kept, tool)
	}
	object["tools"] = kept
}

func collectToolReferenceNames(value any, names map[string]struct{}) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			collectToolReferenceNames(item, names)
		}
	case map[string]any:
		if typed["type"] == "tool_reference" {
			if name, ok := typed["tool_name"].(string); ok {
				names[name] = struct{}{}
			}
		}
		for _, item := range typed {
			collectToolReferenceNames(item, names)
		}
	}
}

func replaceRequestMedia(request any, encodedMedia map[string][]int) int {
	items, _ := request.([]any)
	if object, ok := request.(map[string]any); ok {
		items, _ = object["messages"].([]any)
		if items == nil {
			items, _ = object["input"].([]any)
		}
	}
	tokens := 0
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := object["role"].(string); role != "" {
			tokens += replaceMediaContent(object["content"], encodedMedia)
		} else if object["type"] == "function_call_output" {
			tokens += replaceMediaContent(object["output"], encodedMedia)
		}
	}
	return tokens
}

func replaceMediaContent(content any, encodedMedia map[string][]int) int {
	blocks, _ := content.([]any)
	tokens := 0
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		var payload map[string]any
		var field string
		switch block["type"] {
		case "tool_result":
			tokens += replaceMediaContent(block["content"], encodedMedia)
			continue
		case "input_image":
			payload, field = block, "image_url"
		case "input_file":
			payload, field = block, "file_data"
		case "file":
			payload, _ = block["file"].(map[string]any)
			field = "file_data"
		case "image_url":
			payload, _ = block["image_url"].(map[string]any)
			field = "url"
		case "image", "document":
			payload, _ = block["source"].(map[string]any)
			if payload["type"] != "base64" {
				continue
			}
			field = "data"
		default:
			continue
		}
		encoded, _ := payload[field].(string)
		if field != "data" {
			index := strings.Index(encoded, ";base64,")
			if !strings.HasPrefix(encoded, "data:") || index < 0 {
				continue
			}
			encoded = encoded[index+8:]
		}
		estimates := encodedMedia[encoded]
		if len(estimates) == 0 {
			continue
		}
		payload[field] = "<resolved-media>"
		tokens += estimates[0]
		encodedMedia[encoded] = estimates[1:]
	}
	return tokens
}
