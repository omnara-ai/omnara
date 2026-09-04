package openaichatcompletions

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

func (p protocol) ProjectRenderedMedia(bundle modelcontext.Bundle) []modelcontext.RenderedMedia {
	var rendered []modelcontext.RenderedMedia
	compat := p.client.compat()
	modelCandidates := []string{p.client.ProviderModelSlug}
	if compat.routesFallbackModels {
		modelCandidates = append(
			modelCandidates,
			openRouterFallbackModelSlugs(p.client.APIVariantOptions)...,
		)
	}
	for _, occurrence := range modelcontext.ResolvedMediaOccurrences(bundle) {
		if occurrence.MessageRole != modelprotocol.RoleUser && !occurrence.IsToolResult() {
			continue
		}
		item := occurrence.Media
		if occurrence.IsToolResult() && item.Kind != modelcontext.AttachmentKindImage {
			continue
		}
		representation := modelcontext.MediaRepresentationInline
		tokenEstimate := 0
		routeParsed := compat.parsesPDFDocuments && item.MediaType == "application/pdf"
		switch item.Kind {
		case modelcontext.AttachmentKindImage:
			tokenEstimate = model.OpenAIImageTokenEstimateForModels(modelCandidates, item)
		case modelcontext.AttachmentKindDocument:
			if !occurrence.Opening && item.MediaType == "application/pdf" && !routeParsed &&
				!p.client.ModelCapabilities.AllowsInputModality("file") {
				continue
			}
			if !rendersAsChatDocument(item) {
				continue
			}
			if rendersAsChatText(item) {
				representation = modelcontext.MediaRepresentationInlineText
			}
		default:
			continue
		}
		rendered = append(rendered, modelcontext.RenderedMedia{
			Occurrence:     occurrence.Ref,
			Media:          item,
			Representation: representation,
			RouteParsed:    routeParsed,
			TokenEstimate:  tokenEstimate,
		})
	}
	return rendered
}

func openRouterFallbackModelSlugs(options json.RawMessage) []string {
	var decoded struct {
		Models []string `json:"models"`
	}
	if json.Unmarshal(options, &decoded) != nil {
		return nil
	}
	out := make([]string, 0, len(decoded.Models))
	for _, modelSlug := range decoded.Models {
		if modelSlug = strings.TrimSpace(modelSlug); modelSlug != "" {
			out = append(out, modelSlug)
		}
	}
	return out
}

