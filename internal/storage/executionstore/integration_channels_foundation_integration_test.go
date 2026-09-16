//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

type blockingDeleteInstallAccess struct {
	reachedClear  chan struct{}
	continueClear chan struct{}
}

func (a *blockingDeleteInstallAccess) ValidateInstallBinding(
	ctx context.Context,
	tx pgx.Tx,
	binding integrationstore.InstallBinding,
) error {
	return (executionstore.IntegrationInstallAccess{}).ValidateInstallBinding(ctx, tx, binding)
}

func (a *blockingDeleteInstallAccess) ClearInstallTargetsFromAgents(
	ctx context.Context,
	tx pgx.Tx,
	projectID, installID uuid.UUID,
) error {
	close(a.reachedClear)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.continueClear:
	}
	return (executionstore.IntegrationInstallAccess{}).ClearInstallTargetsFromAgents(
		ctx,
		tx,
		projectID,
		installID,
	)
}

const (
	testChannelConnector = channelconnector.BuiltInConnectorKey
	testChannelProvider  = "discord"
	testChannelHandler   = "test_channel_single_agent"
)

func testChannelCapabilities(provider string) []channelconnector.Capability {
	return []channelconnector.Capability{testChannelCapability(provider)}
}

func testChannelCapability(provider string) channelconnector.Capability {
	return channelconnector.Capability{ConnectorKey: testChannelConnector, Provider: provider}
}

