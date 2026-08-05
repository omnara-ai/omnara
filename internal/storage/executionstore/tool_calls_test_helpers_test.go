//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type toolCallSpecForTest struct {
	Label string
	Name  string
	Input json.RawMessage
}

func createReadyToolCallsForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	agentID, userID, configID ID,
	lock executionstore.AgentRuntimeLockRecord,
	label string,
	specs []toolCallSpecForTest,
) map[string]ID {
	t.Helper()
	input, _, _, err := store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        agentID,
			Actor:          mustOmnaraActorParams(t, userID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"seed tool calls"}]`),
			IdempotencyKey: "tool-input-" + label,
		},
	)
	if err != nil {
		t.Fatalf("create tool-call seed input: %v", err)
	}
	admitted, found := admitNextAgentInputAndOpenTurnForTest(
		t,
		ctx,
		store,
		testProjectID,
		agentID,
		lock.ID,
	)
	if !found {
		t.Fatal("expected tool-call seed input admission")
	}
	claim, err := store.Execution().ClaimNormalModelCall(
		ctx,
		executionstore.ClaimNormalModelCallInput{
			ProjectID:          testProjectID,
			AgentID:            agentID,
			RuntimeLockID:      lock.ID,
			OpeningInputIDs:    []ID{input.ID},
			AgentConfigID:      configID,
			InputEventSequence: admitted.Events[0].Sequence,
		},
	)
	if err != nil {
		t.Fatalf("claim tool-call seed model context: %v", err)
	}
	bindings := make([]executionstore.ToolCallBindingInput, 0, len(specs))
	for _, spec := range specs {
		bindings = append(bindings, executionstore.ToolCallBindingInput{
			ID:             testID("tool_call_" + label + "_" + spec.Label),
			ProviderCallID: "call_" + label + "_" + spec.Label,
			Type:           toolcatalog.ToolTypeBuiltIn,
		})
	}
	event, records, err := store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID:          testProjectID,
			AgentID:            agentID,
			RuntimeLockID:      lock.ID,
			ModelCallContextID: claim.Context.ID,
			ProviderResponse: toolCallProviderResponseForTest(
				label,
				modelprotocol.APIFormatOpenAIResponses,
				modelprotocol.APIVariantDefault,
				specs,
			),
			ToolCallBindings: bindings,
		},
	)
	if err != nil {
		t.Fatalf("record tool-call seed output: %v", err)
	}
	if len(records) != len(specs) {
		t.Fatalf("recorded tool calls = %d, want %d", len(records), len(specs))
	}
	out := make(map[string]ID, len(records))
	for index, record := range records {
		if !record.CreatedAt.Equal(event.At) {
			t.Fatalf(
				"tool call created_at = %s, model output event time = %s",
				record.CreatedAt,
				event.At,
			)
		}
		if _, err := store.Execution().MarkToolCallReady(
			ctx,
			executionstore.MarkToolCallReadyInput{
				ProjectID:     testProjectID,
				AgentID:       agentID,
				ID:            record.ID,
				RuntimeLockID: lock.ID,
			},
		); err != nil {
			t.Fatalf("mark tool execution ready: %v", err)
		}
		out[specs[index].Label] = record.ID
	}
	return out
}

func toolCallProviderResponseForTest(
	label string,
	apiFormat modelprotocol.APIFormat,
	apiVariant modelprotocol.APIVariant,
	specs []toolCallSpecForTest,
) modelenvelope.ResponseEnvelope {
	parts := make([]modelenvelope.ResponsePart, 0, len(specs))
	for _, spec := range specs {
		parts = append(parts, modelenvelope.ResponsePart{
			Type:           modelenvelope.ResponsePartTypeToolCall,
			ProviderCallID: "call_" + label + "_" + spec.Label,
			ToolName:       spec.Name,
			ToolInput:      spec.Input,
		})
	}
	return modelenvelope.ResponseEnvelope{
		RequestedProviderModelSlug: "gpt-test",
		ServedProviderModelSlug:    "gpt-test",
		APIFormat:                  apiFormat,
		APIVariant:                 apiVariant,
		ProviderReplay:             json.RawMessage(`{}`),
		Normalized: modelenvelope.ResponseNormalized{
			ID:         "resp_" + label,
			Content:    parts,
			StopReason: modelenvelope.StopReasonToolUse,
		},
	}
}
