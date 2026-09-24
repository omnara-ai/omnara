//go:build integration

package integrationstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestIntegrationConversationSelectionsRemainIndependentOfSubscriptions(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	execution := executionstore.New(f.pool, executionstore.Config{})
	var configID uuid.UUID
	require.NoError(
		t,
		f.pool.QueryRow(f.ctx, `SELECT id FROM agent_configs WHERE project_id=$1 LIMIT 1`, f.project).Scan(&configID),
	)
	launch, err := execution.LaunchAgent(
		f.ctx,
		executionstore.LaunchAgentInput{
			ProjectID:     f.project,
			AgentConfigID: configID,
			LaunchedBy:    identitystore.NewUserPrincipal(f.user),
		},
	)
	require.NoError(t, err)
	store := integrationstore.New(f.pool, executionstore.IntegrationAccess{})
	createIntegration := func(name string) uuid.UUID {
		t.Helper()
		return f.addIntegration(t, name, integrationstore.ProjectIntegrationSettings{}).ID
	}
	input := integrationstore.EnsureConversationTargetInput{
		ProjectID: f.project, AgentID: launch.Agent.ID,
		Address:       integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:123.456"},
		IntegrationID: createIntegration("first"), SelectionSlot: "agent",
	}
	ensure := func(
		input integrationstore.EnsureConversationTargetInput,
	) (integrationstore.IntegrationTargetRecord, error) {
		return f.ensureConversationTarget(input)
	}
	first, err := ensure(input)
	require.NoError(t, err)
	require.True(t, first.Created)
	_, found, err := store.GetAgentIntegrationConversation(f.ctx, f.project, input.AgentID, input.IntegrationID)
	require.NoError(t, err)
	require.False(t, found)
	replay, err := ensure(input)
	require.NoError(t, err)
	require.Equal(t, first.ID, replay.ID)
	require.False(t, replay.Created)
	input.SelectionSlot = "another-slot"
	_, err = ensure(input)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	input.SelectionSlot = "agent"
	input.IntegrationID = createIntegration("second")
	sharedAgent, err := ensure(input)
	require.NoError(t, err)
	require.NotEqual(t, first.ID, sharedAgent.ID)
	secondLaunch, err := execution.LaunchAgent(
		f.ctx,
		executionstore.LaunchAgentInput{
			ProjectID:     f.project,
			AgentConfigID: configID,
			LaunchedBy:    identitystore.NewUserPrincipal(f.user),
		},
	)
	require.NoError(t, err)
	input.AgentID = secondLaunch.Agent.ID
	_, err = ensure(input)
	require.ErrorIs(t, err, storeerr.ErrConflict, "an integration slot cannot select another agent")
	input.SelectionSlot = "reviewer"
	second, err := ensure(input)
	require.NoError(t, err)
	require.NotEqual(t, first.ID, second.ID)
	var subscriptions int
	require.NoError(
		t,
		f.pool.QueryRow(
			f.ctx,
			`SELECT count(*) FROM integration_subscriptions WHERE agent_id=$1`,
			launch.Agent.ID,
		).Scan(
			&subscriptions,
		),
	)
	require.Zero(t, subscriptions)
	input.SelectionSlot = ""
	attribution, err := ensure(input)
	require.NoError(t, err)
	require.Equal(t, second.ID, attribution.ID)
	require.Equal(t, "reviewer", attribution.SelectionSlot, "input attribution must preserve launch selection")

	originalAddress := input.Address
	input.Address.Ref = "C123:789.123"
	attribution, err = ensure(input)
	require.NoError(t, err)
	require.Empty(t, attribution.SelectionSlot)
	replay, err = ensure(input)
	require.NoError(t, err)
	require.Equal(t, attribution.ID, replay.ID)
	require.Empty(t, replay.SelectionSlot)
	input.Address = originalAddress

	input.IntegrationID = first.IntegrationID
	attribution, err = ensure(input)
	require.NoError(t, err)
	require.Empty(t, attribution.SelectionSlot)
	_, err = store.CreateIntegrationSubscription(f.ctx, integrationstore.CreateIntegrationSubscriptionInput{
		OrgID: f.org, ProjectID: f.project, IntegrationID: input.IntegrationID, AgentID: input.AgentID,
		Conversation: []byte(`{"channel_id":"C123","thread_ts":"123.456"}`),
	})
	require.NoError(t, err)

	f.exec(t, `UPDATE integration_targets SET deleted_at=now() WHERE id=$1`, second.ID)
	input.IntegrationID, input.SelectionSlot = second.IntegrationID, "reviewer"
	_, err = ensure(input)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	require.NoError(t, lifecyclelock.EnterActiveProject(f.ctx, tx, f.org, f.project))
	require.NoError(
		t,
		integrationstore.LockIntegrationsTx(f.ctx, tx, f.project, nil, first.IntegrationID, second.IntegrationID),
	)
	require.NoError(t, integrationstore.LockConversationTx(f.ctx, tx, f.project, input.IntegrationID, input.Address))
	candidates, err := store.IntegrationRoutingCandidatesTx(
		f.ctx,
		tx,
		f.project,
		input.IntegrationID,
		input.Address,
		[]integrationstore.ConversationAddress{input.Address},
	)
	require.NoError(t, err)
	require.Len(t, candidates.Selections, 2)
	require.Empty(t, candidates.Subscriptions)
	require.ElementsMatch(
		t,
		[]uuid.UUID{sharedAgent.ID, second.ID},
		[]uuid.UUID{candidates.Selections[0].ID, candidates.Selections[1].ID},
	)
	require.NoError(t, integrationstore.LockConversationTx(f.ctx, tx, f.project, first.IntegrationID, input.Address))
	other, err := store.IntegrationRoutingCandidatesTx(f.ctx, tx, f.project, first.IntegrationID, input.Address,
		[]integrationstore.ConversationAddress{input.Address})
	require.NoError(t, err)
	require.Len(t, other.Selections, 1)
	require.Equal(t, first.ID, other.Selections[0].ID)
	require.Len(t, other.Subscriptions, 1)
	require.Equal(t, input.AgentID, other.Subscriptions[0].AgentID)
}

func (f inboxFixture) ensureConversationTarget(
	input integrationstore.EnsureConversationTargetInput,
) (integrationstore.IntegrationTargetRecord, error) {
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		return integrationstore.IntegrationTargetRecord{}, err
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	if err := lifecyclelock.EnterActiveProject(f.ctx, tx, f.org, f.project); err != nil {
		return integrationstore.IntegrationTargetRecord{}, err
	}
	if err := integrationstore.LockIntegrationsTx(f.ctx, tx, f.project, nil, input.IntegrationID); err != nil {
		return integrationstore.IntegrationTargetRecord{}, err
	}
	if err := integrationstore.LockConversationTx(f.ctx, tx, f.project, input.IntegrationID, input.Address); err != nil {
		return integrationstore.IntegrationTargetRecord{}, err
	}
	if err := lifecyclelock.Agents(
		f.ctx, tx, []lifecyclelock.AgentRef{{ProjectID: f.project, AgentID: input.AgentID}},
	); err != nil {
		return integrationstore.IntegrationTargetRecord{}, err
	}
	record, err := f.store.EnsureConversationTargetTx(f.ctx, tx, input)
	if err != nil {
		return record, err
	}
	return record, tx.Commit(f.ctx)
}
