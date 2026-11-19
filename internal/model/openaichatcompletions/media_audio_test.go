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
		client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "google/gemini-test"}
		prepared, err := client.Prepare(
			context.Background(),
			model.PrepareInput{Context: audioTestBundle(mediaType)},
		)
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
		if content[1].Text != "artifact_id: "+mediaTestPublicID(mediaTestAudioID) ||
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
		{"unspecified", nil, true},
		{"audio", []string{"text", "audio"}, true},
		{"no audio", []string{"text", "image", "file"}, false},
	} {
		client := Client{
			ProviderModelSlug: "google/gemini-test",
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
	client := Client{ProviderModelSlug: "google/gemini-test"}
	if rendered := client.ProjectRenderedMedia(bundle); len(rendered) != 0 {
		t.Fatalf("rendered = %+v, want no tool-result audio", rendered)
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
