package openaichatcompletions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestPrepareBuildsChatCompletionsPayload(t *testing.T) {
	replayItem := json.RawMessage(
		`{"id":"call_1","type":"function","function":` +
			`{"name":"run_command","arguments":"{\"command\":\"true\"}"}}`,
	)
	replay := testProviderReplay(
		"gpt-4.1",
		modelprotocol.APIFormatOpenAIChatCompletions,
		modelprotocol.APIVariantDefault,
		json.RawMessage(`{"role":"assistant","tool_calls":[`+string(replayItem)+`]}`),
	)
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "gpt-4.1",
		ModelCapabilities:     model.Capabilities{SupportsReasoning: true},
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			ContextCheckpoint: &modelcontext.CheckpointRef{
				ID:      "ccp_1",
				Summary: "old summary",
			},
			Messages: []modelcontext.Message{
				{Sequence: 1, Role: modelprotocol.RoleUser, Content: json.RawMessage(`[{"type":"text","text":"hi"}]`)},
				withToolCallLinks(chatReplayMessage("mcc_1", replay), "call_1"),
			},
			ToolSpecs: []modelcontext.ToolSpec{{
				Name:        "run_command",
				Description: "run",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}},
			ToolResults: []modelcontext.ToolResultRef{{ToolCallID: "call_1",
				ModelCallContextID: "mcc_1",
				ProviderCallID:     "call_1",
				Name:               "run_command",
				Input:              json.RawMessage(`{"command":"true"}`),
				ContentParts:       json.RawMessage(`[{"type":"structured_data","value":{"ok":true}}]`),
			}},
		},
		Policy: model.RequestPolicy{
			MaxOutputTokens: 1234,
			CacheRetention:  model.CacheRetentionLong,
			ReasoningEffort: "medium",
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Model               string `json:"model"`
		Store               *bool  `json:"store"`
		MaxCompletionTokens int    `json:"max_completion_tokens"`
		ReasoningEffort     string `json:"reasoning_effort"`
		ToolChoice          string `json:"tool_choice"`
		ParallelToolCalls   *bool  `json:"parallel_tool_calls"`
		Messages            []struct {
			Role      string `json:"role"`
			Content   any    `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode prepared payload: %v", err)
	}
	if payload.Model != "gpt-4.1" ||
		payload.Store == nil ||
		*payload.Store ||
		payload.MaxCompletionTokens != 1234 ||
		payload.ReasoningEffort != "medium" {
		t.Fatalf("unexpected payload header: %+v", payload)
	}
	if payload.ToolChoice != "auto" || payload.ParallelToolCalls != nil {
		t.Fatalf("unexpected tool controls: %+v", payload)
	}
	if len(payload.Tools) != 1 || payload.Tools[0].Type != "function" || payload.Tools[0].Function.Name != "run_command" {
		t.Fatalf("unexpected tools: %+v", payload.Tools)
	}
	if len(payload.Messages) != 5 {
		t.Fatalf(
			"expected system, checkpoint, user, assistant tool call, and tool result messages, got %d: %s",
			len(payload.Messages),
			prepared.Body,
		)
	}
	systemContent, ok := payload.Messages[0].Content.(string)
	if !ok {
		t.Fatalf("system content = %T, want string", payload.Messages[0].Content)
	}
	if payload.Messages[0].Role != string(chatRoleSystem) ||
		!strings.HasPrefix(systemContent, "sys\n\n") ||
		!strings.Contains(systemContent, "<context_checkpoint>") ||
		payload.Messages[1].Role != string(chatRoleUser) ||
		payload.Messages[1].Content != "<context_checkpoint>\nold summary\n</context_checkpoint>" ||
		payload.Messages[2].Content != "hi" {
		t.Fatalf("unexpected transcript prefix: %+v", payload.Messages[:3])
	}
	if payload.Messages[3].Role != string(chatRoleAssistant) ||
		len(payload.Messages[3].ToolCalls) != 1 ||
		payload.Messages[3].ToolCalls[0].ID != "call_1" ||
		payload.Messages[3].ToolCalls[0].Function.Arguments != `{"command":"true"}` {
		t.Fatalf("provider replay tool call not preserved: %+v", payload.Messages[3])
	}
	if payload.Messages[4].Role != "tool" ||
		payload.Messages[4].ToolCallID != "call_1" ||
		payload.Messages[4].Content != `{"ok":true}` {
		t.Fatalf("unexpected tool result message: %+v", payload.Messages[4])
	}
}

func TestPrepareProjectsRuntimeResourcesOnlyForEnabledTools(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	withoutTools, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			AvailableMachinePools: []modelcontext.MachinePoolRef{{
				MachinePoolName: "Build Pool",
			}},
			IntegrationTargets: []modelcontext.IntegrationTargetRef{{
				TargetRef: "slack-abcd",
				Provider:  "slack",
			}},
		},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare without tools: %v", err)
	}
	if strings.Contains(string(withoutTools.Body), "Build Pool") ||
		strings.Contains(string(withoutTools.Body), "slack-abcd") {
		t.Fatalf("runtime resource context leaked without usable tools: %s", withoutTools.Body)
	}

	withTools, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			ToolSpecs: []modelcontext.ToolSpec{
				{Name: toolcatalog.ToolNameCreateMachine},
				{Name: toolcatalog.ToolNameSendIntegrationMessage},
			},
		},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare with tools: %v", err)
	}
	body := string(withTools.Body)
	if !strings.Contains(body, "no machine pools are currently available") ||
		!strings.Contains(body, "No external integration targets are currently available") {
		t.Fatalf("missing empty resource context for enabled tools: %s", body)
	}
}

func TestPrepareKeepsCompletedToolExchangeBeforeLaterUserTurnWithoutDuplicatingAssistantText(t *testing.T) {
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "gpt-test",
	}
	replay := testProviderReplay(
		"gpt-test",
		modelprotocol.APIFormatOpenAIChatCompletions,
		modelprotocol.APIVariantDefault,
		json.RawMessage(`{
			"role":"assistant",
			"content":"replayed duplicate text",
			"tool_calls":[{"id":"call_1","type":"function","function":{"name":"run_command","arguments":"{\"command\":\"true\"}"}}]
		}`),
	)
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		Messages: []modelcontext.Message{
			{Role: modelprotocol.RoleUser, Sequence: 10, Content: json.RawMessage(`[{"type":"text","text":"start"}]`)},
			withToolCallLinks(modelcontext.Message{
				Role:                 modelprotocol.RoleAssistant,
				Sequence:             20,
				ModelCallContextID:   "mcc_1",
				Content:              json.RawMessage(`[{"type":"text","text":"canonical assistant text"}]`),
				ProviderReplay:       replay.payload,
				ProviderReplaySource: replay.source,
			}, "tcl_1"),
			{Role: modelprotocol.RoleUser, Sequence: 40, Content: json.RawMessage(`[{"type":"text","text":"next turn"}]`)},
		},
		ToolResults: []modelcontext.ToolResultRef{{
			ToolCallID:          "tcl_1",
			ModelCallContextID:  "mcc_1",
			ProviderCallID:      "call_1",
			Name:                "run_command",
			Input:               json.RawMessage(`{"command":"true"}`),
			ContentParts:        json.RawMessage(`[{"type":"text","text":"done"}]`),
			SourceEventSequence: 20,
			ResultEventSequence: 30,
		}},
	}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    any    `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []any  `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Messages) != 4 || payload.Messages[0].Role != string(chatRoleUser) ||
		payload.Messages[1].Role != string(chatRoleAssistant) || payload.Messages[2].Role != string(chatRoleTool) ||
		payload.Messages[3].Role != string(chatRoleUser) {
		t.Fatalf("message chronology is wrong: %s", prepared.Body)
	}
	if payload.Messages[1].Content != "canonical assistant text" || len(payload.Messages[1].ToolCalls) != 1 {
		t.Fatalf("assistant source event was not merged with its tool call: %s", prepared.Body)
	}
	if strings.Contains(string(prepared.Body), "replayed duplicate text") {
		t.Fatalf("provider replay duplicated canonical assistant text: %s", prepared.Body)
	}
}

func TestPrepareBuildsOpenRouterPayload(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath,
		ProviderModelSlug: "anthropic/claude-sonnet-4",
		ModelCapabilities: model.Capabilities{SupportsReasoning: true},
		APIVariant:        modelprotocol.APIVariantOpenRouter,
		APIVariantOptions: json.RawMessage(
			`{"provider":{"only":["anthropic"]}}`,
		),
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser,
				Content: json.RawMessage(`[{"type":"text","text":"hi"}]`),
			}},
			ToolSpecs: []modelcontext.ToolSpec{{
				Name:        "run_command",
				Description: "run",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}},
		},
		Policy: model.RequestPolicy{
			MaxOutputTokens: 2048,
			CacheRetention:  model.CacheRetentionLong,
			ReasoningEffort: "high",
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode prepared payload: %v", err)
	}
	if _, ok := payload["store"]; ok {
		t.Fatalf("openrouter payload should omit store: %s", prepared.Body)
	}
	if _, ok := payload["prompt_cache_retention"]; ok {
		t.Fatalf("openrouter payload should omit prompt_cache_retention: %s", prepared.Body)
	}
	if _, ok := payload["max_tokens"]; ok {
		t.Fatalf("openrouter payload should use max_completion_tokens, not deprecated max_tokens: %s", prepared.Body)
	}
	if _, ok := payload["usage"]; ok {
		t.Fatalf("openrouter payload should omit usage request options because usage is always returned: %s", prepared.Body)
	}
	if _, ok := payload["parallel_tool_calls"]; ok {
		t.Fatalf(
			"openrouter payload should omit parallel_tool_calls so provider is not constrained: %s",
			prepared.Body,
		)
	}
	if _, ok := payload["reasoning_effort"]; ok {
		t.Fatalf("openrouter payload should use reasoning object, not reasoning_effort: %s", prepared.Body)
	}
	if _, ok := payload["cache_control"]; ok {
		t.Fatalf("openrouter payload should mark content blocks instead of top-level cache_control: %s", prepared.Body)
	}
	var header struct {
		Model               string `json:"model"`
		MaxCompletionTokens int    `json:"max_completion_tokens"`
		Reasoning           struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		Provider struct {
			Only []string `json:"only"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(prepared.Body, &header); err != nil {
		t.Fatalf("decode openrouter payload: %v", err)
	}
	if header.Model != "anthropic/claude-sonnet-4" ||
		header.MaxCompletionTokens != 2048 ||
		header.Reasoning.Effort != "high" ||
		len(header.Provider.Only) != 1 ||
		header.Provider.Only[0] != "anthropic" {
		t.Fatalf("unexpected openrouter payload: %+v body=%s", header, prepared.Body)
	}
	marks := cacheControlMarks(t, prepared.Body)
	if len(marks) != 1 || marks[0].role != "user" || marks[0].index != 0 ||
		marks[0].control.Type != "ephemeral" || marks[0].control.TTL != "1h" {
		t.Fatalf("cache_control marks = %+v, want ephemeral 1h on the last user block: %s", marks, prepared.Body)
	}
	var provider map[string]json.RawMessage
	if err := json.Unmarshal(payload["provider"], &provider); err != nil {
		t.Fatalf("decode provider: %v", err)
	}
	if _, ok := provider["require_parameters"]; ok {
		t.Fatalf("openrouter payload should not inject require_parameters: %s", prepared.Body)
	}
}

func TestAPIVariantOptionsReachProviderRequest(t *testing.T) {
	var sent map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(requestBody, &sent); err != nil {
			t.Fatalf("decode request body: %v body=%s", err, requestBody)
		}
		responseBody := `{"id":"chatcmpl_extra","model":"gpt-test","choices":[` +
			`{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
		_, _ = w.Write([]byte(responseBody))
	}))
	defer server.Close()

	client := Client{EndpointPath: testEndpointPath,
		Auth:              route.BearerToken{Token: "test-key"},
		BaseURL:           server.URL,
		ProviderModelSlug: "gpt-test",
		HTTPClient:        server.Client(),
		APIVariantOptions: json.RawMessage(
			`{"temperature":0.2,"top_k":40,"repetition_penalty":1.1,` +
				`"stream":true,"stream_options":{"include_usage":true},"n":1,` +
				`"response_format":{"type":"json_object"}}`,
		),
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser,
			Content: json.RawMessage(`[{"type":"text","text":"hi"}]`),
		}}},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := client.Respond(context.Background(), model.Request{ProviderRequest: prepared.Body}); err != nil {
		t.Fatalf("respond: %v", err)
	}
	for key, want := range map[string]string{
		"temperature":        "0.2",
		"top_k":              "40",
		"repetition_penalty": "1.1",
		"stream":             "true",
		"n":                  "1",
	} {
		if string(sent[key]) != want {
			t.Fatalf("api_variant_options.%s = %s, want %s in provider request %v", key, sent[key], want, sent)
		}
	}
	if string(sent["stream_options"]) != `{"include_usage":true}` ||
		string(sent["response_format"]) != `{"type":"json_object"}` {
		t.Fatalf("provider-specific api_variant_options fields missing in provider request: %v", sent)
	}
	if string(sent["model"]) != `"gpt-test"` || len(sent["messages"]) == 0 {
		t.Fatalf("owned adapter fields missing after api_variant_options merge: %v", sent)
	}
}

func TestAPIVariantOptionsDoNotOverrideAdapterFields(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath,
		ProviderModelSlug: "gpt-test",
		APIVariantOptions: json.RawMessage(
			`{"model":"override-model","messages":[],"tools":[{"type":"function"}],` +
				`"tool_choice":"required","n":2,"max_tokens":12,"max_completion_tokens":13,` +
				`"temperature":0.2}`,
		),
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser,
			Content: json.RawMessage(`[{"type":"text","text":"hi"}]`),
		}}},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Model       string            `json:"model"`
		Messages    []json.RawMessage `json:"messages"`
		N           int               `json:"n"`
		MaxTokens   *int              `json:"max_tokens"`
		MaxOutput   int               `json:"max_completion_tokens"`
		Temperature float64           `json:"temperature"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v body=%s", err, prepared.Body)
	}
	if payload.Model != "gpt-test" {
		t.Fatalf("model = %q, want adapter-owned provider model", payload.Model)
	}
	if len(payload.Messages) == 0 {
		t.Fatalf("messages were overwritten by api_variant_options: %s", prepared.Body)
	}
	if payload.N != 1 {
		t.Fatalf("n = %d, want adapter-owned single choice", payload.N)
	}
	if payload.MaxTokens != nil || payload.MaxOutput != 64 {
		t.Fatalf(
			"output limits = max_tokens:%v max_completion_tokens:%d, want nil/64",
			payload.MaxTokens,
			payload.MaxOutput,
		)
	}
	if payload.Temperature != 0.2 {
		t.Fatalf("temperature = %v, want provider-specific api_variant_options value", payload.Temperature)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(prepared.Body, &raw); err != nil {
		t.Fatalf("decode raw payload: %v", err)
	}
	if _, ok := raw["tools"]; ok {
		t.Fatalf("tools were injected by api_variant_options: %s", prepared.Body)
	}
	if _, ok := raw["tool_choice"]; ok {
		t.Fatalf("tool_choice was injected by api_variant_options: %s", prepared.Body)
	}
}

func TestAPIVariantOptionsCanOverrideUnownedChatFields(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath,
		ProviderModelSlug: "gpt-test",
		APIVariantOptions: json.RawMessage(`{"store":true,"parallel_tool_calls":false}`),
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser,
				Content: json.RawMessage(`[{"type":"text","text":"hi"}]`),
			}},
			ToolSpecs: []modelcontext.ToolSpec{{
				Name:        "run_command",
				Description: "Run a command",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}},
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if string(payload["store"]) != `true` || string(payload["parallel_tool_calls"]) != `false` {
		t.Fatalf("unowned fields were not overridden by api_variant_options: %s", prepared.Body)
	}
	if len(payload["tools"]) == 0 || string(payload["tool_choice"]) != `"auto"` {
		t.Fatalf("adapter-owned tool fields missing: %s", prepared.Body)
	}
}

