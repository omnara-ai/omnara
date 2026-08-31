package openairesponses

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func openAITextMessage(role modelprotocol.MessageRole, text string) modelcontext.Message {
	return modelcontext.Message{
		Role:     role,
		Sequence: 1,
		Content:  json.RawMessage(`[{"type":"text","text":` + strconv.Quote(text) + `}]`),
	}
}

func TestPreparePassesThroughContextMessageRoles(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{Messages: []modelcontext.Message{
			{Sequence: 1, Role: modelprotocol.RoleUser, Content: json.RawMessage(`[{"type":"text","text":"hi"}]`)},
			{Sequence: 2, Role: modelprotocol.RoleAssistant, Content: json.RawMessage(`[{"type":"text","text":"hello"}]`)},
		}},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode prepared payload: %v", err)
	}
	if len(payload.Input) != 2 {
		t.Fatalf("input count = %d, want 2: %s", len(payload.Input), prepared.Body)
	}
	wantRoles := []string{string(responsesRoleUser), string(responsesRoleAssistant)}
	for i, want := range wantRoles {
		if payload.Input[i].Role != want {
			t.Fatalf(
				"input %d role = %q, want %q; payload=%s",
				i,
				payload.Input[i].Role,
				want,
				prepared.Body,
			)
		}
	}
	wantContentTypes := []string{
		string(responsesContentTypeInputText),
		string(responsesContentTypeOutputText),
	}
	for i, want := range wantContentTypes {
		if len(payload.Input[i].Content) != 1 || payload.Input[i].Content[0].Type != want {
			t.Fatalf("input %d content = %+v, want one %q part; payload=%s", i, payload.Input[i].Content, want, prepared.Body)
		}
	}
	if strings.Contains(string(prepared.Body), "Message from omnara_user") {
		t.Fatalf("human producer sender leaked into model content: %s", prepared.Body)
	}
}

