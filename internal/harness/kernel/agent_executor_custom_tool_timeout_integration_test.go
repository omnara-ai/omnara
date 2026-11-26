//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestAgentExecutorCustomToolTimeouts(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	sourceYAML := `instruction: Use the custom tools when needed.
model:
  provider_config: openai-prod
  name: kernel-test
tools:
  announce:
    type: custom
    description: Show a notice to the user.
    input_schema: {type: object}
    timeout_seconds: 0
  confirm:
    type: custom
    permission: {mode: always_ask}
    description: Show a notice that needs approval.
    input_schema: {type: object}
    timeout_seconds: 0
  await_reply:
    type: custom
    description: Wait briefly for a reply.
    input_schema: {type: object}
    timeout_seconds: 30
  lookup:
    type: custom
    description: Look something up.
    input_schema: {type: object}
    timeout_seconds: 30
`
	compiled := fixture.compileAgentYAMLResolved(t, ctx, sourceYAML)
	config, err := fixture.Store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               kernelTestProjectID,
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       parseConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		EffectiveDefinitionHash: compiled.Hash,
	})
	require.NoError(t, err)
	profile, err := fixture.Store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID:       kernelTestProjectID,
		Name:            "Kernel Custom Tool Timeout Test",
		CurrentConfigID: config.ID,
		IdempotencyKey:  "kernel-custom-tool-timeout-agent",
	})
	require.NoError(t, err)
	launch, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      kernelTestProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     kernelTestUserPrincipal(kernelTestUserID),
		IdempotencyKey: "kernel-custom-tool-timeout-launch",
	})
	require.NoError(t, err)
	agentID := launch.Agent.ID
	call := func(name string) model.ToolCall {
		return model.ToolCall{ID: "call_" + name, Name: name, Input: json.RawMessage(`{}`)}
	}
	modelClient := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		responses: []model.Response{
			{
				ID:         "resp_send_only",
				StopReason: model.StopReasonToolUse,
				Content:    modeltest.ResponsePartsForToolCalls([]model.ToolCall{call("announce")}),
			},
			{
				ID:         "resp_waiting",
				StopReason: model.StopReasonToolUse,
				Content: modeltest.ResponsePartsForToolCalls(
					[]model.ToolCall{call("confirm"), call("await_reply"), call("lookup")},
				),
			},
			{
				ID:         "resp_final",
				Content:    []model.ResponsePart{{Type: "text", Text: "done"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	toolCall := func(name string) executionstore.ToolCallRecord {
		t.Helper()
		var id uuid.UUID
		require.NoError(t, fixture.Pool.QueryRow(ctx,
			`SELECT id FROM tool_calls WHERE agent_id = $1 AND provider_call_id = $2`, agentID, "call_"+name,
		).Scan(&id))
		record, err := fixture.Store.Execution().GetToolCall(ctx, kernelTestProjectID, agentID, id)
		require.NoError(t, err)
		return record
	}
	requireResult := func(name string, outcome executionstore.ToolResultOutcome, text string) {
		t.Helper()
		record := toolCall(name)
		require.Equal(t, executionstore.ToolCallStateCompleted, record.State)
		require.Equal(t, outcome, record.Outcome)
		require.JSONEq(t, `[{"type":"text","text":"`+text+`"}]`, string(record.ResultContentParts))
	}
	runToolWork := func(prior ModelWorkExecution) {
		t.Helper()
		scope := executeNextToolWork(t, ctx, fixture, executor, prior)
		<-scope.Done()
		require.NoError(t, scope.Err())
	}

	turn := fixture.admitContentInputTurn(t, ctx, agentID, kernelTestUserID, "go", fixture.Now.Add(time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, turn))
	runToolWork(turn)
	requireResult("announce", executionstore.ToolResultOutcomeSucceeded, "Sent. This tool does not wait for a result.")

	turn = executeNextModelWork(t, ctx, fixture, executor, turn)
	require.Contains(t, string(modelClient.prepared[1].ProviderRequest), "Sent. This tool does not wait for a result.")
	runToolWork(turn)
	require.Equal(t, executionstore.ToolCallStateAwaitingPermission, toolCall("confirm").State)
	require.Equal(t, executionstore.ToolCallStateReady, toolCall("await_reply").State)
	require.Equal(t, executionstore.ToolCallStateReady, toolCall("lookup").State)

	interaction, found, err := fixture.Store.Execution().GetAgentInteractionByToolCallKind(
		ctx, kernelTestProjectID, agentID, toolCall("confirm").ID, "permission",
	)
	require.NoError(t, err)
	require.True(t, found)
	resolvedBy, err := executionstore.OmnaraActorParams(kernelTestOrgID, kernelTestUserPrincipal(kernelTestUserID))
	require.NoError(t, err)
	_, err = fixture.Store.Execution().ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
		ProjectID: kernelTestProjectID,
		AgentID:   agentID,
		ID:        interaction.ID,
		Resolution: interactionform.Resolution{Answers: []interactionform.Answer{{
			OptionIndices: []int{toolpermission.AllowOptionIndex},
		}}},
		Actor: resolvedBy,
	})
	require.NoError(t, err)
	requireResult("confirm", executionstore.ToolResultOutcomeSucceeded, "Sent. This tool does not wait for a result.")

	submit := func(name, text string) error {
		_, err := fixture.Store.Execution().CompleteCustomToolCall(ctx, executionstore.CompleteCustomToolCallInput{
			ProjectID:     kernelTestProjectID,
			AgentID:       agentID,
			ID:            toolCall(name).ID,
			Outcome:       executionstore.ToolResultOutcomeSucceeded,
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"` + text + `"}]`),
		})
		return err
	}
	require.NoError(t, submit("lookup", "Found."))
	_, err = fixture.Pool.Exec(ctx,
		`UPDATE tool_calls SET expires_at = statement_timestamp() - interval '1 second'
		 WHERE agent_id = $1 AND expires_at IS NOT NULL`, agentID)
	require.NoError(t, err)
	expired, _, err := fixture.Store.Execution().ExpireCustomToolCalls(ctx, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, expired)
	requireResult("await_reply", executionstore.ToolResultOutcomeFailed, "No result was submitted within 30 seconds.")
	requireResult("lookup", executionstore.ToolResultOutcomeSucceeded, "Found.")
	require.ErrorIs(t, submit("await_reply", "Too late."), storeerr.ErrToolCallCompleted)

	executeNextModelWork(t, ctx, fixture, executor, turn)
	require.Equal(t, 3, modelClient.preparedCount())
	require.Contains(t, string(modelClient.prepared[2].ProviderRequest), "No result was submitted within 30 seconds.")
}
