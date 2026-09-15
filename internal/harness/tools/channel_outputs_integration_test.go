//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/publicid"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func unexpectedChannelOperations(t *testing.T) ChannelOperationsClient {
	t.Helper()
	return testChannelOperations(func(
		context.Context, channelconnector.OperationRequest,
	) (channelconnector.OperationResult, error) {
		t.Error("unexpected managed channel operation")
		return channelconnector.OperationResult{}, errors.New("unexpected operation")
	})
}

func completedTestChannelInteraction(request channelconnector.OperationRequest) channelconnector.OperationResult {
	return channelconnector.OperationResult{RequestID: request.RequestID, Outcome: channelconnector.OperationCompleted,
		Payload: json.RawMessage(`{"message_id":"prompt-1"}`)}
}

func TestChannelQuestionPresentationKeepsCanonicalDashboardAuthority(t *testing.T) {
	for _, outcome := range []string{"completed", "failed", "unknown", "transport", "invalid", "disabled"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			f := newIntegrationToolFixture(t, ctx, "question-output-"+outcome)
			if outcome == "disabled" {
				_, err := f.Store.Integrations().DisableIntegrationInstall(ctx, integrationstore.DisableIntegrationInstallInput{
					ProjectID: toolsTestProjectID, ID: f.Install.ID, ExpectedOAuthFlowID: &f.Install.LastOAuthFlowID,
				})
				require.NoError(t, err)
			}
			call := f.recordToolCall(t, ctx, "question", toolcatalog.ToolNameAskQuestion,
				`{"questions":[{"prompt":"Ship it?","options":[{"label":"Yes"},{"label":"No"}]}]}`, f.Now.Add(20*time.Second))
			count := 0
			e := Executor{Store: f.Store, ChannelOperations: testChannelOperations(func(
				ioCtx context.Context, request channelconnector.OperationRequest,
			) (channelconnector.OperationResult, error) {
				count++
				_, bounded := ioCtx.Deadline()
				require.True(t, bounded)
				require.Equal(t, channelconnector.OperationInteraction, request.Kind)
				id, err := publicid.Decode(publicid.KindAgentInteraction, request.RequestID)
				require.NoError(t, err)
				interaction, found, err := f.Store.Execution().GetAgentInteraction(ctx, toolsTestProjectID, f.Agent.ID, id)
				require.NoError(t, err)
				require.True(t, found, "canonical interaction commits before provider I/O")
				require.Equal(t, executionstore.AgentInteractionStateOpen, interaction.State)
				var payload channelconnector.InteractionPayload
				require.NoError(t, json.Unmarshal(request.Payload, &payload))
				require.NoError(t, payload.Validate())
				require.Equal(t, request.RequestID, payload.InteractionID)
				require.Equal(t, request.Scope.AgentID, payload.AgentID)
				require.Equal(t, request.Scope.ChannelID, payload.ChannelID)
				canonical, err := interaction.Form()
				require.NoError(t, err)
				require.Equal(t, canonical, payload.Form)
				result := completedTestChannelInteraction(request)
				switch outcome {
				case "failed", "unknown":
					result.Outcome = channelconnector.OperationOutcome(outcome)
				case "transport":
					return channelconnector.OperationResult{}, errors.New("secret-provider-token")
				case "invalid":
					result.Payload = json.RawMessage(`{"answer":"yes"}`)
				}
				return result, nil
			})}
			result, err := dispatchToolAndDrainAsync(t, ctx, e, f.turn(), call)
			require.NoError(t, err)
			require.Equal(t, DispatchDeferred, result.Disposition)
			wantCount := 1
			if outcome == "disabled" {
				wantCount = 0
			}
			require.Equal(t, wantCount, count)
			record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolCallStateWaiting, record.State)
			require.Equal(t, uuid.Nil, record.RuntimeLockID)
			require.JSONEq(t, string(call.Input), string(record.Input))
			interaction := integrationToolInteraction(t, ctx, f, record.ID, "question")
			require.Equal(t, executionstore.AgentInteractionStateOpen, interaction.State)
			_, err = e.Dispatch(ctx, f.turn(), call)
			require.NoError(t, err)
			require.Equal(t, wantCount, count, "replay does not re-present or retry unknown output")
		})
	}
}

