//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestStaleIntegrationRuntimeReleaseOnlyRelinquishesLease(t *testing.T) {
	testCases := []string{"spec_revision", "app_revision", "install_revision", "expired_lease"}
	for _, testCase := range testCases {
		t.Run(testCase, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			store := newSecretIntegrationStore(pool)
			_, _, app, install := createChannelLifecycleFixture(
				t,
				ctx,
				store,
				"stale-release-"+testCase,
			)
			unitInput := integrationstore.UpsertIntegrationRuntimeUnitInput{
				OrgID: testOrgID, IntegrationAppID: app.ID,
				ProjectID: testProjectID, IntegrationInstallID: install.ID,
				UnitKey: "stale-release", RuntimeKind: "provider_socket",
				DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
				SpecRevision: 1, Configuration: json.RawMessage(`{"revision":1}`),
			}
			unit, err := store.Integrations().UpsertIntegrationRuntimeUnit(ctx, unitInput)
			if err != nil {
				t.Fatalf("create integration runtime: %v", err)
			}
			lease := claimOnlyIntegrationRuntime(t, ctx, store, "stale-release-owner", unit.ID)
			lease, err = store.Integrations().HeartbeatIntegrationRuntimeUnit(
				ctx,
				integrationstore.HeartbeatIntegrationRuntimeUnitInput{
					ID: lease.ID, LeaseToken: lease.LeaseToken,
					LeaseGeneration: lease.LeaseGeneration, LeaseDuration: time.Minute,
					WriteCheckpoint: true, CheckpointVersion: 1,
					Checkpoint:   json.RawMessage(`{"cursor":"durable"}`),
					Capabilities: testChannelCapabilities(testChannelProvider),
				},
			)
			if err != nil {
				t.Fatalf("write durable runtime checkpoint: %v", err)
			}

			switch testCase {
			case "spec_revision":
				unitInput.SpecRevision = 2
				unitInput.Configuration = json.RawMessage(`{"revision":2}`)
				if _, err := store.Integrations().UpsertIntegrationRuntimeUnit(ctx, unitInput); err != nil {
					t.Fatalf("advance runtime specification: %v", err)
				}
			case "app_revision":
				if _, err := pool.Exec(
					ctx,
					`UPDATE integration_apps SET provider_config = '{"revision":2}' WHERE id = $1`,
					app.ID,
				); err != nil {
					t.Fatalf("advance app configuration: %v", err)
				}
			case "install_revision":
				if _, err := pool.Exec(
					ctx,
					`UPDATE integration_installs SET provider_config = '{"revision":2}' WHERE id = $1`,
					install.ID,
				); err != nil {
					t.Fatalf("advance install configuration: %v", err)
				}
			case "expired_lease":
				if _, err := pool.Exec(
					ctx,
					`UPDATE integration_runtime_units
					 SET leased_at = statement_timestamp() - interval '2 seconds',
					     renewed_at = statement_timestamp() - interval '2 seconds',
					     lease_expires_at = statement_timestamp() - interval '1 second',
					     available_at = statement_timestamp() - interval '1 second'
					 WHERE id = $1`,
					unit.ID,
				); err != nil {
					t.Fatalf("expire runtime lease: %v", err)
				}
			}

			var beforeCheckpoint, beforeError json.RawMessage
			var beforeCheckpointVersion, beforeSpecRevision, beforeFailureCount int
			var beforeCheckpointRevision, appRevision, installRevision int64
			if err := pool.QueryRow(ctx, `
SELECT unit.checkpoint_version, unit.checkpoint_revision, unit.checkpoint,
       unit.last_error, unit.failure_count, unit.spec_revision,
       app.configuration_revision, install.configuration_revision
FROM integration_runtime_units unit
JOIN integration_apps app ON app.id = unit.integration_app_id
JOIN integration_installs install ON install.id = unit.integration_install_id
WHERE unit.id = $1
`, unit.ID).Scan(
				&beforeCheckpointVersion,
				&beforeCheckpointRevision,
				&beforeCheckpoint,
				&beforeError,
				&beforeFailureCount,
				&beforeSpecRevision,
				&appRevision,
				&installRevision,
			); err != nil {
				t.Fatalf("load state before stale release: %v", err)
			}

			relinquished, err := store.Integrations().ReleaseIntegrationRuntimeUnit(
				ctx,
				integrationstore.ReleaseIntegrationRuntimeUnitInput{
					ID: lease.ID, LeaseToken: lease.LeaseToken,
					LeaseGeneration: lease.LeaseGeneration,
					WriteCheckpoint: true, CheckpointVersion: 99,
					Checkpoint:   json.RawMessage(`{"cursor":"stale"}`),
					LastError:    json.RawMessage(`{"code":"stale_worker"}`),
					Capabilities: testChannelCapabilities(testChannelProvider),
				},
			)
			if err != nil {
				t.Fatalf("relinquish stale runtime lease: %v", err)
			}
			if relinquished.Status != integrationstore.IntegrationRuntimeStatusIdle ||
				relinquished.LeaseToken != integrationstore.NilID {
				t.Fatalf("stale runtime release did not relinquish ownership: %+v", relinquished)
			}

			var afterCheckpoint, afterError json.RawMessage
			var afterCheckpointVersion, afterFailureCount int
			var afterCheckpointRevision int64
			if err := pool.QueryRow(ctx, `
SELECT checkpoint_version, checkpoint_revision, checkpoint, last_error, failure_count
FROM integration_runtime_units
WHERE id = $1
`, unit.ID).Scan(
				&afterCheckpointVersion,
				&afterCheckpointRevision,
				&afterCheckpoint,
				&afterError,
				&afterFailureCount,
			); err != nil {
				t.Fatalf("load state after stale release: %v", err)
			}
			if afterCheckpointVersion != beforeCheckpointVersion ||
				afterCheckpointRevision != beforeCheckpointRevision ||
				!sameJSON(afterCheckpoint, beforeCheckpoint) ||
				!sameJSON(afterError, beforeError) ||
				afterFailureCount != beforeFailureCount {
				t.Fatalf(
					"stale release published outcome: checkpoint %d/%d %s -> %d/%d %s, error %s -> %s, failures %d -> %d",
					beforeCheckpointVersion,
					beforeCheckpointRevision,
					beforeCheckpoint,
					afterCheckpointVersion,
					afterCheckpointRevision,
					afterCheckpoint,
					beforeError,
					afterError,
					beforeFailureCount,
					afterFailureCount,
				)
			}

			fresh := claimOnlyIntegrationRuntime(t, ctx, store, "stale-release-replacement", unit.ID)
			if fresh.LeaseGeneration != lease.LeaseGeneration+1 ||
				fresh.LeaseSpecRevision != beforeSpecRevision ||
				fresh.LeaseAppConfigurationRevision != appRevision ||
				fresh.LeaseInstallConfigRevision != installRevision {
				t.Fatalf("replacement runtime did not claim current revisions: %+v", fresh)
			}
		})
	}
}

