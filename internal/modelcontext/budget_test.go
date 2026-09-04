package modelcontext

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func budgetFixtureJSON(t testing.TB, value any) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal budget fixture JSON: %v", err)
	}
	return body
}

func TestEstimatePreparedRequestCountsProtocolFramingWithoutChargingBase64AsText(t *testing.T) {
	data := bytes.Repeat([]byte("image-bytes"), 8_192)
	encoded := base64.StdEncoding.EncodeToString(data)
	body := budgetFixtureJSON(t, map[string]any{
		"model": "test-model",
		"input": []map[string]string{
			{"type": "input_image", "image_url": "data:image/png;base64," + encoded},
			{"type": "input_image", "image_url": "data:image/png;base64," + encoded},
		},
		"tools": []map[string]any{{
			"name": "read_file",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]string{"type": "string"}},
			},
		}},
	})
	media := ResolvedMedia{
		Kind:      AttachmentKindImage,
		MediaType: "image/png",
		SizeBytes: int64(len(data)),
		Data:      data,
	}
	estimate := EstimatePreparedRequest(body, []RenderedMedia{
		{Media: media, Representation: MediaRepresentationInline},
		{Media: media, Representation: MediaRepresentationInline},
	})
	if estimate < 2*DefaultImageTokenEstimate {
		t.Fatalf("prepared request estimate = %d, want both image occurrences charged", estimate)
	}
	if estimate >= len(body)/4 {
		t.Fatalf("prepared request estimate = %d, raw base64 byte estimate = %d", estimate, len(body)/4)
	}
}

func TestPreparedRequestBudgetRemovesOnlyStructuredMediaFields(t *testing.T) {
	data := bytes.Repeat([]byte("same-as-user-text"), 512)
	encoded := base64.StdEncoding.EncodeToString(data)
	body := budgetFixtureJSON(t, map[string]any{
		"input": []map[string]any{
			{"type": "input_text", "text": encoded},
			{"type": "input_image", "image_url": "data:image/png;base64," + encoded},
		},
	})
	media := []RenderedMedia{{
		Media: ResolvedMedia{
			Kind:      AttachmentKindImage,
			MediaType: "image/png",
			SizeBytes: int64(len(data)),
			Data:      data,
		},
		Representation: MediaRepresentationInline,
	}}
	projected := preparedRequestWithoutInlineMedia(body, media)
	if !bytes.Contains(projected, []byte(encoded)) {
		t.Fatalf("ordinary user text matching media base64 was removed: %s", projected)
	}
	if bytes.Contains(projected, []byte("data:image/png;base64,"+encoded)) {
		t.Fatalf("structured inline media payload remains in estimate projection: %s", projected)
	}
	if estimate := EstimatePreparedRequest(body, media); estimate <= len(encoded)/4 {
		t.Fatalf("estimate = %d, want matching user text to remain charged", estimate)
	}
}

func TestPreparedRequestBudgetRemovesChatFileData(t *testing.T) {
	data := bytes.Repeat([]byte("pdf-bytes"), 8_192)
	encoded := base64.StdEncoding.EncodeToString(data)
	body := budgetFixtureJSON(t, map[string]any{
		"messages": []map[string]any{{
			"content": []map[string]any{{
				"type": "file",
				"file": map[string]string{
					"filename":  "report.pdf",
					"file_data": "data:application/pdf;base64," + encoded,
				},
			}},
		}},
	})
	media := []RenderedMedia{{
		Media: ResolvedMedia{
			Kind:      AttachmentKindDocument,
			MediaType: "application/pdf",
			Data:      data,
		},
		Representation: MediaRepresentationInline,
	}}
	if estimate := EstimatePreparedRequest(body, media); estimate >= len(body)/4 {
		t.Fatalf("estimate = %d, raw base64 byte estimate = %d", estimate, len(body)/4)
	}
}

func TestEstimatePreparedRequestDoesNotInferAbsentMedia(t *testing.T) {
	body := budgetFixtureJSON(t, map[string]any{"input": "small textual fallback"})
	withNoRenderedMedia := EstimatePreparedRequest(body, nil)
	if want := ceilDiv(len(body), 4); withNoRenderedMedia != want {
		t.Fatalf("estimate = %d, want body-only estimate %d", withNoRenderedMedia, want)
	}
}

func TestEstimateSerializedTextTokensAccountsForDenseScripts(t *testing.T) {
	if got := estimateSerializedTextTokens([]byte("abcd你好世界")); got != 5 {
		t.Fatalf("mixed text estimate = %d, want 5", got)
	}
}

