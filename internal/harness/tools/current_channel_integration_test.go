//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestCurrentChannelToolJourneyPinsPromptsAndReplaysSelection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "current-channel-journey")
	connection := createConnectorToolChannel(t, ctx, fixture, "current-channel-journey")
	definition, err := fixture.Store.Integrations().PublishConnectorChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: toolsTestProjectID, IntegrationInstallID: connection.Install.ID,
			ImplementationKey: "current-channel", Kind: integrationstore.ChannelKindExternal,
			Description: "Example channel", SendParamsSchema: json.RawMessage(`{"type":"object"}`),
			Capabilities: integrationstore.ChannelCapabilities{Read: true, Send: true, Text: true},
			ConnectorCapabilities: []channelconnector.Capability{{ConnectorKey: connection.App.ConnectorKey,
				Provider: connection.App.Provider}},
		})
	require.NoError(t, err)
	var targets []integrationstore.IntegrationTargetRecord
	var channelIDs []string
	for _, label := range []string{"a", "b"} {
		target, err := fixture.Store.Integrations().CreateIntegrationTarget(ctx,
			integrationstore.CreateIntegrationTargetInput{
				ProjectID: toolsTestProjectID, IntegrationInstallID: connection.Install.ID,
				ChannelDefinitionID: definition.ID, ProviderRef: "private-" + label, ProviderRefKind: "thread",
			})
		require.NoError(t, err)
		_, err = fixture.Store.Integrations().CreateIntegrationTargetBinding(ctx,
			integrationstore.CreateIntegrationTargetBindingInput{
				ProjectID: toolsTestProjectID, AgentID: fixture.Agent.ID, IntegrationInstallID: connection.Install.ID,
				IntegrationTargetID: target.ID,
				ReceiveAllowed:      true,
				ReadAllowed:         true,
				SendAllowed:         true,
				Source:              "current-channel-test",
			})
		require.NoError(t, err)
		id, err := publicid.Encode(publicid.KindIntegrationTarget, target.ID)
		require.NoError(t, err)
		targets = append(targets, target)
		channelIDs = append(channelIDs, id)
	}
	channelArgs := func(id string) json.RawMessage { return json.RawMessage(fmt.Sprintf(`{"channel_id":%q}`, id)) }
	calls := []model.ToolCall{
		{ID: "set-a", Name: toolcatalog.ToolNameSetCurrentChannel, Input: channelArgs(channelIDs[0])},
		{ID: "list-a", Name: toolcatalog.ToolNameListChannels, Input: json.RawMessage(`{"limit":1}`)},
		{ID: "get-a", Name: toolcatalog.ToolNameGetChannel, Input: channelArgs(channelIDs[0])},
		{ID: "permission-a", Name: toolcatalog.ToolNameWebFetch, Input: json.RawMessage(`{"url":"https://example.com"}`)},
		{ID: "set-b", Name: toolcatalog.ToolNameSetCurrentChannel, Input: channelArgs(channelIDs[1])},
		{ID: "question-b",
			Name:  toolcatalog.ToolNameAskQuestion,
			Input: json.RawMessage(`{"questions":[{"prompt":"Continue?","options":[{"label":"Yes"},{"label":"No"}]}]}`)},
		{ID: "clear", Name: toolcatalog.ToolNameSetCurrentChannel, Input: json.RawMessage(`{"channel_id":null}`)},
		{ID: "list-clear", Name: toolcatalog.ToolNameListChannels, Input: json.RawMessage(`{}`)},
		{ID: "permission-none", Name: toolcatalog.ToolNameWebFetch, Input: json.RawMessage(`{"url":"https://example.com"}`)},
		{ID: "list-unavailable", Name: toolcatalog.ToolNameListChannels, Input: json.RawMessage(`{}`)},
		{ID: "get-unavailable", Name: toolcatalog.ToolNameGetChannel, Input: channelArgs(channelIDs[0])},
		{ID: "get-deleted", Name: toolcatalog.ToolNameGetChannel, Input: channelArgs(channelIDs[0])},
	}
	fixture.recordPendingToolCalls(t, ctx, calls, fixture.Now.Add(20*time.Second))
	turn := fixture.turn()
	for _, call := range calls {
		mode := toolpermission.ModeAlwaysAllow
		if call.Name == toolcatalog.ToolNameWebFetch {
			mode = toolpermission.ModeAlwaysAsk
		}
		turn.Tools[call.Name] = ToolSpec{
			Type:       toolcatalog.ToolTypeBuiltIn,
			Permission: toolpermission.DefaultSelection(mode),
		}
	}
	executor := Executor{Store: fixture.Store, Now: func() time.Time { return fixture.Now.Add(21 * time.Second) }}
	dispatch := func(index int) Result {
		t.Helper()
		require.NoError(t, executor.PrepareToolCallPermission(ctx, turn, calls[index]))
		result, err := executor.Dispatch(ctx, turn, calls[index])
		require.NoError(t, err)
		require.Equal(t, DispatchCompleted, result.Disposition)
		return result
	}
	selected := dispatch(0)
	require.Equal(t, channelIDs[0], toolResultMapFromTestParts(t, selected.ContentParts)["current_channel_id"])
	listed := toolResultMapFromTestParts(t, dispatch(1).ContentParts)
	require.Equal(t, channelIDs[0], listed["current_channel_id"])
	page, ok := listed["channels"].([]any)
	require.True(t, ok)
	require.Len(t, page, 1)
	firstChannel, ok := page[0].(map[string]any)
	require.True(t, ok)
	require.NotEqual(t,
		channelIDs[0],
		firstChannel["channel_id"],
		"current selection is visible even outside the page")
	require.Equal(t, true, toolResultMapFromTestParts(t, dispatch(2).ContentParts)["is_current"])
	require.NoError(t, executor.PrepareToolCallPermission(ctx, turn, calls[3]))
	permission := integrationToolInteraction(t, ctx, fixture, fixture.toolCallID(t, ctx, calls[3].ID), "permission")
	require.Equal(t, targets[0].ID, permission.IntegrationTargetID)
	dispatch(4)
	require.NoError(t, executor.PrepareToolCallPermission(ctx, turn, calls[5]))
	_, err = dispatchToolAndDrainAsync(t, ctx, executor, turn, calls[5])
	require.NoError(t, err)
	question := integrationToolInteraction(t, ctx, fixture, fixture.toolCallID(t, ctx, calls[5].ID), "question")
	require.Equal(t, targets[1].ID, question.IntegrationTargetID)
	require.Equal(t,
		executionstore.AgentInteractionStateOpen,
		question.State,
		"unsupported channel presentation still leaves the dashboard question open")
	dispatch(6)
	require.Nil(t, toolResultMapFromTestParts(t, dispatch(7).ContentParts)["current_channel_id"])
	require.NoError(t, executor.PrepareToolCallPermission(ctx, turn, calls[8]))
	unrouted := integrationToolInteraction(t, ctx, fixture, fixture.toolCallID(t, ctx, calls[8].ID), "permission")
	require.Equal(t, storage.NilID, unrouted.IntegrationTargetID)

	replayed, err := executor.Dispatch(ctx, turn, calls[0])
	require.NoError(t, err)
	require.JSONEq(t, string(selected.ContentParts), string(replayed.ContentParts))
	current, err := fixture.Store.Execution().GetAgentCurrentChannelID(ctx, toolsTestProjectID, fixture.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, storage.NilID, current, "replaying a completed setter must not reapply its selection")
	permission = integrationToolInteraction(t, ctx, fixture, permission.ToolCallID, "permission")
	question = integrationToolInteraction(t, ctx, fixture, question.ToolCallID, "question")
	require.Equal(t, targets[0].ID, permission.IntegrationTargetID)
	require.Equal(t, targets[1].ID, question.IntegrationTargetID)
	_, err = fixture.Pool.Exec(ctx,
		`UPDATE agent_interactions SET integration_target_id = $1 WHERE id = $2`,
		targets[1].ID,
		permission.ID)
	require.Error(t, err, "the database rejects a changed prompt destination")
	// A prompt created without a destination stays dashboard-only after selection.
	_, err = fixture.Pool.Exec(ctx,
		`UPDATE agents SET integration_target_id = $1 WHERE project_id = $2 AND id = $3`,
		targets[0].ID,
		toolsTestProjectID,
		fixture.Agent.ID)
	require.NoError(t, err)
	unrouted = integrationToolInteraction(t, ctx, fixture, unrouted.ToolCallID, "permission")
	require.Equal(t, storage.NilID, unrouted.IntegrationTargetID)
	require.NoError(t, executor.postIntegrationPrompt(ctx, turn, unrouted))

	_, err = fixture.Pool.Exec(ctx, `UPDATE integration_apps SET state = 'disabled' WHERE id = $1`, connection.App.ID)
	require.NoError(t, err)
	require.Equal(t, channelIDs[0], toolResultMapFromTestParts(t, dispatch(9).ContentParts)["current_channel_id"])
	unavailable := toolResultMapFromTestParts(t, dispatch(10).ContentParts)
	require.Equal(t, true, unavailable["is_current"])
	require.Equal(t, false, unavailable["active"])
	capabilities, ok := unavailable["capabilities"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, false, capabilities["send"])

	// Deleting the installation clears routing state but retains prompt history.
	require.NoError(t,
		fixture.Store.Integrations().DeleteIntegrationInstall(ctx,
			toolsTestProjectID,
			connection.Install.ID))
	current, err = fixture.Store.Execution().GetAgentCurrentChannelID(ctx, toolsTestProjectID, fixture.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, storage.NilID, current)
	permission = integrationToolInteraction(t, ctx, fixture, permission.ToolCallID, "permission")
	require.Equal(t, targets[0].ID, permission.IntegrationTargetID)
	require.NoError(t, executor.postIntegrationPrompt(ctx, turn, permission))
	missing := dispatch(11)
	require.Equal(t, "unavailable_channel", toolResultMapFromTestParts(t, missing.ContentParts)["error_code"])
	record, err := fixture.Store.Execution().GetToolCall(ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
		fixture.toolCallID(t,
			ctx,
			calls[11].ID))
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)

}

