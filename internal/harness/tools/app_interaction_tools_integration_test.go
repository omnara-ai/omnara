//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func activateInteractionToolHandlers(
	t *testing.T,
	ctx context.Context,
	f integrationToolFixture,
	keys ...string,
) executionstore.AgentConfigRecord {
	t.Helper()
	source, err := agentconfig.ParseSource(
		agentconfig.SourceFormat(f.AgentConfig.SourceFormat),
		[]byte(f.AgentConfig.Source),
	)
	require.NoError(t, err)
	if source.Tools == nil {
		source.Tools = map[string]agentconfig.AgentConfigToolSource{}
	}
	for _, name := range toolcatalog.InteractionHandlerToolNames() {
		source.Tools[name] = agentconfig.AgentConfigToolSource{}
	}
	source.InteractionHandlers = map[string]agentconfig.AgentConfigAppCapabilitySource{}
	for _, key := range keys {
		if key != f.Install.Name {
			createSlackToolApp(t, ctx, f.Store, f.User.ID, key, key)
		}
		source.InteractionHandlers[key] = agentconfig.AgentConfigAppCapabilitySource{}
	}
	changed, err := f.Store.Execution().ChangeAgentConfig(ctx, appToolConfigChangeInput(t, f, source))
	require.NoError(t, err)
	return changed.AgentConfig
}

func newInteractionToolFixture(
	t *testing.T,
	ctx context.Context,
	label string,
	keys ...string,
) integrationToolFixture {
	t.Helper()
	f := newIntegrationToolFixture(t, ctx, label)
	config := activateInteractionToolHandlers(t, ctx, f, keys...)
	actor, err := executionstore.OmnaraActorParams(
		toolsTestOrgID,
		toolsTestUserPrincipal(f.User.ID),
	)
	require.NoError(t, err)
	launch, err := f.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      toolsTestProjectID,
		AgentConfigID:  config.ID,
		LaunchedBy:     toolsTestUserPrincipal(f.User.ID),
		IdempotencyKey: "handler-agent-" + label,
		InitialInput: &executionstore.LaunchInitialInput{
			Actor:            actor,
			SemanticEventKey: "handler-input-" + label,
			ContentBlocks:    json.RawMessage(`[{"type":"text","text":"select a handler"}]`),
		},
	})
	require.NoError(t, err)
	claim, found, err := f.Store.Execution().ClaimNextAgentWork(ctx, toolsTestClaimInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.AgentWorkModel, claim.Kind)
	require.Equal(t, launch.AgentInput.ID, claim.Model.AdmittedInputTurn.Inputs[0].ID)
	modelCall := claimNormalModelCallForToolsTest(
		t,
		ctx,
		f.Store,
		toolsTestProjectID,
		launch.Agent.ID,
		claim.RuntimeLock,
		[]uuid.UUID{
			launch.AgentInput.ID,
		},
		config.ID,
		claim.Model.AdmittedInputTurn.Events[0].Sequence,
		uuid.Nil,
	)
	f.Agent, f.AgentConfig, f.Lock = launch.Agent, config, claim.RuntimeLock
	f.ModelCallContextID, f.ModelOutputEventID = modelCall.Context.ID, uuid.Nil
	f.Target = launch.IntegrationTarget
	return f
}

func interactionToolTurn(f integrationToolFixture, mode string) Turn {
	turn := f.turn()
	turn.Tools = map[string]ToolSpec{}
	for _, name := range toolcatalog.InteractionHandlerToolNames() {
		turn.Tools[name] = ToolSpec{Permission: toolpermission.DefaultSelection(mode)}
	}
	return turn
}

func dispatchInteractionHandler(
	t *testing.T, ctx context.Context, f integrationToolFixture, turn Turn, call model.ToolCall,
) Result {
	t.Helper()
	result, err := (Executor{Store: f.Store}).Dispatch(ctx, turn, call)
	require.NoError(t, err)
	return result
}

func interactionToolResult(
	t *testing.T, ctx context.Context, f integrationToolFixture, call model.ToolCall,
) map[string]any {
	t.Helper()
	record, err := f.Store.Execution().
		GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateCompleted, record.State)
	return toolResultMapFromTestParts(t, record.ResultContentParts)
}

