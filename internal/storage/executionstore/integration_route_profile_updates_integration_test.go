//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestIntegrationRouteProfileFirstLaunchSerializesWithChange(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"replace_before_launch", "clear_before_launch", "launch_before_replace", "launch_before_clear",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			f := newChannelWorkflowFixture(t, ctx, "profile-order")
			input := f.event(t, ctx, "first")
			route := integrationProfileRoute(t, ctx, f)
			replacement := createIntegrationTestProfile(t, ctx, f.Store, "replacement")
			newProfile := replacement.ID
			if name == "clear_before_launch" || name == "launch_before_clear" {
				newProfile = uuid.Nil
			}
			launchFirst := name == "launch_before_replace" || name == "launch_before_clear"
			blocker := integrationdb.BeginTx(t, ctx, f.Store.pool)
			var blockerPID int32
			require.NoError(t, blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID))
			if launchFirst {
				_, err := blocker.Exec(ctx, `SELECT id FROM agent_profiles WHERE id=$1 FOR UPDATE`, route.AgentProfileID)
				require.NoError(t, err)
			} else {
				_, err := blocker.Exec(ctx, `SELECT id FROM integration_routes WHERE id=$1 FOR UPDATE`, route.ID)
				require.NoError(t, err)
			}
			setProfile := func() (integrationstore.IntegrationRouteRecord, error) {
				return f.Store.Integrations().SetIntegrationRouteProfile(ctx, integrationProfileUpdate(f, newProfile))
			}
			launch := func() (executionstore.ChannelInputResult, error) {
				return f.Store.Execution().DeliverChannelWorkflow(ctx, input)
			}
			var changed <-chan integrationdb.AsyncResult[integrationstore.IntegrationRouteRecord]
			var delivered <-chan integrationdb.AsyncResult[executionstore.ChannelInputResult]
			if launchFirst {
				delivered = integrationdb.RunAsync(launch)
				integrationdb.WaitForLockWaitBlockedBy(t, ctx, f.Store.pool, "-- name: LockAgentProfile ", blockerPID)
				changed = integrationdb.RunAsync(setProfile)
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.Store.pool, "LockIntegrationInstallForRouteMutation", 1)
			} else {
				changed = integrationdb.RunAsync(setProfile)
				integrationdb.WaitForLockWaitBlockedBy(t, ctx, f.Store.pool,
					"-- name: LockIntegrationRouteByDeploymentKey ", blockerPID)
				delivered = integrationdb.RunAsync(launch)
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.Store.pool, "LockConnectorIntegrationAuthority", 1)
			}
			require.NoError(t, blocker.Commit(ctx))
			updated := integrationdb.AwaitSuccess(t, changed, "route profile change")
			require.Equal(t, route.ID, updated.ID)
			require.Equal(t, newProfile, updated.AgentProfileID)
			result := integrationdb.Await(t, delivered, "first launch after profile lock wait")
			if !launchFirst && newProfile == uuid.Nil {
				require.ErrorIs(t, result.Err, storeerr.ErrUnauthorized)
				var agents, inputs, workflows int
				require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT
					(SELECT count(*) FROM agents WHERE id=$1),
					(SELECT count(*) FROM agent_inputs WHERE agent_id=$1),
					(SELECT count(*) FROM integration_workflows WHERE integration_route_id=$2)`,
					input.Prepared.AgentID(), route.ID).Scan(&agents, &inputs, &workflows))
				require.Zero(t, agents)
				require.Zero(t, inputs)
				require.Zero(t, workflows)
				return
			}
			require.NoError(t, result.Err)
			require.True(t, result.Value.CreatedAgent)
			agent, err := f.Store.Execution().GetAgentInProject(ctx, f.Identity.ProjectID, result.Value.AgentInput.AgentID)
			require.NoError(t, err)
			wantProfile := newProfile
			if launchFirst {
				wantProfile = route.AgentProfileID
			}
			require.Equal(t, wantProfile, agent.AgentProfileID, "admission must use the profile serialized before its launch")
		})
	}
}

func TestIntegrationRouteProfileChangePreservesExistingWorkflowAndGrants(t *testing.T) {
	t.Parallel()
	for _, clearProfile := range []bool{false, true} {
		name := "replace"
		if clearProfile {
			name = "clear"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newChannelWorkflowFixture(t, ctx, "profile-existing")
			first := f.event(t, ctx, "first")
			first.ReadAllowed = false
			accepted, err := f.Store.Execution().DeliverChannelWorkflow(ctx, first)
			require.NoError(t, err)
			originalRoute := integrationProfileRoute(t, ctx, f)
			originalBinding, err := f.Store.Integrations().GetIntegrationTargetBinding(
				ctx, f.Identity.ProjectID, accepted.BindingID)
			require.NoError(t, err)
			require.False(t, originalBinding.ReadAllowed)
			replacement := createIntegrationTestProfile(t, ctx, f.Store, "next-profile")
			profileID := replacement.ID
			if clearProfile {
				profileID = uuid.Nil
			}
			update := integrationProfileUpdate(f, profileID)
			update.State = integrationstore.IntegrationRouteStateDisabled
			update.Configuration = json.RawMessage(`{"create_only":true}`)
			updated, err := f.Store.Integrations().SetIntegrationRouteProfile(ctx, update)
			require.NoError(t, err)
			want := originalRoute
			want.AgentProfileID, want.UpdatedAt = profileID, updated.UpdatedAt
			require.Equal(t, want, updated, "only the profile and update timestamp may change")
			// Editable behavior settings must preserve workflow identity and grants,
			// and partial changes must retain unrelated configuration keys.
			update.ConfigurationPatch = json.RawMessage(`{"mode":"mentions","retained":true}`)
			configured, err := f.Store.Integrations().SetIntegrationRouteProfile(ctx, update)
			require.NoError(t, err)
			require.Equal(t, originalRoute.ID, configured.ID)
			update.ConfigurationPatch = json.RawMessage(`{"mode":"all"}`)
			configured, err = f.Store.Integrations().SetIntegrationRouteProfile(ctx, update)
			require.NoError(t, err)
			require.Equal(t, originalRoute.ID, configured.ID)
			require.JSONEq(t, `{"mode":"all","retained":true}`, string(configured.Configuration))
			first.Prepared, err = f.Store.Execution().PrepareChannelWorkflow(ctx, f.Identity)
			require.NoError(t, err, "an existing workflow does not need a launch profile")
			replay, err := f.Store.Execution().DeliverChannelWorkflow(ctx, first)
			require.NoError(t, err)
			require.False(t, replay.CreatedAgent)
			require.False(t, replay.CreatedInput)
			require.Equal(t, accepted.AgentInput.ID, replay.AgentInput.ID)
			require.Equal(t, accepted.BindingID, replay.BindingID)
			require.Equal(t, accepted.ChannelID, replay.ChannelID)
			// Even a subsequent behavior proposal asking for read must not widen the
			// retained binding while appending content to the existing agent.
			next, err := f.Store.Execution().DeliverChannelWorkflow(ctx, f.event(t, ctx, "next"))
			require.NoError(t, err)
			require.True(t, next.CreatedInput)
			require.False(t, next.CreatedAgent)
			require.Equal(t, accepted.AgentInput.AgentID, next.AgentInput.AgentID)
			require.Equal(t, accepted.ChannelID, next.ChannelID)
			require.Equal(t, accepted.BindingID, next.BindingID)
			binding, err := f.Store.Integrations().GetIntegrationTargetBinding(ctx, f.Identity.ProjectID, accepted.BindingID)
			require.NoError(t, err)
			require.Equal(t, originalBinding, binding)
			agent, err := f.Store.Execution().GetAgentInProject(ctx, f.Identity.ProjectID, accepted.AgentInput.AgentID)
			require.NoError(t, err)
			require.Equal(t, originalRoute.AgentProfileID, agent.AgentProfileID)
			fresh := f
			fresh.Identity.InstanceKey = "new-conversation"
			if clearProfile {
				_, err := f.Store.Execution().PrepareChannelWorkflow(ctx, fresh.Identity)
				require.ErrorIs(t, err, storeerr.ErrUnauthorized)
			} else {
				input := fresh.event(t, ctx, "new-conversation")
				input.Target.ProviderRef = "new-conversation"
				created, err := f.Store.Execution().DeliverChannelWorkflow(ctx, input)
				require.NoError(t, err)
				newAgent, err := f.Store.Execution().GetAgentInProject(ctx, f.Identity.ProjectID, created.AgentInput.AgentID)
				require.NoError(t, err)
				require.Equal(t, replacement.ID, newAgent.AgentProfileID)
			}
		})
	}
}

func TestIntegrationRouteProfileReplacementDeletionRace(t *testing.T) {
	t.Parallel()
	for _, assignmentFirst := range []bool{false, true} {
		name := "deletion_first"
		if assignmentFirst {
			name = "assignment_first"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			f := newChannelWorkflowFixture(t, ctx, "profile-delete")
			original := integrationProfileRoute(t, ctx, f)
			replacement := createIntegrationTestProfile(t, ctx, f.Store, "delete-replacement")
			blocker := integrationdb.BeginTx(t, ctx, f.Store.pool)
			_, err := blocker.Exec(ctx, `SELECT id FROM agent_profiles WHERE id=$1 FOR UPDATE`, replacement.ID)
			require.NoError(t, err)
			assign := func() error {
				_, err := f.Store.Integrations().SetIntegrationRouteProfile(ctx, integrationProfileUpdate(f, replacement.ID))
				return err
			}
			remove := func() error { return f.Store.Execution().DeleteAgentProfile(ctx, f.Identity.ProjectID, replacement.ID) }
			var assigned, deleted <-chan error
			if assignmentFirst {
				assigned = integrationdb.RunAsyncError(assign)
			} else {
				deleted = integrationdb.RunAsyncError(remove)
			}
			integrationdb.WaitForNamedLockWaiters(t, ctx, f.Store.pool, "LockAgentProfile", 1)
			if assignmentFirst {
				deleted = integrationdb.RunAsyncError(remove)
			} else {
				assigned = integrationdb.RunAsyncError(assign)
			}
			integrationdb.WaitForNamedLockWaiters(t, ctx, f.Store.pool, "LockAgentProfile", 2)
			require.NoError(t, blocker.Commit(ctx))
			assignmentErr := integrationdb.Await(t, assigned, "profile assignment")
			deletionErr := integrationdb.Await(t, deleted, "replacement profile deletion")
			if assignmentFirst {
				require.NoError(t, assignmentErr)
				require.ErrorIs(t, deletionErr, storeerr.ErrConflict)
				require.Equal(t, replacement.ID, integrationProfileRoute(t, ctx, f).AgentProfileID)
			} else {
				require.NoError(t, deletionErr)
				require.ErrorIs(t, assignmentErr, storeerr.ErrNotFound)
				require.Equal(t, original, integrationProfileRoute(t, ctx, f))
			}
		})
	}
}

func TestIntegrationRouteProfileRejectsForeignAndMissingProfiles(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelWorkflowFixture(t, ctx, "profile-scope")
	original := integrationProfileRoute(t, ctx, f)
	install, err := f.Store.Integrations().GetIntegrationInstallByID(ctx, f.Identity.IntegrationInstallID)
	require.NoError(t, err)
	otherProject, err := f.Store.Identity().CreateProjectForPrincipal(ctx, identitystore.CreateProjectForPrincipalInput{
		OrgID: install.OrgID, Creator: install.InstalledBy, Name: "Other profile project", IdempotencyKey: "profile-project",
	})
	require.NoError(t, err)
	other, err := f.Store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: otherProject.ID, CurrentConfigID: mustCreateAgentConfig(t, ctx, f.Store, otherProject.ID),
		Name: "Foreign profile", IdempotencyKey: "foreign-profile",
	})
	require.NoError(t, err)
	for _, profileID := range []uuid.UUID{uuid.New(), other.ID} {
		_, err := f.Store.Integrations().SetIntegrationRouteProfile(ctx, integrationProfileUpdate(f, profileID))
		require.ErrorIs(t, err, storeerr.ErrNotFound)
		require.Equal(t, original, integrationProfileRoute(t, ctx, f))
	}
	wrongBehavior := integrationProfileUpdate(f, original.AgentProfileID)
	wrongBehavior.BehaviorKey = "different"
	_, err = f.Store.Integrations().SetIntegrationRouteProfile(ctx, wrongBehavior)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	require.Equal(t, original, integrationProfileRoute(t, ctx, f))
}

func TestIntegrationRouteProfileDisabledConnectionAllowsConfigurationOnly(t *testing.T) {
	t.Parallel()
	for _, owner := range []string{"app", "installation"} {
		t.Run(owner, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newChannelWorkflowFixture(t, ctx, "disabled-profile")
			input := f.event(t, ctx, "before-disable")
			original := integrationProfileRoute(t, ctx, f)
			replacement := createIntegrationTestProfile(t, ctx, f.Store, "disabled-replacement")
			install, err := f.Store.Integrations().GetIntegrationInstallByID(ctx, f.Identity.IntegrationInstallID)
			require.NoError(t, err)
			app, err := f.Store.Integrations().GetIntegrationApp(ctx, install.OrgID, install.IntegrationAppID)
			require.NoError(t, err)
			setActive := func(active bool) {
				if owner == "app" {
					state := integrationstore.IntegrationAppStateDisabled
					if active {
						state = integrationstore.IntegrationAppStateActive
					}
					_, err := f.Store.Integrations().UpdateIntegrationApp(ctx, integrationstore.UpdateIntegrationAppInput{
						OrgID: app.OrgID, ID: app.ID, State: &state,
					})
					require.NoError(t, err)
					return
				}
				state := integrationstore.IntegrationInstallStateDisabled
				if active {
					state = integrationstore.IntegrationInstallStateActive
				}
				current, err := f.Store.Integrations().GetIntegrationInstallByID(ctx, install.ID)
				require.NoError(t, err)
				_, err = f.Store.Integrations().SetConnectorInstallationProviderState(ctx,
					integrationstore.SetConnectorInstallationProviderStateInput{
						IntegrationAppID: app.ID, IntegrationInstallID: install.ID,
						ProviderTenantID: install.ProviderTenantID, ProviderAccountRef: install.ProviderAccountRef,
						ExpectedAppConfigurationRevision: app.ConfigurationRevision,
						ExpectedConfigurationRevision:    current.ConfigurationRevision,
						State:                            state, Capabilities: f.Identity.Capabilities,
					})
				require.NoError(t, err)
			}
			setActive(false)
			updated, err := f.Store.Integrations().SetIntegrationRouteProfile(ctx, integrationProfileUpdate(f, replacement.ID))
			require.NoError(t, err)
			require.Equal(t, original.ID, updated.ID)
			require.Equal(t, replacement.ID, updated.AgentProfileID)
			_, err = f.Store.Execution().PrepareChannelWorkflow(ctx, f.Identity)
			require.ErrorIs(t, err, storeerr.ErrNotFound)
			_, err = f.Store.Execution().DeliverChannelWorkflow(ctx, input)
			require.ErrorIs(t, err, storeerr.ErrNotFound, "previous preparation cannot bypass disabled authority")
			var agents, workflows int
			require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM agents WHERE id=$1),
				(SELECT count(*) FROM integration_workflows WHERE integration_route_id=$2)`,
				input.Prepared.AgentID(), original.ID).Scan(&agents, &workflows))
			require.Zero(t, agents)
			require.Zero(t, workflows)
			setActive(true)
			accepted, err := f.Store.Execution().DeliverChannelWorkflow(ctx, input)
			require.NoError(t, err)
			require.True(t, accepted.CreatedAgent)
			agent, err := f.Store.Execution().GetAgentInProject(ctx, f.Identity.ProjectID, accepted.AgentInput.AgentID)
			require.NoError(t, err)
			require.Equal(t, replacement.ID, agent.AgentProfileID)
			require.Equal(t, updated, integrationProfileRoute(t, ctx, f))
		})
	}
}

func integrationProfileUpdate(
	f channelWorkflowFixture, profileID uuid.UUID,
) integrationstore.SetIntegrationRouteProfileInput {
	return integrationstore.SetIntegrationRouteProfileInput{
		CreateIntegrationRouteInput: integrationstore.CreateIntegrationRouteInput{
			ProjectID: f.Identity.ProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
			DeploymentKey: "conversation", BehaviorKey: "conversation", AgentProfileID: profileID,
			State: integrationstore.IntegrationRouteStateActive,
		},
	}
}

func integrationProfileRoute(
	t *testing.T, ctx context.Context, f channelWorkflowFixture,
) integrationstore.IntegrationRouteRecord {
	t.Helper()
	route, err := f.Store.Integrations().GetIntegrationRouteByDeploymentKey(
		ctx, f.Identity.ProjectID, f.Identity.IntegrationInstallID, "conversation")
	require.NoError(t, err)
	return route
}
