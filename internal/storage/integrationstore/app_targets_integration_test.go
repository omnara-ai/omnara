//go:build integration

package integrationstore_test

import (
	"fmt"
	"strings"
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

func TestAppConversationSelectionsRemainIndependentOfSubscriptions(t *testing.T) {
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
		return f.ensureConversationTarget(input)
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
	var subscriptions int
	require.NoError(
		t,
		f.pool.QueryRow(
			f.ctx,
			`SELECT count(*) FROM app_subscriptions WHERE agent_id=$1`,
			launch.Agent.ID,
		).Scan(
			&subscriptions,
		),
	)
	require.Zero(t, subscriptions)
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
	require.Len(t, candidates.Selections, 2) // A follow without a live subscription does not suppress launch.
	require.Empty(t, candidates.Subscriptions)
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
	if err := integrationstore.LockAppsTx(f.ctx, tx, f.project, nil, input.AppID); err != nil {
		return integrationstore.IntegrationTargetRecord{}, err
	}
	if err := integrationstore.LockConversationTx(f.ctx, tx, f.project, input.AppID, input.Address); err != nil {
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

func TestAgentAppToolContextIsImmutableAndSurvivesRetirement(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	execution := executionstore.New(f.pool, executionstore.Config{})
	var configID uuid.UUID
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT id FROM agent_configs WHERE project_id=$1 LIMIT 1`, f.project).Scan(&configID))
	launch, err := execution.LaunchAgent(f.ctx, executionstore.LaunchAgentInput{
		ProjectID: f.project, AgentConfigID: configID, LaunchedBy: identitystore.NewUserPrincipal(f.user),
	})
	require.NoError(t, err)
	input := integrationstore.EnsureConversationTargetInput{
		ProjectID: f.project, AgentID: launch.Agent.ID, AppID: f.appID,
		Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"},
		Role:    integrationstore.TargetAttribution,
	}
	ordinary, err := f.ensureConversationTarget(input)
	require.NoError(t, err)
	require.False(t, ordinary.IsToolContext)
	input.IsToolContext = true
	_, err = f.ensureConversationTarget(input)
	require.ErrorIs(t, err, storeerr.ErrConflict, "an existing attribution target cannot be promoted")
	input.IsToolContext, input.Role = false, integrationstore.TargetFollowed
	followed, err := f.ensureConversationTarget(input)
	require.NoError(t, err)
	require.Equal(t, integrationstore.TargetFollowed, followed.RoutingRole)
	require.False(t, followed.IsToolContext)
	_, found, err := f.store.GetAgentAppToolContext(f.ctx, f.project, input.AgentID, f.appID)
	require.NoError(t, err)
	require.False(t, found, "followed/attribution roles do not imply tool context")

	input.Address.Ref = "C123:3.4"
	input.IsToolContext, input.Role, input.SelectionSlot = true, integrationstore.TargetSelected, "reviewer"
	original, err := f.ensureConversationTarget(input)
	require.NoError(t, err)
	require.True(t, original.Created)
	require.True(t, original.IsToolContext)
	record, err := f.store.GetIntegrationTarget(f.ctx, f.project, original.ID)
	require.NoError(t, err)
	require.True(t, record.IsToolContext)
	require.Equal(t, original.RoutingRole, record.RoutingRole)
	require.Equal(t, original.SelectionSlot, record.SelectionSlot)
	replay, err := f.ensureConversationTarget(input)
	require.NoError(t, err)
	require.Equal(t, original.ID, replay.ID)
	require.False(t, replay.Created)
	input.IsToolContext = false
	replay, err = f.ensureConversationTarget(input)
	require.NoError(t, err)
	require.True(t, replay.IsToolContext, "ordinary attribution cannot clear an existing context")

	// Database writes cannot bypass the write-once designation or retarget its identity.
	_, err = f.pool.Exec(f.ctx, `UPDATE integration_targets SET is_tool_context=true WHERE id=$1`, ordinary.ID)
	require.ErrorContains(t, err, "tool context is immutable")
	for _, statement := range []string{
		`UPDATE integration_targets SET is_tool_context=false WHERE id=$1`,
		`UPDATE integration_targets SET provider_ref='C456:1.2' WHERE id=$1`,
		`UPDATE integration_targets SET provider_ref_kind='channel' WHERE id=$1`,
		`UPDATE integration_targets SET agent_id=$2 WHERE id=$1`,
		`UPDATE integration_targets SET app_id=$2 WHERE id=$1`,
		`UPDATE integration_targets SET project_id=$2 WHERE id=$1`,
	} {
		args := []any{original.ID}
		if strings.Contains(statement, "$2") {
			args = append(args, uuid.New())
		}
		_, err = f.pool.Exec(f.ctx, statement, args...)
		require.ErrorContains(t, err, "tool context is immutable")
	}
	// Display metadata and routing remain independent of destination confinement.
	f.exec(t,
		`UPDATE integration_targets SET display_name='renamed',provider_metadata='{"retained":true}' WHERE id=$1`,
		original.ID,
	)
	input.IsToolContext, input.Role, input.SelectionSlot = true, integrationstore.TargetAttribution, ""
	input.Address.Ref = "C123:5.6"
	_, err = f.ensureConversationTarget(input)
	require.ErrorIs(t, err, storeerr.ErrConflict, "a different address cannot replace the context")
	otherApp := f.addApp(t, "second-context", integrationstore.ProjectAppSettings{})
	input.AppID = otherApp.ID
	second, err := f.ensureConversationTarget(input)
	require.NoError(t, err)
	require.True(t, second.IsToolContext, "each app has its own context")
	input.AppID = f.appID
	secondLaunch, err := execution.LaunchAgent(f.ctx, executionstore.LaunchAgentInput{
		ProjectID: f.project, AgentConfigID: configID, LaunchedBy: identitystore.NewUserPrincipal(f.user),
	})
	require.NoError(t, err)
	input.AgentID = secondLaunch.Agent.ID
	_, err = f.ensureConversationTarget(input)
	require.NoError(t, err, "each agent has its own context")
	input.AgentID = launch.Agent.ID

	f.exec(t, `UPDATE integration_targets SET deleted_at=now() WHERE id=$1`, original.ID)
	_, err = f.ensureConversationTarget(input)
	require.ErrorIs(t, err, storeerr.ErrConflict, "retiring a context cannot free its unique slot")
	input.Address.Ref = original.ProviderRef
	_, err = f.ensureConversationTarget(input)
	require.ErrorIs(t, err, storeerr.ErrConflict, "even a retired context at the same address cannot be recreated")
	f.exec(t, `UPDATE project_apps SET state='disconnected' WHERE id=$1`, f.appID)
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	for _, scope := range [][3]uuid.UUID{
		{f.project, launch.Agent.ID, f.appID},
		{uuid.New(), launch.Agent.ID, f.appID},
		{f.project, uuid.New(), f.appID},
		{f.project, launch.Agent.ID, uuid.New()},
	} {
		wantFound := scope == [3]uuid.UUID{f.project, launch.Agent.ID, f.appID}
		record, found, err := f.store.GetAgentAppToolContext(f.ctx, scope[0], scope[1], scope[2])
		require.NoError(t, err)
		require.Equal(t, wantFound, found)
		inTx, txFound, err := f.store.GetAgentAppToolContextTx(f.ctx, tx, scope[0], scope[1], scope[2])
		require.NoError(t, err)
		require.Equal(t, found, txFound)
		require.Equal(t, record, inTx)
		if found {
			require.Equal(t, original.ID, record.ID)
			require.Equal(t, original.ProviderRef, record.ProviderRef)
			require.True(t, record.IsToolContext)
			require.NotNil(t, record.DeletedAt)
			require.Equal(t, "renamed", record.DisplayName)
			require.JSONEq(t, `{"retained":true}`, string(record.ProviderMetadata))
		}
	}
}

func TestAgentAppToolContextConcurrentCreation(t *testing.T) {
	t.Parallel()
	for _, sameAddress := range []bool{false, true} {
		t.Run(fmt.Sprintf("same_address=%t", sameAddress), func(t *testing.T) {
			t.Parallel()
			f := newInboxFixture(t)
			var configID uuid.UUID
			require.NoError(t, f.pool.QueryRow(f.ctx,
				`SELECT id FROM agent_configs WHERE project_id=$1 LIMIT 1`, f.project).Scan(&configID))
			execution := executionstore.New(f.pool, executionstore.Config{})
			launch, err := execution.LaunchAgent(f.ctx, executionstore.LaunchAgentInput{
				ProjectID: f.project, AgentConfigID: configID, LaunchedBy: identitystore.NewUserPrincipal(f.user),
			})
			require.NoError(t, err)
			input := integrationstore.EnsureConversationTargetInput{
				ProjectID: f.project, AgentID: launch.Agent.ID, AppID: f.appID,
				Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"},
				Role:    integrationstore.TargetAttribution, IsToolContext: true,
			}
			type result struct {
				record integrationstore.IntegrationTargetRecord
				err    error
			}
			results := make(chan result, 2)
			start := make(chan struct{})
			for i := range 2 {
				candidate := input
				if i == 1 && !sameAddress {
					candidate.Address.Ref = "C123:3.4"
				}
				go func() {
					<-start
					record, err := f.ensureConversationTarget(candidate)
					results <- result{record, err}
				}()
			}
			close(start)
			first := integrationdb.Await(t, results, "first context creation")
			second := integrationdb.Await(t, results, "second context creation")
			if first.err != nil {
				first, second = second, first
			}
			require.NoError(t, first.err)
			if sameAddress {
				require.NoError(t, second.err)
				require.Equal(t, first.record.ID, second.record.ID)
				require.NotEqual(t, first.record.Created, second.record.Created)
			} else {
				require.ErrorIs(t, second.err, storeerr.ErrConflict)
			}
			actual, found, err := f.store.GetAgentAppToolContext(f.ctx, f.project, input.AgentID, f.appID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, first.record.ID, actual.ID)
		})
	}
}
