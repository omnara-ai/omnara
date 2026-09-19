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
	resources := map[string]agentconfig.AppResourceCompiled{"chat": f.resource()}
	definition := f.definition(t, "receive", resources)
	launch := f.launchInput(uuid.Nil, "listener-admission")
	launch.DerivedConfig = &definition
	agent, err := f.store.Execution().LaunchAgent(f.ctx, launch)
	require.NoError(t, err)
	slot := inboxInputPlan(agent.Agent.ID, f.connection, "message:listener")
	slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}
	slot.Listener = &executionstore.InboxListenerAuthority{
		Event: "message",
		Alternatives: []executionstore.InboxListenerReference{
			{ResourceKey: "chat", Address: integrationstore.ConversationAddress{Kind: "channel", Ref: "C123"}},
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
