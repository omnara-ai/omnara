//go:build integration

package executionstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestIntegrationRuntimeOwnerAvailabilityFencesLeases(t *testing.T) {
	t.Run("installation", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pool := openIntegrationDB(t, ctx)
		seedMigratedDB(t, ctx, pool)
		store := newSecretIntegrationStore(pool)
		_, _, app, install := createChannelLifecycleFixture(t, ctx, store, "runtime-install-disable")
		input := integrationstore.UpsertIntegrationRuntimeUnitInput{
			OrgID: testOrgID, IntegrationAppID: app.ID,
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			UnitKey: "install-session", RuntimeKind: "provider_socket",
			DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
			SpecRevision: 1,
		}
		unit, err := store.Integrations().UpsertIntegrationRuntimeUnit(ctx, input)
		if err != nil {
			t.Fatalf("create installation runtime: %v", err)
		}
		lease := claimOnlyIntegrationRuntime(t, ctx, store, "install-disable-before", unit.ID)

		if _, err := pool.Exec(
			ctx,
			`UPDATE integration_installs SET state = 'disabled' WHERE id = $1`,
			install.ID,
		); err != nil {
			t.Fatalf("disable runtime installation: %v", err)
		}
		assertRunningIntegrationRuntime(t, ctx, pool, unit.ID)
		assertIntegrationRuntimeLeaseFenced(t, ctx, store, lease)
		assertNoIntegrationRuntimeClaims(t, ctx, store, "install-disable-after")
		if _, err := store.Integrations().UpsertIntegrationRuntimeUnit(ctx, input); !errors.Is(
			err,
			storeerr.ErrConflict,
		) {
			t.Fatalf("reconcile disabled installation runtime error = %v, want conflict", err)
		}
		if _, err := pool.Exec(
			ctx,
			`UPDATE integration_installs SET state = 'active' WHERE id = $1`,
			install.ID,
		); err != nil {
			t.Fatalf("re-enable runtime installation: %v", err)
		}
		if _, err := pool.Exec(
			ctx,
			`UPDATE integration_runtime_units
			 SET renewed_at = statement_timestamp() - interval '2 seconds',
			     lease_expires_at = statement_timestamp() - interval '1 second',
			     available_at = statement_timestamp() - interval '1 second'
			 WHERE id = $1`,
			unit.ID,
		); err != nil {
			t.Fatalf("expire disabled installation runtime lease: %v", err)
		}
		claimOnlyIntegrationRuntime(t, ctx, store, "install-reenable", unit.ID)
	})

	t.Run("application", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pool := openIntegrationDB(t, ctx)
		seedMigratedDB(t, ctx, pool)
		store := newSecretIntegrationStore(pool)
		_, _, app, install := createChannelLifecycleFixture(t, ctx, store, "runtime-app-disable")
		appInput := integrationstore.UpsertIntegrationRuntimeUnitInput{
			OrgID: testOrgID, IntegrationAppID: app.ID,
			UnitKey: "app-session", RuntimeKind: "provider_gateway",
			DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
			SpecRevision: 1,
		}
		installInput := integrationstore.UpsertIntegrationRuntimeUnitInput{
			OrgID: testOrgID, IntegrationAppID: app.ID,
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			UnitKey: "install-session", RuntimeKind: "provider_socket",
			DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
			SpecRevision: 1,
		}
		appUnit, err := store.Integrations().UpsertIntegrationRuntimeUnit(ctx, appInput)
		if err != nil {
			t.Fatalf("create app runtime: %v", err)
		}
		installUnit, err := store.Integrations().UpsertIntegrationRuntimeUnit(ctx, installInput)
		if err != nil {
			t.Fatalf("create app installation runtime: %v", err)
		}
		leases := claimIntegrationRuntimes(t, ctx, store, "app-disable-before", 2)
		if !runtimeClaimsContain(leasingIDs(leases), appUnit.ID, installUnit.ID) {
			t.Fatalf("runtime claims before app disable = %+v", leases)
		}

		if _, err := pool.Exec(
			ctx,
			`UPDATE integration_apps SET state = 'disabled' WHERE id = $1`,
			app.ID,
		); err != nil {
			t.Fatalf("disable runtime app: %v", err)
		}
		assertRunningIntegrationRuntime(t, ctx, pool, appUnit.ID)
		assertRunningIntegrationRuntime(t, ctx, pool, installUnit.ID)
		for _, lease := range leases {
			assertIntegrationRuntimeLeaseFenced(t, ctx, store, lease)
		}
		assertNoIntegrationRuntimeClaims(t, ctx, store, "app-disable-after")
		for _, input := range []integrationstore.UpsertIntegrationRuntimeUnitInput{
			appInput,
			installInput,
		} {
			if _, err := store.Integrations().UpsertIntegrationRuntimeUnit(ctx, input); !errors.Is(
				err,
				storeerr.ErrConflict,
			) {
				t.Fatalf("reconcile disabled app runtime error = %v, want conflict", err)
			}
		}
		if _, err := pool.Exec(
			ctx,
			`UPDATE integration_apps SET state = 'active' WHERE id = $1`,
			app.ID,
		); err != nil {
			t.Fatalf("re-enable runtime app: %v", err)
		}
		if _, err := pool.Exec(
			ctx,
			`UPDATE integration_runtime_units
			 SET renewed_at = statement_timestamp() - interval '2 seconds',
			     lease_expires_at = statement_timestamp() - interval '1 second',
			     available_at = statement_timestamp() - interval '1 second'
			 WHERE integration_app_id = $1`,
			app.ID,
		); err != nil {
			t.Fatalf("expire disabled app runtime leases: %v", err)
		}
		restarted := claimIntegrationRuntimes(t, ctx, store, "app-reenable", 2)
		if !runtimeClaimsContain(leasingIDs(restarted), appUnit.ID, installUnit.ID) {
			t.Fatalf("runtime claims after app re-enable = %+v", restarted)
		}
	})
}

