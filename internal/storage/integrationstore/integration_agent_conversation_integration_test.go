//go:build integration

package integrationstore_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
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

func (f agentConversationFixture) assign(integrationID uuid.UUID, address integrationstore.ConversationAddress) error {
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	if err := lifecyclelock.EnterActiveProject(f.ctx, tx, f.org, f.project); err != nil {
		return err
	}
	if err := integrationstore.LockIntegrationsTx(f.ctx, tx, f.project, nil, integrationID); err != nil {
		return err
	}
	agents := []lifecyclelock.AgentRef{{ProjectID: f.project, AgentID: f.agentID}}
	if err := lifecyclelock.Agents(f.ctx, tx, agents); err != nil {
		return err
	}
	if err := f.store.AssignAgentIntegrationConversationTx(
		f.ctx,
		tx,
		f.project,
		f.agentID,
		integrationID,
		address,
	); err != nil {
		return err
	}
	return tx.Commit(f.ctx)
}

func TestAgentIntegrationConversationStateIsolationAndLifecycle(t *testing.T) {
	t.Parallel()
	f := newAgentConversationFixture(t)
	first := integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}
	second := integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:3.4"}
	require.NoError(t, f.assign(f.integrationID, first))
	otherIntegration := f.addIntegration(t, "second-context", integrationstore.ProjectIntegrationSettings{})
	require.NoError(t, f.assign(otherIntegration.ID, second))
	for _, test := range []struct {
		project, agent, integration uuid.UUID
		want                        integrationstore.ConversationAddress
	}{
		{f.project, f.agentID, f.integrationID, first},
		{f.project, f.agentID, otherIntegration.ID, second},
		{uuid.New(), f.agentID, f.integrationID, integrationstore.ConversationAddress{}},
		{f.project, uuid.New(), f.integrationID, integrationstore.ConversationAddress{}},
		{f.project, f.agentID, uuid.New(), integrationstore.ConversationAddress{}},
	} {
		got, found, err := f.store.GetAgentIntegrationConversation(f.ctx, test.project, test.agent, test.integration)
		require.NoError(t, err)
		require.Equal(t, test.want.Kind != "", found)
		require.Equal(t, test.want, got)
	}
	execution := executionstore.New(f.pool, executionstore.Config{})
	_, _, err := execution.ArchiveAgent(f.ctx, f.project, f.agentID, identitystore.NewUserPrincipal(f.user))
	require.NoError(t, err)
	f.exec(t, `UPDATE project_integrations SET state='disconnected' WHERE id=$1`, f.integrationID)
	got, found, err := f.store.GetAgentIntegrationConversation(f.ctx, f.project, f.agentID, f.integrationID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, first, got)
	count, err := f.store.CleanupIntegrationStates(f.ctx, integrationstore.IntegrationProfileChoiceMinRetention, 100)
	require.NoError(t, err)
	require.Zero(t, count, "conversation state is retained without a chooser deadline")
	require.NoError(t, f.store.DeleteProjectIntegration(f.ctx, f.org, f.project, f.integrationID))
	count, err = f.store.CleanupIntegrationStates(f.ctx, integrationstore.IntegrationProfileChoiceMinRetention, 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	_, found, err = f.store.GetAgentIntegrationConversation(f.ctx, f.project, f.agentID, f.integrationID)
	require.NoError(t, err)
	require.False(t, found)
	got, found, err = f.store.GetAgentIntegrationConversation(f.ctx, f.project, f.agentID, otherIntegration.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, second, got)
}

func TestAgentIntegrationConversationConcurrentAssignment(t *testing.T) {
	t.Parallel()
	for _, sameAddress := range []bool{false, true} {
		t.Run(fmt.Sprintf("same_address=%t", sameAddress), func(t *testing.T) {
			t.Parallel()
			f := newAgentConversationFixture(t)
			type result struct {
				address integrationstore.ConversationAddress
				err     error
			}
			results := make(chan result, 2)
			start := make(chan struct{})
			for i := range 2 {
				address := integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}
				if i == 1 && !sameAddress {
					address.Ref = "C123:3.4"
				}
				go func() {
					<-start
					results <- result{address, f.assign(f.integrationID, address)}
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
			actual, found, err := f.store.GetAgentIntegrationConversation(f.ctx, f.project, f.agentID, f.integrationID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, first.address, actual)
		})
	}
}

func TestAgentIntegrationConversationRejectsMalformedState(t *testing.T) {
	t.Parallel()
	f := newAgentConversationFixture(t)
	require.NoError(t, f.assign(f.integrationID, integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}))
	for _, data := range []string{
		`{}`, `{"kind":"thread"}`, `{"kind":"thread","ref":"C123:1.2","extra":true}`,
		`{"kind":42,"ref":"C123:1.2"}`, `{"kind":"thread","ref":null}`,
	} {
		f.exec(t, `UPDATE integration_states SET data=$1 WHERE project_id=$2 AND integration_id=$3
 AND kind='agent_conversation' AND key=$4`, data, f.project, f.integrationID, f.agentID.String())
		_, found, err := f.store.GetAgentIntegrationConversation(f.ctx, f.project, f.agentID, f.integrationID)
		require.Error(t, err)
		require.False(t, found)
	}
}

func TestAgentIntegrationConversationAssignmentRequiresLiveOwners(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"missing-project", "missing-agent", "missing-integration", "archived", "disconnected", "deleted",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newAgentConversationFixture(t)
			projectID, agentID, integrationID := f.project, f.agentID, f.integrationID
			switch scenario {
			case "missing-project":
				projectID = uuid.New()
			case "missing-agent":
				agentID = uuid.New()
			case "missing-integration":
				integrationID = uuid.New()
			case "archived":
				execution := executionstore.New(f.pool, executionstore.Config{})
				_, _, err := execution.ArchiveAgent(f.ctx, f.project, f.agentID, identitystore.NewUserPrincipal(f.user))
				require.NoError(t, err)
			case "disconnected":
				_, err := f.store.DisconnectProjectIntegration(f.ctx, integrationstore.DisconnectProjectIntegrationInput{
					ProjectID: f.project, IntegrationID: f.integrationID,
				})
				require.NoError(t, err)
			case "deleted":
				require.NoError(t, f.store.DeleteProjectIntegration(f.ctx, f.org, f.project, f.integrationID))
			}
			tx := integrationdb.BeginTx(t, f.ctx, f.pool)
			err := f.store.AssignAgentIntegrationConversationTx(f.ctx, tx, projectID, agentID, integrationID,
				integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"})
			require.ErrorIs(t, err, storeerr.ErrNotFound)
		})
	}
}
