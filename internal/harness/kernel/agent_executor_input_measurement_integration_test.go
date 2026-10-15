//go:build integration

package kernel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/stretchr/testify/require"
)

func TestAgentExecutorReusesPersistedMeasuredInputForActualAdapterRequest(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgentWithModelOptions(t, ctx, "openai/measured-input", fixture.Now,
		kernelConfiguredModelOptions{ContextWindowTokens: new(10000), MaxOutputTokens: new(9000)})
	var requests []json.RawMessage
	client := openaichatcompletions.Client{
		ProviderModelSlug: "measured-input", APIVariant: modelprotocol.APIVariantOpenRouter,
		BaseURL: "https://provider.test", EndpointPath: "/chat/completions",
		ModelCapabilities: model.Capabilities{ContextWindowTokens: 10000, DefaultMaxOutputTokens: 9000},
		HTTPClient: &http.Client{Transport: kernelSlackRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			requests = append(requests, append(json.RawMessage(nil), body...))
			response := fmt.Sprintf(`{"id":"response_%d","object":"chat.completion","model":"measured-input-2026-10-01",
				"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":1000,"completion_tokens":4000,
				"completion_tokens_details":{"reasoning_tokens":3990}}}`, len(requests))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": {"application/json"}, "X-Request-Id": {fmt.Sprintf("request_%d", len(requests))},
				},
				Body: io.NopCloser(strings.NewReader(response)),
			}, nil
		})},
	}
	now := fixture.Now.Add(2 * time.Second)
	executor := AgentExecutor{
		Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client),
		Now: func() time.Time { return now },
	}
	largeInput := strings.Repeat("closed historical detail ", 2400)
	first := fixture.admitContentInputTurn(t, ctx, agentID, userID, largeInput, fixture.Now.Add(time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, first))
	require.Len(t, requests, 1)
	require.Contains(t, string(requests[0]), largeInput, "uncertain first input must reach the provider intact")
	var measuredTokens int
	var servedModel string
	require.NoError(t, fixture.Pool.QueryRow(ctx, `
		SELECT context.input_tokens_total,output.served_provider_model_slug
		FROM model_call_contexts context JOIN model_outputs output ON output.model_call_context_id=context.id
		WHERE context.agent_id=$1 AND context.state='succeeded' AND context.operation_kind='normal'
	`, agentID).Scan(&measuredTokens, &servedModel))
	require.Equal(t, 1000, measuredTokens)
	require.Equal(t, "measured-input-2026-10-01", servedModel)
	require.NoError(t, fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx, kernelTestProjectID, agentID, first.RuntimeLockID,
	))

	second := fixture.admitContentInputTurn(t, ctx, agentID, userID, "continue", fixture.Now.Add(3*time.Second))
	now = fixture.Now.Add(4 * time.Second)
	executor = AgentExecutor{
		Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client),
		Now: func() time.Time { return now },
	}
	require.NoError(t, executor.ExecuteModelWork(ctx, second))
	require.Len(t, requests, 2, "measured request should go directly to normal generation")
	var compactions, successfulNormal int
	require.NoError(t, fixture.Pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE operation_kind='compaction'),
			count(*) FILTER (WHERE operation_kind='normal' AND state='succeeded')
		FROM model_call_contexts WHERE agent_id=$1
	`, agentID).Scan(&compactions, &successfulNormal))
	require.Zero(t, compactions, "large local estimate and billed hidden output must not override valid measured input")
	require.Equal(t, 2, successfulNormal)
	require.Contains(t, string(requests[1]), largeInput)
	require.Contains(t, string(requests[1]), "continue")
}

func TestAgentExecutorInvalidatesMeasuredInputAfterLiveSkillChange(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	var skills []skillstore.SkillRecord
	yaml := "instruction: Preserve the user's exact historical details.\n" +
		"model:\n  provider_config: openai-prod\n  name: live-static-shrink\nskills:\n"
	for i := range 24 {
		skill, err := fixture.Store.Skills().CreateSkillRevision(ctx, skillstore.CreateSkillInput{
			OrgID: kernelTestOrgID, OwnerKind: skillstore.SkillOwnerProject, OwnerProjectID: kernelTestProjectID,
			Name: fmt.Sprintf("measurement-skill-%02d", i), Description: strings.Repeat("Detailed guidance. ", 50),
			SkillMd: "# Skill\nDetailed instructions.", ArchiveBytes: []byte(fmt.Sprintf("initial archive %d", i)),
			Actor: kernelTestUserPrincipal(kernelTestUserID),
		})
		require.NoError(t, err)
		require.LessOrEqual(t, len(skill.Description), 1024)
		skills = append(skills, skill)
		id, err := publicid.Encode(publicid.KindSkill, skill.ID)
		require.NoError(t, err)
		yaml += "  - " + id + "\n"
	}
	profile := fixture.createConfigAndProfileBookmarkWithModelOptions(
		t, ctx, "Live Static Shrink", "measurement-live-static-shrink", yaml,
		kernelConfiguredModelOptions{ContextWindowTokens: new(10000), MaxOutputTokens: new(1024)})
	launch, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: kernelTestProjectID, ProfileID: profile.ID, AgentConfigID: profile.CurrentConfigID,
		LaunchedBy: kernelTestUserPrincipal(kernelTestUserID), IdempotencyKey: "measurement-static-shrink-launch",
	})
	require.NoError(t, err)
	agentID := launch.Agent.ID
	config := fixture.currentAgentConfig(t, ctx, agentID)
	revisionID := currentRevisionIDForKernelConfig(t, ctx, fixture.Store, config)
	var requests []json.RawMessage
	var observedInput []int
	client := openaichatcompletions.Client{
		ProviderModelSlug: "live-static-shrink", APIVariant: modelprotocol.APIVariantOpenRouter,
		BaseURL: "https://provider.test", EndpointPath: "/chat/completions",
		ModelCapabilities: model.Capabilities{
			ContextWindowTokens: 10000, DefaultMaxOutputTokens: 1024, SupportsTools: new(true),
		},
		HTTPClient: &http.Client{Transport: kernelSlackRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			requests = append(requests, append(json.RawMessage(nil), body...))
			measured := modelcontext.EstimatePreparedRequest(body, nil)
			observedInput = append(observedInput, measured)
			response := fmt.Sprintf(
				`{"id":"measurement_response_%d","object":"chat.completion","model":"live-static-shrink",`+
					`"choices":[{"index":0,"message":{"role":"assistant",`+
					`"content":"Keep the earlier historical objective and the user's exact requirements."},`+
					`"finish_reason":"stop"}],"usage":{"prompt_tokens":%d,"completion_tokens":16}}`,
				len(requests), measured,
			)
			return &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(response)),
			}, nil
		})},
	}
	now := fixture.Now.Add(2 * time.Second)
	executor := AgentExecutor{
		Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client),
		Now: func() time.Time { return now },
	}
	history := "HISTORY_MUST_STAY_VERBATIM " + strings.Repeat("specific historical detail ", 100)
	first := fixture.admitContentInputTurn(t, ctx, agentID, kernelTestUserID, history, fixture.Now.Add(time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, first))
	require.Len(t, requests, 1)
	target := model.UsableInputTokensForRequest(
		client.ModelCapabilities, model.RequestPolicyFromCapabilities(client.ModelCapabilities),
	)
	require.Greater(t, observedInput[0], target, "fixture needs measured input above the soft target")
	require.LessOrEqual(t, observedInput[0]+1024, 10000, "first request fits even a strict total input/output window")
	var persistedInput, firstCheckpoints int
	require.NoError(t, fixture.Pool.QueryRow(ctx,
		`SELECT input_tokens_total FROM model_call_contexts `+
			`WHERE agent_id=$1 AND operation_kind='normal' AND state='succeeded'`,
		agentID).Scan(&persistedInput))
	require.Equal(t, observedInput[0], persistedInput)
	require.NoError(t, fixture.Pool.QueryRow(ctx,
		`SELECT count(*) FROM context_checkpoints WHERE agent_id=$1`, agentID).Scan(&firstCheckpoints))
	require.Zero(t, firstCheckpoints)
	fixture.releaseModelRuntimeLock(t, ctx, first)
	for _, skill := range skills {
		_, err := fixture.Store.Skills().CreateSkillRevisionForSkill(ctx, skillstore.CreateSkillRevisionForSkillInput{
			OrgID: kernelTestOrgID, SkillID: skill.ID, BaseRevisionID: skill.RevisionID,
			Name: skill.Name, Description: "Brief guidance.",
			SkillMd: "# Skill\nBrief instructions.", ArchiveBytes: []byte("updated archive " + skill.ID.String()),
			Actor: kernelTestUserPrincipal(kernelTestUserID),
		})
		require.NoError(t, err)
	}
	require.Equal(t, config.ID, fixture.currentAgentConfig(t, ctx, agentID).ID)
	require.Equal(t, revisionID, currentRevisionIDForKernelConfig(t, ctx, fixture.Store, config))
	second := fixture.admitContentInputTurn(
		t, ctx, agentID, kernelTestUserID, "Continue using the original details.", fixture.Now.Add(3*time.Second),
	)
	now = fixture.Now.Add(4 * time.Second)
	bundle, err := executor.contextBuilder().Build(ctx, modelcontext.BuildInput{
		ProjectID: kernelTestProjectID, AgentID: agentID, TurnID: second.TurnID, OpeningInputIDs: second.InputIDs, Now: now,
		MediaProjector:             model.MediaProjectorForClient(client),
		TextAttachmentBudgetTokens: model.TextAttachmentBudgetTokens(client),
	})
	require.NoError(t, err)
	bundle.ConfiguredModelRevisionID = revisionID
	in := model.PrepareForSendInput{Context: bundle, Policy: model.RequestPolicyFromCapabilities(client.ModelCapabilities)}
	prepared, err := model.PrepareForSend(ctx, client, in)
	require.NoError(t, err)
	require.False(t, prepared.UsesProviderInputUsage)
	require.True(t, prepared.InputBudget.Fits())
	require.Contains(t, string(prepared.Body), history)
	require.NoError(t, executor.ExecuteModelWork(ctx, second))
	var checkpoints, compactions int
	require.NoError(t, fixture.Pool.QueryRow(ctx,
		`SELECT count(*) FROM context_checkpoints WHERE agent_id=$1`, agentID).Scan(&checkpoints))
	require.NoError(t, fixture.Pool.QueryRow(ctx,
		`SELECT count(*) FROM model_call_contexts WHERE agent_id=$1 AND operation_kind='compaction'`, agentID).Scan(&compactions))
	require.Zero(t, checkpoints)
	require.Zero(t, compactions)
	require.Len(t, requests, 2)
	require.Contains(t, string(requests[1]), history)
	require.Contains(t, string(requests[1]), "Continue using the original details.")
}

func TestAgentExecutorKeepsFittingHistoryAfterProviderMediaPruning(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgentWithModelOptions(t, ctx, "anthropic/media-pressure", fixture.Now,
		kernelConfiguredModelOptions{ContextWindowTokens: new(10_000), MaxOutputTokens: new(1_024)})
	var artifacts []artifactstore.ArtifactRecord
	var imageDigests [][32]byte
	for i := range 3 {
		frame := image.NewNRGBA(image.Rect(0, 0, 1225, 1225))
		frame.SetNRGBA(0, 0, color.NRGBA{R: uint8(i), A: 255})
		var data bytes.Buffer
		encoder := png.Encoder{CompressionLevel: png.NoCompression}
		require.NoError(t, encoder.Encode(&data, frame))
		require.Less(t, int64(4*data.Len()), modelcontext.MaxResolvedMediaBytes)
		artifact, err := fixture.Store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
			ProjectID: kernelTestProjectID, AgentID: agentID, ContentType: "image/png",
			Filename: fmt.Sprintf("frame-%d.png", i), Content: data.Bytes(),
		})
		require.NoError(t, err)
		artifacts = append(artifacts, artifact)
		imageDigests = append(imageDigests, sha256.Sum256([]byte(base64.StdEncoding.EncodeToString(data.Bytes()))))
	}
	var sentImages [][][32]byte
	client := anthropicmessages.Client{
		ProviderModelSlug: "media-pressure", BaseURL: "https://provider.test", EndpointPath: "/messages",
		ModelCapabilities: model.Capabilities{ContextWindowTokens: 10_000, DefaultMaxOutputTokens: 1_024,
			InputModalities: []string{modelcontext.InputModalityText, modelcontext.InputModalityImage}},
		HTTPClient: &http.Client{Transport: kernelSlackRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			var body struct {
				Messages []struct {
					Content []struct {
						Type   string `json:"type"`
						Source struct {
							Data string `json:"data"`
						} `json:"source"`
					} `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				return nil, err
			}
			var images [][32]byte
			for _, message := range body.Messages {
				for _, part := range message.Content {
					if part.Type == "image" {
						images = append(images, sha256.Sum256([]byte(part.Source.Data)))
					}
				}
			}
			sentImages = append(sentImages, images)
			response := fmt.Sprintf(`{"id":"media_response_%d","type":"message","role":"assistant",`+
				`"model":"media-pressure","content":[{"type":"text","text":"Preserve the image details and continue."}],`+
				`"stop_reason":"end_turn","usage":{"input_tokens":7500,"output_tokens":10}}`, len(sentImages))
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(response))}, nil
		})},
	}
	admit := func(text string, attached []artifactstore.ArtifactRecord, now time.Time) ModelWorkExecution {
		blocks := []map[string]string{{"type": "text", "text": text}}
		for _, artifact := range attached {
			blocks = append(blocks, map[string]string{"type": "media_ref", "artifact_id": artifact.ID.String()})
		}
		_, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
			ProjectID: kernelTestProjectID, AgentID: agentID, Actor: kernelTestOmnaraActorParams(t, userID),
			ContentBlocks: mustKernelJSON(blocks), IdempotencyKey: text,
		})
		require.NoError(t, err)
		claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, kernelTestClaimInput(now))
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, executionstore.AgentWorkModel, claim.Kind)
		return modelWorkExecutionFromClaimForKernelTest(claim, now)
	}
	now := fixture.Now.Add(time.Second)
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client),
		Now: func() time.Time { return now }}
	first := admit("Remember these three historical images.", artifacts, now)
	require.NoError(t, executor.ExecuteModelWork(ctx, first))
	require.Equal(t, [][][32]byte{imageDigests}, sentImages)
	fixture.releaseModelRuntimeLock(t, ctx, first)
	now = now.Add(time.Second)
	second := admit("Compare the earlier images with this image again.", artifacts[:1], now)
	require.NoError(t, executor.ExecuteModelWork(ctx, second))
	var checkpoints, compactions int
	require.NoError(t, fixture.Pool.QueryRow(ctx,
		`SELECT count(*) FROM context_checkpoints WHERE agent_id=$1`, agentID).Scan(&checkpoints))
	require.NoError(t, fixture.Pool.QueryRow(ctx,
		`SELECT count(*) FROM model_call_contexts WHERE agent_id=$1 AND operation_kind='compaction'`,
		agentID).Scan(&compactions))
	require.Zero(t, checkpoints)
	require.Zero(t, compactions)
	require.Len(t, sentImages, 2)
	require.Equal(t, [][32]byte{imageDigests[1], imageDigests[2], imageDigests[0]}, sentImages[1])
}
