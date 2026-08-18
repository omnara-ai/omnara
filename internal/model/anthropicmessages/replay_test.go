package anthropicmessages

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestPreparePreservesToolUseBeforeAssistantText(t *testing.T) {
	replay := testProviderReplay(
		"claude-test",
		modelprotocol.APIFormatAnthropicMessages,
		json.RawMessage(`[
			{"type":"tool_use","id":"toolu_1","name":"run_command","input":{"command":"true"}},
			{"type":"text","text":"after the call"}
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
				ProviderModelSlug:     "claude-test",
			}).Prepare(context.Background(), model.PrepareInput{
				Context: modelcontext.Bundle{
					Messages: []modelcontext.Message{message},
					ToolResults: []modelcontext.ToolResultRef{{
						ToolCallID:         "tcl_1",
						ModelCallContextID: "mcc_1",
						ProviderCallID:     "toolu_1",
						Name:               "run_command",
						Input:              json.RawMessage(`{"command":"true"}`),
						ContentParts:       json.RawMessage(`[{"type":"text","text":"done"}]`),
					}},
				},
				Policy: model.RequestPolicy{MaxOutputTokens: 64},
			})
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			var payload struct {
				Messages []struct {
					Role    string `json:"role"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(prepared.Body, &payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			assistant := payload.Messages[1]
			if assistant.Role != string(anthropicRoleAssistant) ||
				len(assistant.Content) != 2 ||
				assistant.Content[0].Type != "tool_use" ||
				assistant.Content[1].Type != "text" ||
				assistant.Content[1].Text != "after the call" {
				t.Fatalf("assistant order changed: %s", prepared.Body)
			}
		})
	}
}

func TestPrepareFallsBackWhenReplaySemanticsDiffer(t *testing.T) {
	replay := testProviderReplay(
		"claude-test",
		modelprotocol.APIFormatAnthropicMessages,
		json.RawMessage(`[
			{"type":"tool_use","id":"toolu_stale","name":"run_command","input":{"command":"wrong"}}
		]`),
	)
	message := withToolCallLinks(anthropicReplayMessage("mcc_1", replay), "tcl_1")
	prepared, err := (Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "claude-test",
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
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	if strings.Contains(body, "wrong") || !strings.Contains(body, `"command":"right"`) {
		t.Fatalf("semantic mismatch did not use canonical fallback: %s", body)
	}
}

func TestPrepareReplaysThinkingAndToolUseAsOneValidatedOutput(t *testing.T) {
	replay := testProviderReplay(
		"claude-test",
		modelprotocol.APIFormatAnthropicMessages,
		json.RawMessage(`[
			{"type":"thinking","thinking":"reasoning step","signature":"sig_1"},
			{"type":"tool_use","id":"toolu_replayed","name":"run_command","input":{"command":"true"}}
		]`),
	)
	message := withToolCallLinks(anthropicReplayMessage("mcc_1", replay), "tcl_1")
	message.Content = json.RawMessage(`[
		{"type":"reasoning","text":"reasoning step"},
		{"type":"tool_call","tool_call_id":"tcl_1"}
	]`)
	prepared, err := (Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "claude-test",
	}).Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{message},
			ToolResults: []modelcontext.ToolResultRef{{
				ToolCallID:         "tcl_1",
				ModelCallContextID: "mcc_1",
				ProviderCallID:     "toolu_replayed",
				Name:               "run_command",
				Input:              json.RawMessage(`{"command":"true"}`),
				ContentParts:       json.RawMessage(`[{"type":"text","text":"done"}]`),
			}},
		},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	if !strings.Contains(body, `"signature":"sig_1"`) ||
		!strings.Contains(body, `"id":"toolu_replayed"`) {
		t.Fatalf("compatible whole-output replay was not preserved: %s", body)
	}
}

