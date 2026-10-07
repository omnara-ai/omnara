//go:build integration

package integrationstore_test

import (
	"encoding/json"
	"sync"
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

func subscriptionAgent(t *testing.T, f inboxFixture) executionstore.AgentRecord {
	t.Helper()
	var configID uuid.UUID
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT id FROM agent_configs WHERE project_id=$1 LIMIT 1`, f.project).Scan(&configID))
	launch, err := executionstore.New(f.pool, executionstore.Config{}).LaunchAgent(f.ctx, executionstore.LaunchAgentInput{
		ProjectID: f.project, AgentConfigID: configID, LaunchedBy: identitystore.NewUserPrincipal(f.user),
	})
	require.NoError(t, err)
	return launch.Agent
}

func subscriptionInput(
	f inboxFixture, agentID uuid.UUID, conversation string,
) integrationstore.CreateIntegrationSubscriptionInput {
	return integrationstore.CreateIntegrationSubscriptionInput{
		OrgID: f.org, ProjectID: f.project, IntegrationID: f.integrationID, AgentID: agentID,
		Conversation: json.RawMessage(conversation)}
}

func TestIntegrationSubscriptionsIndependentIdentityPaginationAndDetach(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	agent := subscriptionAgent(t, f)
	input := subscriptionInput(f, agent.ID, `{"channel_id":"C123","thread_ts":"111.222"}`)
	first, err := f.store.CreateIntegrationSubscription(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, agent.Name, first.AgentName)
	require.JSONEq(t, string(input.Conversation), string(first.Conversation))
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			replay, err := f.store.CreateIntegrationSubscription(f.ctx, input)
			if err != nil || replay.ID != first.ID {
				t.Errorf("concurrent attachment changed identity: %v %v", replay.ID, err)
			}
		})
	}
	wg.Wait()
	input.Conversation = json.RawMessage(`{"channel_id":"C456","thread_ts":"333.444"}`)
	second, err := f.store.CreateIntegrationSubscription(f.ctx, input)
	require.NoError(t, err)
	page, err := f.store.ListIntegrationSubscriptions(f.ctx, integrationstore.ListIntegrationSubscriptionsInput{
		ProjectID: f.project, IntegrationID: f.integrationID, Limit: 1,
	})
	require.NoError(t, err)
	require.True(t, page.HasMore)
	require.Equal(t, second.ID, page.Subscriptions[0].ID)
	next, err := f.store.ListIntegrationSubscriptions(f.ctx, integrationstore.ListIntegrationSubscriptionsInput{
		ProjectID: f.project, IntegrationID: f.integrationID, Limit: 1, After: page.Next,
	})
	require.NoError(t, err)
	require.False(t, next.HasMore)
	require.Equal(t, first.ID, next.Subscriptions[0].ID)
	var targets int
	var currentConfig uuid.UUID
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT current_config_id,
	 (SELECT count(*) FROM integration_targets WHERE agent_id=$1) FROM agents WHERE id=$1`,
		agent.ID).Scan(&currentConfig, &targets))
	require.Equal(t, agent.CurrentConfigID, currentConfig)
	require.Zero(t, targets)
	require.NoError(t, f.store.DeleteIntegrationSubscription(f.ctx, f.org, f.project, f.integrationID, second.ID))
	replacement, err := f.store.CreateIntegrationSubscription(f.ctx, input)
	require.NoError(t, err)
	require.NotEqual(t, second.ID, replacement.ID)
	require.NoError(t, f.store.DeleteIntegrationSubscription(f.ctx, f.org, f.project, f.integrationID, second.ID))
	page, err = f.store.ListIntegrationSubscriptions(f.ctx, integrationstore.ListIntegrationSubscriptionsInput{
		ProjectID: f.project, IntegrationID: f.integrationID, Limit: 100,
	})
	require.NoError(t, err)
	require.Len(t, page.Subscriptions, 2, "an old delete must not remove a replacement")
	_, err = f.store.ListIntegrationSubscriptions(f.ctx, integrationstore.ListIntegrationSubscriptionsInput{
		ProjectID: uuid.New(), IntegrationID: f.integrationID, Limit: 100,
	})
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	foreign := input
	foreign.ProjectID = uuid.New()
	_, err = f.store.CreateIntegrationSubscription(f.ctx, foreign)
	require.Error(t, err)
	foreign = input
	foreign.AgentID = uuid.New()
	_, err = f.store.CreateIntegrationSubscription(f.ctx, foreign)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}

