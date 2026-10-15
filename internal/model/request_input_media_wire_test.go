package model_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
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
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps}) {
		t.Run(tc.name, func(t *testing.T) {
			openingID := uuid.New()
			input := model.PrepareForSendInput{
				Policy: model.RequestPolicy{MaxOutputTokens: 1_024, CacheRetention: model.CacheRetentionNone},
				Context: modelcontext.Bundle{
					AgentConfigID: uuid.New(), ConfiguredModelRevisionID: uuid.New(), InputEventSequence: 1,
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
			originalRequest := bytes.Clone(input.Context.Messages[0].Content)
			input.Context.InputEventSequence = 3
			input.Context.Messages = append(input.Context.Messages, modelcontext.Message{
				InputFingerprint: first.InputFingerprint,
				ID:               "read-image-output", ModelCallContextID: contextID,
				Role: modelprotocol.RoleAssistant, Sequence: 2,
				InputEventSequence: 1, AgentConfigID: input.Context.AgentConfigID,
				ConfiguredModelRevisionID: input.Context.ConfiguredModelRevisionID,
				ProviderReplaySource:      model.ProviderReplayIdentityForClient("", tc.client),
				Content:                   json.RawMessage(`[{"type":"tool_call","tool_call_id":"` + callID + `"}]`),
				Usage:                     modelenvelope.Usage{InputTokens: measured, OutputTokens: 30},
				StopReason:                modelenvelope.StopReasonToolUse,
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
			require.True(t, next.UsesProviderInputUsage)
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
