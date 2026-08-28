package openaichatcompletions

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func (p protocol) ProjectRenderedMedia(bundle modelcontext.Bundle) []modelcontext.RenderedMedia {
	var rendered []modelcontext.RenderedMedia
	modelCandidates := []string{p.client.ProviderModelSlug}
	if p.client.ModelAPIVariant() == modelprotocol.APIVariantOpenRouter {
		modelCandidates = append(
			modelCandidates,
			openRouterFallbackModelSlugs(p.client.APIVariantOptions)...,
		)
	}
	for _, occurrence := range modelcontext.ResolvedMediaOccurrences(bundle) {
		if occurrence.MessageRole != modelprotocol.RoleUser ||
			occurrence.Media.Kind != modelcontext.AttachmentKindImage {
			continue
		}
		rendered = append(rendered, modelcontext.RenderedMedia{
			Occurrence:     occurrence.Ref,
			Media:          occurrence.Media,
			Representation: modelcontext.MediaRepresentationInline,
			TokenEstimate:  model.OpenAIImageTokenEstimateForModels(modelCandidates, occurrence.Media),
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
		if resolved.Kind == modelcontext.AttachmentKindImage && len(resolved.Data) > 0 {
			out[id] = resolved
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func userChatContentFromParts(raw json.RawMessage, media map[string]modelcontext.ResolvedMedia) any {
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
				if block, ok := chatImagePart(resolved); ok {
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

func chatImagePart(resolved modelcontext.ResolvedMedia) (map[string]any, bool) {
	if resolved.Kind != modelcontext.AttachmentKindImage {
		return nil, false
	}
	dataURL := "data:" + resolved.MediaType + ";base64," + base64.StdEncoding.EncodeToString(resolved.Data)
	return map[string]any{"type": "image_url", "image_url": map[string]string{"url": dataURL}}, true
}

func toolResultOutput(result modelcontext.ToolResultRef) string {
	return textContentFromParts(result.ContentParts, nil)
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
