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

func TestPrepareAppliesProviderReplayCutoffPerMessage(t *testing.T) {
	oldReplay := testProviderReplay(
		"gpt-test",
		modelprotocol.APIFormatOpenAIResponses,
		json.RawMessage(`[
			{"id":"rs_old","type":"reasoning","encrypted_content":"old-encrypted-replay"},
			{"id":"msg_old","type":"message","role":"assistant","content":[{"type":"output_text","text":"old answer"}]}
		]`),
	)
	newReplay := testProviderReplay(
		"gpt-test",
		modelprotocol.APIFormatOpenAIResponses,
		json.RawMessage(`[
			{"id":"rs_new","type":"reasoning","encrypted_content":"new-encrypted-replay"},
			{"id":"msg_new","type":"message","role":"assistant","content":[{"type":"output_text","text":"new answer"}]}
		]`),
	)
	oldMessage := openAIReplayMessage("mcc_old", oldReplay)
	oldMessage.Content = json.RawMessage(`[{"type":"text","text":"old answer"}]`)
	newMessage := openAIReplayMessage("mcc_new", newReplay)
	newMessage.Sequence = 2
	newMessage.Content = json.RawMessage(`[{"type":"text","text":"new answer"}]`)

	prepared, err := (Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "gpt-test",
	}).Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{Messages: []modelcontext.Message{oldMessage, newMessage}},
		Policy:  model.RequestPolicy{ProviderReplayCutoffEventSequence: 1},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	if strings.Contains(body, "old-encrypted-replay") ||
		!strings.Contains(body, "old answer") ||
		!strings.Contains(body, "new-encrypted-replay") {
		t.Fatalf("provider replay cutoff was not applied per message: %s", body)
	}
}

func TestPreparePreservesFunctionCallBeforeAssistantMessage(t *testing.T) {
	replay := testProviderReplay(
		"gpt-test",
		modelprotocol.APIFormatOpenAIResponses,
		json.RawMessage(`[
			{
				"id":"fc_1","type":"function_call","call_id":"call_1","name":"run_command",
				"arguments":"{\"command\":\"true\"}","status":"completed"
			},
			{
				"id":"msg_1","type":"message","role":"assistant","status":"completed",
				"content":[{"type":"output_text","text":"after the call"}]
			}
		]`),
	)
	for _, test := range []struct {
		name   string
		replay *providerReplayFixture
	}{
		{name: "compatible replay", replay: &replay},
		{name: "canonical rebuild"},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := modelcontext.Message{
				Role:               modelprotocol.RoleAssistant,
				Sequence:           1,
				ModelCallContextID: "mcc_1",
				Content: json.RawMessage(`[
					{"type":"tool_call","tool_call_id":"tcl_1"},
					{"type":"text","text":"after the call"}
				]`),
			}
			if test.replay != nil {
				message.ProviderReplay = test.replay.payload
				message.ProviderReplaySource = test.replay.source
			}
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
			})
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			var payload struct {
				Input []json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(prepared.Body, &payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			if len(payload.Input) != 3 ||
				!strings.Contains(string(payload.Input[0]), `"type":"function_call"`) ||
				!strings.Contains(string(payload.Input[1]), `"role":"assistant"`) ||
				!strings.Contains(string(payload.Input[2]), `"type":"function_call_output"`) {
				t.Fatalf("assistant item order changed: %s", prepared.Body)
			}
		})
	}
}

