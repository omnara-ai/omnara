//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestAppPostCompletionAtomicallyRegistersFollow(t *testing.T) {
	for _, scenario := range []string{
		"success_replay", "completion_rollback", "revoked", "invalid_address", "unsupported_tool",
	} {
		t.Run(scenario, func(t *testing.T) {
			f := newAppActivationFixture(t)
			resource := f.listener()
			resource.Config = json.RawMessage(`{}`)
			toolName := "app__chat__post_message"
			arguments := json.RawMessage(`{"text":"Start here","follow_replies":true}`)
			if scenario == "unsupported_tool" {
				toolName = "app__chat__read"
				arguments = json.RawMessage(`{}`)
			}
			resources := map[string]agentconfig.AppCapabilityCompiled{"chat__thread_messages": resource}
			definition := f.withSendingTools(t, f.definition(t, "Confirmed replies", resources))
			launchInput := f.launchInput(uuid.Nil, "post-follow")
			launchInput.DerivedConfig = &definition
			launch, err := f.store.Execution().LaunchAgent(f.ctx, launchInput)
			require.NoError(t, err)
			lock, err := f.store.Execution().AcquireAgentRuntimeLock(
				f.ctx,
				testProjectID,
				launch.Agent.ID,
				testWorkerProcessID,
				testAgentRuntimeLockLeaseDuration,
			)
			require.NoError(t, err)
			fixture := processDaemonFixture{Store: f.store, AgentID: launch.Agent.ID, UserID: f.user.ID, Lock: lock}
			ids := createToolCallBatchForProcessTest(t, f.ctx, fixture, "confirmed-follow", []processToolCallBatchItem{
				{
					TestName: "post",
					ToolName: toolName,
					ToolType: toolcatalog.ToolTypeBuiltIn,
					Allowed:  true,
					Input:    arguments,
				},
				{
					TestName: "post-again",
					ToolName: "app__chat__post_message",
					ToolType: toolcatalog.ToolTypeBuiltIn,
					Allowed:  true,
					Input:    json.RawMessage(`{"text":"Another update","follow_replies":true}`),
				},
			})
			claimToolCallForTest(t, f.ctx, f.store, launch.Agent.ID, ids[0], lock.ID, true)
			completion := executionstore.CompleteRuntimeToolCallInput{
				ProjectID:     testProjectID,
				AgentID:       launch.Agent.ID,
				ID:            ids[0],
				RuntimeLockID: lock.ID,
				Outcome:       executionstore.ToolResultOutcomeSucceeded,
				ResultContentParts: json.RawMessage(
					`[{"type":"structured_data","value":{"message_ts":"111.222","follow_replies":true}}]`,
				),
			}
			follow := executionstore.ConfirmedAppFollow{
				ListenerKey: "chat__thread_messages",
				AppID:       f.app.ID,
				Scope: appdefinition.Scope{
					Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "111.222"},
				},
			}
			switch scenario {
			case "completion_rollback":
				_, err := f.store.pool.Exec(
					f.ctx,
					`CREATE FUNCTION reject_follow_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
				IF NEW.state='completed' THEN RAISE EXCEPTION 'injected completion failure'; END IF; RETURN NEW; END $$;
				CREATE TRIGGER reject_follow_completion BEFORE UPDATE ON tool_calls
				FOR EACH ROW EXECUTE FUNCTION reject_follow_completion()`,
				)
				require.NoError(t, err)
			case "revoked":
				resources = nil
				_, err := f.store.Execution().ChangeAgentConfig(
					f.ctx,
					f.changeInput(t, launch.Agent.ID, "Stop following", resources, "revoke-follow"),
				)
				require.NoError(t, err)
			case "invalid_address":
				follow.Scope.Slack.ChannelID = "invalid"
			}
			result, err := f.store.Execution().CompleteAppPostToolCall(f.ctx, completion, follow)
			if scenario != "success_replay" {
				if scenario == "completion_rollback" {
					require.ErrorContains(t, err, "injected completion failure")
				} else if scenario == "invalid_address" {
					require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
				} else {
					require.ErrorIs(t, err, storeerr.ErrUnauthorized)
				}
				require.Empty(t, f.listeners(t, launch.Agent.ID))
				var targets int
				require.NoError(
					t,
					f.store.pool.QueryRow(
						f.ctx,
						`SELECT count(*) FROM integration_targets WHERE agent_id=$1`,
						launch.Agent.ID,
					).Scan(
						&targets,
					),
				)
				require.Zero(t, targets)
				return
			}
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolCallStateCompleted, result.State)
			listeners := f.listeners(t, launch.Agent.ID)
			require.Len(t, listeners, 1)
			require.Equal(t, "C123:111.222", listeners[0].ScopeRef)
			require.Equal(t, ids[0], *listeners[0].ToolCallID)
			_, err = f.store.Execution().CompleteAppPostToolCall(f.ctx, completion, follow)
			require.NoError(t, err)
			require.Len(t, f.listeners(t, launch.Agent.ID), 1)
			// Freeze a reply before a second post refreshes this same follow.
			// The new post changes provenance, not permission to receive the reply.
			slot := inboxInputPlan(launch.Agent.ID, f.app, "message:between-posts")
			slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"}
			slot.Listener = &executionstore.InboxListenerAuthority{
				Event: "message",
				Alternatives: []executionstore.InboxListenerReference{{
					ListenerKey: "chat__thread_messages", Address: slot.Input.Origin.Address,
				}},
			}
			receipt := freezeInboxInput(t, f, slot, "reply-between-posts", time.Minute)
			claimToolCallForTest(t, f.ctx, f.store, launch.Agent.ID, ids[1], lock.ID, true)
			second := completion
			second.ID = ids[1]
			_, err = f.store.Execution().CompleteAppPostToolCall(f.ctx, second, follow)
			require.NoError(t, err)
			listeners = f.listeners(t, launch.Agent.ID)
			require.Len(t, listeners, 1, "another post into the same followed conversation reuses its listener")
			require.Equal(t, ids[1], *listeners[0].ToolCallID)
			admitted, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
			require.NoError(t, err)
			require.True(t, admitted.Created)
			slot.Input.IdempotencyKey = "message:revoked-follow"
			revoked := freezeInboxInput(t, f, slot, "revoked-follow-reply", time.Minute)
			resources = nil
			_, err = f.store.Execution().ChangeAgentConfig(
				f.ctx,
				f.changeInput(t, launch.Agent.ID, "Stop following", resources, "revoke-follow"),
			)
			require.NoError(t, err)
			require.Empty(t, f.listeners(t, launch.Agent.ID))
			_, err = f.store.Execution().AdmitInboxInputSlot(f.ctx, revoked.Lease(), "recipient")
			require.ErrorIs(t, err, storeerr.ErrUnauthorized)
			replayed, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
			require.NoError(t, err)
			require.False(t, replayed.Created)
			require.Equal(t, admitted.AgentInput.ID, replayed.AgentInput.ID)
			_, err = f.store.Execution().CompleteAppPostToolCall(f.ctx, completion, follow)
			require.NoError(t, err)
			require.Empty(t, f.listeners(t, launch.Agent.ID), "replaying completion must not restore a revoked follow")
		})
	}
}