func TestPreparePreservesCanonicalToolResultContent(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	canonicalValue := json.RawMessage(
		`{"answer":"visible answer","tool_call_id":"tcl_canonical","interaction_id":"int_canonical","model_call_context_id":"mcc_canonical","provider_metadata":{"raw":true},"provider_operation_id":"pop_canonical","machine_connection_id":"mcn_canonical","connector_installation_id":"cin_canonical","process_id":"prc_canonical","payload":{"visible":true,"lease_id":"lse_canonical"}}`,
	)
	prepared, err := client.Prepare(
		context.Background(),
		model.PrepareInput{Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{
				assistantToolCallMessage("mcc_internal", "tcl_internal"),
			},
			ToolResults: []modelcontext.ToolResultRef{
				{ToolCallID: "tcl_internal",
					ModelCallContextID: "mcc_internal",
					ProviderCallID:     "call_ordinary",
					Name:               "ask_question",
					Input:              json.RawMessage(`{"question":"continue?"}`),
					Outcome:            executionstore.ToolResultOutcomeSucceeded,
					ContentParts: json.RawMessage(
						`[{"type":"structured_data","value":{"outcome":"succeeded"}},{"type":"structured_data","value":` +
							string(canonicalValue) + `}]`,
					),
				},
			},
		}},
	)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Input []struct {
			Type   string `json:"type"`
			Output []struct {
				Text string `json:"text"`
			} `json:"output"`
		} `json:"input"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	for _, item := range payload.Input {
		if item.Type != "function_call_output" {
			continue
		}
		if len(item.Output) != 2 ||
			item.Output[0].Text != `{"outcome":"succeeded"}` ||
			item.Output[1].Text != string(canonicalValue) {
			t.Fatalf("canonical tool result content changed: %+v; payload=%s", item.Output, prepared.Body)
		}
		return
	}
	t.Fatalf("function_call_output not found in payload: %s", prepared.Body)
}

func TestPrepareIncludesAvailableMachinePoolsInProviderInput(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(
		context.Background(),
		model.PrepareInput{
			Context: modelcontext.Bundle{
				SystemPrompt:          "sys",
				ToolSpecs:             []modelcontext.ToolSpec{{Name: toolcatalog.ToolNameCreateMachine}},
				AvailableMachinePools: []modelcontext.MachinePoolRef{{MachinePoolName: "Build Pool", Description: "Build workers"}},
			},
		},
	)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Input []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode prepared payload: %v", err)
	}
	if len(payload.Input) != 1 || payload.Input[0].Role != string(responsesRoleSystem) {
		t.Fatalf("expected one system machine-pools input, got %+v", payload.Input)
	}
	for _, want := range []string{"Available machine pools", "create_machine", "machine_pool_name", "Build Pool"} {
		if !strings.Contains(payload.Input[0].Content, want) {
			t.Fatalf("machine pool content missing %q: %s", want, payload.Input[0].Content)
		}
	}
}

func TestPrepareExplainsWhenCreateMachineHasNoAvailablePools(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		SystemPrompt: "sys",
		ToolSpecs:    []modelcontext.ToolSpec{{Name: toolcatalog.ToolNameCreateMachine}},
	}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !strings.Contains(string(prepared.Body), "no machine pools are currently available") {
		t.Fatalf("missing empty machine-pool context: %s", prepared.Body)
	}
}

func TestPrepareIncludesIntegrationTargetsAtEndOfProviderInput(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		SystemPrompt: "sys",
		Messages:     []modelcontext.Message{openAITextMessage(modelprotocol.RoleUser, "latest user message")},
		ToolSpecs:    []modelcontext.ToolSpec{{Name: toolcatalog.ToolNameSendIntegrationMessage}},
		IntegrationTargets: []modelcontext.IntegrationTargetRef{{
			TargetRef:       "slack-abcd",
			DurableID:       "internal-target-id",
			Provider:        "slack",
			ProviderRefKind: "thread",
			Label:           "slack thread C123:1712345678.000100",
			IsCurrent:       true,
		}},
	}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Input []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode prepared payload: %v", err)
	}
	if len(payload.Input) != 2 {
		t.Fatalf("expected message and integration targets, got %d: %s", len(payload.Input), prepared.Body)
	}
	last := payload.Input[len(payload.Input)-1]
	var lastContent string
	if err := json.Unmarshal(last.Content, &lastContent); err != nil {
		t.Fatalf("system content not a string: %s", last.Content)
	}
	if last.Role != string(responsesRoleSystem) || !strings.Contains(lastContent, "External integration targets") ||
		!strings.Contains(lastContent, "slack-abcd") ||
		!strings.Contains(lastContent, `"is_current":true`) {
		t.Fatalf("expected integration targets as final provider input item, got %+v in %s", last, prepared.Body)
	}
	if strings.Contains(lastContent, "internal-target-id") {
		t.Fatalf("integration target content leaked durable id: %s", lastContent)
	}
}

func TestPrepareOmitsIntegrationTargetsForAskQuestion(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		SystemPrompt: "sys",
		Messages:     []modelcontext.Message{openAITextMessage(modelprotocol.RoleUser, "latest user message")},
		ToolSpecs:    []modelcontext.ToolSpec{{Name: toolcatalog.ToolNameAskQuestion}},
		IntegrationTargets: []modelcontext.IntegrationTargetRef{{
			TargetRef: "slack-abcd",
			Provider:  "slack",
		}},
	}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if strings.Contains(string(prepared.Body), "External integration targets") ||
		strings.Contains(string(prepared.Body), "slack-abcd") {
		t.Fatalf("integration target context leaked into ask_question request: %s", prepared.Body)
	}
}

func TestPrepareRejectsStructuredDataOutsideToolResults(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	content := json.RawMessage(
		`[{"type":"structured_data","value":{"visible":true,"raw":"user raw value","process_id":"prc_internal"}}]`,
	)
	_, err := client.Prepare(
		context.Background(),
		model.PrepareInput{
			Context: modelcontext.Bundle{
				SystemPrompt: "sys",
				Messages:     []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser, Content: content}},
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), `unsupported type "structured_data"`) {
		t.Fatalf("structured message content error = %v", err)
	}
}

func TestPreparePassesThroughAssistantMessageRole(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(
		context.Background(),
		model.PrepareInput{Context: modelcontext.Bundle{SystemPrompt: "sys", Messages: []modelcontext.Message{
			{Sequence: 1, Role: modelprotocol.RoleAssistant, Content: json.RawMessage(`[{"type":"text","text":"agent says hi"}]`)},
		}}},
	)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	for _, want := range []string{
		`"role":"` + string(responsesRoleAssistant) + `"`,
		`"type":"` + string(responsesContentTypeOutputText) + `"`,
		`"text":"agent says hi"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("prepared body missing %s: %s", want, body)
		}
	}
}

func TestPrepareSkipsReasoningContentPartsInProviderInput(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			Messages: []modelcontext.Message{
				{Sequence: 1, Role: modelprotocol.RoleAssistant,
					Content: json.RawMessage(
						`[{"type":"reasoning","text":"my hidden reasoning"},{"type":"text","text":"visible answer"}]`,
					),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	if strings.Contains(body, "my hidden reasoning") {
		t.Fatalf("visible reasoning summary was echoed back to the model: %s", body)
	}
	if !strings.Contains(body, "visible answer") {
		t.Fatalf("assistant text was dropped: %s", body)
	}
}
