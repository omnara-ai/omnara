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
		"success_replay", "completion_rollback", "disconnected", "invalid_address", "unsupported_tool",
	} {
		t.Run(scenario, func(t *testing.T) {
			f := newAppActivationFixture(t)
			toolName := "app__chat__post_message"
			arguments := json.RawMessage(`{"channel_id":"C123","text":"Start here","follow_replies":true}`)
			if scenario == "unsupported_tool" {
				toolName = "app__chat__read"
				arguments = json.RawMessage(`{}`)
			}
			definition := f.withSendingTools(t, f.definition(t, "Confirmed replies"))
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
					Input:    json.RawMessage(`{"channel_id":"C123","text":"Another update","follow_replies":true}`),
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
				SubscriptionType: "thread_messages",
				AppID:            f.app.ID,
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
			case "disconnected":
				f.disable(t)
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
				require.Empty(t, f.subscriptions(t, launch.Agent.ID))
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
			subscriptions := f.subscriptions(t, launch.Agent.ID)
			require.Len(t, subscriptions, 1)
			require.Equal(t, "C123:111.222", subscriptions[0].ScopeRef)
			require.Equal(t, ids[0], *subscriptions[0].ToolCallID)
			_, err = f.store.Execution().CompleteAppPostToolCall(f.ctx, completion, follow)
			require.NoError(t, err)
			require.Len(t, f.subscriptions(t, launch.Agent.ID), 1)
			// Freeze a reply before a second post reuses this same subscription.
			slot := inboxInputPlan(launch.Agent.ID, f.app, "message:between-posts")
			slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"}
			slot.Subscription = &executionstore.InboxSubscriptionAuthority{
				Event: "message",
				Alternatives: []executionstore.InboxSubscriptionReference{{
					Type: "thread_messages", Address: slot.Input.Origin.Address,
				}},
			}
			receipt := freezeInboxInput(t, f, slot, "reply-between-posts", time.Minute)
			// Lowering quota blocks growth, not existing routing or a fresh post to
			// an already followed conversation.
			_, err = f.store.pool.Exec(f.ctx, `INSERT INTO org_resource_limit_overrides
			 (org_id,max_active_app_subscriptions_per_agent) VALUES($1,0)`, testOrgID)
			require.NoError(t, err)
			attachment := integrationstore.CreateAppSubscriptionInput{
				OrgID: testOrgID, ProjectID: testProjectID, AppID: f.app.ID, AgentID: launch.Agent.ID,
				Type: "thread_messages", Conversation: json.RawMessage(`{"channel_id":"C123","thread_ts":"111.222"}`),
			}
			existing, err := f.store.Integrations().CreateAppSubscription(f.ctx, attachment)
			require.NoError(t, err)
			require.Equal(t, subscriptions[0].ID, existing.ID)
			attachment.Conversation = json.RawMessage(`{"channel_id":"C123","thread_ts":"999.000"}`)
			_, err = f.store.Integrations().CreateAppSubscription(f.ctx, attachment)
			require.ErrorIs(t, err, storeerr.ErrConflict)
			claimToolCallForTest(t, f.ctx, f.store, launch.Agent.ID, ids[1], lock.ID, true)
			second := completion
			second.ID = ids[1]
			_, err = f.store.Execution().CompleteAppPostToolCall(f.ctx, second, follow)
			require.NoError(t, err)
			subscriptions = f.subscriptions(t, launch.Agent.ID)
			require.Len(t, subscriptions, 1, "another post into the same followed conversation reuses its subscription")
			require.Equal(t, ids[0], *subscriptions[0].ToolCallID,
				"reusing a subscription preserves its identity and provenance")
			admitted, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
			require.NoError(t, err)
			require.True(t, admitted.Created)
			slot.Input.IdempotencyKey = "message:revoked-follow"
			revoked := freezeInboxInput(t, f, slot, "revoked-follow-reply", time.Minute)
			err = f.store.Integrations().DeleteAppSubscription(f.ctx, testOrgID, testProjectID, f.app.ID, subscriptions[0].ID)
			require.NoError(t, err)
			require.Empty(t, f.subscriptions(t, launch.Agent.ID))
			_, err = f.store.Execution().AdmitInboxInputSlot(f.ctx, revoked.Lease(), "recipient")
			require.ErrorIs(t, err, storeerr.ErrUnauthorized)
			replayed, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
			require.NoError(t, err)
			require.False(t, replayed.Created)
			require.Equal(t, admitted.AgentInput.ID, replayed.AgentInput.ID)
			_, err = f.store.Execution().CompleteAppPostToolCall(f.ctx, completion, follow)
			require.NoError(t, err)
			require.Empty(t, f.subscriptions(t, launch.Agent.ID), "replaying completion must not restore a revoked follow")
		})
	}
}