func TestChannelPermissionCopyDoesNotBlockOrResolveCanonicalPrompt(t *testing.T) {
	ctx := context.Background()
	f := newIntegrationToolFixture(t, ctx, "permission-output-background")
	runner, err := NewBackgroundExecutionRunner(ctx, nil, 1)
	require.NoError(t, err)
	defer runner.Shutdown()
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	defer close(release)
	call := f.recordPendingToolCall(
		t, ctx, "permission", toolcatalog.ToolNameListProcesses, `{}`, f.Now.Add(20*time.Second))
	turn := f.turn()
	turn.Tools[call.Name] = ToolSpec{Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)}
	e := Executor{Store: f.Store, BackgroundRunner: runner,
		ChannelOperations: testChannelOperations(func(
			ioCtx context.Context, request channelconnector.OperationRequest,
		) (channelconnector.OperationResult, error) {
			close(started)
			select {
			case <-release:
			case <-ioCtx.Done():
			}
			close(finished)
			return channelconnector.OperationResult{}, ioCtx.Err()
		})}
	done := make(chan error, 1)
	go func() { done <- e.PrepareToolCallPermission(ctx, turn, call) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("copy did not start")
	}
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("permission waited for copy")
	}
	record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateAwaitingPermission, record.State)
	require.JSONEq(t, string(call.Input), string(record.Input))
	interaction := integrationToolInteraction(t, ctx, f, record.ID, "permission")
	require.Equal(t, executionstore.AgentInteractionStateOpen, interaction.State)
	require.NoError(t, e.PrepareToolCallPermission(ctx, turn, call))
	// Shutdown cancels actual I/O while the same UI prompt remains authoritative.
	runner.Shutdown()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("copy I/O did not cancel")
	}
	interaction = integrationToolInteraction(t, ctx, f, record.ID, "permission")
	require.Equal(t, executionstore.AgentInteractionStateOpen, interaction.State)
}

func TestManagedPresentationUsesCanonicalPinAndRejectsReplacementBinding(t *testing.T) {
	ctx := context.Background()
	f := newIntegrationToolFixture(t, ctx, "presentation-pin")
	other := createConnectorToolChannel(t, ctx, f, "presentation-pin")
	call := f.recordPendingToolCall(
		t, ctx, "permission", toolcatalog.ToolNameListProcesses, `{}`, f.Now.Add(20*time.Second))
	turn := f.turn()
	turn.Tools[call.Name] = ToolSpec{Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)}
	e := Executor{Store: f.Store}
	require.NoError(t, e.PrepareToolCallPermission(ctx, turn, call))
	interaction := integrationToolInteraction(t, ctx, f, f.toolCallID(t, ctx, call.ID), "permission")
	prepared, err := f.Store.Execution().PrepareChannelPresentation(ctx, turn.ProjectID, turn.AgentID, interaction.ID)
	require.NoError(t, err)
	require.NoError(t, seedIntegrationToolCurrentChannel(ctx, f.Pool, turn.ProjectID, turn.AgentID, other.Target.ID))
	// The caller's stale/tampered copy and changed current selection cannot move it.
	tampered := interaction
	tampered.IntegrationTargetID = other.Target.ID
	tampered.Request = json.RawMessage(`{}`)
	count := 0
	e.ChannelOperations = testChannelOperations(func(
		_ context.Context, request channelconnector.OperationRequest,
	) (channelconnector.OperationResult, error) {
		count++
		id, err := publicid.Decode(publicid.KindIntegrationTarget, request.Scope.ChannelID)
		require.NoError(t, err)
		require.Equal(t, f.Target.ID, id)
		return completedTestChannelInteraction(request), nil
	})
	require.NoError(t, e.postIntegrationPrompt(ctx, turn, tampered))
	require.Equal(t, 1, count)
	binding, err := f.Store.Integrations().GetActiveSendBindingForTarget(ctx, turn.ProjectID, turn.AgentID, f.Target.ID)
	require.NoError(t, err)
	require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(ctx, turn.ProjectID, binding.ID))
	bindIntegrationToolTarget(t, ctx, f.Store, turn.AgentID, f.Target)
	_, err = f.Store.Execution().RecheckChannelOutput(ctx, prepared)
	require.Error(t, err, "new binding cannot revive a prepared presentation")
}

