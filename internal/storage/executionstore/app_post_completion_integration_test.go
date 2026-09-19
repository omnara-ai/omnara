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
	"github.com/stretchr/testify/require"
)

func TestAppPostCompletionAtomicallyRegistersFollow(t *testing.T) {
	for _, scenario := range []string{
		"success_replay", "completion_rollback", "revoked", "outside_scope", "unsupported_tool",
	} {
		t.Run(scenario, func(t *testing.T) {
			f := newAppActivationFixture(t)
			resource := f.resource()
			resource.Listener = nil
			resource.Tools = []string{toolcatalog.ToolNameSlackPostMessage, toolcatalog.ToolNameSlackRead}
			toolName := toolcatalog.ToolNameSlackPostMessage
			arguments := json.RawMessage(`{"text":"Start here","follow_replies":true}`)
			if scenario == "unsupported_tool" {
				toolName = toolcatalog.ToolNameSlackRead
				arguments = json.RawMessage(`{}`)
			}
			resources := map[string]agentconfig.AppResourceCompiled{"chat": resource}
			definition := f.definition(t, "Confirmed replies", resources)
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
					ToolName: toolcatalog.ToolNameSlackPostMessage,
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
				ResourceKey:  "chat",
				ConnectionID: f.connection.ID,
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
				resource.Follow = nil
				resources["chat"] = resource
				_, err := f.store.Execution().ChangeAgentConfig(
					f.ctx,
					f.changeInput(t, launch.Agent.ID, "Stop following", resources, "revoke-follow"),
				)
				require.NoError(t, err)
			case "outside_scope":
				follow.Scope.Slack.ChannelID = "C999"
			}
			result, err := f.store.Execution().CompleteAppPostToolCall(f.ctx, completion, follow)
			if scenario != "success_replay" {
				if scenario == "completion_rollback" {
					require.ErrorContains(t, err, "injected completion failure")
				} else if scenario == "unsupported_tool" {
					require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
					require.ErrorContains(t, err, "this tool does not support following replies")
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
			slot := inboxInputPlan(launch.Agent.ID, f.connection, "message:between-posts")
			slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"}
			slot.Listener = &executionstore.InboxListenerAuthority{
				Event: "message",
				Alternatives: []executionstore.InboxListenerReference{{
					ResourceKey: "chat", Address: slot.Input.Origin.Address, Followed: true,
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
			resource.Follow = nil
			resources["chat"] = resource
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
