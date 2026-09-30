package model_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/stretchr/testify/require"
)

func TestPreparedAdaptersChargeReadFileImageAlongsideMeasuredInput(t *testing.T) {
	const (
		artifactID = "019b18be-0000-7000-8000-00000000a035"
		callID     = "read-image-call"
		contextID  = "read-image-context"
		measured   = 1_200
	)
	var imageData bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	require.NoError(t, encoder.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 256, 256))))
	encodedImage := base64.StdEncoding.EncodeToString(imageData.Bytes())
	resultContent, err := json.Marshal([]any{
		map[string]any{"type": "structured_data", "value": map[string]any{
			"path": "/tmp/screenshot.png", "content_type": "image/png", "size_bytes": imageData.Len(),
		}},
		map[string]string{"type": "media_ref", "artifact_id": artifactID},
	})
	require.NoError(t, err)
	caps := model.Capabilities{
		ContextWindowTokens: 200_000, DefaultMaxOutputTokens: 1_024,
		InputModalities: []string{modelcontext.InputModalityText, modelcontext.InputModalityImage},
	}
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps, inputIdentityScope: "read-image-route"}) {
		t.Run(tc.name, func(t *testing.T) {
			openingID := uuid.New()
			input := model.PrepareForSendInput{
				Policy: model.RequestPolicy{MaxOutputTokens: 1_024, CacheRetention: model.CacheRetentionNone},
				Context: modelcontext.Bundle{
					SystemPrompt:    strings.Repeat("large retained instructions ", 6_000),
					OpeningInputIDs: []uuid.UUID{openingID},
					Messages: []modelcontext.Message{{
						ID: "current-request", AgentInputID: openingID.String(), Role: modelprotocol.RoleUser, Sequence: 1,
						Content: json.RawMessage(`[{"type":"text","text":"Inspect the screenshot and report its contents."}]`),
					}},
					ToolSpecs: []modelcontext.ToolSpec{{
						Name: "read_file", Description: "Read a file or image.",
						InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
					}},
				},
			}
			first, err := model.PrepareForSend(t.Context(), tc.client, input)
			require.NoError(t, err)
			require.NotNil(t, first.RequestInputIdentity)
			originalRequest := bytes.Clone(input.Context.Messages[0].Content)
			input.Context.Messages = append(input.Context.Messages, modelcontext.Message{
				ID: "read-image-output", ModelCallContextID: contextID, Role: modelprotocol.RoleAssistant, Sequence: 2,
				Content:              json.RawMessage(`[{"type":"tool_call","tool_call_id":"` + callID + `"}]`),
				RequestInputIdentity: first.RequestInputIdentity, ServedProviderModelSlug: "test-model",
				Usage: modelenvelope.Usage{InputTokens: measured, OutputTokens: 30}, StopReason: modelenvelope.StopReasonToolUse,
			})
			input.Context.ToolResults = []modelcontext.ToolResultRef{{
				ToolCallID: callID, ModelCallContextID: contextID, ProviderCallID: callID, Name: "read_file",
				SourceEventSequence: 2, ResultEventSequence: 3, Outcome: "succeeded",
				Input: json.RawMessage(`{"path":"/tmp/screenshot.png"}`), ContentParts: bytes.Clone(resultContent),
			}}
			input.Context.ResolvedMedia = map[string]modelcontext.ResolvedMedia{
				artifactID: {
					ArtifactID: artifactID, Kind: modelcontext.AttachmentKindImage, MediaType: "image/png",
					Filename: "screenshot.png", SizeBytes: int64(imageData.Len()), Data: imageData.Bytes(),
				},
			}
			input.Context.RenderedMedia = model.MediaProjectorForClient(tc.client).ProjectRenderedMedia(input.Context)
			next, err := model.PrepareForSend(t.Context(), tc.client, input)
			require.NoError(t, err)
			require.True(t, next.HasMeasuredInputPrefix)
			require.NotNil(t, next.RequestInputIdentity)
			require.Len(t, next.RenderedMedia, 1)
			imageTokens := next.RenderedMedia[0].TokenEstimate
			require.Positive(t, imageTokens)
			require.Greater(t, first.InputTokenEstimate, measured+imageTokens+1_000)
			require.GreaterOrEqual(t, next.InputTokenEstimate, measured+imageTokens)
			require.Less(t, next.InputTokenEstimate, measured+imageTokens+500)
			require.True(t, next.InputBudget.Fits())
			require.Equal(t, 1, strings.Count(string(next.Body), encodedImage))
			require.Contains(t, string(next.Body), "Inspect the screenshot and report its contents.")
			require.Contains(t, string(next.Body), "/tmp/screenshot.png")
			require.Equal(t, originalRequest, []byte(input.Context.Messages[0].Content))
			require.Equal(t, resultContent, []byte(input.Context.ToolResults[0].ContentParts))
		})
	}
}

