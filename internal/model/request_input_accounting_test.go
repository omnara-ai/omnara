package model_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestProviderBaselineSurvivesEmptySuccessfulOutput(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 20_000, DefaultMaxOutputTokens: 1_000}
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps}) {
		for _, stop := range []modelenvelope.StopReason{
			modelenvelope.StopReasonEndTurn, modelenvelope.StopReasonContentFilter,
		} {
			t.Run(tc.name+"/"+string(stop), func(t *testing.T) {
				bundle := measuredInputBundle(t, tc.client)
				bundle.SystemPrompt = strings.Repeat("retained instructions ", 6_000)
				bundle.Messages[1].Content = json.RawMessage(`[]`)
				bundle.Messages[1].StopReason = stop
				bundle.Messages[1].Usage = modelenvelope.Usage{InputTokens: 1_000}
				setMeasuredInputFingerprint(t, tc.client, &bundle)
				bundle.Messages = append(bundle.Messages, modelcontext.Message{
					Role: modelprotocol.RoleUser, Sequence: 4,
					Content: json.RawMessage(`[{"type":"text","text":"next question"}]`),
				})
				bundle.InputEventSequence = 4
				prepare := func() model.PreparedRequest {
					t.Helper()
					prepared, err := model.PrepareForSend(t.Context(), tc.client, model.PrepareForSendInput{
						Context: bundle, Policy: model.RequestPolicyFromCapabilities(caps),
					})
					require.NoError(t, err)
					require.True(t, prepared.UsesProviderInputUsage)
					require.True(t, prepared.InputBudget.Fits())
					return prepared
				}
				before := prepare()
				require.InDelta(t, 1_000, before.InputTokenEstimate, 100)

				bundle.Messages = append(bundle.Messages, modelcontext.Message{
					Role: modelprotocol.RoleAssistant, Sequence: 5, StopReason: modelenvelope.StopReasonError,
					Content: json.RawMessage(`[{"type":"error","text":"` + strings.Repeat("failed attempt ", 200) + `"}]`),
				}, modelcontext.Message{
					Role: modelprotocol.RoleUser, Sequence: 6,
					Content: json.RawMessage(`[{"type":"text","text":"try again"}]`),
				})
				bundle.InputEventSequence = 6
				after := prepare()
				require.InDelta(t, 750, after.InputTokenEstimate-before.InputTokenEstimate, 100,
					"later failure text remains new input even when the measured output was empty")
				require.Contains(t, string(after.Body), "failed attempt")
			})
		}
	}
}

func TestProviderBaselineRequiresKnownRequestAccounting(t *testing.T) {
	client := unknownAccountingClient{}
	bundle := measuredInputBundle(t, client)
	bundle.Messages = append(bundle.Messages, modelcontext.Message{
		Role: modelprotocol.RoleUser, Sequence: 4,
		Content: json.RawMessage(`[{"type":"text","text":"next"}]`),
	})
	bundle.InputEventSequence = 4
	prepared, err := model.PrepareForSend(t.Context(), client, model.PrepareForSendInput{
		Context: bundle, Policy: model.RequestPolicyFromCapabilities(client.Capabilities()),
	})
	require.NoError(t, err)
	require.False(t, prepared.UsesProviderInputUsage, "unknown accounting is different from a known empty output")
	require.Equal(t, 50_000, prepared.InputTokenEstimate)
}

type unknownAccountingClient struct{}

func (unknownAccountingClient) RequestedProviderModelSlug() string { return "unknown-accounting" }
func (unknownAccountingClient) APIFormat() modelprotocol.APIFormat {
	return modelprotocol.APIFormatOpenAIResponses
}
func (unknownAccountingClient) ModelAPIVariant() modelprotocol.APIVariant {
	return modelprotocol.APIVariantDefault
}
func (unknownAccountingClient) Capabilities() model.Capabilities {
	return model.Capabilities{ContextWindowTokens: 100_000, DefaultMaxOutputTokens: 1_000}
}
func (unknownAccountingClient) Respond(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, nil
}
func (unknownAccountingClient) Prepare(_ context.Context, input model.PrepareInput) (model.PreparedRequest, error) {
	body, err := json.Marshal(map[string]any{"contents": input.Context.Messages})
	return model.PreparedRequest{Body: body, InputTokenEstimate: 50_000}, err
}