func TestIntegrationSubscriptionsConversationNamesAndPagination(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	firstAgent, secondAgent, manualAgent := subscriptionAgent(t, f), subscriptionAgent(t, f), subscriptionAgent(t, f)
	otherIntegration := f.addIntegration(t, "other-labels", integrationstore.IntegrationSettings(`{}`))
	expected := make(map[uuid.UUID]integrationstore.IntegrationSubscriptionRecord)
	attach := func(agentID uuid.UUID, conversation, name string) integrationstore.IntegrationSubscriptionRecord {
		t.Helper()
		record, err := f.store.CreateIntegrationSubscription(f.ctx, subscriptionInput(f, agentID, conversation))
		require.NoError(t, err)
		record.ConversationName = name
		expected[record.ID] = record
		return record
	}
	target := func(record integrationstore.IntegrationSubscriptionRecord, integrationID uuid.UUID, name string) uuid.UUID {
		t.Helper()
		saved, err := f.ensureConversationTarget(integrationstore.EnsureConversationTargetInput{
			ProjectID: f.project, AgentID: record.AgentID, IntegrationID: integrationID,
			Address: record.Address, DisplayName: name,
		})
		require.NoError(t, err)
		return saved.ID
	}
	const conversation = `{"channel_id":"C123","thread_ts":"111.222"}`
	first := attach(firstAgent.ID, conversation, "#support · First conversation")
	retired := target(first, f.integrationID, "Outdated name")
	f.exec(t, `UPDATE integration_targets SET deleted_at=now() WHERE id=$1`, retired)
	target(first, f.integrationID, first.ConversationName)
	second := attach(secondAgent.ID, conversation, "#support · Second conversation")
	target(second, f.integrationID, second.ConversationName)
	attach(manualAgent.ID, conversation, "")
	unnamed := attach(firstAgent.ID, `{"channel_id":"C456"}`, "")
	target(unnamed, f.integrationID, "")
	foreign := attach(firstAgent.ID, `{"channel_id":"C789"}`, "")
	target(foreign, otherIntegration.ID, "Another integration's name")
	retiredOnly := attach(firstAgent.ID, `{"channel_id":"C999"}`, "")
	retired = target(retiredOnly, f.integrationID, "Retired name")
	f.exec(t, `UPDATE integration_targets SET deleted_at=now() WHERE id=$1`, retired)

	input := integrationstore.ListIntegrationSubscriptionsInput{
		ProjectID: f.project, IntegrationID: f.integrationID, Limit: 2,
	}
	seen := make(map[uuid.UUID]bool)
	for pageNumber := 0; ; pageNumber++ {
		require.Less(t, pageNumber, len(expected), "pagination must advance")
		page, err := f.store.ListIntegrationSubscriptions(f.ctx, input)
		require.NoError(t, err)
		require.Len(t, page.Subscriptions, 2)
		for _, subscription := range page.Subscriptions {
			require.False(t, seen[subscription.ID], "target history must not duplicate a subscription")
			seen[subscription.ID] = true
			require.Equal(t, expected[subscription.ID], subscription)
		}
		if !page.HasMore {
			break
		}
		require.True(t, page.Next.Set)
		require.Equal(t, page.Subscriptions[len(page.Subscriptions)-1].ID, page.Next.ID)
		input.After = page.Next
	}
	require.Len(t, seen, len(expected), "manual subscriptions must remain visible without targets")
}