func TestChannelFoundationRuntimeLeaseFencingAndCheckpoint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "channel-runtime@example.com")
	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, OwnerProjectID: testProjectID,
			Provider: testChannelProvider, ProviderAppRef: "discord-runtime-app",
			DisplayName: "Runtime app", ConnectorKey: testChannelConnector,
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create runtime app: %v", err)
	}
	install, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
			InstalledBy: identitystore.NewUserPrincipal(admin.ID),
			Provider:    testChannelProvider, IntegrationKind: integrationstore.IntegrationKindManaged,
			ConnectionMode: "gateway", State: integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: "runtime-guild", ProviderAccountRef: "runtime-bot",
			DisplayName: "Runtime bot",
		},
	)
	if err != nil {
		t.Fatalf("create runtime installation: %v", err)
	}
	unitInput := integrationstore.UpsertIntegrationRuntimeUnitInput{
		OrgID: testOrgID, IntegrationAppID: app.ID,
		UnitKey:     "gateway-shard-0",
		RuntimeKind: "provider_gateway", DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
		SpecRevision: 1, Configuration: json.RawMessage(`{"shard":0}`),
	}
	unit, err := store.Integrations().UpsertIntegrationRuntimeUnit(ctx, unitInput)
	if err != nil {
		t.Fatalf("create runtime unit: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_installs SET provider_account_ref = 'moved-account' WHERE id = $1`,
		install.ID,
	); !isPgCode(err, "25006") {
		t.Fatalf("change integration install identity error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_apps
		 SET configuration_revision = configuration_revision - 1
		 WHERE id = $1`,
		app.ID,
	); !isPgCode(err, "25006") {
		t.Fatalf("decrease integration app revision error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_installs
		 SET configuration_revision = configuration_revision - 1
		 WHERE id = $1`,
		install.ID,
	); !isPgCode(err, "25006") {
		t.Fatalf("decrease integration install revision error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_runtime_units SET unit_key = 'moved-unit' WHERE id = $1`,
		unit.ID,
	); !isPgCode(err, "25006") {
		t.Fatalf("change integration runtime identity error = %v, want SQLSTATE 25006", err)
	}
	replayedUnit, err := store.Integrations().UpsertIntegrationRuntimeUnit(ctx, unitInput)
	if err != nil || replayedUnit.ID != unit.ID || replayedUnit.SpecRevision != unit.SpecRevision {
		t.Fatalf("replay identical runtime specification = %+v, %v", replayedUnit, err)
	}
	changedSameRevision := unitInput
	changedSameRevision.Configuration = json.RawMessage(`{"shard":1}`)
	if _, err := store.Integrations().UpsertIntegrationRuntimeUnit(
		ctx,
		changedSameRevision,
	); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("changed same-revision runtime specification error = %v, want conflict", err)
	}
	claim := func(owner string) integrationstore.IntegrationRuntimeUnitRecord {
		rows, err := store.Integrations().ClaimIntegrationRuntimeUnits(
			ctx,
			integrationstore.ClaimIntegrationRuntimeUnitsInput{
				LeaseOwner: owner, LeaseDuration: time.Minute,
				Capability: testChannelCapability(testChannelProvider),
				Limit:      1,
			},
		)
		if err != nil {
			t.Fatalf("claim runtime unit: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("runtime claims = %+v", rows)
		}
		return rows[0]
	}
	first := claim("gateway-a")
	if first.ID != unit.ID || first.LeaseToken == uuid.Nil || first.LeaseGeneration != 1 ||
		first.LeaseSpecRevision != unit.SpecRevision ||
		first.LeaseAppConfigurationRevision != app.ConfigurationRevision ||
		first.LeaseInstallConfigRevision != 0 {
		t.Fatalf("first runtime lease = %+v", first)
	}
	current, err := store.Integrations().IntegrationRuntimeLeaseIsCurrent(
		ctx,
		app.ID,
		first.ID,
		install.ID,
		first.LeaseToken,
		first.LeaseGeneration,
	)
	if err != nil || !current {
		t.Fatalf("current runtime lease = %v, %v", current, err)
	}
	current, err = store.Integrations().IntegrationRuntimeLeaseIsCurrent(
		ctx,
		app.ID,
		first.ID,
		uuid.New(),
		first.LeaseToken,
		first.LeaseGeneration,
	)
	if err != nil || current {
		t.Fatalf("app runtime lease accepted an unrelated installation = %v, %v", current, err)
	}
	heartbeat, err := store.Integrations().HeartbeatIntegrationRuntimeUnit(
		ctx,
		integrationstore.HeartbeatIntegrationRuntimeUnitInput{
			ID: first.ID, LeaseToken: first.LeaseToken, LeaseGeneration: first.LeaseGeneration,
			LeaseDuration: time.Minute, WriteCheckpoint: true, CheckpointVersion: 1,
			Checkpoint:   json.RawMessage(`{"sequence":42}`),
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	)
	if err != nil || heartbeat.CheckpointRevision != 1 {
		t.Fatalf("heartbeat runtime unit = %+v, %v", heartbeat, err)
	}
	if _, err := store.Integrations().HeartbeatIntegrationRuntimeUnit(
		ctx,
		integrationstore.HeartbeatIntegrationRuntimeUnitInput{
			ID: first.ID, LeaseToken: uuid.New(), LeaseGeneration: first.LeaseGeneration,
			LeaseDuration: time.Minute, Capabilities: testChannelCapabilities(testChannelProvider),
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("wrong-token heartbeat error = %v", err)
	}
	released, err := store.Integrations().ReleaseIntegrationRuntimeUnit(
		ctx,
		integrationstore.ReleaseIntegrationRuntimeUnitInput{
			ID: first.ID, LeaseToken: first.LeaseToken, LeaseGeneration: first.LeaseGeneration,
			WriteCheckpoint: true, CheckpointVersion: 1,
			Checkpoint:   json.RawMessage(`{"sequence":43}`),
			LastError:    json.RawMessage(`{"code":"connection_failed"}`),
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	)
	if err != nil || released.Status != integrationstore.IntegrationRuntimeStatusError {
		t.Fatalf("release failed runtime = %+v, %v", released, err)
	}
	if released.CheckpointRevision != 2 ||
		!sameJSON(released.Checkpoint, json.RawMessage(`{"sequence":43}`)) {
		t.Fatalf("release did not flush final runtime checkpoint: %+v", released)
	}
	current, err = store.Integrations().IntegrationRuntimeLeaseIsCurrent(
		ctx,
		app.ID,
		first.ID,
		install.ID,
		first.LeaseToken,
		first.LeaseGeneration,
	)
	if err != nil || current {
		t.Fatalf("released runtime lease = %v, %v", current, err)
	}

	unitInput.SpecRevision = 2
	unitInput.Configuration = json.RawMessage(`{"shard":0,"resume":true}`)
	updated, err := store.Integrations().UpsertIntegrationRuntimeUnit(ctx, unitInput)
	if err != nil {
		t.Fatalf("update runtime configuration: %v", err)
	}
	if updated.CheckpointRevision != 2 {
		t.Fatalf("configuration update discarded checkpoint: %+v", updated)
	}
	lowerRevision := unitInput
	lowerRevision.SpecRevision = 1
	if _, err := store.Integrations().UpsertIntegrationRuntimeUnit(
		ctx,
		lowerRevision,
	); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("lower runtime specification revision error = %v, want conflict", err)
	}
	second := claim("gateway-b")
	if second.LeaseGeneration != first.LeaseGeneration+1 || second.CheckpointRevision != 2 {
		t.Fatalf("reclaimed runtime unit = %+v", second)
	}
	if _, err := store.Integrations().HeartbeatIntegrationRuntimeUnit(
		ctx,
		integrationstore.HeartbeatIntegrationRuntimeUnitInput{
			ID: first.ID, LeaseToken: first.LeaseToken, LeaseGeneration: first.LeaseGeneration,
			LeaseDuration: time.Minute, Capabilities: testChannelCapabilities(testChannelProvider),
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("superseded runtime heartbeat error = %v", err)
	}

	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_apps SET provider_config = '{"revision":2}'::jsonb WHERE id = $1`,
		app.ID,
	); err != nil {
		t.Fatalf("rotate runtime app configuration: %v", err)
	}
	current, err = store.Integrations().IntegrationRuntimeLeaseIsCurrent(
		ctx,
		app.ID,
		second.ID,
		install.ID,
		second.LeaseToken,
		second.LeaseGeneration,
	)
	if err != nil || current {
		t.Fatalf("app-revision-stale runtime lease = %v, %v", current, err)
	}
	if _, err := store.Integrations().HeartbeatIntegrationRuntimeUnit(
		ctx,
		integrationstore.HeartbeatIntegrationRuntimeUnitInput{
			ID: second.ID, LeaseToken: second.LeaseToken,
			LeaseGeneration: second.LeaseGeneration, LeaseDuration: time.Minute,
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("app-revision-stale heartbeat error = %v", err)
	}
	if _, err := store.Integrations().ReleaseIntegrationRuntimeUnit(
		ctx,
		integrationstore.ReleaseIntegrationRuntimeUnitInput{
			ID: second.ID, LeaseToken: second.LeaseToken,
			LeaseGeneration: second.LeaseGeneration, LastError: json.RawMessage(`{}`),
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	); err != nil {
		t.Fatalf("release app-revision-stale runtime: %v", err)
	}
	third := claim("gateway-c")
	if third.LeaseAppConfigurationRevision != app.ConfigurationRevision+1 {
		t.Fatalf("refreshed app runtime lease = %+v", third)
	}
	unitInput.SpecRevision = 3
	unitInput.DesiredState = integrationstore.IntegrationRuntimeDesiredStateStopped
	if _, err := store.Integrations().UpsertIntegrationRuntimeUnit(ctx, unitInput); err != nil {
		t.Fatalf("stop app-wide runtime unit: %v", err)
	}
	if _, err := store.Integrations().ReleaseIntegrationRuntimeUnit(
		ctx,
		integrationstore.ReleaseIntegrationRuntimeUnitInput{
			ID: third.ID, LeaseToken: third.LeaseToken,
			LeaseGeneration: third.LeaseGeneration, LastError: json.RawMessage(`{}`),
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("release fenced stopped app-wide runtime error = %v, want conflict", err)
	}

	installUnit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
		ctx,
		integrationstore.UpsertIntegrationRuntimeUnitInput{
			OrgID: testOrgID, IntegrationAppID: app.ID,
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			UnitKey: "installation-runtime", RuntimeKind: "provider_socket",
			DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
			SpecRevision: 1, Configuration: json.RawMessage(`{}`),
		},
	)
	if err != nil {
		t.Fatalf("create installation runtime unit: %v", err)
	}
	installLease := claim("gateway-install")
	if installLease.ID != installUnit.ID ||
		installLease.LeaseInstallConfigRevision != install.ConfigurationRevision {
		t.Fatalf("installation runtime lease = %+v", installLease)
	}
	current, err = store.Integrations().IntegrationRuntimeLeaseIsCurrent(
		ctx,
		app.ID,
		installLease.ID,
		install.ID,
		installLease.LeaseToken,
		installLease.LeaseGeneration,
	)
	if err != nil || !current {
		t.Fatalf("installation runtime lease = %v, %v", current, err)
	}
	current, err = store.Integrations().IntegrationRuntimeLeaseIsCurrent(
		ctx,
		app.ID,
		installLease.ID,
		uuid.New(),
		installLease.LeaseToken,
		installLease.LeaseGeneration,
	)
	if err != nil || current {
		t.Fatalf("cross-install runtime lease = %v, %v", current, err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_installs SET provider_config = '{"revision":2}'::jsonb WHERE id = $1`,
		install.ID,
	); err != nil {
		t.Fatalf("rotate runtime installation configuration: %v", err)
	}
	current, err = store.Integrations().IntegrationRuntimeLeaseIsCurrent(
		ctx,
		app.ID,
		installLease.ID,
		install.ID,
		installLease.LeaseToken,
		installLease.LeaseGeneration,
	)
	if err != nil || current {
		t.Fatalf("install-revision-stale runtime lease = %v, %v", current, err)
	}
	if _, err := store.Integrations().HeartbeatIntegrationRuntimeUnit(
		ctx,
		integrationstore.HeartbeatIntegrationRuntimeUnitInput{
			ID: installLease.ID, LeaseToken: installLease.LeaseToken,
			LeaseGeneration: installLease.LeaseGeneration, LeaseDuration: time.Minute,
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("install-revision-stale heartbeat error = %v", err)
	}
	if err := store.Integrations().DeleteIntegrationInstall(ctx, testProjectID, install.ID); err != nil {
		t.Fatalf("delete leased runtime installation: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_apps
		 SET state = 'disabled', deleted_at = statement_timestamp()
		 WHERE id = $1`,
		app.ID,
	); err != nil {
		t.Fatalf("delete leased runtime app: %v", err)
	}
	if _, err := store.Integrations().ReleaseIntegrationRuntimeUnit(
		ctx,
		integrationstore.ReleaseIntegrationRuntimeUnitInput{
			ID: installLease.ID, LeaseToken: installLease.LeaseToken,
			LeaseGeneration: installLease.LeaseGeneration, LastError: json.RawMessage(`{}`),
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("release fenced runtime after app deletion error = %v, want conflict", err)
	}
}

func TestChannelFoundationLifecycleDeletion(t *testing.T) {
	t.Parallel()
	t.Run("install after app disable", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pool := openIntegrationDB(t, ctx)
		seedMigratedDB(t, ctx, pool)
		store := newSecretIntegrationStore(pool)
		_, _, app, install := createChannelLifecycleFixture(t, ctx, store, "install-delete")

		if _, err := pool.Exec(
			ctx,
			`UPDATE integration_apps SET state = 'disabled' WHERE id = $1`,
			app.ID,
		); err != nil {
			t.Fatalf("disable integration app: %v", err)
		}
		if err := store.Integrations().DeleteIntegrationInstall(ctx, testProjectID, install.ID); err != nil {
			t.Fatalf("delete install after parent app disable: %v", err)
		}
		var deleted bool
		if err := pool.QueryRow(
			ctx,
			`SELECT deleted_at IS NOT NULL FROM integration_installs WHERE id = $1`,
			install.ID,
		).Scan(&deleted); err != nil {
			t.Fatalf("load deleted integration install: %v", err)
		}
		if !deleted {
			t.Fatal("integration install was not soft deleted")
		}
	})

	t.Run("project with active integration", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pool := openIntegrationDB(t, ctx)
		seedMigratedDB(t, ctx, pool)
		store := newSecretIntegrationStore(pool)
		admin, _, app, install := createChannelLifecycleFixture(t, ctx, store, "project-delete")
		unit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
			ctx,
			integrationstore.UpsertIntegrationRuntimeUnitInput{
				OrgID: testOrgID, IntegrationAppID: app.ID,
				ProjectID: testProjectID, IntegrationInstallID: install.ID,
				UnitKey: "project-runtime", RuntimeKind: "provider_socket",
				DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
				SpecRevision: 1,
			},
		)
		if err != nil {
			t.Fatalf("create project runtime unit: %v", err)
		}
		appUnit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
			ctx,
			integrationstore.UpsertIntegrationRuntimeUnitInput{
				OrgID: testOrgID, IntegrationAppID: app.ID,
				UnitKey: "project-owned-app-runtime", RuntimeKind: "provider_gateway",
				DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
				SpecRevision: 1,
			},
		)
		if err != nil {
			t.Fatalf("create project-owned app runtime unit: %v", err)
		}
		otherProject, err := store.Identity().CreateProjectForPrincipal(
			ctx,
			identitystore.CreateProjectForPrincipalInput{
				OrgID: testOrgID, Creator: identitystore.NewUserPrincipal(admin.ID),
				Name: "Other integration project", IdempotencyKey: "other-integration-project",
			},
		)
		if err != nil {
			t.Fatalf("create other project for project deletion: %v", err)
		}
		otherApp, err := store.Integrations().CreateIntegrationApp(
			ctx,
			integrationstore.CreateIntegrationAppInput{
				OrgID: testOrgID, OwnerProjectID: otherProject.ID, Provider: testChannelProvider,
				ProviderAppRef: "other-project-deletion-app", DisplayName: "Other project app",
				ConnectorKey: testChannelConnector, State: integrationstore.IntegrationAppStateActive,
			},
		)
		if err != nil {
			t.Fatalf("create other-project app for project deletion: %v", err)
		}
		otherUnit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
			ctx,
			integrationstore.UpsertIntegrationRuntimeUnitInput{
				OrgID: testOrgID, IntegrationAppID: otherApp.ID,
				UnitKey: "other-project-app-runtime", RuntimeKind: "provider_gateway",
				DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
				SpecRevision: 1,
			},
		)
		if err != nil {
			t.Fatalf("create other-project app runtime unit: %v", err)
		}

		if _, err := store.Organizations().DeleteProject(
			ctx,
			testOrgID,
			testProjectID,
			identitystore.NewUserPrincipal(admin.ID),
		); err != nil {
			t.Fatalf("delete project with active integration: %v", err)
		}
		assertChannelLifecycleRowsDeleted(t, ctx, pool, app.ID, install.ID)
		assertChannelRuntimeUnitDeleted(t, ctx, pool, unit.ID)
		assertChannelRuntimeUnitDeleted(t, ctx, pool, appUnit.ID)
		var otherDeleted bool
		if err := pool.QueryRow(
			ctx,
			`SELECT deleted_at IS NOT NULL FROM integration_runtime_units WHERE id = $1`,
			otherUnit.ID,
		).Scan(&otherDeleted); err != nil {
			t.Fatalf("load other-project integration runtime after project deletion: %v", err)
		}
		if otherDeleted {
			t.Fatal("project deletion removed another project's integration runtime")
		}
	})

	t.Run("organization with active integration", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pool := openIntegrationDB(t, ctx)
		seedMigratedDB(t, ctx, pool)
		store := newSecretIntegrationStore(pool)
		admin, _, app, install := createChannelLifecycleFixture(t, ctx, store, "org-delete")
		unit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
			ctx,
			integrationstore.UpsertIntegrationRuntimeUnitInput{
				OrgID: testOrgID, IntegrationAppID: app.ID,
				UnitKey: "org-runtime", RuntimeKind: "provider_gateway",
				DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
				SpecRevision: 1,
			},
		)
		if err != nil {
			t.Fatalf("create organization runtime unit: %v", err)
		}

		if _, err := store.Organizations().DeleteOrganization(
			ctx,
			testOrgID,
			identitystore.NewUserPrincipal(admin.ID),
		); err != nil {
			t.Fatalf("delete organization with active integration: %v", err)
		}
		assertChannelLifecycleRowsDeleted(t, ctx, pool, app.ID, install.ID)
		assertChannelRuntimeUnitDeleted(t, ctx, pool, unit.ID)
	})
}

func TestChannelFoundationInstallDeletionRevokesConcurrentBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, "delete-binding-race")
	definitionID := createChannelTestDefinition(t, ctx, store, install)
	route, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "delete-binding-race", BehaviorKey: testChannelHandler,
			State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create deletion-race route: %v", err)
	}
	target, err := store.Integrations().CreateIntegrationTarget(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, ChannelDefinitionID: definitionID,
			IntegrationInstallID: install.ID, ProviderRef: "delete-binding-race-target",
			ProviderRefKind: "thread", DisplayName: "Deletion race",
		},
	)
	if err != nil {
		t.Fatalf("create deletion-race target: %v", err)
	}

	creatorTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin concurrent binding create: %v", err)
	}
	t.Cleanup(func() { _ = creatorTx.Rollback(ctx) })
	binding, err := store.Integrations().CreateIntegrationTargetBindingTx(
		ctx,
		creatorTx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			IntegrationRouteID: route.ID, ReceiveAllowed: true, SendAllowed: true,
			Source: "concurrent-create",
		},
	)
	if err != nil {
		t.Fatalf("create uncommitted concurrent binding: %v", err)
	}
	var creatorPID int32
	if err := creatorTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&creatorPID); err != nil {
		t.Fatalf("load concurrent binding creator backend: %v", err)
	}

	access := &blockingDeleteInstallAccess{
		reachedClear:  make(chan struct{}),
		continueClear: make(chan struct{}),
	}
	deletingStore := integrationstore.New(pool, access)
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- deletingStore.DeleteIntegrationInstall(ctx, testProjectID, install.ID)
	}()

	integrationdb.WaitForLockWaitBlockedBy(
		t, ctx, pool, "-- name: LockIntegrationInstallLifecycleExclusive ", creatorPID,
	)
	if err := creatorTx.Commit(ctx); err != nil {
		t.Fatalf("commit concurrent binding create: %v", err)
	}
	select {
	case <-access.reachedClear:
		close(access.continueClear)
	case <-time.After(5 * time.Second):
		t.Fatal("delete install did not reach target cleanup after binding commit")
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("delete install racing with binding create: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delete install did not finish after concurrent binding commit")
	}

	var revoked, targetDeleted bool
	if err := pool.QueryRow(
		ctx,
		`SELECT binding.revoked_at IS NOT NULL, target.deleted_at IS NOT NULL
		 FROM integration_target_bindings binding
		 JOIN integration_targets target ON target.id = binding.integration_target_id
		 WHERE binding.id = $1`,
		binding.ID,
	).Scan(&revoked, &targetDeleted); err != nil {
		t.Fatalf("load concurrent binding deletion state: %v", err)
	}
	if !revoked || !targetDeleted {
		t.Fatalf("concurrent binding deletion revoked=%t target_deleted=%t", revoked, targetDeleted)
	}
}

func TestChannelFoundationInstallDeletionFencesConcurrentTargetCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, _, _, install := createChannelLifecycleFixture(t, ctx, store, "delete-target-race")
	definitionID := createChannelTestDefinition(t, ctx, store, install)

	creatorTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin concurrent target create: %v", err)
	}
	t.Cleanup(func() { _ = creatorTx.Rollback(ctx) })
	target, err := store.Integrations().CreateIntegrationTargetTx(
		ctx,
		creatorTx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, ChannelDefinitionID: definitionID,
			IntegrationInstallID: install.ID, ProviderRef: "delete-target-race-target",
			ProviderRefKind: "thread", DisplayName: "Deletion target race",
		},
	)
	if err != nil {
		t.Fatalf("create uncommitted concurrent target: %v", err)
	}
	var creatorPID int32
	if err := creatorTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&creatorPID); err != nil {
		t.Fatalf("load concurrent target creator backend: %v", err)
	}

	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- store.Integrations().DeleteIntegrationInstall(ctx, testProjectID, install.ID)
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t, ctx, pool, "-- name: LockIntegrationInstallLifecycleExclusive ", creatorPID,
	)
	if err := creatorTx.Commit(ctx); err != nil {
		t.Fatalf("commit concurrent target create: %v", err)
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("delete install racing with target create: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delete install did not finish after concurrent target commit")
	}

	var deleted bool
	if err := pool.QueryRow(
		ctx,
		`SELECT deleted_at IS NOT NULL FROM integration_targets WHERE id = $1`,
		target.ID,
	).Scan(&deleted); err != nil {
		t.Fatalf("load concurrent target deletion state: %v", err)
	}
	if !deleted {
		t.Fatal("target committed before install deletion remained active")
	}
}

func TestChannelFoundationAppCredentialContractIsImmutable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, _, app, _ := createChannelLifecycleFixture(t, ctx, store, "immutable-app-contract")

	_, err := pool.Exec(
		ctx,
		`UPDATE integration_apps SET installation_credential_kind = 'generic' WHERE id = $1`,
		app.ID,
	)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "25006" {
		t.Fatalf("change integration app credential contract error = %v, want SQLSTATE 25006", err)
	}
}

func TestChannelFoundationRouteDefinitionIsImmutable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, _, _, install := createChannelLifecycleFixture(t, ctx, store, "immutable-route")
	route, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "immutable-route", BehaviorKey: testChannelHandler, Configuration: json.RawMessage(`{"mode":"mentions"}`), State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create immutable route: %v", err)
	}
	replayed, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "immutable-route", BehaviorKey: testChannelHandler, Configuration: json.RawMessage(`{ "mode": "mentions" }`), State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil || replayed.ID != route.ID {
		t.Fatalf("canonical route create replay = %+v, %v", replayed, err)
	}
	if _, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "immutable-route", BehaviorKey: "changed-behavior", Configuration: json.RawMessage(`{"mode":"mentions"}`), State: integrationstore.IntegrationRouteStateActive,
		},
	); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("changed route create replay error = %v, want idempotency conflict", err)
	}
	_, err = pool.Exec(
		ctx,
		`UPDATE integration_routes SET configuration = '{"mode":"all"}'::jsonb WHERE id = $1`,
		route.ID,
	)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "25006" {
		t.Fatalf("change integration route definition error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_routes SET state = 'disabled' WHERE id = $1`,
		route.ID,
	); err != nil {
		t.Fatalf("disable immutable route: %v", err)
	}
	disabledReplay, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "immutable-route", BehaviorKey: testChannelHandler, Configuration: json.RawMessage(`{"mode":"mentions"}`), State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil || disabledReplay.ID != route.ID ||
		disabledReplay.State != integrationstore.IntegrationRouteStateDisabled {
		t.Fatalf("disabled route create replay changed lifecycle = %+v, %v", disabledReplay, err)
	}

}

func TestChannelFoundationTargetAndBindingDefinitionsAreImmutable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, "immutable-address")
	definitionID := createChannelTestDefinition(t, ctx, store, install)

	createRoute := func(handler string) integrationstore.IntegrationRouteRecord {
		t.Helper()
		route, err := store.Integrations().CreateIntegrationRoute(
			ctx,
			integrationstore.CreateIntegrationRouteInput{
				ProjectID: testProjectID, IntegrationInstallID: install.ID,
				DeploymentKey: "route-" + handler, BehaviorKey: handler, State: integrationstore.IntegrationRouteStateActive,
			},
		)
		if err != nil {
			t.Fatalf("create %s route: %v", handler, err)
		}
		return route
	}
	route := createRoute("immutable_address_primary")
	alternateRoute := createRoute("immutable_address_alternate")
	if _, err := store.Integrations().CreateIntegrationTarget(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID:            testProjectID,
			IntegrationInstallID: install.ID, ProviderRef: "missing-definition-target",
			ProviderRefKind: "thread",
		},
	); err == nil {
		t.Fatal("target creation accepted a missing definition")
	}
	target, err := store.Integrations().CreateIntegrationTarget(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, ChannelDefinitionID: definitionID,
			IntegrationInstallID: install.ID, ProviderRef: "immutable-address-thread",
			ProviderRefKind: "thread", DisplayName: "Original address",
		},
	)
	if err != nil {
		t.Fatalf("create immutable target: %v", err)
	}
	binding, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			IntegrationRouteID: route.ID, ReceiveAllowed: true, SendAllowed: true,
			Source: "test", Metadata: json.RawMessage(`{"scope":"thread"}`),
		},
	)
	if err != nil {
		t.Fatalf("create immutable binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO agent_inputs(
  project_id, agent_id, state, input_kind, delivery_mode,
  integration_target_id, idempotency_scope, input_idempotency_key,
  queued_at, metadata
)
VALUES (
  $1, $2, 'received', 'content', 'queued', $3,
  'missing-binding-provenance', 'missing-binding-provenance',
  statement_timestamp(), '{}'::jsonb
)
`, testProjectID, agent.ID, target.ID); !isPgCode(err, "23514") {
		t.Fatalf("connector input without binding error = %v, want SQLSTATE 23514", err)
	}
	var targetXmin, bindingXmin string
	var targetUpdatedAt, bindingUpdatedAt time.Time
	if err := pool.QueryRow(
		ctx,
		`SELECT xmin::text, updated_at FROM integration_targets WHERE id = $1`,
		target.ID,
	).Scan(&targetXmin, &targetUpdatedAt); err != nil {
		t.Fatalf("load target replay markers: %v", err)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT xmin::text, updated_at FROM integration_target_bindings WHERE id = $1`,
		binding.ID,
	).Scan(&bindingXmin, &bindingUpdatedAt); err != nil {
		t.Fatalf("load binding replay markers: %v", err)
	}
	replayedTarget, err := store.Integrations().CreateIntegrationTarget(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, ChannelDefinitionID: definitionID,
			IntegrationInstallID: install.ID, ProviderRef: "immutable-address-thread",
			ProviderRefKind: "thread", DisplayName: "Original address",
		},
	)
	if err != nil || replayedTarget.ID != target.ID {
		t.Fatalf("replay immutable target = %+v, %v", replayedTarget, err)
	}
	replayedBinding, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			IntegrationRouteID: route.ID, ReceiveAllowed: true, SendAllowed: true,
			Source: "test", Metadata: json.RawMessage(`{"scope":"thread"}`),
		},
	)
	if err != nil || replayedBinding.ID != binding.ID {
		t.Fatalf("replay immutable binding = %+v, %v", replayedBinding, err)
	}
	var replayedTargetXmin, replayedBindingXmin string
	var replayedTargetUpdatedAt, replayedBindingUpdatedAt time.Time
	if err := pool.QueryRow(
		ctx,
		`SELECT xmin::text, updated_at FROM integration_targets WHERE id = $1`,
		target.ID,
	).Scan(&replayedTargetXmin, &replayedTargetUpdatedAt); err != nil {
		t.Fatalf("reload target replay markers: %v", err)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT xmin::text, updated_at FROM integration_target_bindings WHERE id = $1`,
		binding.ID,
	).Scan(&replayedBindingXmin, &replayedBindingUpdatedAt); err != nil {
		t.Fatalf("reload binding replay markers: %v", err)
	}
	if replayedTargetXmin != targetXmin || !replayedTargetUpdatedAt.Equal(targetUpdatedAt) {
		t.Fatalf("exact target replay wrote the row: xmin %s -> %s, updated %s -> %s",
			targetXmin, replayedTargetXmin, targetUpdatedAt, replayedTargetUpdatedAt)
	}
	if replayedBindingXmin != bindingXmin || !replayedBindingUpdatedAt.Equal(bindingUpdatedAt) {
		t.Fatalf("exact binding replay wrote the row: xmin %s -> %s, updated %s -> %s",
			bindingXmin, replayedBindingXmin, bindingUpdatedAt, replayedBindingUpdatedAt)
	}
	metadataRefresh, err := store.Integrations().CreateIntegrationTarget(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, ChannelDefinitionID: definitionID,
			IntegrationInstallID: install.ID, ProviderRef: "immutable-address-thread",
			ProviderRefKind: "thread", ProviderMetadata: json.RawMessage(`{"fresh":true}`),
		},
	)
	if err != nil {
		t.Fatalf("refresh target provider metadata without a display name: %v", err)
	}
	if metadataRefresh.DisplayName != "Original address" ||
		!sameJSON(metadataRefresh.ProviderMetadata, json.RawMessage(`{"fresh":true}`)) {
		t.Fatalf("metadata-only target refresh = %+v", metadataRefresh)
	}
	omittedMetadata, err := store.Integrations().CreateIntegrationTarget(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, ChannelDefinitionID: definitionID,
			IntegrationInstallID: install.ID, ProviderRef: "immutable-address-thread",
			ProviderRefKind: "thread",
		},
	)
	if err != nil || !sameJSON(
		omittedMetadata.ProviderMetadata,
		json.RawMessage(`{"fresh":true}`),
	) {
		t.Fatalf("omitted target metadata replay = %+v, %v", omittedMetadata, err)
	}
	clearedMetadata, err := store.Integrations().CreateIntegrationTarget(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, ChannelDefinitionID: definitionID,
			IntegrationInstallID: install.ID, ProviderRef: "immutable-address-thread",
			ProviderRefKind: "thread", ProviderMetadata: json.RawMessage(`{}`),
		},
	)
	if err != nil || !sameJSON(clearedMetadata.ProviderMetadata, json.RawMessage(`{}`)) {
		t.Fatalf("explicit target metadata clear = %+v, %v", clearedMetadata, err)
	}
	replacement, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			IntegrationRouteID: route.ID, ReceiveAllowed: true, SendAllowed: false,
			Source: "test", Metadata: json.RawMessage(`{"scope":"read_only"}`),
		},
	)
	if err != nil || replacement.ID == binding.ID {
		t.Fatalf("replace immutable binding = %+v, %v", replacement, err)
	}
	var oldRevoked, replacementRevoked bool
	if err := pool.QueryRow(
		ctx,
		`SELECT old_binding.revoked_at IS NOT NULL, new_binding.revoked_at IS NOT NULL
		 FROM integration_target_bindings old_binding
		 JOIN integration_target_bindings new_binding ON new_binding.id = $2
		 WHERE old_binding.id = $1`,
		binding.ID,
		replacement.ID,
	).Scan(&oldRevoked, &replacementRevoked); err != nil {
		t.Fatalf("load binding replacement lifecycle: %v", err)
	}
	if !oldRevoked || replacementRevoked {
		t.Fatalf("binding replacement lifecycle old_revoked=%v new_revoked=%v", oldRevoked, replacementRevoked)
	}

	_, err = pool.Exec(
		ctx,
		`UPDATE integration_targets SET provider_ref = 'rewritten-address' WHERE id = $1`,
		target.ID,
	)
	if !isPgCode(err, "25006") {
		t.Fatalf("change integration target identity error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_targets
		 SET display_name = 'Refreshed address', provider_metadata = '{"fresh":true}'::jsonb,
		     updated_at = statement_timestamp()
		 WHERE id = $1`,
		target.ID,
	); err != nil {
		t.Fatalf("refresh mutable integration target metadata: %v", err)
	}

	_, err = pool.Exec(
		ctx,
		`UPDATE integration_target_bindings SET integration_route_id = $2 WHERE id = $1`,
		replacement.ID,
		alternateRoute.ID,
	)
	if !isPgCode(err, "25006") {
		t.Fatalf("change integration binding route error = %v, want SQLSTATE 25006", err)
	}
	_, err = pool.Exec(
		ctx,
		`UPDATE integration_target_bindings SET send_allowed = true WHERE id = $1`,
		replacement.ID,
	)
	if !isPgCode(err, "25006") {
		t.Fatalf("change integration binding permission error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_target_bindings
		 SET revoked_at = statement_timestamp(), updated_at = statement_timestamp()
		 WHERE id = $1`,
		replacement.ID,
	); err != nil {
		t.Fatalf("revoke immutable integration binding: %v", err)
	}
	_, err = pool.Exec(
		ctx,
		`UPDATE integration_target_bindings
		 SET revoked_at = NULL, updated_at = statement_timestamp()
		 WHERE id = $1`,
		replacement.ID,
	)
	if !isPgCode(err, "25006") {
		t.Fatalf("reopen revoked integration binding error = %v, want SQLSTATE 25006", err)
	}
}

func TestDeleteIntegrationRouteRevokesOnlyItsBindings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, "delete-single-route")
	definitionID := createChannelTestDefinition(t, ctx, store, install)
	target, err := store.Integrations().CreateIntegrationTarget(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, ChannelDefinitionID: definitionID,
			IntegrationInstallID: install.ID, ProviderRef: "delete-single-route-channel",
			ProviderRefKind: "channel",
		},
	)
	if err != nil {
		t.Fatalf("create route lifecycle target: %v", err)
	}
	createRouteAndBinding := func(label string) (
		integrationstore.IntegrationRouteRecord,
		integrationstore.IntegrationTargetBindingRecord,
	) {
		t.Helper()
		route, err := store.Integrations().CreateIntegrationRoute(
			ctx,
			integrationstore.CreateIntegrationRouteInput{
				ProjectID: testProjectID, IntegrationInstallID: install.ID,
				DeploymentKey: "delete-single-route-" + label,
				BehaviorKey:   "delete_single_route", State: integrationstore.IntegrationRouteStateActive,
			},
		)
		if err != nil {
			t.Fatalf("create %s route: %v", label, err)
		}
		binding, err := store.Integrations().CreateIntegrationTargetBinding(
			ctx,
			integrationstore.CreateIntegrationTargetBindingInput{
				ProjectID: testProjectID, AgentID: agent.ID,
				IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
				IntegrationRouteID: route.ID, ReceiveAllowed: true, SendAllowed: true,
				Source: "test",
			},
		)
		if err != nil {
			t.Fatalf("create %s binding: %v", label, err)
		}
		return route, binding
	}
	deletedRoute, deletedBinding := createRouteAndBinding("deleted")
	siblingRoute, siblingBinding := createRouteAndBinding("sibling")

	if err := store.Integrations().DeleteIntegrationRoute(
		ctx,
		testProjectID,
		install.ID,
		deletedRoute.ID,
	); err != nil {
		t.Fatalf("delete one integration route: %v", err)
	}
	if err := store.Integrations().DeleteIntegrationRoute(
		ctx,
		testProjectID,
		install.ID,
		deletedRoute.ID,
	); err != nil {
		t.Fatalf("replay integration route delete: %v", err)
	}

	var deletedBindingRevoked bool
	if err := pool.QueryRow(
		ctx,
		`SELECT revoked_at IS NOT NULL FROM integration_target_bindings
WHERE project_id = $1 AND id = $2`,
		testProjectID,
		deletedBinding.ID,
	).Scan(&deletedBindingRevoked); err != nil {
		t.Fatalf("load deleted route binding history: %v", err)
	}
	if !deletedBindingRevoked {
		t.Fatal("deleted route binding remains active")
	}
	if active, err := store.Integrations().GetActiveSendBindingForTarget(
		ctx,
		testProjectID,
		agent.ID,
		target.ID,
	); err != nil || active.ID != siblingBinding.ID {
		t.Fatalf("sibling route send binding = %+v, %v", active, err)
	}
	routes, err := store.Integrations().ListActiveIntegrationRoutes(
		ctx,
		testProjectID,
		install.ID,
	)
	if err != nil || len(routes) != 1 || routes[0].ID != siblingRoute.ID {
		t.Fatalf("active routes after single delete = %+v, %v", routes, err)
	}
	channels, err := store.Integrations().ListAgentChannelTargets(
		ctx,
		testProjectID,
		agent.ID,
		integrationstore.ListAgentChannelTargetsInput{Limit: 10},
	)
	if err != nil || len(channels.Targets) != 1 || channels.Targets[0].ID != target.ID ||
		!channels.Targets[0].ReceiveAllowed || !channels.Targets[0].SendAllowed {
		t.Fatalf("sibling route channel authority = %+v, %v", channels, err)
	}
	var deletedState, siblingState string
	var deletedAt, siblingDeleted bool
	if err := pool.QueryRow(ctx, `
SELECT deleted.state, deleted.deleted_at IS NOT NULL,
       sibling.state, sibling.deleted_at IS NOT NULL
FROM integration_routes deleted
JOIN integration_routes sibling ON sibling.id = $2
WHERE deleted.id = $1
`, deletedRoute.ID, siblingRoute.ID).Scan(
		&deletedState, &deletedAt, &siblingState, &siblingDeleted,
	); err != nil {
		t.Fatalf("load route deletion lifecycle: %v", err)
	}
	if deletedState != "disabled" || !deletedAt || siblingState != "active" || siblingDeleted {
		t.Fatalf(
			"route lifecycle deleted=%q/%t sibling=%q/%t",
			deletedState, deletedAt, siblingState, siblingDeleted,
		)
	}
}

func TestChannelFoundationReceiveBindingLimitIsWriteSafe(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "binding-limit@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "binding-limit-profile")

	agents := make([]executionstore.AgentRecord, integrationstore.MaxActiveReceiveBindingsPerTargetRoute+3)
	for index := range agents {
		agents[index] = createIntegrationBoundAgent(
			t,
			ctx,
			store,
			profile,
			admin.ID,
			fmt.Sprintf("binding-limit-agent-%03d", index),
		)
	}
	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, OwnerProjectID: testProjectID,
			Provider: testChannelProvider, ProviderAppRef: "binding-limit-app",
			DisplayName: "Binding limit", ConnectorKey: testChannelConnector,
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create binding-limit app: %v", err)
	}
	install, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
			InstalledBy: identitystore.NewUserPrincipal(admin.ID),
			Provider:    testChannelProvider, IntegrationKind: integrationstore.IntegrationKindManaged,
			ConnectionMode: "gateway", State: integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: "binding-limit-tenant", ProviderAccountRef: "binding-limit-account",
			DisplayName: "Binding limit bot",
		},
	)
	if err != nil {
		t.Fatalf("create binding-limit install: %v", err)
	}
	definitionID := createChannelTestDefinition(t, ctx, store, install)
	route, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "binding-limit", BehaviorKey: testChannelHandler, State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create binding-limit route: %v", err)
	}
	target, err := store.Integrations().CreateIntegrationTarget(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, ChannelDefinitionID: definitionID,
			IntegrationInstallID: install.ID, ProviderRef: "binding-limit-channel",
			ProviderRefKind: "channel", DisplayName: "Binding limit channel",
		},
	)
	if err != nil {
		t.Fatalf("create binding-limit target: %v", err)
	}
	bindingInput := func(
		agentID uuid.UUID,
		receiveAllowed, sendAllowed bool,
		source string,
	) integrationstore.CreateIntegrationTargetBindingInput {
		return integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: agentID,
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			IntegrationRouteID: route.ID, ReceiveAllowed: receiveAllowed,
			SendAllowed: sendAllowed, Source: source,
		}
	}

	sendOnly, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		bindingInput(agents[0].ID, false, true, "send-only"),
	)
	if err != nil {
		t.Fatalf("create send-only capacity candidate: %v", err)
	}
	for index := 1; index <= integrationstore.MaxActiveReceiveBindingsPerTargetRoute-1; index++ {
		if _, err := store.Integrations().CreateIntegrationTargetBinding(
			ctx,
			bindingInput(agents[index].ID, true, true, "initial-receive"),
		); err != nil {
			t.Fatalf("create receive binding %d: %v", index, err)
		}
	}

	type createResult struct {
		agentID uuid.UUID
		binding integrationstore.IntegrationTargetBindingRecord
		err     error
	}
	results := make(chan createResult, 2)
	capacity := integrationstore.MaxActiveReceiveBindingsPerTargetRoute
	for _, agent := range agents[capacity : capacity+2] {
		go func() {
			binding, createErr := store.Integrations().CreateIntegrationTargetBinding(
				ctx,
				bindingInput(agent.ID, true, true, "concurrent-receive"),
			)
			results <- createResult{agentID: agent.ID, binding: binding, err: createErr}
		}()
	}
	var winner createResult
	var successes, capacityFailures int
	for range 2 {
		result := <-results
		if result.err == nil {
			winner = result
			successes++
		} else if errors.Is(result.err, storeerr.ErrInvalidRequest) {
			capacityFailures++
		} else {
			t.Fatalf("concurrent receive binding failed unexpectedly: %v", result.err)
		}
	}
	if successes != 1 || capacityFailures != 1 {
		t.Fatalf("concurrent capacity boundary successes=%d failures=%d", successes, capacityFailures)
	}

	replayed, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		bindingInput(winner.agentID, true, true, "concurrent-receive"),
	)
	if err != nil || replayed.ID != winner.binding.ID {
		t.Fatalf("replay at receive-binding capacity = %+v, %v", replayed, err)
	}
	if _, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		bindingInput(
			agents[integrationstore.MaxActiveReceiveBindingsPerTargetRoute+2].ID,
			true,
			true,
			"sequential-overflow",
		),
	); !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("sequential receive binding over capacity error = %v", err)
	}
	if _, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		bindingInput(agents[0].ID, true, true, "send-to-receive-at-capacity"),
	); !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("send-only to receive replacement at capacity error = %v", err)
	}
	stillSendOnly, err := store.Integrations().GetIntegrationTargetBinding(
		ctx,
		testProjectID,
		sendOnly.ID,
	)
	if err != nil || stillSendOnly.ReceiveAllowed || !stillSendOnly.SendAllowed {
		t.Fatalf("failed capacity replacement changed send-only binding = %+v, %v", stillSendOnly, err)
	}

	receiveReplacement, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		bindingInput(winner.agentID, true, false, "receive-to-receive"),
	)
	if err != nil || receiveReplacement.ID == winner.binding.ID {
		t.Fatalf("receive-to-receive replacement at capacity = %+v, %v", receiveReplacement, err)
	}
	if _, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		bindingInput(winner.agentID, false, true, "receive-to-send"),
	); err != nil {
		t.Fatalf("replace receive binding with send-only binding: %v", err)
	}
	if _, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		bindingInput(agents[0].ID, true, true, "send-to-receive-after-space"),
	); err != nil {
		t.Fatalf("replace send-only binding after capacity freed: %v", err)
	}

	var activeReceiveBindings int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*)
		 FROM integration_target_bindings
		 WHERE project_id = $1
		   AND integration_target_id = $2
		   AND integration_route_id = $3
		   AND receive_allowed
		   AND revoked_at IS NULL`,
		testProjectID,
		target.ID,
		route.ID,
	).Scan(&activeReceiveBindings); err != nil {
		t.Fatalf("count final active receive bindings: %v", err)
	}
	if activeReceiveBindings != integrationstore.MaxActiveReceiveBindingsPerTargetRoute {
		t.Fatalf("final active receive bindings = %d", activeReceiveBindings)
	}
}