func renderableMedia(media map[string]modelcontext.ResolvedMedia) map[string]modelcontext.ResolvedMedia {
	out := make(map[string]modelcontext.ResolvedMedia, len(media))
	for id, resolved := range media {
		if len(resolved.Data) == 0 {
			continue
		}
		if resolved.Kind != modelcontext.AttachmentKindImage &&
			(resolved.Kind != modelcontext.AttachmentKindDocument || !rendersAsChatDocument(resolved)) {
			continue
		}
		out[id] = resolved
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func userChatContentFromParts(
	raw json.RawMessage,
	media map[string]modelcontext.ResolvedMedia,
) any {
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil
	}
	blocks := make([]any, 0, len(parts))
	texts := make([]string, 0, len(parts))
	hasMedia := false
	for _, part := range parts {
		partType := jsonFieldText(part["type"])
		if partType == "media_ref" {
			artifactID := jsonFieldText(part["artifact_id"])
			if resolved, ok := media[artifactID]; ok {
				if block, ok := chatMediaPart(resolved); ok {
					hasMedia = true
					if ref := modelcontext.ArtifactPublicID(artifactID); ref != "" {
						blocks = append(blocks, map[string]any{"type": "text", "text": "artifact_id: " + ref})
					}
					blocks = append(blocks, block)
				}
				continue
			}
			if text := modelcontext.MediaRefText(part); text != "" {
				texts = append(texts, text)
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
			}
			continue
		}
		if text := textFromPart(part, partType); strings.TrimSpace(text) != "" {
			texts = append(texts, text)
			blocks = append(blocks, map[string]any{"type": "text", "text": text})
		}
	}
	if len(blocks) == 0 {
		return nil
	}
	if !hasMedia {
		return strings.Join(texts, "\n")
	}
	return blocks
}

func chatMediaPart(resolved modelcontext.ResolvedMedia) (map[string]any, bool) {
	if rendersAsChatText(resolved) {
		text := string(resolved.Data)
		if resolved.Filename != "" {
			text = "File: " + strconv.Quote(resolved.Filename) + "\n\n" + text
		}
		return map[string]any{"type": "text", "text": text}, true
	}
	dataMediaType := resolved.MediaType
	if dataMediaType == "text/tab-separated-values" {
		dataMediaType = "text/tsv"
	}
	dataURL := "data:" + dataMediaType + ";base64," + base64.StdEncoding.EncodeToString(resolved.Data)
	switch resolved.Kind {
	case modelcontext.AttachmentKindImage:
		return map[string]any{"type": "image_url", "image_url": map[string]string{"url": dataURL}}, true
	case modelcontext.AttachmentKindDocument:
		return map[string]any{
			"type": "file",
			"file": map[string]string{
				"filename":  modelcontext.MediaFilename(resolved.Filename, resolved.MediaType),
				"file_data": dataURL,
			},
		}, true
	default:
		return nil, false
	}
}

func rendersAsChatText(resolved modelcontext.ResolvedMedia) bool {
	return modelcontext.IsTextDocumentMediaType(resolved.MediaType) &&
		(len(resolved.Data) == 0 || utf8.Valid(resolved.Data))
}

func rendersAsChatDocument(resolved modelcontext.ResolvedMedia) bool {
	return rendersAsChatText(resolved) || resolved.MediaType == "application/pdf"
}

func toolResultOutput(
	result modelcontext.ToolResultRef,
	media map[string]modelcontext.ResolvedMedia,
) string {
	return textContentFromParts(result.ContentParts, media)
}

func toolResultMediaContent(
	result modelcontext.ToolResultRef,
	media map[string]modelcontext.ResolvedMedia,
) []any {
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(result.ContentParts, &parts); err != nil {
		return nil
	}
	var blocks []any
	for _, part := range parts {
		if jsonFieldText(part["type"]) != "media_ref" {
			continue
		}
		artifactID := jsonFieldText(part["artifact_id"])
		resolved, ok := media[artifactID]
		if !ok || resolved.Kind != modelcontext.AttachmentKindImage {
			continue
		}
		block, ok := chatMediaPart(resolved)
		if !ok {
			continue
		}
		blocks = append(blocks, map[string]any{
			"type": "text",
			"text": "Attachment returned by tool " + result.Name + ". tool_call_id: " +
				result.ProviderCallID + ". artifact_id: " + modelcontext.ArtifactPublicID(artifactID),
		})
		blocks = append(blocks, block)
	}
	return blocks
}

func textContentFromParts(
	raw json.RawMessage,
	media map[string]modelcontext.ResolvedMedia,
) string {
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var b strings.Builder
	emit := func(s string) {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(s)
	}
	for _, part := range parts {
		partType := jsonFieldText(part["type"])
		if partType == "media_ref" {
			artifactID := jsonFieldText(part["artifact_id"])
			if _, ok := media[artifactID]; ok {
				continue
			}
			if text := modelcontext.MediaRefText(part); text != "" {
				emit(text)
			}
			continue
		}
		if text := textFromPart(part, partType); strings.TrimSpace(text) != "" {
			emit(text)
		}
	}
	return b.String()
}

func textFromPart(part map[string]json.RawMessage, partType string) string {
	if partType == "reasoning" {
		return ""
	}
	if text := jsonFieldText(part["text"]); strings.TrimSpace(text) != "" {
		return text
	}
	if partType == "structured_data" {
		if value := part["value"]; len(value) != 0 {
			return string(value)
		}
	}
	switch partType {
	case "status", "error", "summary", "agent_ref", "tool_call_ref", "tool_result_ref", "interaction_ref":
		body, err := json.Marshal(map[string]any{"type": partType, "payload": part})
		if err == nil {
			return string(body)
		}
	}
	return ""
}

func jsonFieldText(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}