func TestInteractionToolListSetClearAndReplay(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newInteractionToolFixture(t, ctx, "interaction-selection", "chat", "overlap")
	overlap, err := f.Store.Integrations().GetProjectAppByName(ctx, toolsTestProjectID, "overlap")
	require.NoError(t, err)
	seedToolContext(t, ctx, f.Pool, f.Store, f.Agent, overlap,
		integrationstore.ConversationAddress{Kind: "thread", Ref: "C999:999.1"})
	calls := []model.ToolCall{
		{
			ID:    "list-before",
			Name:  toolcatalog.ToolNameListInteractionHandlers,
			Input: json.RawMessage(`{}`),
		},
		{ID: "set", Name: toolcatalog.ToolNameSetInteractionHandler,
			Input: json.RawMessage(`{"handler":"overlap","args":{"channel_id":"C123","thread_ts":"111.222"}}`)},
		{
			ID:    "list-after",
			Name:  toolcatalog.ToolNameListInteractionHandlers,
			Input: json.RawMessage(`{"limit":1}`),
		},
		{
			ID:    "clear",
			Name:  toolcatalog.ToolNameSetInteractionHandler,
			Input: json.RawMessage(`{"handler":null,"args":{}}`),
		},
	}
	f.recordToolCalls(t, ctx, calls, f.Now)
	turn := interactionToolTurn(f, toolpermission.ModeAlwaysAllow)
	dispatchInteractionHandler(t, ctx, f, turn, calls[0])
	before := interactionToolResult(t, ctx, f, calls[0])
	require.Nil(t, before["selection"])
	options, ok := before["handlers"].([]any)
	require.True(t, ok)
	require.Len(t, options, 2)
	for index, key := range []string{"chat", "overlap"} {
		choice, ok := options[index].(map[string]any)
		require.True(t, ok)
		require.Equal(t, key, choice["handler"])
		require.NotNil(t, choice["input_schema"])
	}
	dispatchInteractionHandler(t, ctx, f, turn, calls[1])
	selected := map[string]any{"handler": "overlap", "args": map[string]any{"channel_id": "C123", "thread_ts": "111.222"}}
	require.Equal(t, selected, interactionToolResult(t, ctx, f, calls[1])["selection"])
	selection, err := f.Store.Execution().
		GetInteractionSelection(ctx, toolsTestProjectID, f.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, "overlap", selection.HandlerKey)
	require.NotEqual(t, uuid.Nil, selection.IntegrationTargetID)
	require.JSONEq(t, `{"channel_id":"C123","thread_ts":"111.222"}`, string(selection.Args))
	dispatchInteractionHandler(t, ctx, f, turn, calls[2])
	page := interactionToolResult(t, ctx, f, calls[2])
	pageHandlers, ok := page["handlers"].([]any)
	require.True(t, ok)
	require.Len(t, pageHandlers, 1)
	require.NotEmpty(t, page["next_cursor"])
	pageHandler, ok := pageHandlers[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "chat", pageHandler["handler"])
	listed, ok := page["selection"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, selected["handler"], listed["handler"])
	require.Equal(t, selected["args"], listed["args"])
	publicAppID, err := publicid.Encode(publicid.KindProjectApp, overlap.ID)
	require.NoError(t, err)
	require.Equal(t, publicAppID, listed["app_id"])
	dispatchInteractionHandler(t, ctx, f, turn, calls[3])
	require.Nil(t, interactionToolResult(t, ctx, f, calls[3])["selection"])
	replayed, err := (Executor{Store: f.Store}).Dispatch(ctx, turn, calls[1])
	require.NoError(t, err)
	require.Equal(t, selected, toolResultMapFromTestParts(t, replayed.ContentParts)["selection"])
	selection, err = f.Store.Execution().
		GetInteractionSelection(ctx, toolsTestProjectID, f.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.InteractionSelection{}, selection)
}