func TestAppPostCompletionUsesOriginalSenderAndCurrentListener(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		allowed bool
	}{
		{name: "sender_removed", allowed: true},
		{name: "sender_denied", allowed: true},
		{name: "sender_disabled", allowed: true},
		{name: "sender_reconfigured", allowed: true},
		{name: "listener_removed"},
		{name: "app_disconnected"},
		{name: "original_sender_missing"},
		{name: "original_sender_denied"},
		{name: "original_sender_disabled"},
		{name: "original_listener_missing"},
		{name: "follow_not_requested"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newAppActivationFixture(t)
			listener := f.listener()
			listener.Config = json.RawMessage(`{}`)
			definition := f.withSendingTools(
				t,
				f.definition(t, "Follow confirmed replies", map[string]agentconfig.AppCapabilityCompiled{
					"chat__thread_messages": listener,
				}),
			)
			var original, current agentconfig.Compiled
			require.NoError(t, json.Unmarshal(definition.CompiledDefinition, &original))
			require.NoError(t, json.Unmarshal(definition.CompiledDefinition, &current))
			name := toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage)
			sender := original.Tools[name]
			arguments := json.RawMessage(`{"text":"Confirmed post","follow_replies":true}`)
			switch scenario.name {
			case "original_sender_missing":
				delete(original.Tools, name)
			case "original_sender_denied":
				sender.Permission = toolpermission.DefaultSelection(toolpermission.ModeAlwaysDeny)
				original.Tools[name] = sender
			case "original_sender_disabled":
				sender.Enabled = false
				original.Tools[name] = sender
			case "original_listener_missing":
				original.Listeners = nil
			case "follow_not_requested":
				arguments = json.RawMessage(`{"text":"Confirmed post"}`)
			}
			originalDefinition := f.encodedDefinition(t, original)
			launchInput := f.launchInput(uuid.Nil, "confirmed-before-config-edit")
			launchInput.DerivedConfig = &originalDefinition
			launch, err := f.store.Execution().LaunchAgent(f.ctx, launchInput)
			require.NoError(t, err)
			lock, err := f.store.Execution().AcquireAgentRuntimeLock(
				f.ctx, testProjectID, launch.Agent.ID, testWorkerProcessID, testAgentRuntimeLockLeaseDuration,
			)
			require.NoError(t, err)
			fixture := processDaemonFixture{Store: f.store, AgentID: launch.Agent.ID, UserID: f.user.ID, Lock: lock}
			ids := createToolCallBatchForProcessTest(t, f.ctx, fixture, "confirmed-before-edit", []processToolCallBatchItem{{
				TestName: "post", ToolName: name, ToolType: toolcatalog.ToolTypeBuiltIn, Allowed: true, Input: arguments,
			}})
			claimToolCallForTest(t, f.ctx, f.store, launch.Agent.ID, ids[0], lock.ID, true)
			completion := executionstore.CompleteRuntimeToolCallInput{
				ProjectID:     testProjectID,
				AgentID:       launch.Agent.ID,
				ID:            ids[0],
				RuntimeLockID: lock.ID,
				Outcome:       executionstore.ToolResultOutcomeSucceeded,
				ResultContentParts: json.RawMessage(
					`[{"type":"structured_data","value":{"message_ts":"111.222","follow_replies":true}}]`,
				),
			}
			follow := executionstore.ConfirmedAppFollow{
				ListenerKey: "chat__thread_messages", AppID: f.app.ID,
				Scope: appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "111.222"}},
			}
			// The post is already confirmed. Only its original sender and the live
			// receiver authority govern persistence; no provider request is needed.
			sender = current.Tools[name]
			switch scenario.name {
			case "sender_removed":
				delete(current.Tools, name)
			case "sender_denied":
				sender.Permission = toolpermission.DefaultSelection(toolpermission.ModeAlwaysDeny)
				current.Tools[name] = sender
			case "sender_disabled":
				sender.Enabled = false
				current.Tools[name] = sender
			case "sender_reconfigured":
				sender.Config = json.RawMessage(`{"channel_id":"C999"}`)
				current.Tools[name] = sender
			case "listener_removed":
				current.Listeners = nil
			}
			current.Instruction = "Config edited after confirmation"
			update := f.changeInput(t, launch.Agent.ID, current.Instruction, current.Listeners, "after-confirmation")
			update.CreateAgentConfigInput = f.encodedDefinition(t, current)
			changed, err := f.store.Execution().ChangeAgentConfig(f.ctx, update)
			require.NoError(t, err)
			if scenario.name == "app_disconnected" {
				f.disable(t)
			}
			result, err := f.store.Execution().CompleteAppPostToolCall(f.ctx, completion, follow)
			if !scenario.allowed {
				require.ErrorIs(t, err, storeerr.ErrUnauthorized)
				require.Empty(t, f.listeners(t, launch.Agent.ID))
				record, err := f.store.Execution().GetToolCall(f.ctx, testProjectID, launch.Agent.ID, ids[0])
				require.NoError(t, err)
				require.Equal(t, executionstore.ToolCallStateRunning, record.State, "rejected follow must not commit completion")
				return
			}
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolCallStateCompleted, result.State)
			require.Equal(t, executionstore.ToolResultOutcomeSucceeded, result.Outcome)
			listeners := f.listeners(t, launch.Agent.ID)
			require.Len(t, listeners, 1)
			require.Equal(t, f.app.ID, listeners[0].AppID)
			require.Equal(t, "C123:111.222", listeners[0].ScopeRef, "follow the confirmed destination, not the edited sender")
			require.Equal(t, changed.AgentConfig.ID, listeners[0].SourceConfigID)
			require.Equal(t, []string{"message"}, listeners[0].Events)
			require.Equal(t, ids[0], *listeners[0].ToolCallID)
			replayed, err := f.store.Execution().CompleteAppPostToolCall(f.ctx, completion, follow)
			require.NoError(t, err)
			require.JSONEq(t, string(result.ResultContentParts), string(replayed.ResultContentParts))
			require.Equal(t, listeners, f.listeners(t, launch.Agent.ID), "completion replay must preserve the same subscription")
		})
	}
}