func TestIntegrationSubscriptionValidationDisconnectAndLifecycle(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	agent := subscriptionAgent(t, f)
	input := subscriptionInput(f, agent.ID, `{"channel_id":"C123"}`)
	invalid := input
	invalid.Conversation = json.RawMessage(`{"repository_id":123,"pull_request":1}`)
	_, err := f.store.CreateIntegrationSubscription(f.ctx, invalid)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	subscription, err := f.store.CreateIntegrationSubscription(f.ctx, input)
	require.NoError(t, err)
	changed, err := f.store.DisconnectIntegration(f.ctx, integrationstore.DisconnectIntegrationInput{
		ProjectID: f.project, IntegrationID: f.integrationID,
	})
	require.NoError(t, err)
	require.True(t, changed)
	page, err := f.store.ListIntegrationSubscriptions(f.ctx, integrationstore.ListIntegrationSubscriptionsInput{
		ProjectID: f.project, IntegrationID: f.integrationID, Limit: 100,
	})
	require.NoError(t, err)
	require.Len(t, page.Subscriptions, 1, "disconnect preserves routing setup")
	_, err = f.store.CreateIntegrationSubscription(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	require.NoError(t, f.store.DeleteIntegrationSubscription(f.ctx, f.org, f.project, f.integrationID, subscription.ID))
	other := f.addIntegration(t, "other", integrationstore.IntegrationSettings(`{}`))
	input.IntegrationID = other.ID
	_, err = f.store.CreateIntegrationSubscription(f.ctx, input)
	require.NoError(t, err)
	require.NoError(t, f.store.DeleteIntegration(f.ctx, f.org, f.project, other.ID))
	var count int
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM integration_subscriptions WHERE agent_id=$1`, agent.ID).Scan(&count))
	require.Zero(t, count, "integration deletion includes receive-only agents")
	third := f.addIntegration(t, "third", integrationstore.IntegrationSettings(`{}`))
	input.IntegrationID = third.ID
	_, err = f.store.CreateIntegrationSubscription(f.ctx, input)
	require.NoError(t, err)
	_, _, err = executionstore.New(f.pool, executionstore.Config{}).ArchiveAgent(
		f.ctx, f.project, agent.ID, identitystore.NewUserPrincipal(f.user))
	require.NoError(t, err)
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM integration_subscriptions WHERE agent_id=$1`, agent.ID).Scan(&count))
	require.Zero(t, count, "archival releases routing quota and storage")
	_, err = f.store.CreateIntegrationSubscription(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
}

func TestIntegrationSubscriptionDeletionLocksReceiveOnlyAgentAndFencesAttach(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	agent := subscriptionAgent(t, f)
	input := subscriptionInput(f, agent.ID, `{"channel_id":"C123"}`)
	_, err := f.store.CreateIntegrationSubscription(f.ctx, input)
	require.NoError(t, err)
	control := integrationdb.BeginTx(t, f.ctx, f.pool)
	_, err = dbsqlc.New(control).LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: f.project, ID: agent.ID,
	})
	require.NoError(t, err)
	deleted := integrationdb.RunAsync(func() (struct{}, error) {
		return struct{}{}, f.store.DeleteIntegration(f.ctx, f.org, f.project, f.integrationID)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockAgentInProject", 1)
	attached := integrationdb.RunAsync(func() (integrationstore.IntegrationSubscriptionRecord, error) {
		return f.store.CreateIntegrationSubscription(f.ctx, input)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockIntegrationLifecycleShared", 1)
	require.NoError(t, control.Commit(f.ctx))
	integrationdb.AwaitSuccess(t, deleted, "delete integration with receive-only agent")
	outcome := integrationdb.Await(t, attached, "attach after integration deletion")
	require.ErrorIs(t, outcome.Err, storeerr.ErrNotFound)
	var count int
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM integration_subscriptions WHERE integration_id=$1`, f.integrationID).Scan(&count))
	require.Zero(t, count)
}