func TestInteractionToolRejectsUnavailableChoiceWithoutMutation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newInteractionToolFixture(t, ctx, "interaction-revoked", "chat")
	call := f.recordToolCall(t, ctx, "set-revoked", toolcatalog.ToolNameSetInteractionHandler,
		`{"handler":"chat","args":{"channel_id":"C123","thread_ts":"111.222"}}`, f.Now)
	activateInteractionToolHandlers(t, ctx, f)
	result, err := (Executor{Store: f.Store}).Dispatch(
		ctx, interactionToolTurn(f, toolpermission.ModeAlwaysAllow), call,
	)
	require.NoError(t, err)
	require.Contains(t, string(result.ContentParts), "interaction_handler_unavailable")
	selection, err := f.Store.Execution().
		GetInteractionSelection(ctx, toolsTestProjectID, f.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.InteractionSelection{}, selection)
	record, err := f.Store.Execution().
		GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateCompleted, record.State)
	require.Equal(
		t,
		executionstore.ToolResultOutcomeFailed,
		record.Outcome,
		"failed command cannot commit success",
	)
}

func TestInteractionToolAlwaysAskUsesOriginalAuthorizationInput(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newInteractionToolFixture(t, ctx, "interaction-approval", "chat")
	call := f.recordPendingToolCall(
		t,
		ctx,
		"set-approved",
		toolcatalog.ToolNameSetInteractionHandler,
		`{"handler":"chat","args":{"channel_id":"C123","thread_ts":"111.222"}}`,
		f.Now,
	)
	turn := interactionToolTurn(f, toolpermission.ModeAlwaysAsk)
	permission := turn.Tools[call.Name].Permission
	descriptor, found := toolpermission.FindMode(
		toolpermission.CommonModeDescriptors(),
		permission.Mode,
	)
	require.True(t, found)
	request, err := genericPermissionChallenge(ctx, Executor{}, turn, call,
		permissionModeContext{selection: permission, descriptor: descriptor})
	require.NoError(t, err)
	permissionInput := executionstore.CreatePermissionInteractionInput{
		ProjectID:     toolsTestProjectID,
		AgentID:       f.Agent.ID,
		ToolCallID:    f.toolCallID(t, ctx, call.ID),
		RuntimeLockID: f.Lock.ID,
		Request:       request,
	}
	interaction, err := f.Store.Execution().CreatePermissionInteraction(ctx, permissionInput)
	require.NoError(t, err)
	actor, err := executionstore.OmnaraActorParams(
		toolsTestOrgID,
		toolsTestUserPrincipal(f.User.ID),
	)
	require.NoError(t, err)
	_, err = f.Store.Execution().
		ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
			ProjectID: toolsTestProjectID, AgentID: f.Agent.ID, ID: interaction.ID, Actor: actor,
			Resolution: interactionform.Resolution{
				Answers: []interactionform.Answer{
					{OptionIndices: []int{toolpermission.AllowOptionIndex}},
				},
			},
		})
	require.NoError(t, err)
	changed := call
	changed.Input = json.RawMessage(`{"handler":null,"args":{}}`)
	implementation, found, err := toolImplementationFor(call.Name)
	require.NoError(t, err)
	require.True(t, found)
	_, err = (Executor{Store: f.Store}).dispatchToolHandler(
		ctx,
		turn,
		changed,
		f.toolCallID(t, ctx, call.ID),
		implementation.handler,
	)
	require.ErrorIs(
		t,
		err,
		ErrToolAuthorizationInvalidated,
		"approval authorizes the exact requested selection",
	)
	dispatchInteractionHandler(t, ctx, f, turn, call)
	require.Equal(
		t,
		map[string]any{"handler": "chat", "args": map[string]any{"channel_id": "C123", "thread_ts": "111.222"}},
		interactionToolResult(t, ctx, f, call)["selection"],
	)
}

func prepareInteractionPromptFixture(t *testing.T, ctx context.Context, f integrationToolFixture) {
	t.Helper()
	activateInteractionToolHandlers(t, ctx, f, "chat")
	tx, err := f.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(
		ctx,
		"SELECT id FROM agents WHERE project_id = $1 AND id = $2 FOR UPDATE",
		toolsTestProjectID,
		f.Agent.ID,
	)
	require.NoError(t, err)
	selected, err := f.Store.Execution().
		SelectInteractionDestinationForOriginTx(ctx, tx, toolsTestProjectID, f.Agent.ID, f.Target.ID)
	require.NoError(t, err)
	require.Equal(t, "chat", selected.HandlerKey)
	require.NoError(t, tx.Commit(ctx))
}