func TestManagedRuntimeNoticeUsesCurrentChannelAndDoesNotRetryUnknown(t *testing.T) {
	ctx := context.Background()
	f := newIntegrationToolFixtureWithConnectorOrigins(t, ctx, "notice-current", 2)
	turn := f.turn()
	current := f.OriginChannels[1]
	prepared, err := f.Store.Execution().PrepareChannelNotice(ctx, executionstore.PrepareChannelNoticeInput{
		ProjectID: turn.ProjectID, AgentID: turn.AgentID, TurnID: turn.TurnID, RuntimeLockID: turn.RuntimeLockID,
	})
	require.NoError(t, err)
	require.NoError(t, seedIntegrationToolCurrentChannel(ctx, f.Pool, turn.ProjectID, turn.AgentID, f.Target.ID))
	_, err = f.Store.Execution().RecheckChannelOutput(ctx, prepared)
	require.Error(t, err, "a notice must not dispatch after its selected channel changes")
	require.NoError(t, seedIntegrationToolCurrentChannel(ctx, f.Pool, turn.ProjectID, turn.AgentID, current.Target.ID))
	count := 0
	e := Executor{Store: f.Store, ChannelOperations: testChannelOperations(func(
		ioCtx context.Context, request channelconnector.OperationRequest,
	) (channelconnector.OperationResult, error) {
		count++
		require.Contains(t, request.RequestID, turn.TurnID.String())
		require.Contains(t, request.RequestID, turn.RuntimeLockID.String())
		require.Equal(t, channelconnector.OperationSend, request.Kind)
		id, err := publicid.Decode(publicid.KindIntegrationTarget, request.Scope.ChannelID)
		require.NoError(t, err)
		require.Equal(t, current.Target.ID, id)
		var payload channelconnector.SendPayload
		require.NoError(t, json.Unmarshal(request.Payload, &payload))
		require.Equal(t, "notice", payload.Message.Text)
		require.Nil(t, payload.ReplyChannelGrants)
		deadline, bounded := ioCtx.Deadline()
		require.True(t, bounded)
		require.LessOrEqual(t, time.Until(deadline), 10*time.Second)
		return channelconnector.OperationResult{}, errors.New("secret-provider-token")
	})}
	err = e.PostIntegrationRuntimeMessage(ctx, turn, "notice")
	require.ErrorContains(t, err, "unknown")
	require.NotContains(t, err.Error(), "secret-provider-token")
	require.Equal(t, 1, count)
	// Automatic output must never fill an explicit tool's missing channel argument.
	call := f.recordToolCall(t, ctx, "send", toolcatalog.ToolNameSendChannelMessage,
		`{"message":{"text":"explicit destination required"}}`, f.Now.Add(20*time.Second))
	result, err := dispatchAsyncToolToTerminal(t, ctx, e, managedToolTurn(f), call)
	require.NoError(t, err)
	require.Equal(t, "malformed", toolResultMapFromTestParts(t, result.ContentParts)["error_code"])
	require.Equal(t, 1, count)
	require.NoError(t, seedIntegrationToolCurrentChannel(ctx, f.Pool, turn.ProjectID, turn.AgentID, uuid.Nil))
	require.NoError(t, e.PostIntegrationRuntimeMessage(ctx, turn, "notice"))
	require.Equal(t, 1, count)
}

func TestNativeChannelToolsAreUnavailable(t *testing.T) {
	for _, name := range []string{"send_integration_message", "set_integration_target"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newIntegrationToolFixture(t, ctx, strings.ReplaceAll(name, "_", "-"))
			call := f.recordToolCall(t, ctx, "removed", name, `{}`, f.Now.Add(20*time.Second))
			turn := f.turn()
			e := Executor{Store: f.Store, ChannelOperations: unexpectedChannelOperations(t)}
			result, err := e.Dispatch(ctx, turn, call)
			require.NoError(t, err)
			require.Equal(t, DispatchCompleted, result.Disposition)
			require.Equal(t, "unsupported", toolResultMapFromTestParts(t, result.ContentParts)["error_code"])
		})
	}
}

