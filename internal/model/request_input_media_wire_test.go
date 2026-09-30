package model_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
