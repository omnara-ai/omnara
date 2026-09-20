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

func TestAppConversationSelectionsRemainIndependentOfListeners(t *testing.T) {
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
	store := integrationstore.New(f.pool, executionstore.AppAccess{})
	createApp := func(name string) uuid.UUID {
		t.Helper()
		return f.addApp(t, name, integrationstore.ProjectAppSettings{}).ID
	}
	input := integrationstore.EnsureConversationTargetInput{
		ProjectID: f.project, AgentID: launch.Agent.ID,
		Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:123.456"},
		Role:    integrationstore.TargetSelected, AppID: createApp("first"), SelectionSlot: "agent",
	}
	ensure := func(
		input integrationstore.EnsureConversationTargetInput,
	) (integrationstore.IntegrationTargetRecord, error) {
		t.Helper()
		tx, err := f.pool.Begin(f.ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(f.ctx) }()
		require.NoError(t, lifecyclelock.EnterActiveProject(f.ctx, tx, f.org, f.project))
		require.NoError(t, integrationstore.LockAppsTx(f.ctx, tx, f.project, nil, input.AppID))
		require.NoError(t, integrationstore.LockConversationTx(f.ctx, tx, f.project, input.AppID, input.Address))
		require.NoError(
			t,
			lifecyclelock.Agents(f.ctx, tx, []lifecyclelock.AgentRef{{ProjectID: f.project, AgentID: input.AgentID}}),
		)
		record, err := store.EnsureConversationTargetTx(f.ctx, tx, input)
		if err != nil {
			return record, err
		}
		require.NoError(t, tx.Commit(f.ctx))
		return record, nil
	}
	first, err := ensure(input)
	require.NoError(t, err)
	require.True(t, first.Created)
	replay, err := ensure(input)
	require.NoError(t, err)
	require.Equal(t, first.ID, replay.ID)
	require.False(t, replay.Created)
	// One app cannot assign two launch slots to the same agent/conversation.
	input.SelectionSlot = "another-slot"
	_, err = ensure(input)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	input.SelectionSlot = "agent"
	// Independent apps may address the same bot/conversation and agent.
	input.AppID = createApp("second")
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
	require.ErrorIs(t, err, storeerr.ErrConflict, "an app slot cannot select another agent")
	input.SelectionSlot = "reviewer"
	second, err := ensure(input)
	require.NoError(t, err)
	require.NotEqual(t, first.ID, second.ID)
	var listeners int
	require.NoError(
		t,
		f.pool.QueryRow(
			f.ctx,
			`SELECT count(*) FROM agent_listeners WHERE agent_id=$1`,
			launch.Agent.ID,
		).Scan(
			&listeners,
		),
	)
	require.Zero(t, listeners)
	input.Role, input.SelectionSlot = integrationstore.TargetAttribution, ""
	attribution, err := ensure(input)
	require.NoError(t, err)
	require.Equal(t, second.ID, attribution.ID)
	input.Role = integrationstore.TargetFollowed
	follow, err := ensure(input)
	require.NoError(t, err)
	require.Equal(t, attribution.ID, follow.ID)
	require.Equal(
		t,
		integrationstore.TargetSelected,
		follow.RoutingRole,
		"origin/follow must preserve launch selection",
	)
	// A previously unseen conversation can be attributed and subsequently
	// followed without changing its address identity.
	originalAddress := input.Address
	input.Address.Ref = "C123:789.123"
	input.Role = integrationstore.TargetAttribution
	attribution, err = ensure(input)
	require.NoError(t, err)
	input.Role = integrationstore.TargetFollowed
	follow, err = ensure(input)
	require.NoError(t, err)
	require.Equal(t, attribution.ID, follow.ID)
	require.Equal(t, integrationstore.TargetFollowed, follow.RoutingRole)
	input.Address = originalAddress

	// Retiring a selected association retains the selection tombstone. A later
	// mention may not silently launch a replacement for that app/slot.
	f.exec(t, `UPDATE integration_targets SET deleted_at=now() WHERE id=$1`, second.ID)
	input.Role, input.AppID, input.SelectionSlot = integrationstore.TargetSelected, second.AppID, "reviewer"
	_, err = ensure(input)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	require.NoError(t, lifecyclelock.EnterActiveProject(f.ctx, tx, f.org, f.project))
	require.NoError(t, integrationstore.LockAppsTx(f.ctx, tx, f.project, nil, first.AppID, second.AppID))
	require.NoError(t, integrationstore.LockConversationTx(f.ctx, tx, f.project, input.AppID, input.Address))
	candidates, err := store.AppRoutingCandidatesTx(
		f.ctx,
		tx,
		f.project,
		input.AppID,
		input.Address,
		[]integrationstore.ConversationAddress{input.Address},
		"message",
	)
	require.NoError(t, err)
	require.Len(t, candidates.Selections, 2) // A follow without a live listener does not suppress launch.
	require.Empty(t, candidates.Listeners)
	require.ElementsMatch(
		t,
		[]uuid.UUID{sharedAgent.ID, second.ID},
		[]uuid.UUID{candidates.Selections[0].ID, candidates.Selections[1].ID},
	)
	require.NoError(t, integrationstore.LockConversationTx(f.ctx, tx, f.project, first.AppID, input.Address))
	other, err := store.AppRoutingCandidatesTx(f.ctx, tx, f.project, first.AppID, input.Address,
		[]integrationstore.ConversationAddress{input.Address}, "message")
	require.NoError(t, err)
	require.Len(t, other.Selections, 1)
	require.Equal(t, first.ID, other.Selections[0].ID)
}