func TestAnthropicBodyLimitInvalidatesMeasuredPrefixAndIdentifiesFinalProjection(t *testing.T) {
	const imageBytes = 6_100_000
	require.LessOrEqual(t, int64(4*imageBytes), modelcontext.MaxResolvedMediaBytes)
	ids := []string{
		"019b18be-0000-7000-8000-00000000a031",
		"019b18be-0000-7000-8000-00000000a032",
		"019b18be-0000-7000-8000-00000000a033",
		"019b18be-0000-7000-8000-00000000a034",
	}
	media := make(map[string]modelcontext.ResolvedMedia, len(ids))
	for i, id := range ids {
		media[id] = modelcontext.ResolvedMedia{
			ArtifactID: id, Kind: modelcontext.AttachmentKindImage, MediaType: "image/png",
			SizeBytes: imageBytes, Data: bytes.Repeat([]byte{byte('a' + i)}, imageBytes),
		}
	}
	mediaParts := func(artifactIDs []string) json.RawMessage {
		parts := make([]map[string]string, 0, len(artifactIDs))
		for _, id := range artifactIDs {
			parts = append(parts, map[string]string{"type": "media_ref", "artifact_id": id})
		}
		raw, err := json.Marshal(parts)
		require.NoError(t, err)
		return raw
	}
	caps := model.Capabilities{ContextWindowTokens: 200000, DefaultMaxOutputTokens: 1024}
	client := anthropicmessages.Client{
		InputIdentityScope: "media-route", EndpointPath: "/messages", BaseURL: "https://example.test",
		ProviderModelSlug: "test-model", ModelCapabilities: caps,
	}
	input := model.PrepareForSendInput{
		Policy: model.RequestPolicy{MaxOutputTokens: 1024, CacheRetention: model.CacheRetentionNone},
		Context: modelcontext.Bundle{
			ResolvedMedia: media,
			Messages: []modelcontext.Message{{
				ID: "historical-images", Role: modelprotocol.RoleUser, Sequence: 1, Content: mediaParts(ids[:3]),
			}},
		},
	}
	first, err := model.PrepareForSend(t.Context(), client, input)
	require.NoError(t, err)
	require.NotNil(t, first.RequestInputIdentity)
	require.Len(t, first.RenderedMedia, 3)
	historicalContent := json.RawMessage(bytes.Clone(input.Context.Messages[0].Content))
	currentInputID := uuid.New()
	input.Context.OpeningInputIDs = []uuid.UUID{currentInputID}
	input.Context.Messages = append(input.Context.Messages,
		modelcontext.Message{
			ID: "first-answer", Role: modelprotocol.RoleAssistant, Sequence: 2,
			Content:              json.RawMessage(`[{"type":"text","text":"first answer"}]`),
			RequestInputIdentity: first.RequestInputIdentity, ServedProviderModelSlug: "test-model",
			Usage: modelenvelope.Usage{InputTokens: 1000, OutputTokens: 10}, StopReason: modelenvelope.StopReasonEndTurn,
		},
		modelcontext.Message{
			ID: "current-image", AgentInputID: currentInputID.String(), Role: modelprotocol.RoleUser,
			Sequence: 3, Content: mediaParts(ids[3:]),
		},
	)
	changed, err := model.PrepareForSend(t.Context(), client, input)
	require.NoError(t, err)
	require.False(t, changed.HasMeasuredInputPrefix)
	require.NotNil(t, changed.RequestInputIdentity)
	require.Len(t, changed.RenderedMedia, 3)
	for _, occurrence := range changed.RenderedMedia {
		require.NotEqual(t, ids[0], occurrence.Media.ArtifactID)
	}
	require.Equal(t, ids[3], changed.RenderedMedia[2].Media.ArtifactID)
	require.Equal(t, historicalContent, input.Context.Messages[0].Content)
	require.Less(t, len(changed.Body), 32_000_000)

	var body struct {
		Messages []json.RawMessage `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(changed.Body, &body))
	require.Equal(t, len(body.Messages), changed.RequestInputIdentity.ItemCount)
	serializedInput, err := json.Marshal(body.Messages)
	require.NoError(t, err)
	canonicalInput, err := jsoncanonical.Normalize(serializedInput)
	require.NoError(t, err)
	digest := sha256.Sum256(canonicalInput)
	require.Equal(t, hex.EncodeToString(digest[:]), changed.RequestInputIdentity.PrefixFingerprint)

	nextInputID := uuid.New()
	input.Context.OpeningInputIDs = []uuid.UUID{nextInputID}
	input.Context.Messages = append(input.Context.Messages,
		modelcontext.Message{
			ID: "second-answer", Role: modelprotocol.RoleAssistant, Sequence: 4,
			Content:              json.RawMessage(`[{"type":"text","text":"second answer"}]`),
			RequestInputIdentity: changed.RequestInputIdentity, ServedProviderModelSlug: "test-model",
			Usage: modelenvelope.Usage{InputTokens: 6000, OutputTokens: 10}, StopReason: modelenvelope.StopReasonEndTurn,
		},
		modelcontext.Message{
			ID: "next-question", AgentInputID: nextInputID.String(), Role: modelprotocol.RoleUser, Sequence: 5,
			Content: json.RawMessage(`[{"type":"text","text":"continue"}]`),
		},
	)
	next, err := model.PrepareForSend(t.Context(), client, input)
	require.NoError(t, err)
	require.True(t, next.HasMeasuredInputPrefix)
	require.InDelta(t, 6000, next.InputTokenEstimate, 200)
	require.Equal(t, historicalContent, input.Context.Messages[0].Content)
}
