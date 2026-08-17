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

func TestEstimatePreparedRequestDoesNotInferAbsentMedia(t *testing.T) {
	body := budgetFixtureJSON(t, map[string]any{"input": "small textual fallback"})
	withNoRenderedMedia := EstimatePreparedRequest(body, nil)
	if want := len(body)/4 + 1; withNoRenderedMedia != want {
		t.Fatalf("estimate = %d, want body-only estimate %d", withNoRenderedMedia, want)
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

func TestProviderUsageInputFloorUsesLatestCompatibleCallAndVisibleSuffix(t *testing.T) {
	identity := ModelRequestIdentity{
		AgentConfigID:             "config-1",
		ConfiguredModelRevisionID: "revision-1",
		RequestedModelSlug:        "model-1",
		APIFormat:                 modelprotocol.APIFormatOpenAIResponses,
		APIVariant:                modelprotocol.APIVariantDefault,
	}
	bundle := Bundle{
		Messages: []Message{
			{Sequence: 1, Role: modelprotocol.RoleUser, Content: json.RawMessage(`[{"type":"text","text":"old"}]`)},
			{
				Sequence: 2,
				Role:     modelprotocol.RoleAssistant,
				Content:  json.RawMessage(`[{"type":"text","text":"older"}]`),
				UsageAnchor: &ProviderUsageAnchor{
					Identity: identity,
					Usage:    modelenvelope.Usage{InputTokens: 90_000, OutputTokens: 2_000},
				},
			},
			{
				Sequence: 4,
				Role:     modelprotocol.RoleAssistant,
				Content:  json.RawMessage(`[{"type":"text","text":"latest"}]`),
				UsageAnchor: &ProviderUsageAnchor{
					Identity: identity,
					Usage:    modelenvelope.Usage{InputTokens: 1_000, OutputTokens: 100},
				},
			},
			{Sequence: 5, Role: modelprotocol.RoleUser, Content: json.RawMessage(`[{"type":"text","text":"Unicode 你好 and code: map[string]any{}"}]`)},
		},
		ToolResults: []ToolResultRef{
			{
				Name:                "old_result",
				ResultEventSequence: 4,
				ContentParts:        json.RawMessage(`[{"type":"text","text":"already counted"}]`),
			},
			{
				Name:                "read_file",
				ResultEventSequence: 6,
				ContentParts:        json.RawMessage(`[{"type":"text","text":"{\"ok\":true}"}]`),
			},
		},
		RenderedMedia: []RenderedMedia{
			{
				Occurrence:     MediaOccurrenceRef{ownerKind: mediaOccurrenceOwnerMessage, ownerIndex: 2},
				Media:          ResolvedMedia{Kind: AttachmentKindImage},
				Representation: MediaRepresentationInline,
				TokenEstimate:  701,
			},
			{
				Occurrence:     MediaOccurrenceRef{ownerKind: mediaOccurrenceOwnerMessage, ownerIndex: 3},
				Media:          ResolvedMedia{Kind: AttachmentKindImage},
				Representation: MediaRepresentationInline,
				TokenEstimate:  123,
			},
			{
				Occurrence:     MediaOccurrenceRef{ownerKind: mediaOccurrenceOwnerToolResult, ownerIndex: 0},
				Media:          ResolvedMedia{Kind: AttachmentKindImage},
				Representation: MediaRepresentationInline,
				TokenEstimate:  503,
			},
			{
				Occurrence:     MediaOccurrenceRef{ownerKind: mediaOccurrenceOwnerToolResult, ownerIndex: 1},
				Media:          ResolvedMedia{Kind: AttachmentKindImage},
				Representation: MediaRepresentationInline,
				TokenEstimate:  77,
			},
		},
	}
	const projectedSuffix = `{"messages":[{"role":"user","content":` +
		`[{"type":"text","text":"Unicode 你好 and code: map[string]any{}"}]` +
		`}],"tool_results":[{"name":"read_file","content_parts":` +
		`[{"type":"text","text":"{\"ok\":true}"}]` +
		`}]}`
	want := 1_100 + len(projectedSuffix)/4 + 1 + 123 + 77
	got, ok := ProviderUsageInputFloor(bundle, identity, false)
	if !ok || got != want {
		t.Fatalf("provider usage floor = %d ok=%v, want latest-call floor %d", got, ok, want)
	}
}

func TestProviderUsageInputFloorDoesNotFallBackPastLatestUnusableAnchor(t *testing.T) {
	identity := ModelRequestIdentity{
		AgentConfigID:             "config-1",
		ConfiguredModelRevisionID: "revision-1",
		RequestedModelSlug:        "model-1",
		APIFormat:                 modelprotocol.APIFormatOpenAIResponses,
		APIVariant:                modelprotocol.APIVariantDefault,
	}
	incompatibleIdentity := identity
	incompatibleIdentity.AgentConfigID = "config-2"
	tests := []struct {
		name   string
		latest ProviderUsageAnchor
	}{
		{
			name: "malformed",
			latest: ProviderUsageAnchor{
				Identity: identity,
			},
		},
		{
			name: "incompatible",
			latest: ProviderUsageAnchor{
				Identity: incompatibleIdentity,
				Usage:    modelenvelope.Usage{InputTokens: 2_000, OutputTokens: 200},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundle := Bundle{Messages: []Message{
				{
					Sequence: 1,
					UsageAnchor: &ProviderUsageAnchor{
						Identity: identity,
						Usage:    modelenvelope.Usage{InputTokens: 50_000, OutputTokens: 1_000},
					},
				},
				{Sequence: 2, UsageAnchor: &test.latest},
			}}
			if got, ok := ProviderUsageInputFloor(bundle, identity, false); ok || got != 0 {
				t.Fatalf("provider usage floor = %d ok=%v, want latest %s anchor to be a barrier", got, ok, test.name)
			}
		})
	}
}

func TestProviderUsageInputFloorRejectsIncompatibleLineage(t *testing.T) {
	identity := ModelRequestIdentity{
		AgentConfigID:             "config-1",
		ConfiguredModelRevisionID: "revision-1",
		RequestedModelSlug:        "model-1",
		APIFormat:                 modelprotocol.APIFormatOpenAIResponses,
		APIVariant:                modelprotocol.APIVariantDefault,
	}
	base := Bundle{Messages: []Message{{
		Sequence: 10,
		UsageAnchor: &ProviderUsageAnchor{
			Identity: identity,
			Usage:    modelenvelope.Usage{InputTokens: 1_000, OutputTokens: 100},
		},
	}}}
	tests := []struct {
		name     string
		bundle   Bundle
		target   ModelRequestIdentity
		suppress bool
	}{
		{name: "suppressed replay", bundle: base, target: identity, suppress: true},
		{name: "missing current identity", bundle: base},
		{name: "changed agent config", bundle: base, target: func() ModelRequestIdentity {
			changed := identity
			changed.AgentConfigID = "config-2"
			return changed
		}()},
		{name: "changed model revision", bundle: base, target: func() ModelRequestIdentity {
			changed := identity
			changed.ConfiguredModelRevisionID = "revision-2"
			return changed
		}()},
		{name: "changed requested slug", bundle: base, target: func() ModelRequestIdentity {
			changed := identity
			changed.RequestedModelSlug = "model-2"
			return changed
		}()},
		{name: "changed API format", bundle: base, target: func() ModelRequestIdentity {
			changed := identity
			changed.APIFormat = modelprotocol.APIFormatOpenAIChatCompletions
			return changed
		}()},
		{name: "changed API variant", bundle: base, target: func() ModelRequestIdentity {
			changed := identity
			changed.APIVariant = modelprotocol.APIVariantOpenRouter
			return changed
		}()},
		{name: "candidate checkpoint", bundle: func() Bundle {
			changed := base
			changed.ContextCheckpoint = &CheckpointRef{ID: "candidate", SummarizedThroughEventSequence: 5, Summary: "summary"}
			return changed
		}(), target: identity},
		{name: "anchor predates durable checkpoint", bundle: func() Bundle {
			changed := base
			changed.ContextCheckpoint = &CheckpointRef{
				ID:                             "checkpoint",
				PublishedEventSequence:         11,
				SummarizedThroughEventSequence: 5,
				Summary:                        "summary",
			}
			return changed
		}(), target: identity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, ok := ProviderUsageInputFloor(test.bundle, test.target, test.suppress); ok || got != 0 {
				t.Fatalf("provider usage floor = %d ok=%v, want unavailable", got, ok)
			}
		})
	}

	withEarlierCheckpoint := base
	withEarlierCheckpoint.ContextCheckpoint = &CheckpointRef{
		ID:                             "checkpoint",
		PublishedEventSequence:         9,
		SummarizedThroughEventSequence: 5,
		Summary:                        "summary",
	}
	if got, ok := ProviderUsageInputFloor(withEarlierCheckpoint, identity, false); !ok || got <= 1_100 {
		t.Fatalf("post-checkpoint provider usage floor = %d ok=%v", got, ok)
	}
}