func TestPrepareReplaysEncryptedReasoningAndProviderOnlyItemsInOrder(t *testing.T) {
	replay := testProviderReplay(
		"gpt-test",
		modelprotocol.APIFormatOpenAIResponses,
		json.RawMessage(`[
			{"id":"rs_1","type":"reasoning","encrypted_content":"enc_1"},
			{"id":"ws_1","type":"web_search_call","status":"completed"},
			{
				"id":"msg_1","type":"message","role":"assistant","status":"completed",
				"content":[{"type":"output_text","text":"I checked."}]
			},
			{
				"id":"fc_1","type":"function_call","call_id":"call_1","name":"run_command",
				"arguments":"{\"command\":\"true\"}","status":"completed"
			}
		]`),
	)
	message := withToolCallLinks(openAIReplayMessage("mcc_1", replay), "tcl_1")
	message.Content = json.RawMessage(`[
		{"type":"text","text":"I checked."},
		{"type":"tool_call","tool_call_id":"tcl_1"}
	]`)
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
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Input []struct {
			Type string `json:"type"`
		} `json:"input"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	want := []string{"reasoning", "web_search_call", "message", "function_call", "function_call_output"}
	if len(payload.Input) != len(want) {
		t.Fatalf("input count = %d, want %d: %s", len(payload.Input), len(want), prepared.Body)
	}
	for index := range want {
		if payload.Input[index].Type != want[index] {
			t.Fatalf("input[%d].type = %q, want %q: %s", index, payload.Input[index].Type, want[index], prepared.Body)
		}
	}
}

func TestPrepareFallsBackWhenReplayDoesNotMatchCanonicalOutput(t *testing.T) {
	replay := testProviderReplay(
		"gpt-test",
		modelprotocol.APIFormatOpenAIResponses,
		json.RawMessage(`[
			{
				"id":"fc_stale","type":"function_call","call_id":"call_stale","name":"run_command",
				"arguments":"{\"command\":\"wrong\"}","status":"completed"
			}
		]`),
	)
	message := withToolCallLinks(openAIReplayMessage("mcc_1", replay), "tcl_1")
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
				ProviderCallID:     "call_right",
				Name:               "run_command",
				Input:              json.RawMessage(`{"command":"right"}`),
				ContentParts:       json.RawMessage(`[{"type":"text","text":"done"}]`),
			}},
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	if strings.Contains(body, "call_stale") ||
		strings.Contains(body, `\"command\":\"wrong\"`) ||
		!strings.Contains(body, `"call_id":"call_right"`) {
		t.Fatalf("semantic mismatch did not use canonical fallback: %s", body)
	}
}

func TestPrepareFallsBackForMalformedProviderOnlyReplay(t *testing.T) {
	replay := testProviderReplay(
		"gpt-test",
		modelprotocol.APIFormatOpenAIResponses,
		json.RawMessage(`[
			{"id":"opaque_1","type":"unknown_future_item"},
			{
				"id":"msg_1","type":"message","role":"assistant","status":"completed",
				"content":[{"type":"output_text","text":"canonical answer"}]
			}
		]`),
	)
	message := openAIReplayMessage("mcc_1", replay)
	message.Content = json.RawMessage(`[{"type":"text","text":"canonical answer"}]`)
	prepared, err := (Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "gpt-test",
	}).Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{Messages: []modelcontext.Message{message}},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	if strings.Contains(body, "unknown_future_item") ||
		!strings.Contains(body, "canonical answer") {
		t.Fatalf("malformed provider-only replay did not fall back: %s", body)
	}
}

func TestPrepareDropsDanglingEncryptedReasoningReplay(t *testing.T) {
	replay := testProviderReplay(
		"gpt-test",
		modelprotocol.APIFormatOpenAIResponses,
		json.RawMessage(`[{"id":"rs_1","type":"reasoning","encrypted_content":"enc_1"}]`),
	)
	message := openAIReplayMessage("mcc_1", replay)
	message.Content = json.RawMessage(`[{"type":"text","text":"canonical answer"}]`)
	prepared, err := (Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "gpt-test",
	}).Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{Messages: []modelcontext.Message{message}},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	if strings.Contains(body, "enc_1") || !strings.Contains(body, "canonical answer") {
		t.Fatalf("dangling reasoning replay was not replaced canonically: %s", body)
	}
}

func TestPrepareIgnoresReplayFromDifferentProviderIdentity(t *testing.T) {
	replay := testProviderReplay(
		"gpt-old",
		modelprotocol.APIFormatOpenAIResponses,
		json.RawMessage(`[
			{
				"id":"msg_1","type":"message","role":"assistant","status":"completed",
				"content":[{"type":"output_text","text":"stale answer"}]
			}
		]`),
	)
	message := openAIReplayMessage("mcc_1", replay)
	message.Content = json.RawMessage(`[{"type":"text","text":"canonical answer"}]`)
	prepared, err := (Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "gpt-test",
	}).Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{Messages: []modelcontext.Message{message}},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	if strings.Contains(body, "stale answer") || !strings.Contains(body, "canonical answer") {
		t.Fatalf("different-identity replay was not rebuilt canonically: %s", body)
	}
}

func TestResponsesReplayRejectsNonObjectToolInput(t *testing.T) {
	_, ok := responseReplaySemantics([]json.RawMessage{
		json.RawMessage(
			`{"id":"fc_1","type":"function_call","call_id":"call_1","name":"parameterless_tool","arguments":"null"}`,
		),
	})
	if ok {
		t.Fatal("replay with non-object tool input was accepted")
	}
}

func TestResponsesReplayRequiresFunctionCallID(t *testing.T) {
	_, ok := responseReplaySemantics([]json.RawMessage{
		json.RawMessage(
			`{"id":"fc_1","type":"function_call","name":"lookup","arguments":"{}"}`,
		),
	})
	if ok {
		t.Fatal("replay without call_id was accepted using the item id")
	}
}
