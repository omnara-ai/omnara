//go:build integration

package integrationstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func discordRuntimeSetupFixture(t *testing.T) (*Store, integrationstore.UpsertIntegrationInstallInput) {
	t.Helper()
	pool := openIntegrationDB(t, t.Context())
	seedMigratedDB(t, t.Context(), pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, t.Context(), store, "discord-runtime@example.com")
	app, err := store.Integrations().CreateIntegrationApp(t.Context(), integrationstore.CreateIntegrationAppInput{
		OrgID: testOrgID, Provider: "discord", ProviderAppRef: "111", ConnectorKey: testChannelConnector,
		State: integrationstore.IntegrationAppStateActive,
	})
	require.NoError(t, err)
	return store, integrationstore.UpsertIntegrationInstallInput{
		OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
		ExpectedAppConfigurationRevision: app.ConfigurationRevision,
		InstalledBy:                      identitystore.NewUserPrincipal(admin.ID), Provider: "discord",
		IntegrationKind: integrationstore.IntegrationKindManaged, ConnectionMode: "gateway",
		State: integrationstore.IntegrationInstallStateActive, ProviderTenantID: "333", ProviderAccountRef: "222",
		OAuthFlowID: uuid.Must(uuid.NewV7()), DiscordRuntimeShardCount: 2,
	}
}

func discordSetupCounts(t *testing.T, store *Store, appID uuid.UUID, installs, units int) {
	t.Helper()
	var actualInstalls, actualUnits int
	require.NoError(t, store.pool.QueryRow(t.Context(), `SELECT
  (SELECT count(*) FROM integration_installs WHERE integration_app_id=$1 AND deleted_at IS NULL),
  (SELECT count(*) FROM integration_runtime_units WHERE integration_app_id=$1 AND deleted_at IS NULL)`,
		appID).Scan(&actualInstalls, &actualUnits))
	require.Equal(t, installs, actualInstalls)
	require.Equal(t, units, actualUnits)
}