func createChannelLifecycleFixture(
	t *testing.T,
	ctx context.Context,
	store *Store,
	suffix string,
) (
	identitystore.UserRecord,
	executionstore.AgentRecord,
	integrationstore.IntegrationAppRecord,
	integrationstore.IntegrationInstallRecord,
) {
	t.Helper()
	admin := createIntegrationProjectAdmin(t, ctx, store, suffix+"@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, suffix+"-profile")
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, suffix+"-agent")
	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, OwnerProjectID: testProjectID,
			Provider: testChannelProvider, ProviderAppRef: suffix + "-app",
			DisplayName: suffix, ConnectorKey: testChannelConnector,
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create lifecycle integration app: %v", err)
	}
	install, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
			InstalledBy: identitystore.NewUserPrincipal(admin.ID),
			Provider:    testChannelProvider, IntegrationKind: integrationstore.IntegrationKindManaged,
			ConnectionMode: "gateway", State: integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: suffix + "-tenant", ProviderAccountRef: suffix + "-account",
			DisplayName: suffix,
		},
	)
	if err != nil {
		t.Fatalf("create lifecycle integration install: %v", err)
	}
	return admin, agent, app, install
}

func createChannelTestDefinition(
	t *testing.T, ctx context.Context, store *Store, install integrationstore.IntegrationInstallRecord,
) uuid.UUID {
	t.Helper()
	definition, err := store.Integrations().PublishConnectorChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: install.ProjectID, IntegrationInstallID: install.ID,
			ImplementationKey: "conversation", Kind: integrationstore.ChannelKindDiscordThread,
			SendParamsSchema:      json.RawMessage(`{"type":"object","additionalProperties":false}`),
			Capabilities:          integrationstore.ChannelCapabilities{Read: true, Send: true, Text: true},
			ConnectorCapabilities: testChannelCapabilities(install.Provider),
		})
	if err != nil {
		t.Fatalf("publish test channel definition: %v", err)
	}
	return definition.ID
}

