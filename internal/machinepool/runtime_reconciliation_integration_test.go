//go:build integration

package machinepool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestProviderRuntimeReconciliationDeletesConfirmedRunningMismatch(t *testing.T) {
	ctx := context.Background()
	provider := &runtimeReconciliationTestProvider{
		bulkState:   providers.RuntimeStateRunning,
		singleState: providers.RuntimeStateRunning,
	}
	fixture := newRuntimeReconciliationFixture(t, ctx, "confirmed-running", provider)

	discovery, err := fixture.manager.DiscoverProviderRuntimeMismatches(
		ctx,
		RuntimeReconciliationConfig{PageSize: 1},
	)
	if err != nil {
		t.Fatalf("discover provider runtime mismatch: %v", err)
	}
	if discovery.MarkersSet != 1 || discovery.Targets != 1 {
		t.Fatalf("discovery stats = %+v, want one mismatch", discovery)
	}
	fixture.backdateMismatch(t, ctx)

	confirmation, err := fixture.manager.ConfirmProviderRuntimeMismatches(
		ctx,
		RuntimeReconciliationConfig{
			PageSize:          1,
			ConfirmationGrace: time.Millisecond,
			InactivityGrace:   time.Millisecond,
			Concurrency:       1,
		},
	)
	if err != nil {
		t.Fatalf("confirm provider runtime mismatch: %v", err)
	}
	if confirmation.DeletionClaims != 1 || confirmation.Confirmations != 1 {
		t.Fatalf("confirmation stats = %+v, want one deletion", confirmation)
	}
	if deleted := provider.deletedResources(); len(deleted) != 1 || deleted[0] != fixture.resourceID {
		t.Fatalf("deleted resources = %v, want [%s]", deleted, fixture.resourceID)
	}

	var lifecycleState, reasonCode string
	var deletedAt, mismatchSince *time.Time
	if err := fixture.pool.QueryRow(ctx, `
SELECT lifecycle_state, lifecycle_reason_code, deleted_at, provider_runtime_mismatch_since
FROM machines
WHERE org_id = $1 AND id = $2
`, fixture.orgID, fixture.machineID).Scan(
		&lifecycleState,
		&reasonCode,
		&deletedAt,
		&mismatchSince,
	); err != nil {
		t.Fatalf("load reconciled machine: %v", err)
	}
	if lifecycleState != string(executionstore.MachineLifecycleStateDeleted) || deletedAt == nil ||
		reasonCode != "provider_deleted" || mismatchSince == nil {
		t.Fatalf(
			"reconciled machine = state %q reason %q deleted %v mismatch %v",
			lifecycleState,
			reasonCode,
			deletedAt,
			mismatchSince,
		)
	}
	var revokedAt *time.Time
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT revoked_at FROM machine_daemon_tokens WHERE org_id = $1 AND id = $2`,
		fixture.orgID,
		fixture.tokenID,
	).Scan(&revokedAt); err != nil {
		t.Fatalf("load daemon token: %v", err)
	}
	if revokedAt == nil {
		t.Fatal("forced deletion did not revoke daemon authority")
	}
}

func TestProviderRuntimeReconciliationReconnectDefeatsDeletion(t *testing.T) {
	ctx := context.Background()
	provider := &runtimeReconciliationTestProvider{
		bulkState:   providers.RuntimeStateRunning,
		singleState: providers.RuntimeStateRunning,
	}
	fixture := newRuntimeReconciliationFixture(t, ctx, "daemon-reconnect", provider)

	if _, err := fixture.manager.DiscoverProviderRuntimeMismatches(
		ctx,
		RuntimeReconciliationConfig{PageSize: 1},
	); err != nil {
		t.Fatalf("discover provider runtime mismatch: %v", err)
	}
	fixture.backdateMismatch(t, ctx)
	provider.singleHook = func(observationCtx context.Context) error {
		_, err := fixture.store.Execution().RegisterDaemonRuntimeWithReconciliation(
			observationCtx,
			executionstore.RegisterDaemonRuntimeInput{
				OrgID:            fixture.orgID,
				MachineID:        fixture.machineID,
				DaemonTokenID:    fixture.tokenID,
				DaemonInstanceID: uuid.New(),
				DaemonVersion:    "1.0.0",
				LeaseTimeout:     time.Minute,
			},
		)
		return err
	}

	confirmation, err := fixture.manager.ConfirmProviderRuntimeMismatches(
		ctx,
		RuntimeReconciliationConfig{
			PageSize:          1,
			ConfirmationGrace: time.Millisecond,
			InactivityGrace:   time.Millisecond,
			Concurrency:       1,
		},
	)
	if err != nil {
		t.Fatalf("confirm provider runtime mismatch: %v", err)
	}
	if confirmation.DeletionClaims != 0 || confirmation.DeletionClaimRaces != 1 {
		t.Fatalf("confirmation stats = %+v, want reconnect to defeat claim", confirmation)
	}
	if deleted := provider.deletedResources(); len(deleted) != 0 {
		t.Fatalf("provider deleted resources after reconnect: %v", deleted)
	}

	machine, err := fixture.store.Execution().GetMachine(ctx, fixture.orgID, fixture.machineID)
	if err != nil {
		t.Fatalf("get reconnected machine: %v", err)
	}
	if machine.LifecycleState != executionstore.MachineLifecycleStateActive ||
		machine.ConnectionState != executionstore.MachineConnectionStateOnline {
		t.Fatalf("reconnected machine = %+v, want active and online", machine)
	}
	var mismatchSince *time.Time
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT provider_runtime_mismatch_since FROM machines WHERE org_id = $1 AND id = $2`,
		fixture.orgID,
		fixture.machineID,
	).Scan(&mismatchSince); err != nil {
		t.Fatalf("load mismatch marker: %v", err)
	}
	if mismatchSince != nil {
		t.Fatalf("mismatch marker survived daemon reconnect: %v", mismatchSince)
	}
}