func claimOnlyIntegrationRuntime(
	t *testing.T,
	ctx context.Context,
	store *Store,
	owner string,
	wantID integrationstore.ID,
) integrationstore.IntegrationRuntimeUnitRecord {
	t.Helper()
	claims := claimIntegrationRuntimes(t, ctx, store, owner, 1)
	if len(claims) != 1 || claims[0].ID != wantID {
		t.Fatalf("runtime claim = %+v, want %s", claims, wantID)
	}
	return claims[0]
}

func claimIntegrationRuntimes(
	t *testing.T,
	ctx context.Context,
	store *Store,
	owner string,
	limit int,
) []integrationstore.IntegrationRuntimeUnitRecord {
	t.Helper()
	claims, err := store.Integrations().ClaimIntegrationRuntimeUnits(
		ctx,
		integrationstore.ClaimIntegrationRuntimeUnitsInput{
			LeaseOwner: owner, LeaseDuration: time.Minute,
			Capability: testChannelCapability(testChannelProvider), Limit: limit,
		},
	)
	if err != nil {
		t.Fatalf("claim integration runtimes: %v", err)
	}
	return claims
}

func assertNoIntegrationRuntimeClaims(
	t *testing.T,
	ctx context.Context,
	store *Store,
	owner string,
) {
	t.Helper()
	claims := claimIntegrationRuntimes(t, ctx, store, owner, 10)
	if len(claims) != 0 {
		t.Fatalf("unexpected runtime claims = %+v", claims)
	}
}

func assertRunningIntegrationRuntime(
	t *testing.T,
	ctx context.Context,
	pool interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	unitID integrationstore.ID,
) {
	t.Helper()
	var desiredState, status string
	var leasePresent bool
	if err := pool.QueryRow(ctx, `
SELECT desired_state, status, lease_token IS NOT NULL
FROM integration_runtime_units
WHERE id = $1
`, unitID).Scan(&desiredState, &status, &leasePresent); err != nil {
		t.Fatalf("load running runtime: %v", err)
	}
	if desiredState != "running" || status != "running" || !leasePresent {
		t.Fatalf(
			"running runtime desired=%q status=%q lease_present=%t",
			desiredState, status, leasePresent,
		)
	}
}

func assertIntegrationRuntimeLeaseFenced(
	t *testing.T,
	ctx context.Context,
	store *Store,
	lease integrationstore.IntegrationRuntimeUnitRecord,
) {
	t.Helper()
	if _, err := store.Integrations().HeartbeatIntegrationRuntimeUnit(
		ctx,
		integrationstore.HeartbeatIntegrationRuntimeUnitInput{
			ID: lease.ID, LeaseToken: lease.LeaseToken,
			LeaseGeneration: lease.LeaseGeneration, LeaseDuration: time.Minute,
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("heartbeat unavailable runtime error = %v, want conflict", err)
	}
}

func leasingIDs(claims []integrationstore.IntegrationRuntimeUnitRecord) []integrationstore.ID {
	ids := make([]integrationstore.ID, 0, len(claims))
	for _, claim := range claims {
		ids = append(ids, claim.ID)
	}
	return ids
}

func runtimeClaimsContain(ids []integrationstore.ID, expected ...integrationstore.ID) bool {
	if len(ids) != len(expected) {
		return false
	}
	seen := make(map[integrationstore.ID]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}
	for _, id := range expected {
		if !seen[id] {
			return false
		}
	}
	return true
}