func TestPrepareSuppressesRejectedReplayAndRebuildsCanonicalToolExchange(t *testing.T) {
	replay := testProviderReplay(
		"claude-test",
		modelprotocol.APIFormatAnthropicMessages,
		json.RawMessage(`[
			{"type":"thinking","thinking":"private reasoning","signature":"sig_rejected"},
			{"type":"text","text":"visible reply"},
			{"type":"tool_use","id":"toolu_replayed","name":"run_command","input":{"command":"true"}}
		]`),
	)
	message := withToolCallLinks(anthropicReplayMessage("mcc_1", replay), "tcl_1")
	message.Content = json.RawMessage(`[
		{"type":"reasoning","text":"private reasoning"},
		{"type":"text","text":"visible reply"},
		{"type":"tool_call","tool_call_id":"tcl_1"}
	]`)
	prepared, err := (Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "claude-test",
	}).Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{message},
			ToolResults: []modelcontext.ToolResultRef{{
				ToolCallID:         "tcl_1",
				ModelCallContextID: "mcc_1",
				ProviderCallID:     "toolu_replayed",
				Name:               "run_command",
				Input:              json.RawMessage(`{"command":"true"}`),
				ContentParts:       json.RawMessage(`[{"type":"text","text":"done"}]`),
			}},
		},
		Policy: model.RequestPolicy{
			MaxOutputTokens:                   64,
			ProviderReplayCutoffEventSequence: message.Sequence,
		},
	})
	if err != nil {
		t.Fatalf("prepare with replay suppressed: %v", err)
	}
	body := string(prepared.Body)
	if strings.Contains(body, "private reasoning") ||
		strings.Contains(body, "sig_rejected") ||
		strings.Contains(body, `"id":"toolu_replayed"`) {
		t.Fatalf("suppressed replay leaked provider-only data: %s", body)
	}
	if !strings.Contains(body, "visible reply") ||
		!strings.Contains(body, `"type":"tool_use"`) ||
		!strings.Contains(body, `"type":"tool_result"`) ||
		!strings.Contains(body, "done") {
		t.Fatalf("canonical tool exchange was not preserved: %s", body)
	}

	var payload struct {
		Messages []struct {
			Content []struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				ToolUseID string `json:"tool_use_id"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode prepared payload: %v", err)
	}
	var rebuiltToolUseID, resultToolUseID string
	for _, wireMessage := range payload.Messages {
		for _, block := range wireMessage.Content {
			switch block.Type {
			case "tool_use":
				rebuiltToolUseID = block.ID
			case "tool_result":
				resultToolUseID = block.ToolUseID
			}
		}
	}
	if rebuiltToolUseID == "" || resultToolUseID != rebuiltToolUseID {
		t.Fatalf(
			"rebuilt tool boundary tool_use=%q tool_result=%q: %s",
			rebuiltToolUseID,
			resultToolUseID,
			body,
		)
	}
}

func TestPrepareAppliesProviderReplayCutoffPerMessage(t *testing.T) {
	oldReplay := testProviderReplay(
		"claude-test",
		modelprotocol.APIFormatAnthropicMessages,
		json.RawMessage(`[
			{"type":"redacted_thinking","data":"old-redacted-replay"},
			{"type":"text","text":"old answer"}
		]`),
	)
	newReplay := testProviderReplay(
		"claude-test",
		modelprotocol.APIFormatAnthropicMessages,
		json.RawMessage(`[
			{"type":"redacted_thinking","data":"new-redacted-replay"},
			{"type":"text","text":"new answer"}
		]`),
	)
	oldMessage := anthropicReplayMessage("mcc_old", oldReplay)
	oldMessage.Content = json.RawMessage(`[{"type":"text","text":"old answer"}]`)
	newMessage := anthropicReplayMessage("mcc_new", newReplay)
	newMessage.Sequence = 2
	newMessage.Content = json.RawMessage(`[{"type":"text","text":"new answer"}]`)

	prepared, err := (Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "claude-test",
	}).Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{Messages: []modelcontext.Message{oldMessage, newMessage}},
		Policy: model.RequestPolicy{
			MaxOutputTokens:                   64,
			ProviderReplayCutoffEventSequence: 1,
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	if strings.Contains(body, "old-redacted-replay") ||
		!strings.Contains(body, "old answer") ||
		!strings.Contains(body, "new-redacted-replay") {
		t.Fatalf("provider replay cutoff was not applied per message: %s", body)
	}
}

func TestPrepareIgnoresReplayFromDifferentModel(t *testing.T) {
	replay := testProviderReplay(
		"claude-old",
		modelprotocol.APIFormatAnthropicMessages,
		json.RawMessage(`[
			{"type":"tool_use","id":"toolu_stale","name":"run_command","input":{"command":"stale"}}
		]`),
	)
	message := withToolCallLinks(anthropicReplayMessage("mcc_1", replay), "tcl_1")
	prepared, err := (Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "claude-test",
	}).Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{message},
			ToolResults: []modelcontext.ToolResultRef{{
				ToolCallID:         "tcl_1",
				ModelCallContextID: "mcc_1",
				ProviderCallID:     "call_1",
				Name:               "run_command",
				Input:              json.RawMessage(`{"command":"durable"}`),
				ContentParts:       json.RawMessage(`[{"type":"text","text":"done"}]`),
			}},
		},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	if strings.Contains(body, "stale") || !strings.Contains(body, `"command":"durable"`) {
		t.Fatalf("different-model replay was not rebuilt canonically: %s", body)
	}
}

func TestPrepareRebuiltToolUseIDsAreUniqueAcrossOutputs(t *testing.T) {
	first := assistantToolCallMessage("mcc_1", "tcl_1")
	first.Sequence = 1
	second := assistantToolCallMessage("mcc_2", "tcl_2")
	second.Sequence = 2
	prepared, err := (Client{
		EndpointPath:      testEndpointPath,
		ProviderModelSlug: "claude-test",
	}).Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{first, second},
			ToolResults: []modelcontext.ToolResultRef{
				{
					ToolCallID:         "tcl_1",
					ModelCallContextID: "mcc_1",
					ProviderCallID:     "call_same",
					Name:               "run_command",
					Input:              json.RawMessage(`{"command":"one"}`),
					ContentParts:       json.RawMessage(`[{"type":"text","text":"one"}]`),
				},
				{
					ToolCallID:         "tcl_2",
					ModelCallContextID: "mcc_2",
					ProviderCallID:     "call_same",
					Name:               "run_command",
					Input:              json.RawMessage(`{"command":"two"}`),
					ContentParts:       json.RawMessage(`[{"type":"text","text":"two"}]`),
				},
			},
		},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Messages []struct {
			Content []struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var ids []string
	for _, message := range payload.Messages {
		for _, block := range message.Content {
			if block.Type == "tool_use" {
				ids = append(ids, block.ID)
			}
		}
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("rebuilt tool-use IDs must be unique: ids=%v body=%s", ids, prepared.Body)
	}
}

func TestAnthropicReplayRejectsNonObjectToolInput(t *testing.T) {
	_, _, ok := anthropicReplaySemantics([]json.RawMessage{
		json.RawMessage(
			`{"type":"tool_use","id":"toolu_1","name":"parameterless_tool","input":null}`,
		),
	}, nil)
	if ok {
		t.Fatal("replay with non-object tool input was accepted")
	}
}
