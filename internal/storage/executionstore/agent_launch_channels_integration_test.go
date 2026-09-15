//go:build integration

package executionstore_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestLaunchChannelsCommitAtomicallyAndReplayDoesNotGrant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newPublicChannelFixture(t, ctx, "launch-channels")
	second, err := f.store.Integrations().CreateExternalIntegrationInstall(ctx, externalConnectionInput(f.user.ID))
	require.NoError(t, err)
	definition, err := f.store.Integrations().PublishExternalChannelDefinition(ctx, externalDefinitionInput(second.ID))
	require.NoError(t, err)
	target, err := f.store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: second.ID, ChannelDefinitionID: definition.ID,
		ProviderRef: "another-conversation", ProviderRefKind: "thread",
	})
	require.NoError(t, err)
	request := executionstore.LaunchAgentInput{
		ProjectID: testProjectID, AgentConfigID: f.agent.CurrentConfigID, LaunchedBy: userPrincipal(f.user.ID),
		Message: "Start with these two channels", IdempotencyKey: "launch-with-channels",
		ChannelBindings: []executionstore.LaunchChannelBinding{
			{ChannelID: f.target.ID, Grants: integrationstore.ChannelGrants{ReceiveAllowed: true, SendAllowed: true}},
			{ChannelID: target.ID}, // Empty grants fail after the first binding is inserted.
		},
	}
	_, err = f.store.Execution().LaunchAgent(ctx, request)
	require.Error(t, err)
	var launches, bindings int
	require.NoError(t, f.store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agents WHERE idempotency_key=$1`, request.IdempotencyKey).Scan(&launches))
	require.Zero(t, launches, "invalid second grant rolls back the entire launch")
	require.NoError(t, f.store.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_target_bindings WHERE integration_target_id=$1`, f.target.ID).Scan(&bindings))
	require.Zero(t, bindings)

	request.ChannelBindings[1].Grants.ReadAllowed = true
	launched, err := f.store.Execution().LaunchAgent(ctx, request)
	require.NoError(t, err)
	require.True(t, launched.Created)
	require.NotEqual(t, uuid.Nil, launched.AgentInput.ID)
	var selected *uuid.UUID
	require.NoError(t, f.store.pool.QueryRow(ctx,
		`SELECT integration_target_id FROM agents WHERE id=$1`, launched.Agent.ID).Scan(&selected))
	require.Nil(t, selected, "attaching access does not select an origin for an ordinary launch message")
	binding, err := f.store.Integrations().GetActiveReceiveBindingForTarget(
		ctx, testProjectID, launched.Agent.ID, f.target.ID)
	require.NoError(t, err)
	require.NoError(t, f.store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, binding.ID))
	request.ChannelBindings = []executionstore.LaunchChannelBinding{{ChannelID: uuid.New()}}
	replayed, err := f.store.Execution().LaunchAgent(ctx, request)
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, launched.Agent.ID, replayed.Agent.ID)
	_, err = f.store.Integrations().GetActiveReceiveBindingForTarget(
		ctx, testProjectID, launched.Agent.ID, f.target.ID)
	require.Error(t, err, "launch replay must not restore revoked grants")
}

func TestLaunchChannelsLocksInstallationBeforeProfile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newPublicChannelFixture(t, ctx, "launch-channel-lock-order")
	setup, err := f.store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = setup.Rollback(ctx) }()
	var setupPID int32
	require.NoError(t, setup.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&setupPID))
	_, err = setup.Exec(ctx,
		`SELECT id FROM integration_installs WHERE project_id=$1 AND id=$2 FOR UPDATE`, testProjectID, f.install.ID)
	require.NoError(t, err)
	finished := make(chan error, 1)
	go func() {
		_, launchErr := f.store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
			ProjectID: testProjectID, ProfileID: f.agent.AgentProfileID, AgentConfigID: f.agent.CurrentConfigID,
			LaunchedBy: userPrincipal(f.user.ID), IdempotencyKey: "launch-during-setup",
			ChannelBindings: []executionstore.LaunchChannelBinding{{
				ChannelID: f.target.ID, Grants: integrationstore.ChannelGrants{SendAllowed: true}}},
		})
		finished <- launchErr
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx,
		f.store.pool, "-- name: LockIntegrationTargetCreateAuthority ", setupPID)
	// OAuth/route setup already owns the installation and next locks this profile.
	// NOWAIT proves launch has not inverted that order; eventual success alone
	// could conceal a deadlock recovered by the transaction retry helper.
	_, err = setup.Exec(ctx,
		`SELECT id FROM agent_profiles WHERE project_id=$1 AND id=$2 FOR UPDATE NOWAIT`, testProjectID, f.agent.AgentProfileID)
	require.NoError(t, err)
	require.NoError(t, setup.Commit(ctx))
	require.NoError(t, integrationdb.Await(t, finished, "launch after route setup"))
}

func TestLaunchChannelsSharesOneInstallation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newPublicChannelFixture(t, ctx, "two-channels-one-install")
	second, err := f.store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: f.install.ID, ChannelDefinitionID: f.target.ChannelDefinitionID,
		ProviderRef: "second-conversation", ProviderRefKind: "thread",
	})
	require.NoError(t, err)
	launched, err := f.store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: testProjectID, AgentConfigID: f.agent.CurrentConfigID, LaunchedBy: userPrincipal(f.user.ID),
		Message: "Use both conversations", IdempotencyKey: "launch-shared-installation",
		ChannelBindings: []executionstore.LaunchChannelBinding{
			{ChannelID: f.target.ID, Grants: integrationstore.ChannelGrants{ReceiveAllowed: true, SendAllowed: true}},
			{ChannelID: second.ID, Grants: integrationstore.ChannelGrants{ReadAllowed: true}},
		},
	})
	require.NoError(t, err)
	require.True(t, launched.Created)
	var count int
	require.NoError(t, f.store.pool.QueryRow(ctx, `SELECT count(*) FROM integration_target_bindings
WHERE project_id=$1 AND agent_id=$2 AND revoked_at IS NULL`, testProjectID, launched.Agent.ID).Scan(&count))
	require.Equal(t, 2, count, "one installation lifecycle lock covers both independently granted destinations")
}
