package openaichatcompletions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

const mediaTestAudioID = "019b18be-0000-7000-8000-00000000c0a1"

var audioTestCapabilities = model.Capabilities{InputModalities: []string{"text", "audio"}}

func audioTestBundle(mediaType string) modelcontext.Bundle {
	return modelcontext.Bundle{
		Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser, Content: json.RawMessage(`[
			{"type":"text","text":"what key is this in?"},
			{"type":"media_ref","artifact_id":"` + mediaTestAudioID + `"}
		]`)}},
		ResolvedMedia: map[string]modelcontext.ResolvedMedia{mediaTestAudioID: {
			ArtifactID: mediaTestAudioID,
			Kind:       modelcontext.AttachmentKindAudio,
			MediaType:  mediaType,
			Filename:   "take.audio",
			SizeBytes:  1024 * 1024,
			Data:       []byte("audio bytes"),
		}},
	}
}

func TestPrepareBuildsInputAudioParts(t *testing.T) {
	for mediaType, format := range map[string]string{
		"audio/wav":  "wav",
		"audio/mpeg": "mp3",
		"audio/mp4":  "m4a",
		"audio/flac": "flac",
	} {
		client := Client{
			EndpointPath:      testEndpointPath,
			ProviderModelSlug: "google/gemini-test",
			APIVariant:        modelprotocol.APIVariantOpenRouter,
			ModelCapabilities: audioTestCapabilities,
		}
		bundle := audioTestBundle(mediaType)
		if rendered := client.ProjectRenderedMedia(bundle); len(rendered) != 1 {
			t.Fatalf("project %s: rendered = %+v", mediaType, rendered)
		}
		prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: bundle})
		if err != nil {
			t.Fatalf("prepare %s: %v", mediaType, err)
		}
		var payload struct {
			Messages []struct {
				Content []struct {
					Type       string `json:"type"`
					Text       string `json:"text"`
					InputAudio struct {
						Data   string `json:"data"`
						Format string `json:"format"`
					} `json:"input_audio"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(prepared.Body, &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if len(payload.Messages) != 1 || len(payload.Messages[0].Content) != 3 {
			t.Fatalf("unexpected messages for %s: %s", mediaType, prepared.Body)
		}
		content := payload.Messages[0].Content
		if content[1].Text != "\n\nartifact_id: "+mediaTestPublicID(mediaTestAudioID)+"\n\n" ||
			content[2].Type != "input_audio" || content[2].InputAudio.Format != format ||
			content[2].InputAudio.Data != "YXVkaW8gYnl0ZXM=" {
			t.Fatalf("unexpected audio part for %s: %s", mediaType, prepared.Body)
		}
	}
}

func TestProjectRenderedMediaGatesAudioOnInputModalities(t *testing.T) {
	bundle := audioTestBundle("audio/mpeg")
	for _, tc := range []struct {
		name       string
		modalities []string
		want       bool
	}{
		{"unspecified", nil, false},
		{"audio", []string{"text", "audio"}, true},
		{"no audio", []string{"text", "image", "file"}, false},
	} {
		client := Client{
			ProviderModelSlug: "google/gemini-test",
			APIVariant:        modelprotocol.APIVariantOpenRouter,
			ModelCapabilities: model.Capabilities{InputModalities: tc.modalities},
		}
		rendered := client.ProjectRenderedMedia(bundle)
		if (len(rendered) == 1) != tc.want {
			t.Fatalf("%s: rendered = %+v, want rendered=%t", tc.name, rendered, tc.want)
		}
		if !tc.want {
			continue
		}
		if rendered[0].InputModality() != modelcontext.InputModalityAudio ||
			rendered[0].TokenEstimate != 2048 {
			t.Fatalf("%s: unexpected audio projection: %+v", tc.name, rendered[0])
		}
	}
}

func TestProjectRenderedMediaGatesAudioFormatOnAPIVariant(t *testing.T) {
	for _, variant := range []modelprotocol.APIVariant{
		modelprotocol.APIVariantDefault,
		modelprotocol.APIVariantBedrock,
		modelprotocol.APIVariantOpenRouter,
	} {
		for mediaType, openRouterOnly := range map[string]bool{
			"audio/wav":  false,
			"audio/mpeg": false,
			"audio/mp4":  true,
			"audio/flac": true,
		} {
			client := Client{
				ProviderModelSlug: "gpt-audio-test",
				APIVariant:        variant,
				EndpointPath:      testEndpointPath,
				ModelCapabilities: audioTestCapabilities,
			}
			want := !openRouterOnly || variant == modelprotocol.APIVariantOpenRouter
			bundle := audioTestBundle(mediaType)
			rendered := client.ProjectRenderedMedia(bundle)
			if (len(rendered) == 1) != want {
				t.Fatalf("%s %s: rendered = %+v, want rendered=%t", variant, mediaType, rendered, want)
			}
			// The request must apply the same gate even when the bytes are loaded.
			prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: bundle})
			if err != nil {
				t.Fatalf("%s %s: prepare: %v", variant, mediaType, err)
			}
			if strings.Contains(string(prepared.Body), "input_audio") != want {
				t.Fatalf("%s %s: body = %s, want input_audio=%t", variant, mediaType, prepared.Body, want)
			}
		}
	}
}

func TestProjectRenderedMediaOmitsToolResultAudio(t *testing.T) {
	bundle := audioTestBundle("audio/wav")
	bundle.Messages = nil
	bundle.ToolResults = []modelcontext.ToolResultRef{{
		Name:                "record",
		SourceEventSequence: 2,
		ContentParts: json.RawMessage(
			`[{"type":"media_ref","artifact_id":"` + mediaTestAudioID + `"}]`,
		),
	}}
	client := Client{ProviderModelSlug: "google/gemini-test", ModelCapabilities: audioTestCapabilities}
	if rendered := client.ProjectRenderedMedia(bundle); len(rendered) != 0 {
		t.Fatalf("rendered = %+v, want no tool-result audio", rendered)
	}
}

func TestPrepareOmitsLoadedAudioWithoutDeclaredAudioModality(t *testing.T) {
	client := Client{
		EndpointPath:      testEndpointPath,
		ProviderModelSlug: "byo-model",
		APIVariant:        modelprotocol.APIVariantOpenRouter,
	}
	prepared, err := client.Prepare(
		context.Background(),
		model.PrepareInput{Context: audioTestBundle("audio/mpeg")},
	)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if strings.Contains(string(prepared.Body), "input_audio") {
		t.Fatalf("audio sent without a declared audio modality: %s", prepared.Body)
	}
}

func TestPrepareKeepsUnloadedAudioAsReference(t *testing.T) {
	bundle := audioTestBundle("audio/flac")
	resolved := bundle.ResolvedMedia[mediaTestAudioID]
	resolved.Data = nil
	bundle.ResolvedMedia[mediaTestAudioID] = resolved
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: bundle})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	if strings.Contains(body, "input_audio") ||
		!strings.Contains(body, mediaTestPublicID(mediaTestAudioID)) {
		t.Fatalf("unloaded audio was not preserved as a reference: %s", body)
	}
}