func TestProviderRuntimeReconciliationRetainsIncidentMarkerAcrossDeleteRetry(t *testing.T) {
	ctx := context.Background()
	deleteFailure := errors.New("temporary provider delete failure")
	provider := &runtimeReconciliationTestProvider{
		bulkState:   providers.RuntimeStateRunning,
		singleState: providers.RuntimeStateRunning,
		deleteErr:   deleteFailure,
	}
	fixture := newRuntimeReconciliationFixture(t, ctx, "delete-retry", provider)

	if _, err := fixture.manager.DiscoverProviderRuntimeMismatches(
		ctx,
		RuntimeReconciliationConfig{PageSize: 1},
	); err != nil {
		t.Fatalf("discover provider runtime mismatch: %v", err)
	}
	fixture.backdateMismatch(t, ctx)
	if _, err := fixture.manager.ConfirmProviderRuntimeMismatches(
		ctx,
		RuntimeReconciliationConfig{
			PageSize:          1,
			ConfirmationGrace: time.Millisecond,
			InactivityGrace:   time.Millisecond,
			Concurrency:       1,
		},
	); !errors.Is(err, deleteFailure) {
		t.Fatalf("confirmation error = %v, want provider delete failure", err)
	}
	assertRuntimeMismatchMarkerPresent(t, ctx, fixture)

	if _, err := fixture.pool.Exec(ctx, `
UPDATE machines
SET next_reconcile_after = statement_timestamp() - interval '1 second'
WHERE org_id = $1 AND id = $2 AND lifecycle_state = 'delete_failed'
`, fixture.orgID, fixture.machineID); err != nil {
		t.Fatalf("make provider deletion retry due: %v", err)
	}
	provider.setDeleteError(nil)
	count, err := fixture.manager.ReconcileCleanup(ctx, 10)
	if err != nil {
		t.Fatalf("retry provider deletion: %v", err)
	}
	if count != 1 {
		t.Fatalf("cleanup retry count = %d, want 1", count)
	}
	assertRuntimeMismatchMarkerPresent(t, ctx, fixture)

	machine, err := fixture.store.Execution().GetMachine(ctx, fixture.orgID, fixture.machineID)
	if err != nil {
		t.Fatalf("load deleted machine: %v", err)
	}
	if machine.LifecycleState != executionstore.MachineLifecycleStateDeleted {
		t.Fatalf("machine lifecycle = %q, want deleted", machine.LifecycleState)
	}
}

