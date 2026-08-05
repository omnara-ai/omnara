package openairesponses

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestPrepareBuildsStoredResponsesPayload(t *testing.T) {
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "gpt-5-mini",
	}
	replayItem := json.RawMessage(
		`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"run_command","arguments":"{\"command\":\"true\"}","status":"completed"}`,
	)
	replay := testProviderReplay(
		"gpt-5-mini",
		modelprotocol.APIFormatOpenAIResponses,
		json.RawMessage("["+string(replayItem)+"]"),
	)
	assistant := withToolCallLinks(openAIReplayMessage("mcc_1", replay), "call_1")
	assistant.Sequence = 2
	prepared, err := client.Prepare(
		context.Background(),
		model.PrepareInput{
			Context: modelcontext.Bundle{
				SystemPrompt: "sys",
				ContextCheckpoint: &modelcontext.CheckpointRef{
					ID: "ccp_1", Summary: "old summary",
				},
				Messages: []modelcontext.Message{
					{Sequence: 1, Role: modelprotocol.RoleUser, Content: json.RawMessage(`[{"type":"text","text":"hi"}]`)},
					assistant,
				},
				ToolSpecs: []modelcontext.ToolSpec{
					{Name: "run_command", Description: "run", InputSchema: json.RawMessage(`{"type":"object"}`)},
				},
				ToolResults: []modelcontext.ToolResultRef{
					{ToolCallID: "call_1",
						ModelCallContextID: "mcc_1",
						ProviderCallID:     "call_1",
						Name:               "run_command",
						Input:              json.RawMessage(`{"command":"true"}`),
						ContentParts:       json.RawMessage(`[{"type":"structured_data","value":{"ok":true}}]`),
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Model        string            `json:"model"`
		Instructions string            `json:"instructions"`
		Store        bool              `json:"store"`
		Include      []string          `json:"include"`
		Input        []json.RawMessage `json:"input"`
		Tools        []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode prepared payload: %v", err)
	}
	if payload.Model != "gpt-5-mini" ||
		!strings.HasPrefix(payload.Instructions, "sys\n\n") ||
		!strings.Contains(payload.Instructions, "<context_checkpoint>") ||
		payload.Store {
		t.Fatalf("unexpected payload header: %+v", payload)
	}
	if len(payload.Include) != 0 {
		t.Fatalf(
			"Responses payload should not request reasoning replay without a configured capability, got %+v",
			payload.Include,
		)
	}
	if len(payload.Tools) != 1 || payload.Tools[0].Type != "function" || payload.Tools[0].Name != "run_command" {
		t.Fatalf("unexpected tools: %+v", payload.Tools)
	}
	if len(payload.Input) != 4 {
		t.Fatalf(
			"expected checkpoint, message, replayed call, and call output, got %d: %s",
			len(payload.Input),
			prepared.Body,
		)
	}
	var checkpoint struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(payload.Input[0], &checkpoint); err != nil {
		t.Fatalf("decode checkpoint input: %v", err)
	}
	if checkpoint.Role != string(responsesRoleUser) ||
		checkpoint.Content != "<context_checkpoint>\nold summary\n</context_checkpoint>" ||
		strings.Contains(checkpoint.Content, "events") ||
		strings.Contains(checkpoint.Content, "1..10") {
		t.Fatalf("checkpoint provider input leaked event vocabulary: %+v", checkpoint)
	}
	var replayed struct {
		Type   string `json:"type"`
		ID     string `json:"id"`
		CallID string `json:"call_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(payload.Input[2], &replayed); err != nil {
		t.Fatalf("decode replay item: %v", err)
	}
	if replayed.Type != "function_call" ||
		replayed.ID != "fc_1" ||
		replayed.CallID != "call_1" ||
		replayed.Status != "completed" {
		t.Fatalf("provider replay item not preserved: %+v", replayed)
	}
	var output struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Output []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"output"`
	}
	if err := json.Unmarshal(payload.Input[3], &output); err != nil {
		t.Fatalf("decode output item: %v", err)
	}
	if output.Type != "function_call_output" || output.CallID != "call_1" {
		t.Fatalf("unexpected output item: %+v", output)
	}
	if len(output.Output) != 1 || output.Output[0].Type != "input_text" || output.Output[0].Text != `{"ok":true}` {
		t.Fatalf("function_call_output payload lost structured value: %s", payload.Input[3])
	}
}

func TestPrepareOmitsEncryptedReasoningForNonReasoningModel(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-4.1-mini"}
	prepared, err := client.Prepare(
		context.Background(),
		model.PrepareInput{
			Context: modelcontext.Bundle{
				Messages: []modelcontext.Message{
					{Sequence: 1, Role: modelprotocol.RoleUser, Content: json.RawMessage(`[{"type":"text","text":"hi"}]`)},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Include []string `json:"include"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode prepared payload: %v", err)
	}
	if len(payload.Include) != 0 {
		t.Fatalf("non-reasoning OpenAI model must not request encrypted reasoning content, got %+v", payload.Include)
	}
}

func TestPrepareToolControlsFollowToolPresence(t *testing.T) {
	for _, test := range []struct {
		name      string
		toolSpecs []modelcontext.ToolSpec
		want      bool
	}{
		{name: "without tools"},
		{
			name: "with tools",
			toolSpecs: []modelcontext.ToolSpec{{
				Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`),
			}},
			want: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := Client{
				EndpointPath:      testEndpointPath,
				ProviderModelSlug: "gpt-test",
				APIVariantOptions: json.RawMessage(
					`{"tool_choice":"required","parallel_tool_calls":true}`,
				),
			}
			prepared, err := client.Prepare(context.Background(), model.PrepareInput{
				Context: modelcontext.Bundle{
					Messages: []modelcontext.Message{{
						Sequence: 1,
						Role:     modelprotocol.RoleUser,
						Content:  json.RawMessage(`[{"type":"text","text":"hello"}]`),
					}},
					ToolSpecs: test.toolSpecs,
				},
			})
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(prepared.Body, &payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			_, hasChoice := payload["tool_choice"]
			_, hasParallel := payload["parallel_tool_calls"]
			if hasChoice != test.want || hasParallel != test.want {
				t.Fatalf("tool controls presence = (%v, %v), want %v: %s", hasChoice, hasParallel, test.want, prepared.Body)
			}
			if test.want && (string(payload["tool_choice"]) != `"auto"` || string(payload["parallel_tool_calls"]) != `true`) {
				t.Fatalf("tool controls = (%s, %s), want adapter defaults", payload["tool_choice"], payload["parallel_tool_calls"])
			}
		})
	}
}

func TestPrepareRejectsToolsWhenModelDoesNotSupportTools(t *testing.T) {
	supportsTools := false
	client := Client{
		EndpointPath:      testEndpointPath,
		ProviderModelSlug: "gpt-test",
		ModelCapabilities: model.Capabilities{SupportsTools: &supportsTools},
	}
	_, err := client.Prepare(
		context.Background(),
		model.PrepareInput{
			Context: modelcontext.Bundle{
				ToolSpecs: []modelcontext.ToolSpec{{Name: "run_command", InputSchema: json.RawMessage(`{"type":"object"}`)}},
			},
		},
	)
	if err == nil {
		t.Fatal("expected tool-using request to be rejected")
	}
	if !strings.Contains(err.Error(), "does not support tools") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareUsesPolicyToolSupportOverLiveCapabilities(t *testing.T) {
	liveSupportsTools := false
	policySupportsTools := true
	client := Client{EndpointPath: testEndpointPath,
		ProviderModelSlug: "gpt-test",
		ModelCapabilities: model.Capabilities{SupportsTools: &liveSupportsTools},
	}
	if _, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			ToolSpecs: []modelcontext.ToolSpec{{Name: "run_command", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		},
		Policy: model.RequestPolicy{SupportsTools: &policySupportsTools},
	}); err != nil {
		t.Fatalf("policy supports_tools should override live capabilities: %v", err)
	}

	liveSupportsTools = true
	policySupportsTools = false
	client = Client{EndpointPath: testEndpointPath,
		ProviderModelSlug: "gpt-test",
		ModelCapabilities: model.Capabilities{SupportsTools: &liveSupportsTools},
	}
	if _, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			ToolSpecs: []modelcontext.ToolSpec{{Name: "run_command", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		},
		Policy: model.RequestPolicy{SupportsTools: &policySupportsTools},
	}); err == nil || !strings.Contains(err.Error(), "does not support tools") {
		t.Fatalf("policy supports_tools=false should reject tools, got %v", err)
	}
}

func TestPrepareRequestsEncryptedReasoningForReasoningModel(t *testing.T) {
	supportsReasoning := true
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{
				{Sequence: 1, Role: modelprotocol.RoleUser, Content: json.RawMessage(`[{"type":"text","text":"hi"}]`)},
			},
		},
		Policy: model.RequestPolicy{SupportsReasoning: &supportsReasoning, DefaultReasoningEffort: "high"},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Include   []string `json:"include"`
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		Store bool `json:"store"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode prepared payload: %v", err)
	}
	if payload.Store {
		t.Fatalf("expected stateless Responses request")
	}
	if len(payload.Include) != 1 || payload.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("reasoning model should request encrypted reasoning content, got %+v", payload.Include)
	}
	if payload.Reasoning.Effort != "high" {
		t.Fatalf("reasoning effort = %q, want high", payload.Reasoning.Effort)
	}
}

func TestPrepareLowersCacheRetentionToPromptCacheRetention(t *testing.T) {
	tests := []struct {
		name        string
		retention   model.CacheRetention
		wantValue   string
		wantPresent bool
	}{
		{name: "none", retention: model.CacheRetentionNone},
		{name: "short", retention: model.CacheRetentionShort},
		{name: "long", retention: model.CacheRetentionLong, wantValue: "24h", wantPresent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
			prepared, err := client.Prepare(context.Background(), model.PrepareInput{
				Context: modelcontext.Bundle{
					Messages: []modelcontext.Message{
						{Sequence: 1, Role: modelprotocol.RoleUser, Content: json.RawMessage(`[{"type":"text","text":"hi"}]`)},
					},
				},
				Policy: model.RequestPolicy{CacheRetention: tt.retention},
			})
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(prepared.Body, &payload); err != nil {
				t.Fatalf("decode prepared payload: %v", err)
			}
			raw, ok := payload["prompt_cache_retention"]
			if ok != tt.wantPresent {
				t.Fatalf("prompt_cache_retention present = %v, want %v; body=%s", ok, tt.wantPresent, prepared.Body)
			}
			if !ok {
				return
			}
			var got string
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("decode prompt_cache_retention: %v", err)
			}
			if got != tt.wantValue {
				t.Fatalf("prompt_cache_retention = %q, want %q", got, tt.wantValue)
			}
		})
	}
}

func TestPrepareMergesAPIVariantOptions(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath,
		ProviderModelSlug: "gpt-test",
		APIVariantOptions: json.RawMessage(
			`{"temperature":0.2,"top_k":40,"stream":true,"model":"override","input":"override","store":true}`,
		),
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{
				{Sequence: 1, Role: modelprotocol.RoleUser, Content: json.RawMessage(`[{"type":"text","text":"hi"}]`)},
			},
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode prepared payload: %v", err)
	}
	if string(payload["temperature"]) != `0.2` || string(payload["top_k"]) != `40` {
		t.Fatalf("api_variant_options were not passed through: %s", prepared.Body)
	}
	if string(payload["store"]) != `true` {
		t.Fatalf("store = %s, want api_variant_options override", payload["store"])
	}
	if string(payload["stream"]) != `true` {
		t.Fatalf("prepared request should be the exact streaming wire request: %s", prepared.Body)
	}
	if string(payload["model"]) != `"gpt-test"` || len(payload["input"]) == 0 || string(payload["input"]) == `"override"` {
		t.Fatalf("adapter-owned fields were overwritten by api_variant_options: %s", prepared.Body)
	}
}

func TestPrepareKeepsResponsesReasoningIncludeOwned(t *testing.T) {
	supportsReasoning := true
	client := Client{EndpointPath: testEndpointPath,
		ProviderModelSlug: "gpt-test",
		APIVariantOptions: json.RawMessage(
			`{"include":["file_search_call.results"],"reasoning":{"effort":"low"}}`,
		),
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{
				{Sequence: 1, Role: modelprotocol.RoleUser, Content: json.RawMessage(`[{"type":"text","text":"hi"}]`)},
			},
		},
		Policy: model.RequestPolicy{SupportsReasoning: &supportsReasoning, DefaultReasoningEffort: "high"},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode prepared payload: %v", err)
	}
	if string(payload["include"]) != `["reasoning.encrypted_content"]` {
		t.Fatalf("include = %s, want adapter-owned reasoning include in %s", payload["include"], prepared.Body)
	}
	if string(payload["reasoning"]) != `{"effort":"high"}` {
		t.Fatalf("reasoning = %s, want adapter-owned reasoning effort in %s", payload["reasoning"], prepared.Body)
	}
}