func TestEstimateInputFromProviderUsageAddsOnlyTheNewTail(t *testing.T) {
	identity := testModelRequestIdentity()
	bundle := Bundle{
		InputEventSequence: 7,
		Messages: []Message{
			{
				Role:     modelprotocol.RoleAssistant,
				Sequence: 4,
				ProviderUsageAnchor: &ProviderUsageAnchor{
					Identity:           identity,
					InputEventSequence: 3,
					InputTokens:        1_000,
					OutputTokens:       200,
				},
			},
			{
				Role:     modelprotocol.RoleUser,
				Sequence: 5,
				Content:  json.RawMessage(`[{"type":"text","text":"continue with new work"}]`),
			},
		},
	}

	estimate, ok := EstimateInputFromProviderUsage(bundle, identity, 0)
	if !ok || estimate.ProviderInputTokens != 1_000 || estimate.ProviderOutputTokens != 200 ||
		estimate.NewTailTokens <= 0 ||
		estimate.EstimatedInputTokens != 1_200+estimate.NewTailTokens {
		t.Fatalf("provider usage estimate = %+v, available=%t", estimate, ok)
	}
}

func TestEstimateInputFromProviderUsageRejectsIncompatibleHistory(t *testing.T) {
	identity := testModelRequestIdentity()
	newBundle := func() Bundle {
		return Bundle{
			InputEventSequence: 7,
			Messages: []Message{{
				Role:     modelprotocol.RoleAssistant,
				Sequence: 5,
				ProviderUsageAnchor: &ProviderUsageAnchor{
					Identity:           identity,
					InputEventSequence: 4,
					InputTokens:        1_000,
					OutputTokens:       100,
				},
			}},
		}
	}

	tests := map[string]func(*Bundle, *ModelRequestIdentity, *int64){
		"config changed": func(_ *Bundle, target *ModelRequestIdentity, _ *int64) {
			target.AgentConfigID = "other-config"
		},
		"model revision changed": func(_ *Bundle, target *ModelRequestIdentity, _ *int64) {
			target.ConfiguredModelRevisionID = "other-revision"
		},
		"provider config changed": func(_ *Bundle, target *ModelRequestIdentity, _ *int64) {
			target.ProviderRequestIdentity.ModelProviderConfigID = "other-provider-config"
		},
		"checkpoint replaced anchor": func(bundle *Bundle, _ *ModelRequestIdentity, _ *int64) {
			bundle.ContextCheckpoint = &CheckpointRef{EventSequence: 6}
		},
		"replay rejected after anchor": func(_ *Bundle, _ *ModelRequestIdentity, cutoff *int64) {
			*cutoff = 5
		},
		"newer output has no usage": func(bundle *Bundle, _ *ModelRequestIdentity, _ *int64) {
			bundle.Messages = append(bundle.Messages, Message{Role: modelprotocol.RoleAssistant, Sequence: 6})
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			bundle := newBundle()
			target := identity
			var cutoff int64
			mutate(&bundle, &target, &cutoff)
			if _, ok := EstimateInputFromProviderUsage(bundle, target, cutoff); ok {
				t.Fatal("incompatible provider measurement was used")
			}
		})
	}

	bundle := newBundle()
	bundle.ContextCheckpoint = &CheckpointRef{EventSequence: 4}
	if _, ok := EstimateInputFromProviderUsage(bundle, identity, 4); !ok {
		t.Fatal("measurement made at the checkpoint and replay frontier was rejected")
	}
}

func testModelRequestIdentity() ModelRequestIdentity {
	return ModelRequestIdentity{
		AgentConfigID:             "config",
		ConfiguredModelRevisionID: "revision",
		ProviderRequestIdentity: modelenvelope.ProviderReplayIdentity{
			ModelProviderConfigID:      "provider-config",
			RequestedProviderModelSlug: "model",
			APIFormat:                  modelprotocol.APIFormatOpenAIResponses,
			APIVariant:                 modelprotocol.APIVariantDefault,
		},
	}
}

func TestModelWindowComputesExactUsableInputBoundary(t *testing.T) {
	window := ModelWindow{
		ContextTokens:          10_000,
		RequestMaxOutputTokens: 2_000,
		SafetyMarginTokens:     1_000,
	}
	if got := window.UsableInputTokens(); got != 7_000 {
		t.Fatalf("usable input = %d, want 7000", got)
	}
}