func TestProviderRuntimeReconciliationFreshNonRunningStateFailsOpen(t *testing.T) {
	for _, test := range []struct {
		name        string
		state       providers.RuntimeState
		wantCleared bool
	}{
		{name: "inactive clears", state: providers.RuntimeStateInactive, wantCleared: true},
		{name: "terminated clears", state: providers.RuntimeStateTerminated, wantCleared: true},
		{name: "transitional retains", state: providers.RuntimeStateTransitional},
		{name: "unknown retains", state: providers.RuntimeStateUnknown},
		{name: "invalid retains", state: providers.RuntimeState("future")},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			provider := &runtimeReconciliationTestProvider{
				bulkState:   providers.RuntimeStateRunning,
				singleState: test.state,
			}
			fixture := newRuntimeReconciliationFixture(t, ctx, "fresh-"+test.name, provider)
			if _, err := fixture.manager.DiscoverProviderRuntimeMismatches(
				ctx,
				RuntimeReconciliationConfig{PageSize: 1},
			); err != nil {
				t.Fatalf("discover provider runtime mismatch: %v", err)
			}
			fixture.backdateMismatch(t, ctx)
			stats, err := fixture.manager.ConfirmProviderRuntimeMismatches(
				ctx,
				RuntimeReconciliationConfig{
					PageSize:          1,
					ConfirmationGrace: time.Millisecond,
					InactivityGrace:   time.Millisecond,
					Concurrency:       1,
				},
			)
			if err != nil {
				t.Fatalf("confirm provider runtime mismatch: %v", err)
			}
			if stats.DeletionClaims != 0 || len(provider.deletedResources()) != 0 {
				t.Fatalf("fresh state %q caused deletion: stats=%+v", test.state, stats)
			}
			wantObserved := 0
			if test.state.Valid() {
				wantObserved = 1
			}
			if stats.Confirmations != 1 || stats.Observed != wantObserved {
				t.Fatalf(
					"fresh state %q stats = %+v, want one confirmation and %d observations",
					test.state,
					stats,
					wantObserved,
				)
			}
			mismatchSince := runtimeMismatchSince(t, ctx, fixture)
			if test.wantCleared {
				if stats.MarkersCleared != 1 || mismatchSince != nil {
					t.Fatalf("fresh state %q did not clear marker: stats=%+v marker=%v", test.state, stats, mismatchSince)
				}
			} else if stats.MarkersCleared != 0 || mismatchSince == nil {
				t.Fatalf("fresh state %q did not fail open: stats=%+v marker=%v", test.state, stats, mismatchSince)
			}
		})
	}
}

func TestProviderRuntimeReconciliationProviderErrorUsesScopeCooldown(t *testing.T) {
	ctx := context.Background()
	providerErr := errors.New("provider unavailable")
	provider := &runtimeReconciliationTestProvider{
		bulkState:   providers.RuntimeStateRunning,
		singleState: providers.RuntimeStateRunning,
		singleErr:   providerErr,
	}
	fixture := newRuntimeReconciliationFixture(t, ctx, "provider-cooldown", provider)
	if _, err := fixture.manager.DiscoverProviderRuntimeMismatches(
		ctx,
		RuntimeReconciliationConfig{PageSize: 1},
	); err != nil {
		t.Fatalf("discover provider runtime mismatch: %v", err)
	}
	fixture.backdateMismatch(t, ctx)
	config := RuntimeReconciliationConfig{
		PageSize:          1,
		ConfirmationGrace: time.Millisecond,
		InactivityGrace:   time.Millisecond,
		Concurrency:       1,
	}
	stats, err := fixture.manager.ConfirmProviderRuntimeMismatches(ctx, config)
	if !errors.Is(err, providerErr) || stats.ProviderErrors != 1 || stats.Confirmations != 1 {
		t.Fatalf("provider failure = stats %+v error %v", stats, err)
	}
	stats, err = fixture.manager.ConfirmProviderRuntimeMismatches(ctx, config)
	if err != nil || stats.ScopesSkipped != 1 || stats.DeletionClaims != 0 {
		t.Fatalf("cooldown confirmation = stats %+v error %v", stats, err)
	}
	assertRuntimeMismatchMarkerPresent(t, ctx, fixture)
	if deleted := provider.deletedResources(); len(deleted) != 0 {
		t.Fatalf("provider error caused deletions: %v", deleted)
	}
}