func TestExternalPromptAndRuntimeNoticeRetainSeparateFiniteOwners(t *testing.T) {
	ctx := context.Background()
	f := newIntegrationToolFixture(t, ctx, "external-automatic")
	install, err := f.Store.Integrations().CreateExternalIntegrationInstall(ctx,
		integrationstore.CreateExternalIntegrationInstallInput{
			OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID,
			InstalledBy: toolsTestUserPrincipal(f.User.ID), DisplayName: "Customer connector", Metadata: json.RawMessage(`{}`),
		})
	require.NoError(t, err)
	definition, err := f.Store.Integrations().PublishExternalChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: toolsTestProjectID, IntegrationInstallID: install.ID,
			ImplementationKey: "room", Kind: integrationstore.ChannelKindExternal,
			SendParamsSchema: json.RawMessage(`{"type":"object"}`),
			Capabilities:     integrationstore.ChannelCapabilities{Send: true, Text: true, Permissions: true, Questions: true},
		})
	require.NoError(t, err)
	channel, err := f.Store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: toolsTestProjectID, IntegrationInstallID: install.ID, ChannelDefinitionID: definition.ID,
		ProviderRef: "customer-room", ProviderRefKind: "room",
	})
	require.NoError(t, err)
	bindIntegrationToolTarget(t, ctx, f.Store, f.Agent.ID, channel)
	require.NoError(t, seedIntegrationToolCurrentChannel(ctx, f.Pool, toolsTestProjectID, f.Agent.ID, channel.ID))
	call := f.recordPendingToolCall(
		t, ctx, "permission", toolcatalog.ToolNameListProcesses, `{}`, f.Now.Add(20*time.Second))
	turn := f.turn()
	turn.Tools[call.Name] = ToolSpec{Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)}
	e := Executor{Store: f.Store, ChannelOperations: unexpectedChannelOperations(t),
		BackgroundRunner: immediateIntegrationBackgroundRunner(ctx)}
	require.NoError(t, e.PrepareToolCallPermission(ctx, turn, call))
	interaction := integrationToolInteraction(t, ctx, f, f.toolCallID(t, ctx, call.ID), "permission")
	require.NoError(t, e.PostIntegrationRuntimeMessage(ctx, turn, "runtime notice"))
	require.NoError(t, e.PostIntegrationRuntimeMessage(ctx, turn, "runtime notice"))
	require.NoError(t, e.postIntegrationPrompt(ctx, turn, interaction))
	page, err := f.Store.Execution().ListPendingExternalChannelRequests(ctx,
		executionstore.ListExternalChannelRequestsInput{
			ProjectID: toolsTestProjectID, IntegrationInstallID: install.ID, Limit: 10,
		})
	require.NoError(t, err)
	require.Len(t, page.Requests, 2, "replayed acceptance retains the same finite owner")
	for _, request := range page.Requests {
		require.Equal(t, uuid.Nil, request.ToolCallID)
		require.Equal(t, turn.TurnID, request.TurnID)
		require.Equal(t, channel.ID, request.IntegrationTargetID)
		require.WithinDuration(t, request.CreatedAt.Add(executionstore.ExternalChannelRequestTimeout),
			request.Deadline, time.Second)
		id, err := publicid.Encode(publicid.KindExternalChannelRequest, request.ID)
		require.NoError(t, err)
		result := completedTestChannelSend(channelconnector.OperationRequest{RequestID: id})
		if request.Operation == channelconnector.OperationInteraction {
			require.Equal(t, interaction.ID, request.InteractionID)
			var payload channelconnector.InteractionPayload
			require.NoError(t, json.Unmarshal(request.Payload, &payload))
			canonicalID, err := publicid.Encode(publicid.KindAgentInteraction, interaction.ID)
			require.NoError(t, err)
			require.Equal(t, canonicalID, payload.InteractionID)
			result = completedTestChannelInteraction(channelconnector.OperationRequest{RequestID: id})
		} else {
			require.Equal(t, "runtime_error:"+turn.RuntimeLockID.String(), request.NoticeKey)
		}
		_, err = f.Store.Execution().CompleteExternalChannelRequest(ctx, executionstore.CompleteExternalChannelRequestInput{
			ProjectID: toolsTestProjectID, IntegrationInstallID: install.ID, ID: request.ID, Result: result,
		})
		require.NoError(t, err)
	}
	interaction = integrationToolInteraction(t, ctx, f, interaction.ToolCallID, "permission")
	require.Equal(t, executionstore.AgentInteractionStateOpen, interaction.State)
	record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, interaction.ToolCallID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateAwaitingPermission, record.State)
}
