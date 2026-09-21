//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestInboxSubscriptionRechecksRevocationAndPreservesReplay(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	launch := f.launchInput(f.profile.CurrentConfigID, "subscription-admission")
	launch.Subscriptions = []integrationstore.AppSubscriptionAttachment{f.attachment()}
	agent, err := f.store.Execution().LaunchAgent(f.ctx, launch)
	require.NoError(t, err)
	original := f.subscriptions(t, agent.Agent.ID)[0]
	slot := inboxInputPlan(agent.Agent.ID, f.app, "message:subscription")
	slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}
	slot.Subscription = &executionstore.InboxSubscriptionAuthority{
		Event: "message", Alternatives: []executionstore.InboxSubscriptionReference{{
			Type: "thread_messages", Address: integrationstore.ConversationAddress{Kind: "channel", Ref: "C123"},
		}},
	}
	receipt := freezeInboxInput(t, f, slot, "subscription-receipt", time.Minute)
	require.NoError(
		t,
		f.store.Execution().CheckInboxConversationAuthority(f.ctx, receipt.Lease(), "recipient", slot.Input.Origin.Address),
	)
	require.ErrorIs(t, f.store.Execution().CheckInboxConversationAuthority(f.ctx, receipt.Lease(), "recipient",
		integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:9.9"}), storeerr.ErrUnauthorized)
	_, err = f.store.Execution().ChangeAgentConfig(f.ctx, f.changeInput(t, agent.Agent.ID, "unrelated edit", "unrelated"))
	require.NoError(t, err)
	result, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
	require.NoError(t, err)
	require.True(t, result.Created)
	slot.Input.IdempotencyKey = "message:revoked"
	revoked := freezeInboxInput(t, f, slot, "revoked-receipt", time.Minute)
	f.detach(t, agent.Agent.ID)
	require.ErrorIs(
		t,
		f.store.Execution().CheckInboxConversationAuthority(f.ctx, revoked.Lease(), "recipient", slot.Input.Origin.Address),
		storeerr.ErrUnauthorized,
	)
	_, err = f.store.Execution().AdmitInboxInputSlot(f.ctx, revoked.Lease(), "recipient")
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	var count int
	require.NoError(
		t,
		f.store.pool.QueryRow(
			f.ctx,
			`SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND input_kind='content'`,
			agent.Agent.ID,
		).Scan(&count),
	)
	require.Equal(t, 1, count)
	replayed, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, result.AgentInput.ID, replayed.AgentInput.ID)
	require.Empty(t, f.subscriptions(t, agent.Agent.ID))
	replacement := f.attach(t, agent.Agent.ID, f.attachment())
	require.NotEqual(t, original.ID, replacement.ID)
	require.NoError(
		t,
		f.store.Integrations().DeleteAppSubscription(f.ctx, testOrgID, testProjectID, f.app.ID, original.ID),
	)
	result, err = f.store.Execution().AdmitInboxInputSlot(f.ctx, revoked.Lease(), "recipient")
	require.NoError(t, err, "live address/type/event authorization does not pin the old subscription ID")
	require.True(t, result.Created)
}

