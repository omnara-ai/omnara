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
	f.exec(t, `INSERT INTO org_memberships(org_id,user_id,role,created_at)
	 VALUES($1,$2,'owner',now()) ON CONFLICT DO NOTHING`, f.org, f.user)
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
) integrationstore.CreateAppSubscriptionInput {
	return integrationstore.CreateAppSubscriptionInput{
		OrgID: f.org, ProjectID: f.project, AppID: f.appID, AgentID: agentID,
		Type: "thread_messages", Conversation: json.RawMessage(conversation)}
}

func TestAppSubscriptionsIndependentIdentityPaginationAndDetach(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	agent := subscriptionAgent(t, f)
	input := subscriptionInput(f, agent.ID, `{"channel_id":"C123","thread_ts":"111.222"}`)
	first, err := f.store.CreateAppSubscription(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, []string{"message"}, first.Events)
	require.Equal(t, agent.Name, first.AgentName)
	require.JSONEq(t, string(input.Conversation), string(first.Conversation))
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			replay, err := f.store.CreateAppSubscription(f.ctx, input)
			if err != nil || replay.ID != first.ID {
				t.Errorf("concurrent attachment changed identity: %v %v", replay.ID, err)
			}
		})
	}
	wg.Wait()
	input.Conversation = json.RawMessage(`{"channel_id":"C456","thread_ts":"333.444"}`)
	second, err := f.store.CreateAppSubscription(f.ctx, input)
	require.NoError(t, err)
	page, err := f.store.ListAppSubscriptions(f.ctx, integrationstore.ListAppSubscriptionsInput{
		ProjectID: f.project, AppID: f.appID, Limit: 1,
	})
	require.NoError(t, err)
	require.True(t, page.HasMore)
	require.Equal(t, second.ID, page.Subscriptions[0].ID)
	next, err := f.store.ListAppSubscriptions(f.ctx, integrationstore.ListAppSubscriptionsInput{
		ProjectID: f.project, AppID: f.appID, Limit: 1, After: page.Next,
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
	require.NoError(t, f.store.DeleteAppSubscription(f.ctx, f.org, f.project, f.appID, second.ID))
	replacement, err := f.store.CreateAppSubscription(f.ctx, input)
	require.NoError(t, err)
	require.NotEqual(t, second.ID, replacement.ID)
	require.NoError(t, f.store.DeleteAppSubscription(f.ctx, f.org, f.project, f.appID, second.ID))
	page, err = f.store.ListAppSubscriptions(f.ctx, integrationstore.ListAppSubscriptionsInput{
		ProjectID: f.project, AppID: f.appID, Limit: 100,
	})
	require.NoError(t, err)
	require.Len(t, page.Subscriptions, 2, "an old delete must not remove a replacement")
	_, err = f.store.ListAppSubscriptions(f.ctx, integrationstore.ListAppSubscriptionsInput{
		ProjectID: uuid.New(), AppID: f.appID, Limit: 100,
	})
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	foreign := input
	foreign.ProjectID = uuid.New()
	_, err = f.store.CreateAppSubscription(f.ctx, foreign)
	require.Error(t, err)
	foreign = input
	foreign.AgentID = uuid.New()
	_, err = f.store.CreateAppSubscription(f.ctx, foreign)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}

func TestAppSubscriptionValidationDisconnectAndLifecycle(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	agent := subscriptionAgent(t, f)
	input := subscriptionInput(f, agent.ID, `{"channel_id":"C123"}`)
	for _, change := range []struct {
		typeName     string
		conversation string
		events       []string
	}{
		{"unknown", string(input.Conversation), nil},
		{input.Type, `{"repository_id":123,"pull_request":1}`, nil},
		{input.Type, string(input.Conversation), []string{}},
		{input.Type, string(input.Conversation), []string{"unknown"}},
	} {
		invalid := input
		invalid.Type, invalid.Events = change.typeName, change.events
		invalid.Conversation = json.RawMessage(change.conversation)
		_, err := f.store.CreateAppSubscription(f.ctx, invalid)
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	}
	subscription, err := f.store.CreateAppSubscription(f.ctx, input)
	require.NoError(t, err)
	changed, err := f.store.DisconnectProjectApp(f.ctx, integrationstore.DisconnectProjectAppInput{
		ProjectID: f.project, AppID: f.appID,
	})
	require.NoError(t, err)
	require.True(t, changed)
	page, err := f.store.ListAppSubscriptions(f.ctx, integrationstore.ListAppSubscriptionsInput{
		ProjectID: f.project, AppID: f.appID, Limit: 100,
	})
	require.NoError(t, err)
	require.Len(t, page.Subscriptions, 1, "disconnect preserves routing setup")
	_, err = f.store.CreateAppSubscription(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	require.NoError(t, f.store.DeleteAppSubscription(f.ctx, f.org, f.project, f.appID, subscription.ID))
	other := f.addApp(t, "other", integrationstore.ProjectAppSettings{})
	input.AppID = other.ID
	_, err = f.store.CreateAppSubscription(f.ctx, input)
	require.NoError(t, err)
	require.NoError(t, f.store.DeleteProjectApp(f.ctx, f.org, f.project, other.ID))
	var count int
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM app_subscriptions WHERE agent_id=$1`, agent.ID).Scan(&count))
	require.Zero(t, count, "app deletion includes receive-only agents")
	third := f.addApp(t, "third", integrationstore.ProjectAppSettings{})
	input.AppID = third.ID
	_, err = f.store.CreateAppSubscription(f.ctx, input)
	require.NoError(t, err)
	_, _, err = executionstore.New(f.pool, executionstore.Config{}).ArchiveAgent(
		f.ctx, f.project, agent.ID, identitystore.NewUserPrincipal(f.user))
	require.NoError(t, err)
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM app_subscriptions WHERE agent_id=$1`, agent.ID).Scan(&count))
	require.Zero(t, count, "archival releases routing quota and storage")
	_, err = f.store.CreateAppSubscription(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
}

func TestAppSubscriptionDeletionLocksReceiveOnlyAgentAndFencesAttach(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	agent := subscriptionAgent(t, f)
	input := subscriptionInput(f, agent.ID, `{"channel_id":"C123"}`)
	_, err := f.store.CreateAppSubscription(f.ctx, input)
	require.NoError(t, err)
	control := integrationdb.BeginTx(t, f.ctx, f.pool)
	_, err = dbsqlc.New(control).LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: f.project, ID: agent.ID,
	})
	require.NoError(t, err)
	deleted := integrationdb.RunAsync(func() (struct{}, error) {
		return struct{}{}, f.store.DeleteProjectApp(f.ctx, f.org, f.project, f.appID)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockAgentInProject", 1)
	attached := integrationdb.RunAsync(func() (integrationstore.AppSubscriptionRecord, error) {
		return f.store.CreateAppSubscription(f.ctx, input)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockProjectAppLifecycleShared", 1)
	require.NoError(t, control.Commit(f.ctx))
	integrationdb.AwaitSuccess(t, deleted, "delete app with receive-only agent")
	outcome := integrationdb.Await(t, attached, "attach after app deletion")
	require.ErrorIs(t, outcome.Err, storeerr.ErrNotFound)
	var count int
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM app_subscriptions WHERE app_id=$1`, f.appID).Scan(&count))
	require.Zero(t, count)
}