func TestIntegrationRuntimeMutationLocksInstallBeforeRuntimeUnit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, _, app, install := createChannelLifecycleFixture(t, ctx, store, "runtime-lock-order")
	unit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
		ctx,
		integrationstore.UpsertIntegrationRuntimeUnitInput{
			OrgID: testOrgID, IntegrationAppID: app.ID,
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			UnitKey: "runtime-lock-order", RuntimeKind: "provider_gateway",
			DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
			SpecRevision: 1,
		},
	)
	if err != nil {
		t.Fatalf("create integration runtime unit: %v", err)
	}
	claims, err := store.Integrations().ClaimIntegrationRuntimeUnits(
		ctx,
		integrationstore.ClaimIntegrationRuntimeUnitsInput{
			LeaseOwner: "runtime-lock-order", LeaseDuration: time.Minute,
			Capability: testChannelCapability(testChannelProvider), Limit: 1,
		},
	)
	if err != nil || len(claims) != 1 || claims[0].ID != unit.ID {
		t.Fatalf("claim integration runtime unit = %+v, %v", claims, err)
	}
	lease := claims[0]

	unitHolder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin runtime unit holder: %v", err)
	}
	t.Cleanup(func() { _ = unitHolder.Rollback(ctx) })
	if _, err := unitHolder.Exec(
		ctx,
		`SELECT id FROM integration_runtime_units WHERE id = $1 FOR UPDATE`,
		unit.ID,
	); err != nil {
		t.Fatalf("lock runtime unit: %v", err)
	}

	guardTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin runtime mutation guard: %v", err)
	}
	t.Cleanup(func() { _ = guardTx.Rollback(ctx) })
	var guardPID int32
	if err := guardTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&guardPID); err != nil {
		t.Fatalf("load runtime mutation backend: %v", err)
	}
	guardDone := make(chan error, 1)
	go func() {
		guardDone <- integrationstore.LockIntegrationRuntimeLeaseForMutation(
			ctx,
			dbsqlc.New(guardTx),
			&integrationstore.IntegrationRuntimeLeaseProof{
				IntegrationAppID: app.ID,
				UnitID:           unit.ID,
				LeaseToken:       lease.LeaseToken,
				LeaseGeneration:  lease.LeaseGeneration,
			},
			testProjectID,
			install.ID,
		)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(
			ctx,
			`SELECT coalesce(wait_event_type = 'Lock', false) FROM pg_stat_activity WHERE pid = $1`,
			guardPID,
		).Scan(&waiting); err != nil {
			t.Fatalf("observe runtime mutation guard: %v", err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runtime mutation guard did not wait for the locked unit")
		}
		time.Sleep(10 * time.Millisecond)
	}

	probeTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin install lock probe: %v", err)
	}
	_, probeErr := probeTx.Exec(
		ctx,
		`SELECT id FROM integration_installs WHERE id = $1 FOR UPDATE NOWAIT`,
		install.ID,
	)
	_ = probeTx.Rollback(ctx)
	if !isPgCode(probeErr, "55P03") {
		t.Fatalf("install lock probe error = %v, want SQLSTATE 55P03", probeErr)
	}

	if err := unitHolder.Rollback(ctx); err != nil {
		t.Fatalf("release runtime unit holder: %v", err)
	}
	select {
	case err := <-guardDone:
		if err != nil {
			t.Fatalf("finish runtime mutation guard: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runtime mutation guard did not finish after unit unlock")
	}
}