func TestProviderRuntimeDiscoveryPagesFleetAndProcessesScopesConcurrently(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider := &runtimeReconciliationTestProvider{bulkState: providers.RuntimeStateRunning}
	fixture := newRuntimeReconciliationFixture(t, ctx, "concurrent-scopes", provider)
	secondMachineID := addRuntimeReconciliationScopeMachine(
		t,
		ctx,
		fixture,
		"concurrent-scopes-second",
		json.RawMessage(`{"scope":"second"}`),
	)

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	provider.mu.Lock()
	provider.bulkHook = func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	provider.mu.Unlock()
	type discoveryResult struct {
		stats RuntimeReconciliationStats
		err   error
	}
	done := make(chan discoveryResult, 1)
	go func() {
		stats, err := fixture.manager.DiscoverProviderRuntimeMismatches(
			ctx,
			RuntimeReconciliationConfig{PageSize: 1, Concurrency: 2},
		)
		done <- discoveryResult{stats: stats, err: err}
	}()

	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			close(release)
			got := <-done
			t.Fatalf("provider scopes were not observed concurrently: %v (stats %+v)", ctx.Err(), got.stats)
		}
	}
	close(release)
	got := <-done
	if got.err != nil {
		t.Fatalf("discover provider runtime scopes: %v", got.err)
	}
	if got.stats.Scopes != 2 || got.stats.Targets != 2 ||
		got.stats.MarkersSet != 2 || got.stats.Pages < 2 {
		t.Fatalf("multi-scope discovery stats = %+v", got.stats)
	}
	var marked int
	if err := fixture.pool.QueryRow(ctx, `
SELECT count(*)
FROM machines
WHERE id IN ($1, $2) AND provider_runtime_mismatch_since IS NOT NULL
`, fixture.machineID, secondMachineID).Scan(&marked); err != nil {
		t.Fatalf("count marked scope machines: %v", err)
	}
	if marked != 2 {
		t.Fatalf("marked scope machines = %d, want 2", marked)
	}
}