func TestAppPostCompletionUsesOriginalSenderAndLiveApp(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		allowed bool
	}{
		{name: "sender_removed", allowed: true},
		{name: "sender_denied", allowed: true},
		{name: "sender_disabled", allowed: true},
		{name: "app_disconnected"},
		{name: "original_sender_missing"},
		{name: "original_sender_denied"},
		{name: "original_sender_disabled"},
		{name: "follow_not_requested"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newAppActivationFixture(t)
			definition := f.withSendingTools(t, f.definition(t, "Follow confirmed replies"))
			var original, current agentconfig.Compiled
			require.NoError(t, json.Unmarshal(definition.CompiledDefinition, &original))
			require.NoError(t, json.Unmarshal(definition.CompiledDefinition, &current))
			name := toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage)
			sender := original.Tools[name]
			arguments := json.RawMessage(`{"channel_id":"C123","text":"Confirmed post","follow_replies":true}`)
			switch scenario.name {
			case "original_sender_missing":
				delete(original.Tools, name)
			case "original_sender_denied":
				sender.Permission = toolpermission.DefaultSelection(toolpermission.ModeAlwaysDeny)
				original.Tools[name] = sender
			case "original_sender_disabled":
				sender.Enabled = false
				original.Tools[name] = sender
			case "follow_not_requested":
				arguments = json.RawMessage(`{"channel_id":"C123","text":"Confirmed post"}`)
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
				SubscriptionType: "thread_messages", AppID: f.app.ID,
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
			}
			current.Instruction = "Config edited after confirmation"
			update := f.changeInput(t, launch.Agent.ID, current.Instruction, "after-confirmation")
			update.CreateAgentConfigInput = f.encodedDefinition(t, current)
			_, err = f.store.Execution().ChangeAgentConfig(f.ctx, update)
			require.NoError(t, err)
			if scenario.name == "app_disconnected" {
				f.disable(t)
			}
			result, err := f.store.Execution().CompleteAppPostToolCall(f.ctx, completion, follow)
			if !scenario.allowed {
				require.ErrorIs(t, err, storeerr.ErrUnauthorized)
				require.Empty(t, f.subscriptions(t, launch.Agent.ID))
				record, err := f.store.Execution().GetToolCall(f.ctx, testProjectID, launch.Agent.ID, ids[0])
				require.NoError(t, err)
				require.Equal(t, executionstore.ToolCallStateRunning, record.State, "rejected follow must not commit completion")
				return
			}
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolCallStateCompleted, result.State)
			require.Equal(t, executionstore.ToolResultOutcomeSucceeded, result.Outcome)
			subscriptions := f.subscriptions(t, launch.Agent.ID)
			require.Len(t, subscriptions, 1)
			require.Equal(t, f.app.ID, subscriptions[0].AppID)
			require.Equal(t, "C123:111.222", subscriptions[0].ScopeRef,
				"follow the confirmed destination, not the edited sender")
			require.Equal(t, []string{"message"}, subscriptions[0].Events)
			require.Equal(t, ids[0], *subscriptions[0].ToolCallID)
			replayed, err := f.store.Execution().CompleteAppPostToolCall(f.ctx, completion, follow)
			require.NoError(t, err)
			require.JSONEq(t, string(result.ResultContentParts), string(replayed.ResultContentParts))
			require.Equal(t, subscriptions, f.subscriptions(t, launch.Agent.ID),
				"completion replay must preserve the same subscription")
		})
	}
}
