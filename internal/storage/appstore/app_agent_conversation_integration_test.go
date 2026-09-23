//go:build integration

package appstore_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

type agentConversationFixture struct {
	inboxFixture
	agentID uuid.UUID
}

func newAgentConversationFixture(t *testing.T) agentConversationFixture {
	t.Helper()
	f := newInboxFixture(t)
	f.exec(t, `INSERT INTO org_memberships(org_id,user_id,role,created_at) VALUES($1,$2,'owner',now())`, f.org, f.user)
	var configID uuid.UUID
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT id FROM agent_configs WHERE project_id=$1 LIMIT 1`, f.project).Scan(&configID))
	execution := executionstore.New(f.pool, executionstore.Config{})
	launch, err := execution.LaunchAgent(f.ctx, executionstore.LaunchAgentInput{
		ProjectID: f.project, AgentConfigID: configID, LaunchedBy: identitystore.NewUserPrincipal(f.user),
	})
	require.NoError(t, err)
	return agentConversationFixture{inboxFixture: f, agentID: launch.Agent.ID}
}

func (f agentConversationFixture) assign(appID uuid.UUID, address appstore.ConversationAddress) error {
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	if err := lifecyclelock.EnterActiveProject(f.ctx, tx, f.org, f.project); err != nil {
		return err
	}
	if err := appstore.LockAppsTx(f.ctx, tx, f.project, nil, appID); err != nil {
		return err
	}
	agents := []lifecyclelock.AgentRef{{ProjectID: f.project, AgentID: f.agentID}}
	if err := lifecyclelock.Agents(f.ctx, tx, agents); err != nil {
		return err
	}
	if err := f.store.AssignAgentAppConversationTx(f.ctx, tx, f.project, f.agentID, appID, address); err != nil {
		return err
	}
	return tx.Commit(f.ctx)
}

func TestAgentAppConversationStateIsolationAndLifecycle(t *testing.T) {
	t.Parallel()
	f := newAgentConversationFixture(t)
	first := appstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}
	second := appstore.ConversationAddress{Kind: "thread", Ref: "C123:3.4"}
	require.NoError(t, f.assign(f.appID, first))
	otherApp := f.addApp(t, "second-context", appstore.ProjectAppSettings{})
	require.NoError(t, f.assign(otherApp.ID, second))
	for _, test := range []struct {
		project, agent, app uuid.UUID
		want                appstore.ConversationAddress
	}{
		{f.project, f.agentID, f.appID, first},
		{f.project, f.agentID, otherApp.ID, second},
		{uuid.New(), f.agentID, f.appID, appstore.ConversationAddress{}},
		{f.project, uuid.New(), f.appID, appstore.ConversationAddress{}},
		{f.project, f.agentID, uuid.New(), appstore.ConversationAddress{}},
	} {
		got, found, err := f.store.GetAgentAppConversation(f.ctx, test.project, test.agent, test.app)
		require.NoError(t, err)
		require.Equal(t, test.want.Kind != "", found)
		require.Equal(t, test.want, got)
	}
	execution := executionstore.New(f.pool, executionstore.Config{})
	_, _, err := execution.ArchiveAgent(f.ctx, f.project, f.agentID, identitystore.NewUserPrincipal(f.user))
	require.NoError(t, err)
	f.exec(t, `UPDATE project_apps SET state='disconnected' WHERE id=$1`, f.appID)
	got, found, err := f.store.GetAgentAppConversation(f.ctx, f.project, f.agentID, f.appID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, first, got)
	count, err := f.store.CleanupAppStates(f.ctx, appstore.AppProfileChoiceMinRetention, 100)
	require.NoError(t, err)
	require.Zero(t, count, "conversation state is retained without a chooser deadline")
	require.NoError(t, f.store.DeleteProjectApp(f.ctx, f.org, f.project, f.appID))
	count, err = f.store.CleanupAppStates(f.ctx, appstore.AppProfileChoiceMinRetention, 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	_, found, err = f.store.GetAgentAppConversation(f.ctx, f.project, f.agentID, f.appID)
	require.NoError(t, err)
	require.False(t, found)
	got, found, err = f.store.GetAgentAppConversation(f.ctx, f.project, f.agentID, otherApp.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, second, got)
}

func TestAgentAppConversationConcurrentAssignment(t *testing.T) {
	t.Parallel()
	for _, sameAddress := range []bool{false, true} {
		t.Run(fmt.Sprintf("same_address=%t", sameAddress), func(t *testing.T) {
			t.Parallel()
			f := newAgentConversationFixture(t)
			type result struct {
				address appstore.ConversationAddress
				err     error
			}
			results := make(chan result, 2)
			start := make(chan struct{})
			for i := range 2 {
				address := appstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}
				if i == 1 && !sameAddress {
					address.Ref = "C123:3.4"
				}
				go func() {
					<-start
					results <- result{address, f.assign(f.appID, address)}
				}()
			}
			close(start)
			first := integrationdb.Await(t, results, "first assignment")
			second := integrationdb.Await(t, results, "second assignment")
			if first.err != nil {
				first, second = second, first
			}
			require.NoError(t, first.err)
			require.ErrorIs(t, second.err, storeerr.ErrConflict)
			actual, found, err := f.store.GetAgentAppConversation(f.ctx, f.project, f.agentID, f.appID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, first.address, actual)
		})
	}
}

func TestAgentAppConversationRejectsMalformedState(t *testing.T) {
	t.Parallel()
	f := newAgentConversationFixture(t)
	require.NoError(t, f.assign(f.appID, appstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}))
	for _, data := range []string{
		`{}`, `{"kind":"thread"}`, `{"kind":"thread","ref":"C123:1.2","extra":true}`,
		`{"kind":42,"ref":"C123:1.2"}`, `{"kind":"thread","ref":null}`,
	} {
		f.exec(t, `UPDATE app_states SET data=$1 WHERE project_id=$2 AND app_id=$3
 AND kind='agent_conversation' AND key=$4`, data, f.project, f.appID, f.agentID.String())
		_, found, err := f.store.GetAgentAppConversation(f.ctx, f.project, f.agentID, f.appID)
		require.Error(t, err)
		require.False(t, found)
	}
}

func TestAgentAppConversationAssignmentRequiresLiveOwners(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"missing-project", "missing-agent", "missing-app", "archived", "disconnected", "deleted",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newAgentConversationFixture(t)
			projectID, agentID, appID := f.project, f.agentID, f.appID
			switch scenario {
			case "missing-project":
				projectID = uuid.New()
			case "missing-agent":
				agentID = uuid.New()
			case "missing-app":
				appID = uuid.New()
			case "archived":
				execution := executionstore.New(f.pool, executionstore.Config{})
				_, _, err := execution.ArchiveAgent(f.ctx, f.project, f.agentID, identitystore.NewUserPrincipal(f.user))
				require.NoError(t, err)
			case "disconnected":
				_, err := f.store.DisconnectProjectApp(f.ctx, appstore.DisconnectProjectAppInput{
					ProjectID: f.project, AppID: f.appID,
				})
				require.NoError(t, err)
			case "deleted":
				require.NoError(t, f.store.DeleteProjectApp(f.ctx, f.org, f.project, f.appID))
			}
			tx := integrationdb.BeginTx(t, f.ctx, f.pool)
			err := f.store.AssignAgentAppConversationTx(f.ctx, tx, projectID, agentID, appID,
				appstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"})
			require.ErrorIs(t, err, storeerr.ErrNotFound)
		})
	}
}