func TestProviderRuntimeConfirmationBoundsConcurrentTargetsGlobally(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider := &runtimeReconciliationTestProvider{
		bulkState:   providers.RuntimeStateRunning,
		singleState: providers.RuntimeStateUnknown,
	}
	fixture := newRuntimeReconciliationFixture(t, ctx, "confirmation-concurrency", provider)
	for index := range 7 {
		seedInactiveRuntimeMachine(
			t,
			ctx,
			fixture.pool,
			fixture.machinePool,
			fmt.Sprintf("confirmation-concurrency-%d", index),
			time.Now().UTC(),
		)
	}
	addRuntimeReconciliationScopeMachine(
		t,
		ctx,
		fixture,
		"confirmation-concurrency-second-scope",
		json.RawMessage(`{"scope":"second"}`),
	)
	if _, err := fixture.manager.DiscoverProviderRuntimeMismatches(
		ctx,
		RuntimeReconciliationConfig{PageSize: 2, Concurrency: 4},
	); err != nil {
		t.Fatalf("discover provider runtime mismatches: %v", err)
	}
	backdateAllRuntimeMismatches(t, ctx, fixture)

	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var current atomic.Int32
	var maximum atomic.Int32
	provider.singleTargetHook = func(ctx context.Context, _ providers.RuntimeTarget) error {
		inFlight := current.Add(1)
		defer current.Add(-1)
		for {
			observed := maximum.Load()
			if inFlight <= observed || maximum.CompareAndSwap(observed, inFlight) {
				break
			}
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	type confirmationResult struct {
		stats RuntimeReconciliationStats
		err   error
	}
	done := make(chan confirmationResult, 1)
	go func() {
		stats, err := fixture.manager.ConfirmProviderRuntimeMismatches(
			ctx,
			RuntimeReconciliationConfig{
				PageSize:          2,
				ConfirmationGrace: time.Millisecond,
				InactivityGrace:   time.Millisecond,
				Concurrency:       4,
			},
		)
		done <- confirmationResult{stats: stats, err: err}
	}()
	for range 4 {
		select {
		case <-entered:
		case <-ctx.Done():
			close(release)
			result := <-done
			t.Fatalf("exact provider reads did not reach configured concurrency: %v (%+v)", ctx.Err(), result)
		}
	}
	close(release)
	result := <-done
	if result.err != nil {
		t.Fatalf("confirm provider runtime mismatches: %v", result.err)
	}
	if result.stats.Confirmations != 9 || maximum.Load() != 4 {
		t.Fatalf("confirmation stats = %+v, max concurrency = %d, want 9 and 4", result.stats, maximum.Load())
	}
}

func TestProviderRuntimeConfirmationRotatesPastFailingTarget(t *testing.T) {
	ctx := context.Background()
	providerErr := errors.New("target observation failed")
	provider := &runtimeReconciliationTestProvider{
		bulkState:   providers.RuntimeStateRunning,
		singleState: providers.RuntimeStateUnknown,
	}
	fixture := newRuntimeReconciliationFixture(t, ctx, "confirmation-rotation", provider)
	for index := range 8 {
		seedInactiveRuntimeMachine(
			t,
			ctx,
			fixture.pool,
			fixture.machinePool,
			fmt.Sprintf("confirmation-rotation-%d", index),
			time.Now().UTC(),
		)
	}
	if _, err := fixture.manager.DiscoverProviderRuntimeMismatches(
		ctx,
		RuntimeReconciliationConfig{PageSize: 2, Concurrency: 4},
	); err != nil {
		t.Fatalf("discover provider runtime mismatches: %v", err)
	}
	backdateAllRuntimeMismatches(t, ctx, fixture)

	var seenMu sync.Mutex
	seen := make(map[storage.ID]struct{})
	var failed atomic.Bool
	provider.singleTargetHook = func(_ context.Context, target providers.RuntimeTarget) error {
		seenMu.Lock()
		seen[target.MachineID] = struct{}{}
		seenMu.Unlock()
		if failed.CompareAndSwap(false, true) {
			return providerErr
		}
		return nil
	}
	config := RuntimeReconciliationConfig{
		PageSize:          2,
		ConfirmationGrace: time.Millisecond,
		InactivityGrace:   time.Millisecond,
		Concurrency:       4,
	}
	first, err := fixture.manager.ConfirmProviderRuntimeMismatches(ctx, config)
	if !errors.Is(err, providerErr) || first.Confirmations != 4 {
		t.Fatalf("first confirmation = stats %+v error %v, want one failed four-target stripe", first, err)
	}
	seenMu.Lock()
	firstSeen := maps.Clone(seen)
	seen = make(map[storage.ID]struct{})
	seenMu.Unlock()

	fixture.manager.runtimeReconciliationState.mu.Lock()
	clear(fixture.manager.runtimeReconciliationState.cooldowns)
	fixture.manager.runtimeReconciliationState.mu.Unlock()
	failed.Store(false)
	second, err := fixture.manager.ConfirmProviderRuntimeMismatches(ctx, config)
	if !errors.Is(err, providerErr) || second.Confirmations != 4 {
		t.Fatalf("second confirmation = stats %+v error %v, want one rotated four-target stripe", second, err)
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	advanced := false
	for machineID := range seen {
		if _, existed := firstSeen[machineID]; !existed {
			advanced = true
			break
		}
	}
	if !advanced {
		t.Fatalf("confirmation retry did not rotate beyond first stripe: first=%v second=%v", firstSeen, seen)
	}
}

func assertRuntimeMismatchMarkerPresent(
	t *testing.T,
	ctx context.Context,
	fixture runtimeReconciliationFixture,
) {
	t.Helper()
	var mismatchSince *time.Time
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT provider_runtime_mismatch_since FROM machines WHERE org_id = $1 AND id = $2`,
		fixture.orgID,
		fixture.machineID,
	).Scan(&mismatchSince); err != nil {
		t.Fatalf("load runtime mismatch marker: %v", err)
	}
	if mismatchSince == nil {
		t.Fatal("runtime mismatch incident marker was cleared")
	}
}

func runtimeMismatchSince(
	t *testing.T,
	ctx context.Context,
	fixture runtimeReconciliationFixture,
) *time.Time {
	t.Helper()
	var mismatchSince *time.Time
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT provider_runtime_mismatch_since FROM machines WHERE org_id = $1 AND id = $2`,
		fixture.orgID,
		fixture.machineID,
	).Scan(&mismatchSince); err != nil {
		t.Fatalf("load runtime mismatch marker: %v", err)
	}
	return mismatchSince
}

type runtimeReconciliationFixture struct {
	pool        *pgxpool.Pool
	store       *storage.Store
	manager     Manager
	machinePool executionstore.MachinePoolRecord
	orgID       storage.ID
	machineID   storage.ID
	tokenID     storage.ID
	resourceID  string
}

func newRuntimeReconciliationFixture(
	t *testing.T,
	ctx context.Context,
	seed string,
	provider *runtimeReconciliationTestProvider,
) runtimeReconciliationFixture {
	t.Helper()
	pool := openManagerIntegrationDB(t, ctx)
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(managerIntegrationKeyWrapper(t)),
		storage.WithMachinePoolProviders(machinePoolProviderTestResolvers{}),
	)
	now := time.Now().UTC()
	orgID := seedManagerOrg(t, ctx, pool, "runtime-reconciliation-"+seed, now)
	providerAuthSecretID := createProviderAuthSecretForManagerTest(
		t,
		ctx,
		pool,
		store,
		orgID,
		"runtime-reconciliation-"+seed,
		"provider-token",
	)
	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		machinePoolInputWithDefaultMachineForManagerTest(
			t,
			executionstore.CreateMachinePoolInput{
				OrgID:                orgID,
				Name:                 "Runtime Reconciliation " + seed,
				Provider:             "capture",
				ProviderConfig:       json.RawMessage(`{"scope":"test"}`),
				ProviderAuthSecretID: providerAuthSecretID,
				MaxTotalMachines:     10,
				MaxTotalCPU:          intPtrForManagerTest(10),
				MaxTotalMemoryMB:     intPtrForManagerTest(10240),
				MaxMachineCPU:        intPtrForManagerTest(1),
				MaxMachineMemoryMB:   intPtrForManagerTest(1024),
			},
			1,
			1024,
			nil,
			nil,
			map[string]any{},
		),
	)
	if err != nil {
		t.Fatalf("create runtime reconciliation pool: %v", err)
	}
	machineID, tokenID, resourceID := seedInactiveRuntimeMachine(
		t,
		ctx,
		pool,
		machinePool,
		seed,
		now,
	)

	definition := &runtimeReconciliationTestDefinition{provider: provider}
	manager := Manager{
		Execution:                  store.Execution(),
		Identity:                   store.Identity(),
		Catalog:                    testProviderCatalog(definition),
		PublicURL:                  "https://app.omnara.test",
		runtimeReconciliationState: newRuntimeReconciliationState(),
	}
	return runtimeReconciliationFixture{
		pool:        pool,
		store:       store,
		manager:     manager,
		machinePool: machinePool,
		orgID:       orgID,
		machineID:   machineID,
		tokenID:     tokenID,
		resourceID:  resourceID,
	}
}