func TestIntegrationRuntimeLeaseMovesClaimAvailabilityToExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, _, app, install := createChannelLifecycleFixture(t, ctx, store, "runtime-claim-availability")
	unit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
		ctx,
		integrationstore.UpsertIntegrationRuntimeUnitInput{
			OrgID: testOrgID, IntegrationAppID: app.ID,
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			UnitKey: "runtime-claim-availability", RuntimeKind: "provider_gateway",
			DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
			SpecRevision: 1,
		},
	)
	if err != nil {
		t.Fatalf("create integration runtime unit: %v", err)
	}
	claims, err := store.Integrations().ClaimIntegrationRuntimeUnits(
		ctx,
		integrationstore.ClaimIntegrationRuntimeUnitsInput{
			LeaseOwner: "runtime-claim-availability", LeaseDuration: time.Minute,
			Capability: testChannelCapability(testChannelProvider), Limit: 1,
		},
	)
	if err != nil || len(claims) != 1 || claims[0].ID != unit.ID {
		t.Fatalf("claim integration runtime unit = %+v, %v", claims, err)
	}
	lease := claims[0]
	assertRuntimeAvailableAtLeaseExpiry(t, ctx, pool, unit.ID)
	firstExpiry := *lease.LeaseExpiresAt

	heartbeat, err := store.Integrations().HeartbeatIntegrationRuntimeUnit(
		ctx,
		integrationstore.HeartbeatIntegrationRuntimeUnitInput{
			ID: unit.ID, LeaseToken: lease.LeaseToken,
			LeaseGeneration: lease.LeaseGeneration, LeaseDuration: 2 * time.Minute,
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	)
	if err != nil {
		t.Fatalf("heartbeat integration runtime unit: %v", err)
	}
	if heartbeat.LeaseExpiresAt == nil || !heartbeat.LeaseExpiresAt.After(firstExpiry) {
		t.Fatalf("heartbeat lease expiry = %v, want after %v", heartbeat.LeaseExpiresAt, firstExpiry)
	}
	assertRuntimeAvailableAtLeaseExpiry(t, ctx, pool, unit.ID)
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_runtime_units
		 SET leased_at = statement_timestamp() - interval '3 seconds',
		     renewed_at = statement_timestamp() - interval '2 seconds',
		     lease_expires_at = statement_timestamp() - interval '1 second',
		     available_at = statement_timestamp() - interval '1 second'
		 WHERE id = $1`,
		unit.ID,
	); err != nil {
		t.Fatalf("expire runtime lease: %v", err)
	}
	_, err = store.Integrations().HeartbeatIntegrationRuntimeUnit(
		ctx,
		integrationstore.HeartbeatIntegrationRuntimeUnitInput{
			ID: unit.ID, LeaseToken: lease.LeaseToken,
			LeaseGeneration: lease.LeaseGeneration, LeaseDuration: 2 * time.Minute,
			WriteCheckpoint: true, CheckpointVersion: 1,
			Checkpoint:   json.RawMessage(`{"must_not_persist":true}`),
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	)
	if !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("expired runtime heartbeat error = %v, want state transition conflict", err)
	}
	var checkpoint json.RawMessage
	if err := pool.QueryRow(
		ctx,
		`SELECT checkpoint FROM integration_runtime_units WHERE id = $1`,
		unit.ID,
	).Scan(&checkpoint); err != nil {
		t.Fatalf("load expired runtime checkpoint: %v", err)
	}
	if string(checkpoint) != "{}" {
		t.Fatalf("expired heartbeat checkpoint = %s, want {}", checkpoint)
	}
}

func assertRuntimeAvailableAtLeaseExpiry(
	t *testing.T,
	ctx context.Context,
	pool interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	unitID ID,
) {
	t.Helper()
	var equal bool
	if err := pool.QueryRow(ctx, `
SELECT available_at = lease_expires_at
FROM integration_runtime_units
WHERE id = $1
`, unitID).Scan(&equal); err != nil {
		t.Fatalf("load runtime lease availability: %v", err)
	}
	if !equal {
		t.Fatal("leased runtime remained in the immediately claimable index range")
	}
}

func TestExpiredIntegrationRuntimeLeaseCanBeReclaimedWithoutRelease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, _, app, install := createChannelLifecycleFixture(t, ctx, store, "runtime-expiry-takeover")
	unit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
		ctx,
		integrationstore.UpsertIntegrationRuntimeUnitInput{
			OrgID: testOrgID, IntegrationAppID: app.ID,
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			UnitKey: "runtime-expiry-takeover", RuntimeKind: "provider_gateway",
			DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
			SpecRevision: 1,
		},
	)
	if err != nil {
		t.Fatalf("create integration runtime unit: %v", err)
	}
	first := claimOnlyIntegrationRuntime(t, ctx, store, "gateway-expired", unit.ID)
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_runtime_units
		 SET leased_at = statement_timestamp() - interval '3 seconds',
		     renewed_at = statement_timestamp() - interval '2 seconds',
		     lease_expires_at = statement_timestamp() - interval '1 second',
		     available_at = statement_timestamp() - interval '1 second'
		 WHERE id = $1`,
		unit.ID,
	); err != nil {
		t.Fatalf("expire integration runtime lease: %v", err)
	}

	replacement := claimOnlyIntegrationRuntime(t, ctx, store, "gateway-replacement", unit.ID)
	if replacement.LeaseGeneration != first.LeaseGeneration+1 ||
		replacement.LeaseToken == first.LeaseToken || replacement.LeaseOwner != "gateway-replacement" {
		t.Fatalf("replacement runtime lease = %+v, first = %+v", replacement, first)
	}
	_, err = store.Integrations().HeartbeatIntegrationRuntimeUnit(
		ctx,
		integrationstore.HeartbeatIntegrationRuntimeUnitInput{
			ID: first.ID, LeaseToken: first.LeaseToken,
			LeaseGeneration: first.LeaseGeneration, LeaseDuration: time.Minute,
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	)
	if !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("expired owner heartbeat error = %v, want state transition conflict", err)
	}
	_, err = store.Integrations().ReleaseIntegrationRuntimeUnit(
		ctx,
		integrationstore.ReleaseIntegrationRuntimeUnitInput{
			ID: first.ID, LeaseToken: first.LeaseToken,
			LeaseGeneration: first.LeaseGeneration,
			LastError:       json.RawMessage(`{"code":"expired_owner"}`),
			Capabilities:    testChannelCapabilities(testChannelProvider),
		},
	)
	if !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("expired owner release error = %v, want state transition conflict", err)
	}
	heartbeat, err := store.Integrations().HeartbeatIntegrationRuntimeUnit(
		ctx,
		integrationstore.HeartbeatIntegrationRuntimeUnitInput{
			ID: replacement.ID, LeaseToken: replacement.LeaseToken,
			LeaseGeneration: replacement.LeaseGeneration, LeaseDuration: time.Minute,
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	)
	if err != nil || heartbeat.LeaseToken != replacement.LeaseToken ||
		heartbeat.LeaseGeneration != replacement.LeaseGeneration {
		t.Fatalf("replacement runtime heartbeat = %+v, %v", heartbeat, err)
	}
}

