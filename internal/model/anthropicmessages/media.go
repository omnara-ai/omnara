package anthropicmessages

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

// Leave 1 KiB below Anthropic's 32 MB request-body limit.
const anthropicMessagesRequestBodyLimit = 32_000_000 - 1024

// Anthropic applies its 10 MB image limit after base64 encoding.
const anthropicMessagesImageBase64Limit = 10_000_000

func (c Client) prepareWithinRequestBodyLimit(
	ctx context.Context,
	input model.PrepareInput,
	limit int,
) (model.PreparedRequest, error) {
	routeClient := c.routeClient()
	contextCloned := false
	omittedArtifacts := map[string]bool{}
	oversizedBodyBytes := 0
	cloneContext := func() {
		if contextCloned {
			return
		}
		input.Context.Messages = append([]modelcontext.Message(nil), input.Context.Messages...)
		input.Context.ToolResults = append([]modelcontext.ToolResultRef(nil), input.Context.ToolResults...)
		contextCloned = true
	}
	for _, occurrence := range anthropicMediaOccurrences(input.Context) {
		media := occurrence.Media
		if media.Kind != modelcontext.AttachmentKindImage ||
			base64.StdEncoding.EncodedLen(len(media.Data)) <= anthropicMessagesImageBase64Limit {
			continue
		}
		if occurrence.Opening {
			return model.PreparedRequest{}, route.SetupError{
				Kind: model.ErrorKindInvalidRequest,
				Err: fmt.Errorf(
					"current image %s exceeds Anthropic's %d byte base64 image limit",
					media.ArtifactID,
					anthropicMessagesImageBase64Limit,
				),
			}
		}
		cloneContext()
		if err := modelcontext.ReplaceMediaOccurrenceWithText(&input.Context, occurrence.Ref); err != nil {
			return model.PreparedRequest{}, route.SetupError{Err: err}
		}
		omittedArtifacts[media.ArtifactID] = true
	}
	for {
		prepared, err := routeClient.Prepare(ctx, input)
		if err != nil {
			return model.PreparedRequest{}, err
		}
		if limit <= 0 || len(prepared.Body) <= limit {
			logent.ModelRequestMediaOmittedForBodyLimit(
				ctx,
				len(omittedArtifacts),
				oversizedBodyBytes,
				limit,
			)
			return prepared, nil
		}
		if oversizedBodyBytes == 0 {
			oversizedBodyBytes = len(prepared.Body)
		}
		occurrence, found := oldestOmittableAnthropicMediaOccurrence(input.Context)
		if !found {
			return model.PreparedRequest{}, route.SetupError{
				Kind: model.ErrorKindInvalidRequest,
				Err: fmt.Errorf(
					"anthropic-messages request body exceeds the %d byte provider limit",
					limit,
				),
			}
		}
		cloneContext()
		if err := modelcontext.ReplaceMediaOccurrenceWithText(&input.Context, occurrence.Ref); err != nil {
			return model.PreparedRequest{}, route.SetupError{Err: err}
		}
		omittedArtifacts[occurrence.Media.ArtifactID] = true
	}
}

func oldestOmittableAnthropicMediaOccurrence(
	bundle modelcontext.Bundle,
) (modelcontext.ResolvedMediaOccurrence, bool) {
	for _, occurrence := range anthropicMediaOccurrences(bundle) {
		if !occurrence.Opening {
			return occurrence, true
		}
	}
	return modelcontext.ResolvedMediaOccurrence{}, false
}

func (p protocol) ProjectRenderedMedia(bundle modelcontext.Bundle) []modelcontext.RenderedMedia {
	var rendered []modelcontext.RenderedMedia
	for _, occurrence := range anthropicMediaOccurrences(bundle) {
		item := occurrence.Media
		tokenEstimate := 0
		representation := modelcontext.MediaRepresentationInline
		if item.Kind == modelcontext.AttachmentKindImage {
			tokenEstimate = model.AnthropicImageTokenEstimate(p.client.ProviderModelSlug, item)
		} else if modelcontext.IsTextDocumentMediaType(item.MediaType) {
			representation = modelcontext.MediaRepresentationInlineText
		}
		rendered = append(rendered, modelcontext.RenderedMedia{
			Occurrence:     occurrence.Ref,
			Media:          item,
			Representation: representation,
			TokenEstimate:  tokenEstimate,
		})
	}
	return rendered
}

func anthropicMediaOccurrences(bundle modelcontext.Bundle) []modelcontext.ResolvedMediaOccurrence {
	var rendered []modelcontext.ResolvedMediaOccurrence
	for _, occurrence := range modelcontext.ResolvedMediaOccurrences(bundle) {
		if occurrence.MessageRole != modelprotocol.RoleUser && !occurrence.IsToolResult() {
			continue
		}
		media := occurrence.Media
		if media.Kind != modelcontext.AttachmentKindImage && media.MediaType != "application/pdf" &&
			!modelcontext.IsTextDocumentMediaType(media.MediaType) {
			continue
		}
		rendered = append(rendered, occurrence)
	}
	return rendered
}

type mediaBlock struct {
	Type   string      `json:"type"`
	Source mediaSource `json:"source"`
}

type mediaSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

func renderableMedia(media map[string]modelcontext.ResolvedMedia) map[string]modelcontext.ResolvedMedia {
	out := make(map[string]modelcontext.ResolvedMedia, len(media))
	for id, resolved := range media {
		if resolved.Kind == modelcontext.AttachmentKindImage || resolved.MediaType == "application/pdf" ||
			modelcontext.IsTextDocumentMediaType(resolved.MediaType) {
			out[id] = resolved
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func anthropicMediaBlock(resolved modelcontext.ResolvedMedia) (mediaBlock, bool) {
	switch {
	case resolved.Kind == modelcontext.AttachmentKindImage:
		return mediaBlock{
			Type: "image",
			Source: mediaSource{
				Type:      "base64",
				MediaType: resolved.MediaType,
				Data:      base64.StdEncoding.EncodeToString(resolved.Data),
			},
		}, true
	case resolved.MediaType == "application/pdf":
		return mediaBlock{
			Type: "document",
			Source: mediaSource{
				Type:      "base64",
				MediaType: resolved.MediaType,
				Data:      base64.StdEncoding.EncodeToString(resolved.Data),
			},
		}, true
	case modelcontext.IsTextDocumentMediaType(resolved.MediaType):
		return mediaBlock{
			Type:   "document",
			Source: mediaSource{Type: "text", MediaType: "text/plain", Data: string(resolved.Data)},
		}, true
	default:
		return mediaBlock{}, false
	}
}