func addRuntimeReconciliationScopeMachine(
	t *testing.T,
	ctx context.Context,
	fixture runtimeReconciliationFixture,
	seed string,
	providerConfig json.RawMessage,
) storage.ID {
	t.Helper()
	now := time.Now().UTC()
	providerAuthSecretID := createProviderAuthSecretForManagerTest(
		t,
		ctx,
		fixture.pool,
		fixture.store,
		fixture.orgID,
		seed,
		"provider-token",
	)
	machinePool, err := fixture.store.Execution().CreateMachinePool(
		ctx,
		machinePoolInputWithDefaultMachineForManagerTest(
			t,
			executionstore.CreateMachinePoolInput{
				OrgID:                fixture.orgID,
				Name:                 "Runtime Reconciliation " + seed,
				Provider:             "capture",
				ProviderConfig:       providerConfig,
				ProviderAuthSecretID: providerAuthSecretID,
				MaxTotalMachines:     10,
				MaxTotalCPU:          intPtrForManagerTest(10),
				MaxTotalMemoryMB:     intPtrForManagerTest(10240),
				MaxMachineCPU:        intPtrForManagerTest(1),
				MaxMachineMemoryMB:   intPtrForManagerTest(1024),
			},
			1,
			1024,
			nil,
			nil,
			map[string]any{},
		),
	)
	if err != nil {
		t.Fatalf("create second runtime reconciliation pool: %v", err)
	}
	machineID, _, _ := seedInactiveRuntimeMachine(
		t,
		ctx,
		fixture.pool,
		machinePool,
		seed,
		now,
	)
	return machineID
}

