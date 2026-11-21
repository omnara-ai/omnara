package openairesponses

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
)

func TestAudioStaysAnArtifactReference(t *testing.T) {
	const artifactID = "019b18be-0000-7000-8000-00000000c0a1"
	bundle := modelcontext.Bundle{
		Messages: []modelcontext.Message{{Sequence: 1, Role: "user", Content: json.RawMessage(`[
			{"type":"media_ref","artifact_id":"` + artifactID + `"}
		]`)}},
		ResolvedMedia: map[string]modelcontext.ResolvedMedia{artifactID: {
			ArtifactID: artifactID,
			Kind:       modelcontext.AttachmentKindAudio,
			MediaType:  "audio/mpeg",
			Filename:   "take.mp3",
			Data:       []byte("audio bytes"),
		}},
	}
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	if rendered := client.ProjectRenderedMedia(bundle); len(rendered) != 0 {
		t.Fatalf("rendered = %+v, want no audio", rendered)
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: bundle})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	if strings.Contains(body, "YXVkaW8gYnl0ZXM=") ||
		!strings.Contains(body, mediaTestPublicID(artifactID)) {
		t.Fatalf("audio was not preserved as a reference: %s", body)
	}
}
