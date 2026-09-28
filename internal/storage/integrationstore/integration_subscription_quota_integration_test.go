//go:build integration

package integrationstore_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestIntegrationSubscriptionConversationQuotaBoundaryAndReplay(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	var subscriptions []integrationstore.IntegrationSubscriptionRecord
	for range 16 {
		agent := subscriptionAgent(t, f)
		row, err := f.store.CreateIntegrationSubscription(f.ctx,
			subscriptionInput(f, agent.ID, `{"channel_id":"C123"}`))
		require.NoError(t, err)
		subscriptions = append(subscriptions, row)
	}
	agent := subscriptionAgent(t, f)
	input := subscriptionInput(f, agent.ID, `{"channel_id":"C123"}`)
	_, err := f.store.CreateIntegrationSubscription(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	require.ErrorContains(t, err, "integration conversation limit of 16 subscribed agents reached")
	replay, err := f.store.CreateIntegrationSubscription(f.ctx,
		subscriptionInput(f, subscriptions[0].AgentID, `{"channel_id":"C123"}`))
	require.NoError(t, err, "existing subscriptions replay at the conversation limit")
	require.Equal(t, subscriptions[0].ID, replay.ID)

	sibling := subscriptionInput(f, agent.ID, `{"channel_id":"C456"}`)
	siblingRow, err := f.store.CreateIntegrationSubscription(f.ctx, sibling)
	require.NoError(t, err, "another address has independent capacity")
	other := f.addIntegration(t, "other-quota", integrationstore.IntegrationSettings(`{}`))
	otherInput := input
	otherInput.IntegrationID = other.ID
	_, err = f.store.CreateIntegrationSubscription(f.ctx, otherInput)
	require.NoError(t, err, "another integration sharing the provider account has independent capacity")

	f.exec(t, `INSERT INTO org_resource_limit_overrides(org_id,max_active_integration_subscriptions_per_agent)
		VALUES($1,1)`, f.org)
	replay, err = f.store.CreateIntegrationSubscription(f.ctx, sibling)
	require.NoError(t, err, "lowering the independent per-agent quota must not invalidate replay")
	require.Equal(t, siblingRow.ID, replay.ID)
	_, err = f.store.CreateIntegrationSubscription(f.ctx,
		subscriptionInput(f, agent.ID, `{"channel_id":"C789"}`))
	require.ErrorIs(t, err, storeerr.ErrConflict)
	require.ErrorContains(t, err, "integration subscriptions limit of 1 reached")
	var count int
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM integration_subscriptions WHERE agent_id=$1`, agent.ID).Scan(&count))
	require.Equal(t, 2, count, "per-agent quota failure rolls back the newly inserted subscription")

	require.NoError(t, f.store.DeleteIntegrationSubscription(
		f.ctx, f.org, f.project, f.integrationID, subscriptions[0].ID))
	replacement := subscriptionAgent(t, f)
	_, err = f.store.CreateIntegrationSubscription(f.ctx,
		subscriptionInput(f, replacement.ID, `{"channel_id":"C123"}`))
	require.NoError(t, err, "detaching an agent frees a conversation slot")
	_, _, err = executionstore.New(f.pool, executionstore.Config{}).ArchiveAgent(
		f.ctx, f.project, subscriptions[1].AgentID, identitystore.NewUserPrincipal(f.user))
	require.NoError(t, err)
	replacement = subscriptionAgent(t, f)
	_, err = f.store.CreateIntegrationSubscription(f.ctx,
		subscriptionInput(f, replacement.ID, `{"channel_id":"C123"}`))
	require.NoError(t, err, "archiving an agent frees a conversation slot")
}

func TestIntegrationSubscriptionConversationQuotaIndependentScopes(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	var channelAgent uuid.UUID
	var threadSubscription integrationstore.IntegrationSubscriptionRecord
	for _, conversation := range []string{`{"channel_id":"C123"}`, `{"channel_id":"C123","thread_ts":"1.2"}`} {
		for range 16 {
			agent := subscriptionAgent(t, f)
			row, err := f.store.CreateIntegrationSubscription(f.ctx, subscriptionInput(f, agent.ID, conversation))
			require.NoError(t, err)
			if row.Address.Kind == "channel" {
				channelAgent = agent.ID
			} else {
				threadSubscription = row
			}
		}
	}
	q := dbsqlc.New(f.pool)
	matching := dbsqlc.ListMatchingIntegrationSubscriptionsParams{
		ProjectID: f.project, IntegrationID: f.integrationID,
		Scopes: json.RawMessage(`[{"kind":"channel","ref":"C123"},{"kind":"thread","ref":"C123:1.2"}]`),
	}
	rows, err := q.ListMatchingIntegrationSubscriptions(f.ctx, matching)
	require.NoError(t, err)
	require.Len(t, rows, 32, "independent full scopes may match more than 16 agents")
	agents := make(map[uuid.UUID]bool)
	for _, row := range rows {
		agents[row.AgentID] = true
	}
	require.Len(t, agents, 32)

	// The same agent can occupy one slot in each scope; routing retains both matches for deduplication.
	require.NoError(t, f.store.DeleteIntegrationSubscription(
		f.ctx, f.org, f.project, f.integrationID, threadSubscription.ID))
	input := subscriptionInput(f, channelAgent, `{"channel_id":"C123","thread_ts":"1.2"}`)
	added, err := f.store.CreateIntegrationSubscription(f.ctx, input)
	require.NoError(t, err)
	replay, err := f.store.CreateIntegrationSubscription(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, added.ID, replay.ID)
	rows, err = q.ListMatchingIntegrationSubscriptions(f.ctx, matching)
	require.NoError(t, err)
	require.Len(t, rows, 32)
	agents = make(map[uuid.UUID]bool)
	for _, row := range rows {
		agents[row.AgentID] = true
	}
	require.Len(t, agents, 31, "replaying an attachment does not consume another distinct-agent slot")
}

func TestIntegrationSubscriptionConversationQuotaConcurrentLastSlot(t *testing.T) {
	t.Parallel()
	for _, writer := range []string{"attachment", "launch"} {
		t.Run(writer, func(t *testing.T) {
			t.Parallel()
			f := newInboxFixture(t)
			for range 15 {
				agent := subscriptionAgent(t, f)
				_, err := f.store.CreateIntegrationSubscription(f.ctx,
					subscriptionInput(f, agent.ID, `{"channel_id":"C123"}`))
				require.NoError(t, err)
			}
			first := subscriptionAgent(t, f)
			firstInput := subscriptionInput(f, first.ID, `{"channel_id":"C123"}`)
			var second func() error
			if writer == "attachment" {
				agent := subscriptionAgent(t, f)
				input := subscriptionInput(f, agent.ID, `{"channel_id":"C123"}`)
				second = func() error {
					_, err := f.store.CreateIntegrationSubscription(f.ctx, input)
					return err
				}
			} else {
				input := executionstore.LaunchAgentInput{
					ProjectID: f.project, AgentConfigID: first.CurrentConfigID,
					LaunchedBy: identitystore.NewUserPrincipal(f.user), IdempotencyKey: "concurrent-conversation-quota",
					Subscriptions: []integrationstore.IntegrationSubscriptionAttachment{{
						IntegrationID: f.integrationID, Conversation: firstInput.Conversation,
					}},
				}
				second = func() error {
					_, err := executionstore.New(f.pool, executionstore.Config{}).LaunchAgent(f.ctx, input)
					return err
				}
			}
			blocker := integrationdb.BeginTx(t, f.ctx, f.pool)
			require.NoError(t, integrationstore.LockConversationTx(f.ctx, blocker, f.project, f.integrationID,
				integrationstore.ConversationAddress{Kind: "channel", Ref: "C123"}))
			pending := []<-chan error{
				integrationdb.RunAsyncError(func() error {
					_, err := f.store.CreateIntegrationSubscription(f.ctx, firstInput)
					return err
				}),
				integrationdb.RunAsyncError(second),
			}
			integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockIntegrationConversation", 2)
			require.NoError(t, blocker.Commit(f.ctx))
			var succeeded, rejected int
			for _, done := range pending {
				err := integrationdb.Await(t, done, "competing subscription writer")
				if err == nil {
					succeeded++
				} else {
					require.ErrorIs(t, err, storeerr.ErrConflict)
					require.ErrorContains(t, err, "integration conversation limit of 16 subscribed agents reached")
					rejected++
				}
			}
			require.Equal(t, 1, succeeded)
			require.Equal(t, 1, rejected)
			page, err := f.store.ListIntegrationSubscriptions(f.ctx, integrationstore.ListIntegrationSubscriptionsInput{
				ProjectID: f.project, IntegrationID: f.integrationID, Limit: 100,
			})
			require.NoError(t, err)
			require.Len(t, page.Subscriptions, 16)
		})
	}
}

func TestIntegrationSubscriptionConversationQuotaLaunchBatchRollbackAndReplay(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	var configID uuid.UUID
	for range 15 {
		agent := subscriptionAgent(t, f)
		configID = agent.CurrentConfigID
		_, err := f.store.CreateIntegrationSubscription(f.ctx,
			subscriptionInput(f, agent.ID, `{"channel_id":"C123"}`))
		require.NoError(t, err)
	}
	attachment := integrationstore.IntegrationSubscriptionAttachment{
		IntegrationID: f.integrationID, Conversation: json.RawMessage(`{"channel_id":"C123"}`),
	}
	sibling := integrationstore.IntegrationSubscriptionAttachment{
		IntegrationID: f.integrationID, Conversation: json.RawMessage(`{"channel_id":"C456"}`),
	}
	input := executionstore.LaunchAgentInput{
		ProjectID: f.project, AgentConfigID: configID, LaunchedBy: identitystore.NewUserPrincipal(f.user),
		IdempotencyKey: "conversation-quota-launch", Message: "Launch with subscriptions",
		Subscriptions: []integrationstore.IntegrationSubscriptionAttachment{sibling, attachment, attachment},
	}
	execution := executionstore.New(f.pool, executionstore.Config{})
	launched, err := execution.LaunchAgent(f.ctx, input)
	require.NoError(t, err, "duplicate batch attachments must fit in the last distinct-agent slot")
	require.True(t, launched.Created)
	replay, err := execution.LaunchAgent(f.ctx, input)
	require.NoError(t, err)
	require.False(t, replay.Created)
	require.Equal(t, launched.Agent.ID, replay.Agent.ID)
	input.IdempotencyKey = "conversation-quota-overflow"
	_, err = execution.LaunchAgent(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	require.ErrorContains(t, err, "integration conversation limit of 16 subscribed agents reached")
	var agents, subscriptions, siblingSubscriptions int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT
		(SELECT count(*) FROM agents WHERE project_id=$1 AND idempotency_key=$2),
		(SELECT count(*) FROM integration_subscriptions WHERE agent_id=$3),
		(SELECT count(*) FROM integration_subscriptions
		 WHERE project_id=$1 AND integration_id=$4 AND scope_kind='channel' AND scope_ref='C456')`,
		f.project, input.IdempotencyKey, launched.Agent.ID, f.integrationID).
		Scan(&agents, &subscriptions, &siblingSubscriptions))
	require.Zero(t, agents, "quota rejection must roll back the new agent")
	require.Equal(t, 2, subscriptions, "duplicate attachments are stored once")
	require.Equal(t, 1, siblingSubscriptions, "quota rejection rolls back earlier attachments in the launch batch")
}