func seedInactiveRuntimeMachine(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	machinePool executionstore.MachinePoolRecord,
	seed string,
	now time.Time,
) (storage.ID, storage.ID, string) {
	t.Helper()
	resourceID := "runtime-resource-" + seed
	machineID := insertPoolMachineForManagerTest(
		t,
		ctx,
		pool,
		machinePool,
		"active",
		resourceID,
		now,
	)
	tokenID := uuid.New()
	runtimeID := uuid.New()
	inactiveSince := now.Add(-10 * time.Minute)
	if _, err := pool.Exec(ctx, `
INSERT INTO machine_daemon_tokens(
    id, org_id, machine_id, name, token_hash, created_at
) VALUES ($1, $2, $3, 'runtime-reconciliation', $4, $5)
`, tokenID, machinePool.OrgID, machineID, "runtime-reconciliation-token-"+uuid.NewString(), inactiveSince.Add(-time.Minute)); err != nil {
		t.Fatalf("seed runtime reconciliation daemon token: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO daemon_runtimes(
    id, org_id, machine_id, daemon_token_id, daemon_instance_id,
    daemon_version, state, state_reason_code, created_at, last_seen_at,
    lease_expires_at, ended_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, 'test-daemon', 'ended', 'daemon_sleep',
    $6, $6, $7, $7, $7
)
`, runtimeID, machinePool.OrgID, machineID, tokenID, uuid.New(), inactiveSince.Add(-time.Minute), inactiveSince); err != nil {
		t.Fatalf("seed runtime reconciliation daemon runtime: %v", err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE machines
SET current_daemon_runtime_id = $1,
    asleep_since = $2,
    last_observed_at = $2,
    updated_at = $2
WHERE org_id = $3 AND id = $4
`, runtimeID, inactiveSince, machinePool.OrgID, machineID); err != nil {
		t.Fatalf("attach inactive runtime: %v", err)
	}
	return machineID, tokenID, resourceID
}

