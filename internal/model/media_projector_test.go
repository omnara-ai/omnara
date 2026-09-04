package model_test

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestProviderClientsExposeRuntimeMediaProjection(t *testing.T) {
	const artifactID = "019b18be-0000-7000-8000-00000000c001"
	bundle := modelcontext.Bundle{
		Messages: []modelcontext.Message{{
			Role: modelprotocol.RoleUser,
			Content: json.RawMessage(`[
				{"type":"media_ref","artifact_id":"` + artifactID + `"}
			]`),
		}},
		ResolvedMedia: map[string]modelcontext.ResolvedMedia{
			artifactID: {
				ArtifactID: artifactID,
				Kind:       modelcontext.AttachmentKindImage,
				MediaType:  "image/png",
				SizeBytes:  16,
				Data:       []byte("image"),
			},
		},
	}
	clients := map[string]model.Client{
		"anthropic messages": anthropicmessages.Client{},
		"openai chat":        openaichatcompletions.Client{},
		"openai responses":   openairesponses.Client{},
	}
	for name, client := range clients {
		t.Run(name, func(t *testing.T) {
			projector := model.MediaProjectorForClient(client)
			if projector == nil {
				t.Fatal("outer provider client does not expose its media projector")
			}
			projected := projector.ProjectRenderedMedia(bundle)
			if len(projected) != 1 || projected[0].Media.ArtifactID != artifactID {
				t.Fatalf("projected media = %+v, want artifact %s", projected, artifactID)
			}
		})
	}
}

func TestProviderMediaProjectionMatchesSerializedRepresentations(t *testing.T) {
	const (
		imageID         = "019b18be-0000-7000-8000-00000000c011"
		pdfID           = "019b18be-0000-7000-8000-00000000c012"
		textID          = "019b18be-0000-7000-8000-00000000c013"
		officeID        = "019b18be-0000-7000-8000-00000000c014"
		officeMediaType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	)
	bundle := modelcontext.Bundle{
		Messages: []modelcontext.Message{{
			Role: modelprotocol.RoleUser,
			Content: json.RawMessage(`[
				{"type":"media_ref","artifact_id":"` + imageID + `"},
				{"type":"media_ref","artifact_id":"` + pdfID + `"},
				{"type":"media_ref","artifact_id":"` + textID + `"},
				{"type":"media_ref","artifact_id":"` + officeID + `"}
			]`),
		}},
		ResolvedMedia: map[string]modelcontext.ResolvedMedia{
			imageID: {ArtifactID: imageID, Kind: modelcontext.AttachmentKindImage, MediaType: "image/png"},
			pdfID:   {ArtifactID: pdfID, Kind: modelcontext.AttachmentKindDocument, MediaType: "application/pdf"},
			textID:  {ArtifactID: textID, Kind: modelcontext.AttachmentKindDocument, MediaType: "text/plain", SizeBytes: 16},
			officeID: {
				ArtifactID: officeID,
				Kind:       modelcontext.AttachmentKindDocument,
				MediaType:  officeMediaType,
			},
		},
	}
	tests := []struct {
		name   string
		client model.Client
		want   map[string]string
	}{
		{
			name:   "chat sends only supported documents",
			client: openaichatcompletions.Client{},
			want: map[string]string{
				imageID: modelcontext.MediaRepresentationInline + "/image",
				pdfID:   modelcontext.MediaRepresentationInline + "/file",
				textID:  modelcontext.MediaRepresentationInlineText + "/",
			},
		},
		{
			name: "openrouter chat sends only supported documents",
			client: openaichatcompletions.Client{
				APIVariant: modelprotocol.APIVariantOpenRouter,
			},
			want: map[string]string{
				imageID: modelcontext.MediaRepresentationInline + "/image",
				pdfID:   modelcontext.MediaRepresentationInline + "/",
				textID:  modelcontext.MediaRepresentationInlineText + "/",
			},
		},
		{
			name: "chat omits historical PDF excluded by grant",
			client: openaichatcompletions.Client{
				ModelCapabilities: model.Capabilities{InputModalities: []string{"text", "image"}},
			},
			want: map[string]string{
				imageID: modelcontext.MediaRepresentationInline + "/image",
				textID:  modelcontext.MediaRepresentationInlineText + "/",
			},
		},
		{
			name:   "anthropic sends supported media",
			client: anthropicmessages.Client{},
			want: map[string]string{
				imageID: modelcontext.MediaRepresentationInline + "/image",
				pdfID:   modelcontext.MediaRepresentationInline + "/file",
				textID:  modelcontext.MediaRepresentationInlineText + "/",
			},
		},
		{
			name:   "responses sends images and documents",
			client: openairesponses.Client{},
			want: map[string]string{
				imageID:  modelcontext.MediaRepresentationInline + "/image",
				pdfID:    modelcontext.MediaRepresentationInline + "/file",
				textID:   modelcontext.MediaRepresentationInlineText + "/",
				officeID: modelcontext.MediaRepresentationInline + "/file",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projected := model.MediaProjectorForClient(test.client).ProjectRenderedMedia(bundle)
			got := make(map[string]string, len(projected))
			for _, item := range projected {
				got[item.Media.ArtifactID] = item.Representation + "/" + item.InputModality
			}
			if !maps.Equal(got, test.want) {
				t.Fatalf("projected media = %+v, want %+v", got, test.want)
			}
		})
	}
}