func assertChannelLifecycleRowsDeleted(
	t *testing.T,
	ctx context.Context,
	pool interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	appID, installID uuid.UUID,
) {
	t.Helper()
	var appDeleted, installDeleted bool
	if err := pool.QueryRow(
		ctx,
		`SELECT app.deleted_at IS NOT NULL, install.deleted_at IS NOT NULL
		 FROM integration_apps app
		 JOIN integration_installs install ON install.integration_app_id = app.id
		 WHERE app.id = $1 AND install.id = $2`,
		appID,
		installID,
	).Scan(&appDeleted, &installDeleted); err != nil {
		t.Fatalf("load integration lifecycle rows: %v", err)
	}
	if !appDeleted || !installDeleted {
		t.Fatalf("integration lifecycle deletion app=%t install=%t, want both true", appDeleted, installDeleted)
	}
}

func assertChannelRuntimeUnitDeleted(
	t *testing.T,
	ctx context.Context,
	pool interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	unitID uuid.UUID,
) {
	t.Helper()
	var desiredState, status string
	var deleted bool
	if err := pool.QueryRow(
		ctx,
		`SELECT desired_state, status, deleted_at IS NOT NULL
		 FROM integration_runtime_units WHERE id = $1`,
		unitID,
	).Scan(&desiredState, &status, &deleted); err != nil {
		t.Fatalf("load integration runtime lifecycle row: %v", err)
	}
	if desiredState != "stopped" || status != "stopped" || !deleted {
		t.Fatalf("runtime lifecycle state = %s/%s deleted=%t", desiredState, status, deleted)
	}
}