func TestDiscordRuntimeSetupReusesAppShardsAndCheckpointWithoutProfile(t *testing.T) {
	t.Parallel()
	store, input := discordRuntimeSetupFixture(t)
	install, err := store.Integrations().UpsertIntegrationInstall(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, 2, install.DiscordRuntimeShardCount)
	discordSetupCounts(t, store, input.IntegrationAppID, 1, 2)
	routes, err := store.Integrations().ListActiveIntegrationRoutes(t.Context(), testProjectID, install.ID)
	require.NoError(t, err)
	require.Empty(t, routes)
	var bindings int
	require.NoError(t, store.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM integration_target_bindings WHERE integration_install_id=$1`, install.ID).Scan(&bindings))
	require.Zero(t, bindings)
	units := claimIntegrationRuntimes(t, t.Context(), store, "discord-shard-worker", 10)
	require.Len(t, units, 2)
	want := map[string]string{
		"discord_gateway:0": `{"shard_id":0,"shard_count":2}`,
		"discord_gateway:1": `{"shard_id":1,"shard_count":2}`,
	}
	for _, unit := range units {
		require.Equal(t, "discord_gateway", unit.RuntimeKind)
		require.Equal(t, input.IntegrationAppID, unit.IntegrationAppID)
		require.Equal(t, 1, unit.SpecRevision)
		require.JSONEq(t, want[unit.UnitKey], string(unit.Configuration))
		delete(want, unit.UnitKey)
		_, err := store.Integrations().HeartbeatIntegrationRuntimeUnit(t.Context(),
			integrationstore.HeartbeatIntegrationRuntimeUnitInput{
				ID: unit.ID, LeaseToken: unit.LeaseToken, LeaseGeneration: unit.LeaseGeneration,
				LeaseDuration: time.Minute, WriteCheckpoint: true, CheckpointVersion: 1,
				Checkpoint:   json.RawMessage(`{"session":{"sequence":7}}`),
				Capabilities: testChannelCapabilities("discord"),
			})
		require.NoError(t, err)
	}
	require.Empty(t, want)
	input.DiscordRuntimeShardCount, input.OAuthFlowID = 5, uuid.Must(uuid.NewV7())
	reconnected, err := store.Integrations().UpsertIntegrationInstall(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, install.ID, reconnected.ID)
	require.Equal(t, 2, reconnected.DiscordRuntimeShardCount,
		"caller must observe the configured count even when the provider recommends five")
	currentApp, err := store.Integrations().GetIntegrationApp(t.Context(), testOrgID, input.IntegrationAppID)
	require.NoError(t, err)
	require.Equal(t, input.ExpectedAppConfigurationRevision, currentApp.ConfigurationRevision)
	encoded, err := json.Marshal(reconnected)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "DiscordRuntimeShardCount")
	require.NotContains(t, string(encoded), "discord_runtime_shard_count")
	discordSetupCounts(t, store, input.IntegrationAppID, 1, 2)
	for _, unit := range units {
		retained, err := store.Integrations().HeartbeatIntegrationRuntimeUnit(t.Context(),
			integrationstore.HeartbeatIntegrationRuntimeUnitInput{
				ID: unit.ID, LeaseToken: unit.LeaseToken, LeaseGeneration: unit.LeaseGeneration,
				LeaseDuration: time.Minute, Capabilities: testChannelCapabilities("discord"),
			})
		require.NoError(t, err, "reconnect must not replace the app unit or invalidate its existing lease")
		require.Equal(t, unit.SpecRevision, retained.SpecRevision)
		require.JSONEq(t, string(unit.Configuration), string(retained.Configuration))
		require.JSONEq(t, `{"session":{"sequence":7}}`, string(retained.Checkpoint))
		require.Equal(t, 1, retained.CheckpointVersion)
	}
}

func TestDiscordRuntimeSetupRollsBackUnitsInstallationAndOAuthWithRoute(t *testing.T) {
	t.Parallel()
	store, input := discordRuntimeSetupFixture(t)
	input.InitialRoute = &integrationstore.CreateIntegrationRouteInput{
		AgentProfileID: uuid.New(), DeploymentKey: "default", BehaviorKey: "conversation",
	}
	_, err := store.Integrations().UpsertIntegrationInstall(t.Context(), input)
	require.Error(t, err)
	discordSetupCounts(t, store, input.IntegrationAppID, 0, 0)
	consumed, err := store.Integrations().IntegrationOAuthFlowConsumed(t.Context(), input.OAuthFlowID)
	require.NoError(t, err)
	require.False(t, consumed)
	input.InitialRoute = nil
	_, err = store.Integrations().UpsertIntegrationInstall(t.Context(), input)
	require.NoError(t, err, "failed setup must leave the same verified OAuth redemption usable")
	discordSetupCounts(t, store, input.IntegrationAppID, 1, 2)
}

func TestDiscordRuntimeSetupSerializesFirstGuildsBeforeAppLocks(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	store, first := discordRuntimeSetupFixture(t)
	second := first
	second.ProviderTenantID, second.OAuthFlowID, second.DiscordRuntimeShardCount = "444", uuid.Must(uuid.NewV7()), 3
	blocker := integrationdb.BeginTx(t, ctx, store.pool)
	_, err := blocker.Exec(ctx, `SELECT id FROM integration_apps WHERE id=$1 FOR UPDATE`, first.IntegrationAppID)
	require.NoError(t, err)
	type installResult = integrationdb.AsyncResult[integrationstore.IntegrationInstallRecord]
	start := func(input integrationstore.UpsertIntegrationInstallInput) <-chan installResult {
		return integrationdb.RunAsync(func() (integrationstore.IntegrationInstallRecord, error) {
			return store.Integrations().UpsertIntegrationInstall(ctx, input)
		})
	}
	a := start(first)
	integrationdb.WaitForNamedLockWaiters(t, ctx, store.pool, "LockIntegrationAppForInstallation", 1)
	b := start(second)
	integrationdb.WaitForNamedLockWaiters(t, ctx, store.pool, "LockIntegrationRuntimeConfiguration", 1)
	require.NoError(t, blocker.Commit(ctx))
	one := integrationdb.AwaitSuccess(t, a, "first guild setup")
	two := integrationdb.AwaitSuccess(t, b, "second guild setup")
	require.NotEqual(t, one.ID, two.ID)
	require.Equal(t, 2, one.DiscordRuntimeShardCount)
	require.Equal(t, 2, two.DiscordRuntimeShardCount, "second guild must report the first guild's configured count")
	discordSetupCounts(t, store, first.IntegrationAppID, 2, 2)
	units := claimIntegrationRuntimes(t, ctx, store, "both-guild-shards", 10)
	require.Len(t, units, 2)
	for _, unit := range units {
		var configuration map[string]int
		require.NoError(t, json.Unmarshal(unit.Configuration, &configuration))
		require.Equal(t, 2, configuration["shard_count"], "first setup owns the count; second must reuse it")
	}
}

func TestDiscordRuntimeSetupRechecksAppAfterLockWait(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{
		`provider_config='{"rotated":true}'`, `state='disabled'`, `deleted_at=now()`,
	} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			store, input := discordRuntimeSetupFixture(t)
			blocker := integrationdb.BeginTx(t, t.Context(), store.pool)
			_, err := blocker.Exec(t.Context(), "UPDATE integration_apps SET "+mutation+" WHERE id=$1",
				input.IntegrationAppID)
			require.NoError(t, err)
			pending := integrationdb.RunAsync(func() (integrationstore.IntegrationInstallRecord, error) {
				return store.Integrations().UpsertIntegrationInstall(t.Context(), input)
			})
			integrationdb.WaitForNamedLockWaiters(t, t.Context(), store.pool, "LockIntegrationAppForInstallation", 1)
			require.NoError(t, blocker.Commit(t.Context()))
			result := integrationdb.Await(t, pending, "setup after app change")
			require.ErrorIs(t, result.Err, storeerr.ErrStateTransitionConflict)
			discordSetupCounts(t, store, input.IntegrationAppID, 0, 0)
		})
	}
}

func TestDiscordRuntimeSetupRejectsInconsistentExistingSet(t *testing.T) {
	t.Parallel()
	for _, configuration := range []string{
		`{"shard_id":0,"shard_count":2}`, `{"shard_id":null,"shard_count":1}`,
		`{"shard_count":1}`, `{"shard_id":0,"shard_count":1,"extra":0}`,
	} {
		t.Run(configuration, func(t *testing.T) {
			t.Parallel()
			store, input := discordRuntimeSetupFixture(t)
			_, err := store.Integrations().UpsertIntegrationRuntimeUnit(t.Context(),
				integrationstore.UpsertIntegrationRuntimeUnitInput{
					OrgID: testOrgID, IntegrationAppID: input.IntegrationAppID,
					UnitKey: "discord_gateway:0", RuntimeKind: "discord_gateway", SpecRevision: 1,
					DesiredState:  integrationstore.IntegrationRuntimeDesiredStateRunning,
					Configuration: json.RawMessage(configuration),
				})
			require.NoError(t, err)
			_, err = store.Integrations().UpsertIntegrationInstall(t.Context(), input)
			require.ErrorIs(t, err, storeerr.ErrConflict)
			discordSetupCounts(t, store, input.IntegrationAppID, 0, 1)
		})
	}
}

func TestDiscordRuntimeSetupRequiresVerifiedScope(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"negative_count", "unpinned", "provider", "disabled"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			store, input := discordRuntimeSetupFixture(t)
			switch change {
			case "negative_count":
				input.DiscordRuntimeShardCount = -1
			case "unpinned":
				input.ExpectedAppConfigurationRevision = 0
			case "provider":
				input.Provider = "github"
			case "disabled":
				input.State = integrationstore.IntegrationInstallStateDisabled
			}
			_, err := store.Integrations().UpsertIntegrationInstall(t.Context(), input)
			require.Error(t, err, "invalid %s must not create runtime authority", change)
			discordSetupCounts(t, store, input.IntegrationAppID, 0, 0)
		})
	}
}