func TestProviderBaselineWithOnlyReasoningOutputIgnoresByteAlignment(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 20_000, DefaultMaxOutputTokens: 1_000, SupportsReasoning: true}
	providerID := uuid.New().String()
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps, modelProviderConfigID: providerID}) {
		t.Run(tc.name, func(t *testing.T) {
			for padding := range 4 {
				t.Run(fmt.Sprintf("padding_%d", padding), func(t *testing.T) {
					bundle := measuredInputBundle(t, tc.client)
					bundle.Messages[0].Content = json.RawMessage(`[{"type":"text","text":"question` + strings.Repeat("x", padding) + `"}]`)
					bundle.Messages[1].StopReason = modelenvelope.StopReasonEndTurn
					bundle.Messages[1].Usage = modelenvelope.Usage{InputTokens: 1_000, OutputTokens: 500}
					setMeasuredInputFingerprint(t, tc.client, &bundle)
					setReasoningReplay(&bundle.Messages[1], tc.client, providerID,
						strings.Repeat("opaque", 2_000)+strings.Repeat("x", padding))
					bundle.InputEventSequence = 3
					prepared, err := model.PrepareForSend(t.Context(), tc.client, model.PrepareForSendInput{
						Context: bundle, Policy: model.RequestPolicyFromCapabilities(caps),
					})
					require.NoError(t, err)
					require.True(t, prepared.UsesProviderInputUsage)
					require.Contains(t, string(prepared.Body), strings.Repeat("opaque", 2_000))
					require.InDelta(t, 1_500, prepared.InputTokenEstimate, 30)
				})
			}
		})
	}
}

func TestProviderBaselineDoesNotRechargeLoadedAnthropicToolSchema(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 200_000, DefaultMaxOutputTokens: 1_000}
	client := anthropicmessages.Client{ProviderModelSlug: "test-model", EndpointPath: "/messages", ModelCapabilities: caps}
	estimate := func(description string) int {
		t.Helper()
		bundle := measuredInputBundle(t, client)
		bundle.ToolSpecs = []modelcontext.ToolSpec{
			{Name: "tool_search", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "get_weather", Deferred: true, Description: description, InputSchema: json.RawMessage(`{"type":"object"}`)},
		}
		search, err := modelcontext.SearchDeferredTools(bundle.ToolSpecs, "weather", 1)
		require.NoError(t, err)
		parts, err := json.Marshal([]any{map[string]any{"type": "structured_data", "value": search}})
		require.NoError(t, err)
		bundle.Messages[1].ModelCallContextID = "first-search-context"
		bundle.Messages[1].StopReason = modelenvelope.StopReasonToolUse
		bundle.Messages[1].Content = json.RawMessage(`[{"type":"tool_call","tool_call_id":"first-search-call"}]`)
		bundle.ToolResults = []modelcontext.ToolResultRef{{
			ToolCallID: "first-search-call", ModelCallContextID: "first-search-context", ProviderCallID: "first-search",
			SourceEventSequence: 3, ResultEventSequence: 4, Name: "tool_search", Outcome: "succeeded",
			Input: json.RawMessage(`{"pattern":"weather"}`), ContentParts: parts,
		}}
		setMeasuredInputFingerprint(t, client, &bundle)
		bundle.InputEventSequence = 4
		measured, err := model.PrepareForSend(t.Context(), client, model.PrepareForSendInput{
			Context: bundle, Policy: model.RequestPolicyFromCapabilities(caps),
		})
		require.NoError(t, err)
		bundle.Messages = append(bundle.Messages, modelcontext.Message{
			Role: modelprotocol.RoleAssistant, Sequence: 6, InputEventSequence: 4, ModelCallContextID: "second-search-context",
			AgentConfigID: bundle.AgentConfigID, ConfiguredModelRevisionID: bundle.ConfiguredModelRevisionID,
			InputFingerprint: measured.InputFingerprint, StopReason: modelenvelope.StopReasonToolUse,
			Content:              json.RawMessage(`[{"type":"tool_call","tool_call_id":"second-search-call"}]`),
			Usage:                modelenvelope.Usage{InputTokens: 2_000, OutputTokens: 10},
			ProviderReplaySource: model.ProviderReplayIdentityForClient("", client),
		})
		bundle.ToolResults = append(bundle.ToolResults, modelcontext.ToolResultRef{
			ToolCallID: "second-search-call", ModelCallContextID: "second-search-context", ProviderCallID: "second-search",
			SourceEventSequence: 6, ResultEventSequence: 7, Name: "tool_search", Outcome: "succeeded",
			Input: json.RawMessage(`{"pattern":"weather"}`), ContentParts: parts,
		})
		bundle.InputEventSequence = 7
		prepared, err := model.PrepareForSend(t.Context(), client, model.PrepareForSendInput{
			Context: bundle, Policy: model.RequestPolicyFromCapabilities(caps),
		})
		require.NoError(t, err)
		require.True(t, prepared.UsesProviderInputUsage)
		require.Equal(t, 1, strings.Count(string(prepared.Body), description), "the loaded schema is sent once")
		return prepared.InputTokenEstimate
	}
	small := estimate("Get weather.")
	large := estimate(strings.Repeat("schema detail ", 2_000))
	require.InDelta(t, small, large, 5, "the provider's input measurement already includes the loaded schema")
}