func TestAPIVariantOptionsRespectChatReasoningOwnership(t *testing.T) {
	for _, tc := range []struct {
		name              string
		apiVariant        modelprotocol.APIVariant
		options           json.RawMessage
		wantReasoning     string
		wantEffort        string
		wantEffortPresent bool
	}{
		{
			name:              "default variant owns reasoning effort",
			options:           json.RawMessage(`{"reasoning_effort":"low","reasoning":{"effort":"low"}}`),
			wantEffort:        "high",
			wantEffortPresent: true,
		},
		{
			name:          "openrouter owns reasoning object",
			apiVariant:    modelprotocol.APIVariantOpenRouter,
			options:       json.RawMessage(`{"reasoning_effort":"low","reasoning":{"effort":"low"}}`),
			wantReasoning: `{"effort":"high"}`,
		},
		{
			name:          "reasoning passes through when omnara selected no effort",
			options:       json.RawMessage(`{"reasoning":{"enabled":false}}`),
			wantReasoning: `{"enabled":false}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			supportsReasoning := true
			client := Client{EndpointPath: testEndpointPath,
				ProviderModelSlug: "gpt-test",
				APIVariant:        tc.apiVariant,
				ModelCapabilities: model.Capabilities{SupportsReasoning: supportsReasoning},
				APIVariantOptions: tc.options,
			}
			effort := "high"
			if tc.wantEffort == "" && tc.wantReasoning == `{"enabled":false}` {
				effort = ""
			}
			prepared, err := client.Prepare(context.Background(), model.PrepareInput{
				Context: modelcontext.Bundle{Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser,
					Content: json.RawMessage(`[{"type":"text","text":"hi"}]`),
				}}},
				Policy: model.RequestPolicy{ReasoningEffort: effort},
			})
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(prepared.Body, &payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			if tc.wantEffortPresent {
				if string(payload["reasoning_effort"]) != `"`+tc.wantEffort+`"` {
					t.Fatalf("reasoning_effort = %s, want %q in %s", payload["reasoning_effort"], tc.wantEffort, prepared.Body)
				}
			} else if _, ok := payload["reasoning_effort"]; ok {
				t.Fatalf("unexpected reasoning_effort: %s", prepared.Body)
			}
			if tc.wantReasoning != "" && string(payload["reasoning"]) != tc.wantReasoning {
				t.Fatalf("reasoning = %s, want %s in %s", payload["reasoning"], tc.wantReasoning, prepared.Body)
			}
		})
	}
}

func TestPrepareOmitsOpenRouterCacheControlForNonClaudeModels(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath,
		ProviderModelSlug: "openai/gpt-5-mini",
		APIVariant:        modelprotocol.APIVariantOpenRouter,
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser,
				Content: json.RawMessage(`[{"type":"text","text":"hi"}]`),
			}},
		},
		Policy: model.RequestPolicy{CacheRetention: model.CacheRetentionLong},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if strings.Contains(string(prepared.Body), "cache_control") {
		t.Fatalf("non-Claude OpenRouter payload should omit cache_control: %s", prepared.Body)
	}
}

func TestPreparePreservesExplicitOpenRouterRequireParametersFalse(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath,
		ProviderModelSlug: "anthropic/claude-sonnet-4",
		ModelCapabilities: model.Capabilities{SupportsReasoning: true},
		APIVariant:        modelprotocol.APIVariantOpenRouter,
		APIVariantOptions: json.RawMessage(`{"provider":{"require_parameters":false}}`),
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser,
				Content: json.RawMessage(`[{"type":"text","text":"hi"}]`),
			}},
		},
		Policy: model.RequestPolicy{ReasoningEffort: "high"},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var provider map[string]json.RawMessage
	if err := json.Unmarshal(payload["provider"], &provider); err != nil {
		t.Fatalf("decode provider: %v", err)
	}
	if string(provider["require_parameters"]) != "false" {
		t.Fatalf("explicit require_parameters=false was overwritten: %s", prepared.Body)
	}
}

func TestPrepareRejectsToolsWhenUnsupported(t *testing.T) {
	supportsTools := false
	client := Client{
		EndpointPath:      testEndpointPath,
		ProviderModelSlug: "gpt-test",
		ModelCapabilities: model.Capabilities{SupportsTools: &supportsTools},
	}
	_, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser,
			Content: json.RawMessage(`[{"type":"text","text":"hi"}]`),
		}},
		ToolSpecs: []modelcontext.ToolSpec{{
			Name:        "run_command",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	}})
	if err == nil || !strings.Contains(err.Error(), "does not support tools") {
		t.Fatalf("error = %v, want unsupported tools", err)
	}
}

func TestPrepareRebuildsCrossFormatToolReplayAndOrdersGroups(t *testing.T) {
	openAIReplay := json.RawMessage(
		`{"type":"function_call","id":"fc_1","call_id":"call_wrong",` +
			`"name":"run_command","arguments":"{\"command\":\"wrong\"}"}`,
	)
	replay := testProviderReplay(
		"gpt-test",
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
		openAIReplay,
	)
	prepared, err := (Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}).Prepare(
		context.Background(),
		model.PrepareInput{Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{
				withToolCallLinks(modelcontext.Message{
					Role:                 modelprotocol.RoleAssistant,
					Sequence:             1,
					ModelCallContextID:   "mcc_first",
					Content:              json.RawMessage(`[]`),
					ProviderReplay:       replay.payload,
					ProviderReplaySource: replay.source,
				}, "tcl_first_1", "tcl_first_2"),
				messageAtSequence(assistantToolCallMessage("mcc_second", "tcl_second"), 2),
			},
			ToolResults: []modelcontext.ToolResultRef{
				{
					ToolCallID:          "tcl_second",
					ModelCallContextID:  "mcc_second",
					SourceEventSequence: 2,
					ProviderCallID:      "call_second",
					Name:                "run_command",
					Input:               json.RawMessage(`{"command":"second"}`),
					ContentParts:        json.RawMessage(`[{"type":"text","text":"second"}]`),
				},
				{
					ToolCallID:          "tcl_first_1",
					ModelCallContextID:  "mcc_first",
					SourceEventSequence: 1,
					ProviderCallID:      "call_first_1",
					Name:                "run_command",
					Input:               json.RawMessage(`{"command":"first-one"}`),
					ContentParts:        json.RawMessage(`[{"type":"text","text":"first-one"}]`),
				},
				{
					ToolCallID:          "tcl_first_2",
					ModelCallContextID:  "mcc_first",
					SourceEventSequence: 1,
					ProviderCallID:      "call_first_2",
					Name:                "run_command",
					Input:               json.RawMessage(`{"command":"first-two"}`),
					ContentParts:        json.RawMessage(`[{"type":"text","text":"first-two"}]`),
				},
			}},
		},
	)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Messages []struct {
			Role      string `json:"role"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Messages) != 6 || payload.Messages[0].Role != string(chatRoleUser) {
		t.Fatalf(
			"expected guard message, two assistant groups, and three tool results, got %d: %s",
			len(payload.Messages),
			prepared.Body,
		)
	}
	if got := payload.Messages[1].ToolCalls[0].ID; got != "call_first_1" {
		t.Fatalf("first group/order mismatch: got %s body=%s", got, prepared.Body)
	}
	if got := payload.Messages[1].ToolCalls[0].Function.Arguments; got != `{"command":"first-one"}` {
		t.Fatalf("cross-format replay should be rebuilt from normalized input, got %s", got)
	}
	if got := payload.Messages[1].ToolCalls[1].ID; got != "call_first_2" {
		t.Fatalf("first group second call mismatch: got %s body=%s", got, prepared.Body)
	}
	if got := payload.Messages[4].ToolCalls[0].ID; got != "call_second" {
		t.Fatalf("second group mismatch: got %s body=%s", got, prepared.Body)
	}
}

func TestPrepareDoesNotInventToolRoundsForInterleavedCanonicalAssistantContent(t *testing.T) {
	prepared, err := (Client{
		EndpointPath:      testEndpointPath,
		ProviderModelSlug: "gpt-test",
	}).Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{
				{
					Role:     modelprotocol.RoleUser,
					Sequence: 1,
					Content:  json.RawMessage(`[{"type":"text","text":"start"}]`),
				},
				{
					Role:               modelprotocol.RoleAssistant,
					ModelCallContextID: "mcc_1",
					Sequence:           2,
					Content: json.RawMessage(`[
						{"type":"text","text":"before"},
						{"type":"tool_call","tool_call_id":"tcl_1"},
						{"type":"text","text":"between"},
						{"type":"tool_call","tool_call_id":"tcl_2"},
						{"type":"text","text":"after"}
					]`),
				},
			},
			ToolResults: []modelcontext.ToolResultRef{
				{
					ToolCallID:         "tcl_1",
					ModelCallContextID: "mcc_1",
					ProviderCallID:     "call_1",
					Name:               "run_command",
					Input:              json.RawMessage(`{"command":"one"}`),
					ContentParts:       json.RawMessage(`[{"type":"text","text":"result one"}]`),
				},
				{
					ToolCallID:         "tcl_2",
					ModelCallContextID: "mcc_1",
					ProviderCallID:     "call_2",
					Name:               "run_command",
					Input:              json.RawMessage(`{"command":"two"}`),
					ContentParts:       json.RawMessage(`[{"type":"text","text":"result two"}]`),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Messages []struct {
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Messages) != 4 ||
		payload.Messages[1].Content != "before\nbetween\nafter" ||
		len(payload.Messages[1].ToolCalls) != 2 ||
		payload.Messages[1].ToolCalls[0].ID != "call_1" ||
		payload.Messages[1].ToolCalls[1].ID != "call_2" ||
		payload.Messages[2].ToolCallID != "call_1" ||
		payload.Messages[3].ToolCallID != "call_2" {
		t.Fatalf("source output was split into invented tool rounds: %s", prepared.Body)
	}
}

func TestPrepareSkipsReasoningContentPartsInProviderInput(t *testing.T) {
	prepared, err := (Client{
		EndpointPath:      testEndpointPath,
		ProviderModelSlug: "gpt-test",
	}).Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{
				{Sequence: 1, Role: modelprotocol.RoleAssistant,
					Content: json.RawMessage(
						`[{"type":"reasoning","text":"hidden reasoning"},` +
							`{"type":"text","text":"visible reply"}]`,
					),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	if strings.Contains(body, "hidden reasoning") {
		t.Fatalf("visible reasoning summary was echoed back to the model: %s", body)
	}
	if !strings.Contains(body, "visible reply") {
		t.Fatalf("assistant text was dropped: %s", body)
	}
}

func TestPrepareReplaysReasoningOnlyAssistantWithValidContentAndCanonicalRole(t *testing.T) {
	replay := testProviderReplay(
		"gpt-test",
		modelprotocol.APIFormatOpenAIChatCompletions,
		modelprotocol.APIVariantDefault,
		json.RawMessage(`{
			"role":"assistant",
			"reasoning_details":[{"type":"reasoning.summary","summary":"checked"}]
		}`),
	)
	prepared, err := (Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "gpt-test",
	}).Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		Messages: []modelcontext.Message{
			{Sequence: 1, Role: modelprotocol.RoleUser, Content: json.RawMessage(`[{
				"type":"text","text":"continue"
			}]`)},
			{
				Sequence:             2,
				Role:                 modelprotocol.RoleAssistant,
				ModelCallContextID:   "mcc_reasoning",
				Content:              json.RawMessage(`[{"type":"reasoning","text":"checked"}]`),
				ProviderReplay:       replay.payload,
				ProviderReplaySource: replay.source,
			},
		},
	}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Messages []struct {
			Role             string            `json:"role"`
			Content          json.RawMessage   `json:"content"`
			ReasoningDetails []json.RawMessage `json:"reasoning_details"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Messages) != 2 ||
		payload.Messages[1].Role != string(chatRoleAssistant) ||
		len(payload.Messages[1].ReasoningDetails) != 1 {
		t.Fatalf("reasoning-only assistant replay is invalid: %s", prepared.Body)
	}
}

func TestPrepareSuppressesRejectedReplayAndRebuildsCanonicalToolExchange(t *testing.T) {
	replay := testProviderReplay(
		"gpt-test",
		modelprotocol.APIFormatOpenAIChatCompletions,
		modelprotocol.APIVariantDefault,
		json.RawMessage(`{
			"role":"assistant",
			"reasoning_content":"private reasoning",
			"content":"visible reply",
			"tool_calls":[{
				"id":"call_1",
				"type":"function",
				"function":{"name":"run_command","arguments":"{\"command\":\"true\"}"}
			}]
		}`),
	)
	message := withToolCallLinks(modelcontext.Message{
		Sequence:           1,
		Role:               modelprotocol.RoleAssistant,
		ModelCallContextID: "mcc_1",
		Content: json.RawMessage(`[
			{"type":"reasoning","text":"private reasoning"},
			{"type":"text","text":"visible reply"}
		]`),
		ProviderReplay:       replay.payload,
		ProviderReplaySource: replay.source,
	}, "tcl_1")
	prepared, err := (Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "gpt-test",
	}).Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{message},
			ToolResults: []modelcontext.ToolResultRef{{
				ToolCallID:         "tcl_1",
				ModelCallContextID: "mcc_1",
				ProviderCallID:     "call_1",
				Name:               "run_command",
				Input:              json.RawMessage(`{"command":"true"}`),
				ContentParts:       json.RawMessage(`[{"type":"text","text":"done"}]`),
			}},
		},
		Policy: model.RequestPolicy{ProviderReplayCutoffEventSequence: message.Sequence},
	})
	if err != nil {
		t.Fatalf("prepare with replay suppressed: %v", err)
	}
	body := string(prepared.Body)
	if strings.Contains(body, "private reasoning") ||
		strings.Contains(body, "reasoning_content") {
		t.Fatalf("suppressed replay leaked provider-only reasoning: %s", body)
	}
	if !strings.Contains(body, "visible reply") ||
		!strings.Contains(body, `"id":"call_1"`) ||
		!strings.Contains(body, `"tool_call_id":"call_1"`) ||
		!strings.Contains(body, "done") {
		t.Fatalf("canonical tool exchange was not preserved: %s", body)
	}
}

func TestPrepareAppliesProviderReplayCutoffPerMessage(t *testing.T) {
	oldReplay := testProviderReplay(
		"gpt-test",
		modelprotocol.APIFormatOpenAIChatCompletions,
		modelprotocol.APIVariantOpenRouter,
		json.RawMessage(`{
			"role":"assistant",
			"content":"old answer",
			"reasoning_details":[{"type":"opaque","data":"old-reasoning-replay"}]
		}`),
	)
	newReplay := testProviderReplay(
		"gpt-test",
		modelprotocol.APIFormatOpenAIChatCompletions,
		modelprotocol.APIVariantOpenRouter,
		json.RawMessage(`{
			"role":"assistant",
			"content":"new answer",
			"reasoning_details":[{"type":"opaque","data":"new-reasoning-replay"}]
		}`),
	)
	oldMessage := chatReplayMessage("mcc_old", oldReplay)
	oldMessage.Content = json.RawMessage(`[{"type":"text","text":"old answer"}]`)
	newMessage := chatReplayMessage("mcc_new", newReplay)
	newMessage.Sequence = 2
	newMessage.Content = json.RawMessage(`[{"type":"text","text":"new answer"}]`)

	prepared, err := (Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "gpt-test",
		APIVariant:            modelprotocol.APIVariantOpenRouter,
	}).Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{Messages: []modelcontext.Message{oldMessage, newMessage}},
		Policy:  model.RequestPolicy{ProviderReplayCutoffEventSequence: 1},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	if strings.Contains(body, "old-reasoning-replay") ||
		!strings.Contains(body, "old answer") ||
		!strings.Contains(body, "new-reasoning-replay") {
		t.Fatalf("provider replay cutoff was not applied per message: %s", body)
	}
}

type cacheControlMark struct {
	role    string
	index   int
	control chatCacheControl
}

func cacheControlMarks(t *testing.T, body []byte) []cacheControlMark {
	t.Helper()
	var payload struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var marks []cacheControlMark
	for index, message := range payload.Messages {
		var blocks []struct {
			Type         string            `json:"type"`
			CacheControl *chatCacheControl `json:"cache_control"`
		}
		if json.Unmarshal(message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			if block.CacheControl != nil {
				marks = append(marks, cacheControlMark{
					role:    message.Role,
					index:   index,
					control: *block.CacheControl,
				})
			}
		}
	}
	return marks
}

func TestPrepareDefaultsOpenRouterCacheControlForClaudeModels(t *testing.T) {
	for _, tc := range []struct {
		name      string
		retention model.CacheRetention
		wantCache bool
	}{
		{name: "unset", retention: model.CacheRetentionUnset, wantCache: true},
		{name: "none", retention: model.CacheRetentionNone, wantCache: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := Client{EndpointPath: testEndpointPath,
				ProviderModelSlug: "anthropic/claude-sonnet-4",
				APIVariant:        modelprotocol.APIVariantOpenRouter,
			}
			prepared, err := client.Prepare(context.Background(), model.PrepareInput{
				Context: modelcontext.Bundle{
					Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser,
						Content: json.RawMessage(`[{"type":"text","text":"hi"}]`),
					}},
				},
				Policy: model.RequestPolicy{CacheRetention: tc.retention},
			})
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			marks := cacheControlMarks(t, prepared.Body)
			if !tc.wantCache {
				if len(marks) != 0 {
					t.Fatalf("cache_control should be omitted: %s", prepared.Body)
				}
				return
			}
			if len(marks) != 1 || marks[0].role != "user" ||
				marks[0].control.Type != "ephemeral" || marks[0].control.TTL != "" {
				t.Fatalf("cache_control marks = %+v, want ephemeral without ttl on the user block: %s", marks, prepared.Body)
			}
		})
	}
}

func TestPrepareMarksOpenRouterCacheBreakpointsOnSystemAndLastMessage(t *testing.T) {
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "anthropic/claude-sonnet-4",
		APIVariant:            modelprotocol.APIVariantOpenRouter,
	}
	replay := testProviderReplay(
		"anthropic/claude-sonnet-4",
		modelprotocol.APIFormatOpenAIChatCompletions,
		modelprotocol.APIVariantOpenRouter,
		json.RawMessage(`{
			"role":"assistant",
			"content":"replayed",
			"tool_calls":[{"id":"call_1","type":"function","function":{"name":"run_command","arguments":"{}"}}]
		}`),
	)
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		SystemPrompt: "system prompt",
		Messages: []modelcontext.Message{
			{Role: modelprotocol.RoleUser, Sequence: 10, Content: json.RawMessage(`[{"type":"text","text":"start"}]`)},
			withToolCallLinks(modelcontext.Message{
				Role:                 modelprotocol.RoleAssistant,
				Sequence:             20,
				ModelCallContextID:   "mcc_1",
				Content:              json.RawMessage(`[{"type":"text","text":"replayed"}]`),
				ProviderReplay:       replay.payload,
				ProviderReplaySource: replay.source,
			}, "tcl_1"),
		},
		ToolResults: []modelcontext.ToolResultRef{{
			ToolCallID:          "tcl_1",
			ModelCallContextID:  "mcc_1",
			ProviderCallID:      "call_1",
			Name:                "run_command",
			Input:               json.RawMessage(`{}`),
			ContentParts:        json.RawMessage(`[{"type":"text","text":"done"}]`),
			SourceEventSequence: 20,
			ResultEventSequence: 30,
		}},
	}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	marks := cacheControlMarks(t, prepared.Body)
	if len(marks) != 2 ||
		marks[0].role != "system" || marks[0].index != 0 ||
		marks[1].role != "tool" || marks[1].index != 3 ||
		marks[0].control.Type != "ephemeral" || marks[0].control.TTL != "" {
		t.Fatalf("cache_control marks = %+v, want system and last tool message: %s", marks, prepared.Body)
	}
}

func TestPrepareMarksOpenRouterCacheBreakpointBeforeTrailingSystemContext(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath,
		ProviderModelSlug: "anthropic/claude-sonnet-4",
		APIVariant:        modelprotocol.APIVariantOpenRouter,
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		SystemPrompt: "system prompt",
		Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser,
			Content: json.RawMessage(`[{"type":"text","text":"hi"}]`),
		}},
		ToolSpecs: []modelcontext.ToolSpec{
			{Name: toolcatalog.ToolNameCreateMachine},
			{Name: toolcatalog.ToolNameSendIntegrationMessage},
		},
	}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	marks := cacheControlMarks(t, prepared.Body)
	if len(marks) != 2 ||
		marks[0].role != "system" || marks[0].index != 0 ||
		marks[1].role != "user" || marks[1].index != 1 {
		t.Fatalf("cache_control marks = %+v, want system prompt and last user turn: %s", marks, prepared.Body)
	}
}

func TestPrepareWalksPastTrailingReplayedAssistantForOpenRouterCacheBreakpoint(t *testing.T) {
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "anthropic/claude-sonnet-4",
		APIVariant:            modelprotocol.APIVariantOpenRouter,
	}
	replay := testProviderReplay(
		"anthropic/claude-sonnet-4",
		modelprotocol.APIFormatOpenAIChatCompletions,
		modelprotocol.APIVariantOpenRouter,
		json.RawMessage(`{"role":"assistant","content":"replayed"}`),
	)
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		SystemPrompt: "system prompt",
		Messages: []modelcontext.Message{
			{Role: modelprotocol.RoleUser, Sequence: 10, Content: json.RawMessage(`[{"type":"text","text":"start"}]`)},
			{
				Role:                 modelprotocol.RoleAssistant,
				Sequence:             20,
				Content:              json.RawMessage(`[{"type":"text","text":"replayed"}]`),
				ProviderReplay:       replay.payload,
				ProviderReplaySource: replay.source,
			},
		},
	}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !strings.Contains(string(prepared.Body), `"content":"replayed"`) {
		t.Fatalf("assistant turn was not replayed: %s", prepared.Body)
	}
	marks := cacheControlMarks(t, prepared.Body)
	if len(marks) != 2 ||
		marks[0].role != "system" || marks[0].index != 0 ||
		marks[1].role != "user" || marks[1].index != 1 {
		t.Fatalf(
			"cache_control marks = %+v, want system and the user turn before the replayed assistant: %s",
			marks,
			prepared.Body,
		)
	}
}

func TestPrepareSendsConversationKeyByRoute(t *testing.T) {
	agentID := uuid.MustParse("0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b")
	for _, tc := range []struct {
		name               string
		client             Client
		retention          model.CacheRetention
		wantSessionID      bool
		wantPromptCacheKey bool
	}{
		{
			name: "openrouter automatic model",
			client: Client{
				EndpointPath:      testEndpointPath,
				ProviderModelSlug: "moonshotai/kimi-k3",
				APIVariant:        modelprotocol.APIVariantOpenRouter,
			},
			wantSessionID: true,
		},
		{
			name: "openrouter long keeps session id without retention field",
			client: Client{
				EndpointPath:      testEndpointPath,
				ProviderModelSlug: "deepseek/deepseek-v4",
				APIVariant:        modelprotocol.APIVariantOpenRouter,
			},
			retention:     model.CacheRetentionLong,
			wantSessionID: true,
		},
		{
			name: "openrouter none",
			client: Client{
				EndpointPath:      testEndpointPath,
				ProviderModelSlug: "anthropic/claude-sonnet-4",
				APIVariant:        modelprotocol.APIVariantOpenRouter,
			},
			retention: model.CacheRetentionNone,
		},
		{
			name:               "openai default base url",
			client:             Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"},
			wantPromptCacheKey: true,
		},
		{
			name:               "openai long",
			client:             Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"},
			retention:          model.CacheRetentionLong,
			wantPromptCacheKey: true,
		},
		{
			name: "openai-compatible host",
			client: Client{
				EndpointPath:      testEndpointPath,
				BaseURL:           "https://api.deepseek.com/v1",
				ProviderModelSlug: "deepseek-chat",
			},
			retention: model.CacheRetentionLong,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepared, err := tc.client.Prepare(context.Background(), model.PrepareInput{
				Context: modelcontext.Bundle{
					AgentID: agentID,
					Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser,
						Content: json.RawMessage(`[{"type":"text","text":"hi"}]`),
					}},
				},
				Policy: model.RequestPolicy{CacheRetention: tc.retention},
			})
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			var payload struct {
				SessionID      string `json:"session_id"`
				PromptCacheKey string `json:"prompt_cache_key"`
			}
			if err := json.Unmarshal(prepared.Body, &payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			wantSessionID, wantPromptCacheKey := "", ""
			if tc.wantSessionID {
				wantSessionID = agentID.String()
			}
			if tc.wantPromptCacheKey {
				wantPromptCacheKey = agentID.String()
			}
			if payload.SessionID != wantSessionID {
				t.Fatalf("session_id = %q, want %q: %s", payload.SessionID, wantSessionID, prepared.Body)
			}
			if payload.PromptCacheKey != wantPromptCacheKey {
				t.Fatalf("prompt_cache_key = %q, want %q: %s", payload.PromptCacheKey, wantPromptCacheKey, prepared.Body)
			}
			if strings.Contains(string(prepared.Body), "prompt_cache_retention") {
				t.Fatalf("prompt_cache_retention must never be sent: %s", prepared.Body)
			}
		})
	}
}

func TestPrepareOmitsConversationKeyWithoutAgent(t *testing.T) {
	client := Client{
		EndpointPath:      testEndpointPath,
		ProviderModelSlug: "moonshotai/kimi-k3",
		APIVariant:        modelprotocol.APIVariantOpenRouter,
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser,
			Content: json.RawMessage(`[{"type":"text","text":"hi"}]`),
		}},
	}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if strings.Contains(string(prepared.Body), "session_id") {
		t.Fatalf("session_id must be omitted without an agent: %s", prepared.Body)
	}
}

func TestPrepareOpenRouterLongRetentionUsesOneHourTTL(t *testing.T) {
	client := Client{
		EndpointPath:      testEndpointPath,
		ProviderModelSlug: "anthropic/claude-sonnet-4",
		APIVariant:        modelprotocol.APIVariantOpenRouter,
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "system prompt",
			Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser,
				Content: json.RawMessage(`[{"type":"text","text":"hi"}]`),
			}},
		},
		Policy: model.RequestPolicy{CacheRetention: model.CacheRetentionLong},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	marks := cacheControlMarks(t, prepared.Body)
	if len(marks) != 2 || marks[0].control.TTL != "1h" || marks[1].control.TTL != "1h" {
		t.Fatalf("cache_control marks = %+v, want two 1h breakpoints: %s", marks, prepared.Body)
	}
}

func TestPrepareFallsBackToLastMarkableMessageForOpenRouterCacheBreakpoint(t *testing.T) {
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "anthropic/claude-sonnet-4",
		APIVariant:            modelprotocol.APIVariantOpenRouter,
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		SystemPrompt: "system prompt",
		Messages: []modelcontext.Message{
			{Role: modelprotocol.RoleUser, Sequence: 10, Content: json.RawMessage(`[{"type":"text","text":"start"}]`)},
			messageAtSequence(assistantToolCallMessage("mcc_1", "tcl_1"), 20),
		},
		ToolResults: []modelcontext.ToolResultRef{{
			ToolCallID:          "tcl_1",
			ModelCallContextID:  "mcc_1",
			ProviderCallID:      "call_1",
			Name:                "run_command",
			Input:               json.RawMessage(`{}`),
			ContentParts:        json.RawMessage(`[]`),
			SourceEventSequence: 20,
			ResultEventSequence: 30,
		}},
	}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	marks := cacheControlMarks(t, prepared.Body)
	if len(marks) != 2 ||
		marks[0].role != "system" || marks[0].index != 0 ||
		marks[1].role != "user" || marks[1].index != 1 {
		t.Fatalf(
			"cache_control marks = %+v, want system and the user turn before the empty tool result: %s",
			marks,
			prepared.Body,
		)
	}
}

func TestPrepareLetsAPIVariantOptionsOverrideConversationKey(t *testing.T) {
	client := Client{
		EndpointPath:      testEndpointPath,
		ProviderModelSlug: "moonshotai/kimi-k3",
		APIVariant:        modelprotocol.APIVariantOpenRouter,
		APIVariantOptions: json.RawMessage(`{"session_id":"pinned-by-operator"}`),
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		AgentID: uuid.MustParse("0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"),
		Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser,
			Content: json.RawMessage(`[{"type":"text","text":"hi"}]`),
		}},
	}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.SessionID != "pinned-by-operator" {
		t.Fatalf("session_id = %q, want the explicit provider option to win: %s", payload.SessionID, prepared.Body)
	}
}

func TestPrepareSuppressesGeneratedAffinityWhenAnyNativeAffinityIsConfigured(t *testing.T) {
	client := Client{
		EndpointPath:      testEndpointPath,
		ProviderModelSlug: "moonshotai/kimi-k3",
		APIVariant:        modelprotocol.APIVariantOpenRouter,
		APIVariantOptions: json.RawMessage(`{"prompt_cache_key":"pinned-by-operator"}`),
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		AgentID: uuid.MustParse("0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"),
		Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser,
			Content: json.RawMessage(`[{"type":"text","text":"hi"}]`),
		}},
	}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		SessionID      string `json:"session_id"`
		PromptCacheKey string `json:"prompt_cache_key"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.SessionID != "" || payload.PromptCacheKey != "pinned-by-operator" {
		t.Fatalf("payload = %+v, want only the operator's prompt_cache_key: %s", payload, prepared.Body)
	}
}

func TestPrepareMarksOpenRouterExplicitFiveMinuteModelsWithoutTTL(t *testing.T) {
	client := Client{
		EndpointPath:      testEndpointPath,
		ProviderModelSlug: "qwen/qwen3-coder-plus",
		APIVariant:        modelprotocol.APIVariantOpenRouter,
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "system prompt",
			Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser,
				Content: json.RawMessage(`[{"type":"text","text":"hi"}]`),
			}},
		},
		Policy: model.RequestPolicy{CacheRetention: model.CacheRetentionLong},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	marks := cacheControlMarks(t, prepared.Body)
	if len(marks) != 2 || marks[0].control.TTL != "" || marks[1].control.TTL != "" {
		t.Fatalf("cache_control marks = %+v, want two default-ttl breakpoints: %s", marks, prepared.Body)
	}
}