func TestCurrentChannelSetterCanSelectManagedSlackDestination(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "current-native")
	createConnectorToolChannel(t, ctx, fixture, "current-native")
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, fixture.Target.ID)
	require.NoError(t, err)
	calls := []model.ToolCall{
		{ID: "clear", Name: toolcatalog.ToolNameSetCurrentChannel, Input: json.RawMessage(`{"channel_id":null}`)},
		{ID: "select-native",
			Name: toolcatalog.ToolNameSetCurrentChannel,
			Input: json.RawMessage(fmt.Sprintf(`{"channel_id":%q}`,
				channelID))},
		{ID: "permission-native",
			Name:  toolcatalog.ToolNameWebFetch,
			Input: json.RawMessage(`{"url":"https://example.com"}`)},
	}
	fixture.recordPendingToolCalls(t, ctx, calls, fixture.Now.Add(20*time.Second))
	turn := fixture.turn()
	turn.Tools[toolcatalog.ToolNameSetCurrentChannel] = ToolSpec{
		Type:       toolcatalog.ToolTypeBuiltIn,
		Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
	}
	turn.Tools[toolcatalog.ToolNameWebFetch] = ToolSpec{
		Type:       toolcatalog.ToolTypeBuiltIn,
		Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
	}
	posts := 0
	executor := Executor{Store: fixture.Store,
		ChannelOperations: testChannelOperations(func(
			_ context.Context, request channelconnector.OperationRequest,
		) (channelconnector.OperationResult, error) {
			posts++
			require.Equal(t, channelconnector.OperationInteraction, request.Kind)
			require.Equal(t, channelconnector.BuiltInConnectorKey, request.Capability.ConnectorKey)
			require.Equal(t, channelID, request.Scope.ChannelID)
			return completedTestChannelInteraction(request), nil
		}),
		Now: func() time.Time { return fixture.Now.Add(21 * time.Second) }}
	for _, call := range calls[:2] {
		require.NoError(t, executor.PrepareToolCallPermission(ctx, turn, call))
		result, err := executor.Dispatch(ctx, turn, call)
		require.NoError(t, err)
		require.Equal(t, DispatchCompleted, result.Disposition)
	}
	require.NoError(t, executor.PrepareToolCallPermission(ctx, turn, calls[2]))
	interaction := integrationToolInteraction(t, ctx, fixture, fixture.toolCallID(t, ctx, calls[2].ID), "permission")
	require.Equal(t, fixture.Target.ID, interaction.IntegrationTargetID)
	require.NoError(t, executor.postIntegrationPrompt(ctx, turn, interaction))
	require.Equal(t, 1, posts)
}
