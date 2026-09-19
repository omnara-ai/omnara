//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func activateInteractionToolHandlers(t *testing.T, ctx context.Context, f integrationToolFixture, keys ...string) {
	t.Helper()
	source, err := agentconfig.ParseSource(
		agentconfig.SourceFormat(f.AgentConfig.SourceFormat), []byte(f.AgentConfig.Source),
	)
	require.NoError(t, err)
	connection, err := publicid.Encode(publicid.KindIntegrationConnection, f.Install.ID)
	require.NoError(t, err)
	source.AppResources = map[string]agentconfig.AgentConfigAppResourceSource{}
	for _, key := range keys {
		source.AppResources[key] = agentconfig.AgentConfigAppResourceSource{
			Definition: appdefinition.Slack, Connection: connection,
			Scope:              &appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123"}},
			InteractionHandler: &appdefinition.InteractionHandler{Definition: appdefinition.SlackInteractions},
		}
		if f.Install.Provider == appdefinition.ProviderDiscord {
			resource := source.AppResources[key]
			resource.Definition = appdefinition.Discord
			resource.Scope = &appdefinition.Scope{Discord: &appdefinition.DiscordScope{ChannelID: "444"}}
			resource.InteractionHandler = &appdefinition.InteractionHandler{
				Definition: appdefinition.DiscordInteractions,
			}
			source.AppResources[key] = resource
		}

	}
	raw, err := json.Marshal(source)
	require.NoError(t, err)
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatJSON, raw, agentconfig.CompileOptions{
		ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
			return agentconfig.ResolvedModelSelection{ConfiguredModelID: f.AgentConfig.ConfiguredModelID.String()}, nil
		},
		ResolveAppConnection: func(id, _ string) (string, error) { return id, nil },
	})
	require.NoError(t, err)
	_, err = f.Store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: executionstore.CreateAgentConfigInput{
			ProjectID: toolsTestProjectID, Source: string(raw), SourceFormat: "json",
			ConfiguredModelID: f.AgentConfig.ConfiguredModelID, CompiledDefinition: compiled.CanonicalJSON,
			CompilerVersion: agentconfig.CompilerVersion, EffectiveDefinitionHash: compiled.Hash,
		},
		AgentID: f.Agent.ID, ActorType: "user", ActorID: f.User.ID, IdempotencyKey: uuid.NewString(),
	})
	require.NoError(t, err)
}

func interactionToolTurn(f integrationToolFixture, mode string) Turn {
	turn := f.turn()
	turn.Tools = map[string]ToolSpec{}
	for _, name := range toolcatalog.InteractionDestinationToolNames() {
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
	record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateCompleted, record.State)
	return toolResultMapFromTestParts(t, record.ResultContentParts)
}

func TestInteractionToolListSetClearAndReplay(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newIntegrationToolFixture(t, ctx, "interaction-selection")
	activateInteractionToolHandlers(t, ctx, f, "chat", "overlap")
	target, err := publicid.Encode(publicid.KindIntegrationTarget, f.Target.ID)
	require.NoError(t, err)
	calls := []model.ToolCall{
		{ID: "list-before", Name: toolcatalog.ToolNameListInteractionDestinations, Input: json.RawMessage(`{}`)},
		{ID: "set", Name: toolcatalog.ToolNameSetInteractionDestination,
			Input: json.RawMessage(`{"destination":{"target_id":"` + target + `","resource":"overlap"}}`)},
		{ID: "list-after", Name: toolcatalog.ToolNameListInteractionDestinations, Input: json.RawMessage(`{}`)},
		{
			ID:    "clear",
			Name:  toolcatalog.ToolNameSetInteractionDestination,
			Input: json.RawMessage(`{"destination":null}`),
		},
	}
	f.recordToolCalls(t, ctx, calls, f.Now)
	turn := interactionToolTurn(f, toolpermission.ModeAlwaysAllow)
	dispatchInteractionHandler(t, ctx, f, turn, calls[0])
	before := interactionToolResult(t, ctx, f, calls[0])
	require.Nil(t, before["current"])
	options, ok := before["destinations"].([]any)
	require.True(t, ok)
	var found int
	for _, option := range options {
		choice, ok := option.(map[string]any)
		require.True(t, ok)
		if choice["target_id"] == target {
			found++
			require.Contains(t, []string{"chat", "overlap"}, choice["resource"])
			require.Equal(t, map[string]any{"kind": "thread", "ref": "C123:111.222"}, choice["scope"])
		}
	}
	require.Equal(t, 2, found)
	dispatchInteractionHandler(t, ctx, f, turn, calls[1])
	selected := map[string]any{"target_id": target, "resource": "overlap"}
	require.Equal(t, selected, interactionToolResult(t, ctx, f, calls[1])["current"])
	selection, err := f.Store.Execution().GetInteractionSelection(ctx, toolsTestProjectID, f.Agent.ID)
	require.NoError(t, err)
	require.Equal(t,
		executionstore.InteractionSelection{IntegrationTargetID: f.Target.ID, ResourceKey: "overlap"}, selection)
	dispatchInteractionHandler(t, ctx, f, turn, calls[2])
	require.Equal(t, selected, interactionToolResult(t, ctx, f, calls[2])["current"])
	dispatchInteractionHandler(t, ctx, f, turn, calls[3])
	require.Nil(t, interactionToolResult(t, ctx, f, calls[3])["current"])
	// Completed-call replay returns the persisted result without restoring old selection.
	replayed, err := (Executor{Store: f.Store}).Dispatch(ctx, turn, calls[1])
	require.NoError(t, err)
	require.Equal(t, selected, toolResultMapFromTestParts(t, replayed.ContentParts)["current"])
	selection, err = f.Store.Execution().GetInteractionSelection(ctx, toolsTestProjectID, f.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.InteractionSelection{}, selection)
}