func (f runtimeReconciliationFixture) backdateMismatch(t *testing.T, ctx context.Context) {
	t.Helper()
	tag, err := f.pool.Exec(ctx, `
UPDATE machines
SET provider_runtime_mismatch_since = statement_timestamp() - interval '5 minutes'
WHERE org_id = $1 AND id = $2 AND provider_runtime_mismatch_since IS NOT NULL
`, f.orgID, f.machineID)
	if err != nil {
		t.Fatalf("backdate mismatch marker: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("backdate mismatch affected %d rows, want 1", tag.RowsAffected())
	}
}

func backdateAllRuntimeMismatches(
	t *testing.T,
	ctx context.Context,
	fixture runtimeReconciliationFixture,
) {
	t.Helper()
	tag, err := fixture.pool.Exec(ctx, `
UPDATE machines
SET provider_runtime_mismatch_since = statement_timestamp() - interval '5 minutes'
WHERE org_id = $1 AND provider_runtime_mismatch_since IS NOT NULL
`, fixture.orgID)
	if err != nil {
		t.Fatalf("backdate provider runtime mismatches: %v", err)
	}
	if tag.RowsAffected() == 0 {
		t.Fatal("backdate provider runtime mismatches affected no rows")
	}
}

type runtimeReconciliationTestDefinition struct {
	provider *runtimeReconciliationTestProvider
}

func (*runtimeReconciliationTestDefinition) SupportsRuntimeObservation() bool { return true }

func (d *runtimeReconciliationTestDefinition) NewProvider(
	json.RawMessage,
	providers.RuntimeConfig,
) (providers.Provider, error) {
	return d.provider, nil
}

func (*runtimeReconciliationTestDefinition) ResolveMachineProviderOptions(
	defaultOptions,
	projectOptions,
	agentOptions map[string]json.RawMessage,
) map[string]json.RawMessage {
	return providers.MergeOptions(defaultOptions, projectOptions, agentOptions)
}

func (*runtimeReconciliationTestDefinition) ValidatePool(
	executionstore.MachinePoolProviderPolicy,
) error {
	return nil
}

func (*runtimeReconciliationTestDefinition) ValidateMachineProvisioning(
	executionstore.MachinePoolProviderPolicy,
	executionstore.MachineProvisioningConfig,
) error {
	return nil
}

func (*runtimeReconciliationTestDefinition) BuildMachineProvisioningIntent(
	_ executionstore.MachinePoolProviderPolicy,
	provisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineProvisioningConfig, error) {
	return provisioning, nil
}

type runtimeReconciliationTestProvider struct {
	mu               sync.Mutex
	bulkState        providers.RuntimeState
	singleState      providers.RuntimeState
	bulkHook         func(context.Context) error
	singleHook       func(context.Context) error
	singleTargetHook func(context.Context, providers.RuntimeTarget) error
	bulkErr          error
	singleErr        error
	deleteErr        error
	deleted          []string
}

func (*runtimeReconciliationTestProvider) ProvisioningTimeout() time.Duration {
	return time.Second
}

func (*runtimeReconciliationTestProvider) PrepareProvisioning(
	context.Context,
	executionstore.MachineProvisioningConfig,
) (executionstore.MachineResourceFacts, error) {
	return executionstore.MachineResourceFacts{}, errors.New("not implemented by runtime test provider")
}

func (*runtimeReconciliationTestProvider) ProvisionMachine(
	context.Context,
	storage.ID,
	storage.ID,
	executionstore.MachineProvisioningConfig,
	string,
	map[string]string,
) (providers.ProvisionMachineResult, error) {
	return providers.ProvisionMachineResult{}, errors.New("not implemented by runtime test provider")
}

func (*runtimeReconciliationTestProvider) InspectMachine(
	context.Context,
	storage.ID,
	storage.ID,
	executionstore.MachineProvisioningConfig,
	string,
) (string, bool, error) {
	return "", false, errors.New("not implemented by runtime test provider")
}

func (p *runtimeReconciliationTestProvider) DeleteMachine(
	_ context.Context,
	_, _ storage.ID,
	_ executionstore.MachineProvisioningConfig,
	providerResourceID string,
) error {
	p.mu.Lock()
	p.deleted = append(p.deleted, providerResourceID)
	err := p.deleteErr
	p.mu.Unlock()
	return err
}

func (p *runtimeReconciliationTestProvider) ObserveRuntimeStates(
	ctx context.Context,
	targets []providers.RuntimeTarget,
) ([]providers.RuntimeObservation, error) {
	p.mu.Lock()
	hook, state, err := p.bulkHook, p.bulkState, p.bulkErr
	p.mu.Unlock()
	if hook != nil {
		if hookErr := hook(ctx); hookErr != nil {
			return nil, hookErr
		}
	}
	if err != nil {
		return nil, err
	}
	observations := make([]providers.RuntimeObservation, 0, len(targets))
	for _, target := range targets {
		observations = append(observations, providers.RuntimeObservation{
			MachineID:          target.MachineID,
			ProviderResourceID: target.ProviderResourceID,
			State:              state,
		})
	}
	return observations, nil
}

func (p *runtimeReconciliationTestProvider) ObserveRuntimeState(
	ctx context.Context,
	target providers.RuntimeTarget,
) (providers.RuntimeObservation, error) {
	p.mu.Lock()
	hook, targetHook, state, observationErr := p.singleHook, p.singleTargetHook, p.singleState, p.singleErr
	p.singleHook = nil
	p.mu.Unlock()
	if hook != nil {
		if err := hook(ctx); err != nil {
			return providers.RuntimeObservation{}, err
		}
	}
	if targetHook != nil {
		if err := targetHook(ctx, target); err != nil {
			return providers.RuntimeObservation{}, err
		}
	}
	if observationErr != nil {
		return providers.RuntimeObservation{}, observationErr
	}
	return providers.RuntimeObservation{
		MachineID:          target.MachineID,
		ProviderResourceID: target.ProviderResourceID,
		State:              state,
	}, nil
}

func (p *runtimeReconciliationTestProvider) deletedResources() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.deleted...)
}

func (p *runtimeReconciliationTestProvider) setDeleteError(err error) {
	p.mu.Lock()
	p.deleteErr = err
	p.mu.Unlock()
}

var _ providers.RuntimeStateObserver = (*runtimeReconciliationTestProvider)(nil)