func TestDeleteIntegrationInstallRetiresAndFencesRuntimeUnits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin, agent, app, install := createChannelLifecycleFixture(t, ctx, store, "runtime-reinstall")
	route, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID:            testProjectID,
			IntegrationInstallID: install.ID, DeploymentKey: "runtime-reinstall",
			HandlerKey: testChannelHandler, HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create integration route: %v", err)
	}
	target, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, ProviderRef: "runtime-reinstall-thread",
			ProviderRefKind: "thread",
		},
	)
	if err != nil {
		t.Fatalf("create integration target: %v", err)
	}
	binding, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			IntegrationRouteID: route.ID, ReceiveAllowed: true, SendAllowed: true,
			Source: "runtime-reinstall",
		},
	)
	if err != nil {
		t.Fatalf("create integration binding: %v", err)
	}
	unit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
		ctx,
		integrationstore.UpsertIntegrationRuntimeUnitInput{
			OrgID: testOrgID, IntegrationAppID: app.ID,
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			UnitKey: "provider-session", RuntimeKind: "provider_gateway",
			DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
			SpecRevision: 1,
		},
	)
	if err != nil {
		t.Fatalf("create integration runtime unit: %v", err)
	}
	claims, err := store.Integrations().ClaimIntegrationRuntimeUnits(
		ctx,
		integrationstore.ClaimIntegrationRuntimeUnitsInput{
			LeaseOwner: "gateway-before-delete", LeaseDuration: time.Minute,
			Capability: testChannelCapability(testChannelProvider), Limit: 1,
		},
	)
	if err != nil || len(claims) != 1 || claims[0].ID != unit.ID {
		t.Fatalf("claim integration runtime unit = %+v, %v", claims, err)
	}
	lease := claims[0]

	if err := store.Integrations().DeleteIntegrationInstall(ctx, testProjectID, install.ID); err != nil {
		t.Fatalf("delete integration installation: %v", err)
	}
	var runtimeDeleted, leaseCleared, bindingRevoked bool
	var desiredState, status string
	if err := pool.QueryRow(
		ctx,
		`SELECT deleted_at IS NOT NULL, lease_token IS NULL, desired_state, status
		 FROM integration_runtime_units WHERE id = $1`,
		unit.ID,
	).Scan(&runtimeDeleted, &leaseCleared, &desiredState, &status); err != nil {
		t.Fatalf("load retired runtime unit: %v", err)
	}
	if !runtimeDeleted || !leaseCleared || desiredState != "stopped" || status != "stopped" {
		t.Fatalf(
			"retired runtime deleted=%t lease_cleared=%t desired=%q status=%q",
			runtimeDeleted,
			leaseCleared,
			desiredState,
			status,
		)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT revoked_at IS NOT NULL FROM integration_target_bindings WHERE id = $1`,
		binding.ID,
	).Scan(&bindingRevoked); err != nil {
		t.Fatalf("load revoked integration binding: %v", err)
	}
	if !bindingRevoked {
		t.Fatal("integration binding remained active after installation deletion")
	}
	if _, err := store.Integrations().HeartbeatIntegrationRuntimeUnit(
		ctx,
		integrationstore.HeartbeatIntegrationRuntimeUnitInput{
			ID: unit.ID, LeaseToken: lease.LeaseToken,
			LeaseGeneration: lease.LeaseGeneration, LeaseDuration: time.Minute,
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("heartbeat deleted runtime error = %v, want conflict", err)
	}

	reinstalled, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
			InstalledByUserID: admin.ID,
			Provider:          testChannelProvider, IntegrationKind: "lifecycle_test",
			ConnectionMode: "gateway", State: integrationstore.IntegrationInstallStateActive,
			ProviderTenantID:         install.ProviderTenantID,
			ProviderAccountRef:       install.ProviderAccountRef,
			ProviderAgentDisplayName: "runtime-reinstall",
		},
	)
	if err != nil {
		t.Fatalf("reinstall integration: %v", err)
	}
	if reinstalled.ID == install.ID {
		t.Fatalf("reinstall reused deleted installation %s", install.ID)
	}
	fresh, err := store.Integrations().UpsertIntegrationRuntimeUnit(
		ctx,
		integrationstore.UpsertIntegrationRuntimeUnitInput{
			OrgID: testOrgID, IntegrationAppID: app.ID,
			ProjectID: testProjectID, IntegrationInstallID: reinstalled.ID,
			UnitKey: unit.UnitKey, RuntimeKind: unit.RuntimeKind,
			DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
			SpecRevision: 1,
		},
	)
	if err != nil {
		t.Fatalf("create fresh runtime after reinstall: %v", err)
	}
	if fresh.ID == unit.ID || fresh.IntegrationInstallID != reinstalled.ID ||
		fresh.LeaseToken != NilID || fresh.LeaseGeneration != 0 {
		t.Fatalf("fresh reinstalled runtime inherited stale identity or lease: %+v", fresh)
	}
}
