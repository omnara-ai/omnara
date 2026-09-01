package openairesponses

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

type responsesContentType string

const (
	responsesContentTypeInputText  responsesContentType = "input_text"
	responsesContentTypeOutputText responsesContentType = "output_text"
	responsesContentTypeInputImage responsesContentType = "input_image"
	responsesContentTypeInputFile  responsesContentType = "input_file"
)

func (p protocol) ProjectRenderedMedia(bundle modelcontext.Bundle) []modelcontext.RenderedMedia {
	var rendered []modelcontext.RenderedMedia
	for _, occurrence := range modelcontext.ResolvedMediaOccurrences(bundle) {
		item := occurrence.Media
		switch {
		case occurrence.MessageRole == modelprotocol.RoleUser || occurrence.IsToolResult():
			tokenEstimate := 0
			representation := modelcontext.MediaRepresentationInline
			if item.Kind == modelcontext.AttachmentKindImage {
				tokenEstimate = model.OpenAIImageTokenEstimate(p.client.ProviderModelSlug, item)
			} else if rendersAsInputText(item) {
				representation = modelcontext.MediaRepresentationInlineText
			}
			rendered = append(rendered, modelcontext.RenderedMedia{
				Occurrence:     occurrence.Ref,
				Media:          item,
				Representation: representation,
				TokenEstimate:  tokenEstimate,
			})
		}
	}
	return rendered
}

func renderableMedia(media map[string]modelcontext.ResolvedMedia) map[string]modelcontext.ResolvedMedia {
	out := make(map[string]modelcontext.ResolvedMedia, len(media))
	for id, resolved := range media {
		if _, ok := modelcontext.AttachmentKindForMediaType(resolved.MediaType); ok {
			out[id] = resolved
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func openaiMediaPart(resolved modelcontext.ResolvedMedia) (map[string]any, bool) {
	if rendersAsInputText(resolved) {
		text := string(resolved.Data)
		if resolved.Filename != "" {
			text = "File: " + strconv.Quote(resolved.Filename) + "\n\n" + text
		}
		return map[string]any{"type": responsesContentTypeInputText, "text": text}, true
	}
	dataMediaType := resolved.MediaType
	if dataMediaType == "text/tab-separated-values" {
		dataMediaType = "text/tsv"
	}
	dataURL := "data:" + dataMediaType + ";base64," + base64.StdEncoding.EncodeToString(resolved.Data)
	switch resolved.Kind {
	case modelcontext.AttachmentKindImage:
		return map[string]any{"type": responsesContentTypeInputImage, "image_url": dataURL}, true
	case modelcontext.AttachmentKindDocument:
		return map[string]any{
			"type":      responsesContentTypeInputFile,
			"filename":  modelcontext.MediaFilename(resolved.Filename, resolved.MediaType),
			"file_data": dataURL,
		}, true
	default:
		return nil, false
	}
}

func rendersAsInputText(resolved modelcontext.ResolvedMedia) bool {
	return modelcontext.IsTextDocumentMediaType(resolved.MediaType) &&
		(len(resolved.Data) == 0 || utf8.Valid(resolved.Data))
}

func renderOpenAIInputContent(
	raw json.RawMessage,
	media map[string]modelcontext.ResolvedMedia,
) []any {
	return renderOpenAIContent(raw, media, responsesContentTypeInputText)
}

func renderOpenAIAssistantContent(raw json.RawMessage) []any {
	return renderOpenAIContent(raw, nil, responsesContentTypeOutputText)
}

func renderOpenAIContent(
	raw json.RawMessage,
	media map[string]modelcontext.ResolvedMedia,
	textType responsesContentType,
) []any {
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil
	}
	out := make([]any, 0, len(parts))
	for _, part := range parts {
		partType := jsonFieldText(part["type"])
		if partType == "media_ref" {
			artifactID := jsonFieldText(part["artifact_id"])
			if resolved, ok := media[artifactID]; ok {
				if block, ok := openaiMediaPart(resolved); ok {
					if ref := modelcontext.ArtifactPublicID(artifactID); ref != "" {
						out = append(out, map[string]any{"type": textType, "text": "artifact_id: " + ref})
					}
					out = append(out, block)
				}
				continue
			}
			if text := modelcontext.MediaRefText(part); text != "" {
				out = append(out, map[string]any{"type": textType, "text": text})
			}
			continue
		}
		if partType == "reasoning" {
			continue
		}
		if text := jsonFieldText(part["text"]); strings.TrimSpace(text) != "" {
			out = append(out, map[string]any{"type": textType, "text": text})
			continue
		}
		if partType == "structured_data" {
			if value := part["value"]; len(value) != 0 {
				out = append(out, map[string]any{"type": textType, "text": string(value)})
			}
			continue
		}
		switch partType {
		case "status", "error", "summary", "agent_ref", "tool_call_ref", "tool_result_ref", "interaction_ref":
			body, err := json.Marshal(map[string]any{"type": partType, "payload": part})
			if err != nil {
				continue
			}
			out = append(out, map[string]any{"type": textType, "text": string(body)})
		}
	}
	return out
}

func toolResultOutput(
	result modelcontext.ToolResultRef,
	media map[string]modelcontext.ResolvedMedia,
) any {
	return renderOpenAIInputContent(result.ContentParts, media)
}

func jsonFieldText(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}