func TestInboxSubscriptionAuthorityUsesLiveTypeAddressEventAndApp(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"matching alternative",
		"type",
		"address",
		"event",
		"config edit",
		"detached",
		"other app",
		"disconnected",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newAppActivationFixture(t)
			f.app = inboxInputApp(t, f, "github")
			agent, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "keyed-subscription"))
			require.NoError(t, err)
			attachment := integrationstore.AppSubscriptionAttachment{
				AppID: f.app.ID, Type: "pull_request", Conversation: json.RawMessage(`{"repository_id":123,"pull_request":42}`),
				Events: []string{"discussion_comment"},
			}
			f.attach(t, agent.Agent.ID, attachment)
			slot := inboxInputPlan(agent.Agent.ID, f.app, "message:keyed")
			slot.Subscription = &executionstore.InboxSubscriptionAuthority{
				Event: "discussion_comment", Alternatives: []executionstore.InboxSubscriptionReference{
					{Type: "not_exported", Address: slot.Input.Origin.Address},
					{Type: "pull_request", Address: slot.Input.Origin.Address},
				},
			}
			if scenario == "type" {
				slot.Subscription.Alternatives[1].Type = "another_type"
			}
			receipt := freezeInboxInput(t, f, slot, "keyed-subscription-receipt", time.Minute)
			switch scenario {
			case "address", "event", "other app":
				f.detach(t, agent.Agent.ID)
				switch scenario {
				case "address":
					attachment.Conversation = json.RawMessage(`{"repository_id":123,"pull_request":43}`)
				case "event":
					attachment.Events = []string{"review_comment"}
				case "other app":
					other, err := f.store.Integrations().CreateProjectApp(f.ctx, integrationstore.SaveProjectAppInput{
						OrgID: testOrgID, ProjectID: testProjectID, Name: "another-app", DefinitionID: f.app.DefinitionID,
					})
					require.NoError(t, err)
					secret, err := f.store.Secrets().GetSecret(f.ctx, testOrgID, f.app.CredentialSecretID)
					require.NoError(t, err)
					_, err = f.store.Integrations().ConfigureProjectApp(f.ctx, integrationstore.ConfigureProjectAppInput{
						OrgID: testOrgID, ProjectID: testProjectID, AppID: other.ID, InstalledByUserID: f.user.ID,
						Provider: f.app.Provider, ProviderTenantID: f.app.ProviderTenantID,
						ProviderAccountRef: f.app.ProviderAccountRef, CredentialAppID: 123,
						CredentialSecretID: secret.ID, CredentialVersionID: secret.CurrentVersionID,
						ExpectedSetupRevision: other.SetupRevision, ProviderIdentity: f.app.ProviderIdentity,
					})
					require.NoError(t, err)
					attachment.AppID = other.ID
				}
				f.attach(t, agent.Agent.ID, attachment)
			case "config edit":
				_, err = f.store.Execution().ChangeAgentConfig(
					f.ctx,
					f.changeInput(t, agent.Agent.ID, "Config does not own receive authority", "config-edit"),
				)
				require.NoError(t, err)
			case "detached":
				f.detach(t, agent.Agent.ID)
			case "disconnected":
				f.disable(t)
			}
			result, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
			if scenario == "matching alternative" || scenario == "config edit" {
				require.NoError(t, err)
				require.True(t, result.Created)
			} else {
				require.ErrorIs(t, err, storeerr.ErrUnauthorized)
			}
		})
	}
}

func TestSubagentExplicitSubscriptionReceivesAndArchiveCleansUp(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	f.profile = mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		f.ctx,
		f.store,
		"subscription-parent",
		"Subscription parent",
		subagentParentYAML,
	)
	definition := f.withSendingTools(t, f.definition(t, "Parent may send"))
	input := f.launchInput(uuid.Nil, "subscription-parent")
	input.DerivedConfig = &definition
	input.Subscriptions = []integrationstore.AppSubscriptionAttachment{f.attachment()}
	parent, err := f.store.Execution().LaunchAgent(f.ctx, input)
	require.NoError(t, err)
	child, err := spawnSubagentForTest(
		t,
		f.ctx,
		f.store,
		parent.Agent,
		f.profile.CurrentConfigID,
		"receiver",
		"subscription-child",
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, parent.Agent.ID, child.Agent.ParentAgentID)
	require.Empty(t, f.subscriptions(t, child.Agent.ID), "subagents inherit no subscriptions")
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(child.AgentConfig.CompiledDefinition, &compiled))
	require.NotContains(t, compiled.Tools, "app__chat__post_message")
	attachment := f.attachment()
	attachment.Conversation = json.RawMessage(`{"channel_id":"C123","thread_ts":"1.2"}`)
	f.attach(t, child.Agent.ID, attachment)
	slot := inboxInputPlan(child.Agent.ID, f.app, "message:child")
	slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}
	slot.Subscription = &executionstore.InboxSubscriptionAuthority{
		Event: "message", Alternatives: []executionstore.InboxSubscriptionReference{{
			Type: "thread_messages", Address: slot.Input.Origin.Address,
		}},
	}
	receipt := freezeInboxInput(t, f, slot, "child-receipt", time.Minute)
	received, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
	require.NoError(t, err)
	require.True(t, received.Created)
	require.Equal(t, child.Agent.ID, received.AgentInput.AgentID)
	slot.Input.IdempotencyKey = "message:after-archive"
	pending := freezeInboxInput(t, f, slot, "child-after-archive", time.Minute)
	_, _, err = f.store.Execution().ArchiveAgent(f.ctx, testProjectID, child.Agent.ID, userPrincipal(f.user.ID))
	require.NoError(t, err)
	require.Empty(t, f.subscriptions(t, child.Agent.ID))
	require.Len(t, f.subscriptions(t, parent.Agent.ID), 1, "child archival leaves its parent's attachment intact")
	_, err = f.store.Execution().AdmitInboxInputSlot(f.ctx, pending.Lease(), "recipient")
	require.Error(t, err, "archived subagent cannot receive the frozen follow-up")
	_, err = f.store.Integrations().CreateAppSubscription(f.ctx, integrationstore.CreateAppSubscriptionInput{
		OrgID: testOrgID, ProjectID: testProjectID, AgentID: child.Agent.ID, AppID: attachment.AppID,
		Type: attachment.Type, Conversation: attachment.Conversation,
	})
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	require.Empty(t, f.subscriptions(t, child.Agent.ID), "archive cleanup cannot be reversed by attaching again")
}
