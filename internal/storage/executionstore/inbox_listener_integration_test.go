//go:build integration

package executionstore_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestInboxListenerRechecksRevocationAndPreservesReplay(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	resources := map[string]agentconfig.AppCapabilityCompiled{"chat__thread_messages": f.listener()}
	definition := f.definition(t, "receive", resources)
	launch := f.launchInput(uuid.Nil, "listener-admission")
	launch.DerivedConfig = &definition
	agent, err := f.store.Execution().LaunchAgent(f.ctx, launch)
	require.NoError(t, err)
	slot := inboxInputPlan(agent.Agent.ID, f.app, "message:listener")
	slot.Input.Origin.Address = integrationstore.ConversationAddress{
		Kind: "thread",
		Ref:  "C123:1.2",
	}
	slot.Listener = &executionstore.InboxListenerAuthority{
		Event: "message",
		Alternatives: []executionstore.InboxListenerReference{
			{
				ListenerKey: "chat__thread_messages",
				Address:     integrationstore.ConversationAddress{Kind: "channel", Ref: "C123"},
			},
		},
	}
	receipt := freezeInboxInput(t, f, slot, "listener-receipt", time.Minute)
	require.NoError(
		t,
		f.store.Execution().
			CheckInboxConversationAuthority(f.ctx, receipt.Lease(), "recipient", slot.Input.Origin.Address),
	)
	require.ErrorIs(
		t,
		f.store.Execution().
			CheckInboxConversationAuthority(
				f.ctx,
				receipt.Lease(),
				"recipient",
				integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:9.9"},
			),
		storeerr.ErrUnauthorized,
	)
	_, err = f.store.Execution().
		ChangeAgentConfig(f.ctx, f.changeInput(t, agent.Agent.ID, "unrelated edit", resources, "unrelated"))
	require.NoError(t, err)
	result, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
	require.NoError(t, err)
	require.True(t, result.Created)
	// A later frozen input cannot use a removed subscription, while the
	// already committed semantic event remains a read-only replay.
	slot.Input.IdempotencyKey = "message:revoked"
	revoked := freezeInboxInput(t, f, slot, "revoked-receipt", time.Minute)
	_, err = f.store.Execution().
		ChangeAgentConfig(f.ctx, f.changeInput(t, agent.Agent.ID, "removed listener", nil, "remove"))
	require.NoError(t, err)
	require.ErrorIs(
		t,
		f.store.Execution().
			CheckInboxConversationAuthority(f.ctx, revoked.Lease(), "recipient", slot.Input.Origin.Address),
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
		).
			Scan(
				&count,
			),
	)
	require.Equal(t, 1, count)
	replayed, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, result.AgentInput.ID, replayed.AgentInput.ID)
	// Recovery after explicit reauthorization admits the original frozen work.
	_, err = f.store.Execution().
		ChangeAgentConfig(f.ctx, f.changeInput(t, agent.Agent.ID, "restore listener", resources, "restore"))
	require.NoError(t, err)
	result, err = f.store.Execution().AdmitInboxInputSlot(f.ctx, revoked.Lease(), "recipient")
	require.NoError(t, err)
	require.True(t, result.Created)
}

func TestInboxListenerAuthorityUsesCurrentKeyedSubscription(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"origin", "listener key", "address", "event", "config", "inactive", "app"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			f := newAppActivationFixture(t)
			definition := f.definition(
				t,
				"receive",
				map[string]agentconfig.AppCapabilityCompiled{"chat__thread_messages": f.listener()},
			)
			launch := f.launchInput(uuid.Nil, "keyed-listener")
			launch.DerivedConfig = &definition
			agent, err := f.store.Execution().LaunchAgent(f.ctx, launch)
			require.NoError(t, err)
			slot := inboxInputPlan(agent.Agent.ID, f.app, "message:keyed")
			slot.Input.Origin.Address = integrationstore.ConversationAddress{
				Kind: "thread",
				Ref:  "C123:1.2",
			}
			slot.Listener = &executionstore.InboxListenerAuthority{
				Event: "message",
				Alternatives: []executionstore.InboxListenerReference{
					{
						ListenerKey: "missing__thread_messages",
						Address: integrationstore.ConversationAddress{
							Kind: "channel",
							Ref:  "C123",
						},
					},
					{
						ListenerKey: "chat__thread_messages",
						Address: integrationstore.ConversationAddress{
							Kind: "channel",
							Ref:  "C123",
						},
					},
				},
			}
			receipt := freezeInboxInput(t, f, slot, "keyed-listener-receipt", time.Minute)
			// Change one durable authority fact after freezing the recipient.
			switch change {
			case "origin":
				_, err = f.store.pool.Exec(
					f.ctx,
					`UPDATE agent_listeners SET origin='runtime' WHERE agent_id=$1`,
					agent.Agent.ID,
				)
			case "listener key":
				_, err = f.store.pool.Exec(
					f.ctx,
					`UPDATE agent_listeners SET listener_key='replacement__thread_messages' WHERE agent_id=$1`,
					agent.Agent.ID,
				)
			case "address":
				_, err = f.store.pool.Exec(
					f.ctx,
					`UPDATE agent_listeners SET scope_ref='C999' WHERE agent_id=$1`,
					agent.Agent.ID,
				)
			case "event":
				_, err = f.store.pool.Exec(
					f.ctx,
					`UPDATE agent_listeners SET events=ARRAY['reaction'] WHERE agent_id=$1`,
					agent.Agent.ID,
				)
			case "config":
				_, err = f.store.pool.Exec(
					f.ctx,
					`UPDATE agent_listeners SET source_config_id=$2 WHERE agent_id=$1`,
					agent.Agent.ID,
					f.profile.CurrentConfig.ID,
				)
			case "inactive":
				_, err = f.store.pool.Exec(
					f.ctx,
					`UPDATE agent_listeners SET active=false WHERE agent_id=$1`,
					agent.Agent.ID,
				)
			case "app":
				_, err = f.store.Integrations().
					DisconnectProjectApp(f.ctx, integrationstore.DisconnectProjectAppInput{ProjectID: testProjectID, AppID: f.app.ID})
			}
			require.NoError(t, err)
			result, err := f.store.Execution().
				AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
			if change == "origin" {
				require.NoError(t, err, "provenance does not pin receive authority")
				require.True(t, result.Created)
			} else {
				require.ErrorIs(t, err, storeerr.ErrUnauthorized)
			}
		})
	}
}