func TestInteractionToolRejectsUnavailableChoiceWithoutMutation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newIntegrationToolFixture(t, ctx, "interaction-revoked")
	activateInteractionToolHandlers(t, ctx, f, "chat")
	target, err := publicid.Encode(publicid.KindIntegrationTarget, f.Target.ID)
	require.NoError(t, err)
	call := f.recordToolCall(t, ctx, "set-revoked", toolcatalog.ToolNameSetInteractionDestination,
		`{"destination":{"target_id":"`+target+`","resource":"chat"}}`, f.Now)
	// A choice discovered before config replacement cannot restore removed authority.
	activateInteractionToolHandlers(t, ctx, f)
	result, err := (Executor{Store: f.Store}).Dispatch(
		ctx, interactionToolTurn(f, toolpermission.ModeAlwaysAllow), call,
	)
	require.NoError(t, err)
	require.Contains(t, string(result.ContentParts), "interaction_destination_unavailable")
	selection, err := f.Store.Execution().GetInteractionSelection(ctx, toolsTestProjectID, f.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.InteractionSelection{}, selection)
	record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateCompleted, record.State)
	require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome, "failed command cannot commit success")
}

func TestInteractionToolAlwaysAskUsesOriginalAuthorizationInput(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newIntegrationToolFixture(t, ctx, "interaction-approval")
	activateInteractionToolHandlers(t, ctx, f, "chat")
	target, err := publicid.Encode(publicid.KindIntegrationTarget, f.Target.ID)
	require.NoError(t, err)
	call := f.recordPendingToolCall(t, ctx, "set-approved", toolcatalog.ToolNameSetInteractionDestination,
		`{"destination":{"target_id":"`+target+`","resource":"chat"}}`, f.Now)
	turn := interactionToolTurn(f, toolpermission.ModeAlwaysAsk)
	permission := turn.Tools[call.Name].Permission
	descriptor, found := toolpermission.FindMode(toolpermission.CommonModeDescriptors(), permission.Mode)
	require.True(t, found)
	request, err := genericPermissionChallenge(ctx, Executor{}, turn, call,
		permissionModeContext{selection: permission, descriptor: descriptor})
	require.NoError(t, err)
	permissionInput := executionstore.CreatePermissionInteractionInput{
		ProjectID: toolsTestProjectID, AgentID: f.Agent.ID, ToolCallID: f.toolCallID(t, ctx, call.ID),
		RuntimeLockID: f.Lock.ID, Request: request,
	}
	interaction, err := f.Store.Execution().CreatePermissionInteraction(ctx, permissionInput)
	require.NoError(t, err)
	actor, err := executionstore.OmnaraActorParams(toolsTestOrgID, toolsTestUserPrincipal(f.User.ID))
	require.NoError(t, err)
	_, err = f.Store.Execution().ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
		ProjectID: toolsTestProjectID, AgentID: f.Agent.ID, ID: interaction.ID, Actor: actor,
		Resolution: interactionform.Resolution{
			Answers: []interactionform.Answer{{OptionIndices: []int{toolpermission.AllowOptionIndex}}},
		},
	})
	require.NoError(t, err)
	changed := call
	changed.Input = json.RawMessage(`{"destination":null}`)
	_, err = (Executor{Store: f.Store}).dispatchToolHandler(ctx, turn, changed, f.toolCallID(t, ctx, call.ID),
		interactionImplementationForTest(t, call.Name).handler)
	require.ErrorIs(t, err, ErrToolAuthorizationInvalidated, "approval authorizes the exact requested selection")
	dispatchInteractionHandler(t, ctx, f, turn, call)
	require.Equal(t, map[string]any{"target_id": target, "resource": "chat"},
		interactionToolResult(t, ctx, f, call)["current"])
}

// Use the production config reconciliation and origin selector without publishing
// an extra model tool batch in the shared fixture.
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
	require.Equal(t, "chat", selected.ResourceKey)
	require.NoError(t, tx.Commit(ctx))
}