func TestProviderBaselineUsesOutputUsageForResponsesToolSearchCall(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 20_000, DefaultMaxOutputTokens: 1_000, SupportsReasoning: true}
	providerID := uuid.New().String()
	client := openairesponses.Client{
		ModelProviderConfigID: providerID, ProviderModelSlug: "test-model",
		EndpointPath: "/responses", ModelCapabilities: caps,
	}
	estimate := func(pattern string) (int, json.RawMessage) {
		t.Helper()
		bundle := measuredInputBundle(t, client)
		bundle.InputEventSequence = 4
		bundle.ToolSpecs = []modelcontext.ToolSpec{
			{Name: "tool_search", InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"}}}`)},
			{Name: "get_weather", Deferred: true, Description: "Get weather.", InputSchema: json.RawMessage(`{"type":"object"}`)},
		}
		source := &bundle.Messages[1]
		source.ModelCallContextID = "search-context"
		source.StopReason = modelenvelope.StopReasonToolUse
		source.Content = json.RawMessage(`[{"type":"reasoning","text":"thinking"},{"type":"tool_call","tool_call_id":"search-call"}]`)
		source.Usage = modelenvelope.Usage{InputTokens: 1_000, OutputTokens: 500, ReasoningTokens: 400}
		source.ProviderReplaySource = model.ProviderReplayIdentityForClient(providerID, client)
		arguments, err := json.Marshal(map[string]string{"pattern": pattern})
		require.NoError(t, err)
		source.ProviderReplay, err = json.Marshal([]any{
			map[string]any{"type": "reasoning", "id": "reasoning-output", "encrypted_content": strings.Repeat("opaque", 2_000),
				"summary": []any{map[string]string{"type": "summary_text", "text": "thinking"}}},
			map[string]any{"type": "tool_search_call", "id": "search-output", "execution": "client", "call_id": "search",
				"status": "completed", "arguments": json.RawMessage(arguments)},
		})
		require.NoError(t, err)
		search, err := modelcontext.SearchDeferredTools(bundle.ToolSpecs, pattern, 1)
		require.NoError(t, err)
		require.Equal(t, []string{"get_weather"}, search.ToolNames)
		parts, err := json.Marshal([]any{map[string]any{"type": "structured_data", "value": search}})
		require.NoError(t, err)
		bundle.ToolResults = []modelcontext.ToolResultRef{{
			ToolCallID: "search-call", ModelCallContextID: "search-context", ProviderCallID: "search",
			SourceEventSequence: 3, ResultEventSequence: 4, Name: "tool_search", Outcome: "succeeded",
			Input: arguments, ContentParts: parts,
		}}
		setMeasuredInputFingerprint(t, client, &bundle)
		prepared, err := model.PrepareForSend(t.Context(), client, model.PrepareForSendInput{
			Context: bundle, Policy: model.RequestPolicyFromCapabilities(caps),
		})
		require.NoError(t, err)
		require.True(t, prepared.UsesProviderInputUsage)
		require.Contains(t, string(prepared.Body), `"id":"reasoning-output"`)
		require.Contains(t, string(prepared.Body), `"id":"search-output"`, "exact provider replay must survive")
		var body struct {
			Input []json.RawMessage `json:"input"`
		}
		require.NoError(t, json.Unmarshal(prepared.Body, &body))
		for _, item := range body.Input {
			var kind struct {
				Type string `json:"type"`
			}
			require.NoError(t, json.Unmarshal(item, &kind))
			if kind.Type == "tool_search_output" {
				return prepared.InputTokenEstimate, item
			}
		}
		t.Fatal("prepared request omitted the tool search result")
		return 0, nil
	}
	small, smallResult := estimate("weather")
	largePattern := "weather|" + strings.Repeat("x", toolcatalog.ToolSearchMaxPatternLength-len("weather|"))
	large, largeResult := estimate(largePattern)
	require.JSONEq(t, string(smallResult), string(largeResult),
		"only source call arguments grow; the tool result is unchanged")
	require.InDelta(t, small, large, 2, "source call arguments are already covered by its output usage")
}
