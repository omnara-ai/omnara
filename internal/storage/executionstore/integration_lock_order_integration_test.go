//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestProjectDeletionWaitsForInstallBeforeApp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin, agent, app, install := createChannelLifecycleFixture(t, ctx, store, "project-lock-order")

	appHolder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin app holder: %v", err)
	}
	t.Cleanup(func() { _ = appHolder.Rollback(ctx) })
	var appHolderPID int32
	if err := appHolder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&appHolderPID); err != nil {
		t.Fatalf("load app holder backend: %v", err)
	}
	if _, err := appHolder.Exec(
		ctx,
		`SELECT id FROM integration_apps WHERE id = $1 FOR UPDATE`,
		app.ID,
	); err != nil {
		t.Fatalf("lock integration app: %v", err)
	}

	type targetResult struct {
		target integrationstore.IntegrationTargetRecord
		err    error
	}
	targetDone := make(chan targetResult, 1)
	go func() {
		target, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
			context.Background(),
			integrationstore.CreateIntegrationTargetInput{
				ProviderRef:     "project-lock-order-thread",
				ProviderRefKind: "thread", DisplayName: "Project lock order",
				ProviderMetadata: json.RawMessage(`{}`), ProjectID: testProjectID,
				AgentID: agent.ID, IntegrationInstallID: install.ID,
			},
		)
		targetDone <- targetResult{target: target, err: err}
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: LockIntegrationTargetCreateAuthority ",
		appHolderPID,
	)
	var targetPID int32
	if err := pool.QueryRow(ctx, `
SELECT pid
FROM pg_stat_activity
WHERE datname = current_database()
  AND pid <> pg_backend_pid()
	  AND query LIKE '%-- name: LockIntegrationTargetCreateAuthority %'
  AND wait_event_type = 'Lock'
ORDER BY query_start DESC
LIMIT 1
`).Scan(&targetPID); err != nil {
		t.Fatalf("load target creation backend: %v", err)
	}

	deleteDone := make(chan error, 1)
	go func() {
		_, err := store.Organizations().DeleteProject(
			context.Background(),
			testOrgID,
			testProjectID,
			identitystore.NewUserPrincipal(admin.ID),
		)
		deleteDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: LockAgentInProject ",
		targetPID,
	)
	if err := appHolder.Rollback(ctx); err != nil {
		t.Fatalf("release integration app: %v", err)
	}

	var targetID ID
	select {
	case result := <-targetDone:
		if result.err != nil {
			t.Fatalf("finish target creation: %v", result.err)
		}
		targetID = result.target.ID
	case <-time.After(5 * time.Second):
		t.Fatal("target creation did not finish after app unlock")
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("finish project deletion: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("project deletion did not finish after target creation")
	}
	assertChannelLifecycleRowsDeleted(t, ctx, pool, app.ID, install.ID)
	var targetDeleted bool
	if err := pool.QueryRow(
		ctx,
		`SELECT deleted_at IS NOT NULL FROM integration_targets WHERE id = $1`,
		targetID,
	).Scan(&targetDeleted); err != nil {
		t.Fatalf("load target after project deletion: %v", err)
	}
	if !targetDeleted {
		t.Fatal("project deletion did not sweep the raced target")
	}
}

func TestChannelDeletesEnterProjectLifecycleBeforeRowMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, _, _, install := createChannelLifecycleFixture(t, ctx, store, "channel-delete-lifecycle")
	route, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "channel-delete-lifecycle", HandlerKey: "channel_delete_lifecycle",
			HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create route: %v", err)
	}

	assertWaitsForProjectLifecycle := func(label string, deleteResource func(context.Context) error) {
		t.Helper()
		blocker, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin %s lifecycle blocker: %v", label, err)
		}
		t.Cleanup(func() { _ = blocker.Rollback(context.Background()) })
		var blockerPID int32
		if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
			t.Fatalf("load %s lifecycle blocker backend: %v", label, err)
		}
		if _, err := blocker.Exec(
			ctx,
			`SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`,
			testProjectID.String(),
		); err != nil {
			t.Fatalf("lock %s project lifecycle: %v", label, err)
		}

		deleteDone := make(chan error, 1)
		go func() { deleteDone <- deleteResource(context.Background()) }()
		integrationdb.WaitForLockWaitBlockedBy(
			t,
			ctx,
			pool,
			"-- name: LockProjectLifecycleShared ",
			blockerPID,
		)
		if err := blocker.Rollback(ctx); err != nil {
			t.Fatalf("release %s project lifecycle: %v", label, err)
		}
		select {
		case err := <-deleteDone:
			if err != nil {
				t.Fatalf("finish %s deletion: %v", label, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s deletion did not finish after project lifecycle release", label)
		}
	}

	assertWaitsForProjectLifecycle("route", func(deleteCtx context.Context) error {
		return store.Integrations().DeleteIntegrationRoute(
			deleteCtx,
			testProjectID,
			install.ID,
			route.ID,
		)
	})
	assertWaitsForProjectLifecycle("installation", func(deleteCtx context.Context) error {
		return store.Integrations().DeleteIntegrationInstall(deleteCtx, testProjectID, install.ID)
	})
}

func TestRouteDeletionWaitsForBindingCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, "route-binding-lock-order")
	route, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "route-binding-lock-order", HandlerKey: "route_binding_lock_order",
			HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	target, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, ProviderRef: "route-binding-lock-order-channel",
			ProviderRefKind: "channel",
		},
	)
	if err != nil {
		t.Fatalf("create target: %v", err)
	}

	routeHolder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin route holder: %v", err)
	}
	t.Cleanup(func() { _ = routeHolder.Rollback(ctx) })
	var routeHolderPID int32
	if err := routeHolder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&routeHolderPID); err != nil {
		t.Fatalf("load route holder backend: %v", err)
	}
	if _, err := routeHolder.Exec(
		ctx,
		`SELECT id FROM integration_routes WHERE id = $1 FOR UPDATE`,
		route.ID,
	); err != nil {
		t.Fatalf("lock integration route: %v", err)
	}

	type bindingResult struct {
		binding integrationstore.IntegrationTargetBindingRecord
		err     error
	}
	bindingDone := make(chan bindingResult, 1)
	go func() {
		binding, err := store.Integrations().CreateIntegrationTargetBinding(
			context.Background(),
			integrationstore.CreateIntegrationTargetBindingInput{
				ProjectID: testProjectID, AgentID: agent.ID,
				IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
				IntegrationRouteID: route.ID, ReceiveAllowed: true, SendAllowed: true,
				Source: "test",
			},
		)
		bindingDone <- bindingResult{binding: binding, err: err}
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: LockActiveIntegrationRouteForBinding ",
		routeHolderPID,
	)
	var bindingPID int32
	if err := pool.QueryRow(ctx, `
SELECT pid
FROM pg_stat_activity
WHERE datname = current_database()
  AND query LIKE '%-- name: LockActiveIntegrationRouteForBinding %'
  AND wait_event_type = 'Lock'
  AND $1::integer = ANY(pg_blocking_pids(pid))
ORDER BY query_start DESC
LIMIT 1
`, routeHolderPID).Scan(&bindingPID); err != nil {
		t.Fatalf("load binding creation backend: %v", err)
	}

	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- store.Integrations().DeleteIntegrationRoute(
			context.Background(),
			testProjectID,
			install.ID,
			route.ID,
		)
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: LockIntegrationInstallForRouteMutation ",
		bindingPID,
	)
	if err := routeHolder.Rollback(ctx); err != nil {
		t.Fatalf("release integration route: %v", err)
	}

	var created integrationstore.IntegrationTargetBindingRecord
	select {
	case result := <-bindingDone:
		if result.err != nil {
			t.Fatalf("finish binding creation: %v", result.err)
		}
		created = result.binding
	case <-time.After(5 * time.Second):
		t.Fatal("binding creation did not finish after route unlock")
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("finish route deletion: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("route deletion did not finish after binding creation")
	}
	var routeDeletedAt, bindingRevokedAt time.Time
	if err := pool.QueryRow(
		ctx,
		`SELECT route.deleted_at, binding.revoked_at
FROM integration_target_bindings binding
JOIN integration_routes route ON route.id = binding.integration_route_id
WHERE binding.project_id = $1 AND binding.id = $2`,
		testProjectID,
		created.ID,
	).Scan(&routeDeletedAt, &bindingRevokedAt); err != nil {
		t.Fatalf("load raced route and binding history: %v", err)
	}
	if routeDeletedAt.Before(created.CreatedAt) || bindingRevokedAt.Before(created.CreatedAt) {
		t.Fatalf(
			"route deletion history predates raced binding: created=%s route_deleted=%s binding_revoked=%s",
			created.CreatedAt,
			routeDeletedAt,
			bindingRevokedAt,
		)
	}
}

func TestRouteDeletionWaitsForReceiveAuthorization(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, "route-receive-lock-order")
	route, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "route-receive-lock-order", HandlerKey: "route_receive_lock_order",
			HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	target, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, ProviderRef: "route-receive-lock-order-channel",
			ProviderRefKind: "channel",
		},
	)
	if err != nil {
		t.Fatalf("create target: %v", err)
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
		t.Fatalf("create binding: %v", err)
	}

	routeHolder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin route holder: %v", err)
	}
	t.Cleanup(func() { _ = routeHolder.Rollback(ctx) })
	var routeHolderPID int32
	if err := routeHolder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&routeHolderPID); err != nil {
		t.Fatalf("load route holder backend: %v", err)
	}
	if _, err := routeHolder.Exec(
		ctx,
		`SELECT id FROM integration_routes WHERE id = $1 FOR UPDATE`,
		route.ID,
	); err != nil {
		t.Fatalf("lock integration route: %v", err)
	}

	type receiveResult struct {
		binding integrationstore.IntegrationTargetBindingRecord
		err     error
	}
	receiveDone := make(chan receiveResult, 1)
	go func() {
		tx, err := pool.Begin(context.Background())
		if err != nil {
			receiveDone <- receiveResult{err: err}
			return
		}
		record, err := store.Integrations().GetActiveReceiveBindingTx(
			context.Background(),
			tx,
			testProjectID,
			agent.ID,
			install.ID,
			target.ID,
			binding.ID,
		)
		if err == nil {
			err = tx.Commit(context.Background())
		} else {
			_ = tx.Rollback(context.Background())
		}
		receiveDone <- receiveResult{binding: record, err: err}
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: GetActiveReceiveBinding ",
		routeHolderPID,
	)
	var receivePID int32
	if err := pool.QueryRow(ctx, `
SELECT pid
FROM pg_stat_activity
WHERE datname = current_database()
  AND query LIKE '%-- name: GetActiveReceiveBinding %'
  AND wait_event_type = 'Lock'
  AND $1::integer = ANY(pg_blocking_pids(pid))
ORDER BY query_start DESC
LIMIT 1
`, routeHolderPID).Scan(&receivePID); err != nil {
		t.Fatalf("load receive authorization backend: %v", err)
	}

	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- store.Integrations().DeleteIntegrationRoute(
			context.Background(),
			testProjectID,
			install.ID,
			route.ID,
		)
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: LockIntegrationInstallForRouteMutation ",
		receivePID,
	)
	if err := routeHolder.Rollback(ctx); err != nil {
		t.Fatalf("release integration route: %v", err)
	}

	select {
	case result := <-receiveDone:
		if result.err != nil || result.binding.ID != binding.ID {
			t.Fatalf("finish receive authorization = %+v, %v", result.binding, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("receive authorization did not finish after route unlock")
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("finish route deletion: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("route deletion did not finish after receive authorization")
	}
	var revoked, routeDeleted bool
	if err := pool.QueryRow(ctx, `
SELECT binding.revoked_at IS NOT NULL, route.deleted_at IS NOT NULL
FROM integration_target_bindings binding
JOIN integration_routes route ON route.id = binding.integration_route_id
WHERE binding.project_id = $1 AND binding.id = $2
`, testProjectID, binding.ID).Scan(&revoked, &routeDeleted); err != nil {
		t.Fatalf("load receive/delete lifecycle: %v", err)
	}
	if !revoked || !routeDeleted {
		t.Fatalf("receive/delete lifecycle revoked=%t route_deleted=%t", revoked, routeDeleted)
	}
}

func TestConcurrentReceiveTargetRefreshesDoNotDeadlock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin, firstAgent, _, install := createChannelLifecycleFixture(
		t,
		ctx,
		store,
		"concurrent-target-refresh",
	)
	secondProfile := createIntegrationTestProfile(t, ctx, store, "concurrent-target-refresh-second")
	secondAgent := createIntegrationBoundAgent(
		t,
		ctx,
		store,
		secondProfile,
		admin.ID,
		"concurrent-target-refresh-second",
	)
	route, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "concurrent-target-refresh", HandlerKey: "concurrent_target_refresh",
			HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create concurrent-refresh route: %v", err)
	}
	target, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: firstAgent.ID,
			IntegrationInstallID: install.ID, ProviderRef: "concurrent-target-refresh-channel",
			ProviderRefKind: "channel",
		},
	)
	if err != nil {
		t.Fatalf("create concurrent-refresh target: %v", err)
	}
	bindings := make([]integrationstore.IntegrationTargetBindingRecord, 0, 2)
	for _, agent := range []executionstore.AgentRecord{firstAgent, secondAgent} {
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
			t.Fatalf("create concurrent-refresh binding: %v", err)
		}
		bindings = append(bindings, binding)
	}

	txs := make([]pgx.Tx, 0, 2)
	for i, agent := range []executionstore.AgentRecord{firstAgent, secondAgent} {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin concurrent-refresh transaction: %v", err)
		}
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		if _, err := store.Integrations().GetActiveReceiveBindingTx(
			ctx,
			tx,
			testProjectID,
			agent.ID,
			install.ID,
			target.ID,
			bindings[i].ID,
		); err != nil {
			t.Fatalf("authorize concurrent target refresh: %v", err)
		}
		txs = append(txs, tx)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for i, agent := range []executionstore.AgentRecord{firstAgent, secondAgent} {
		tx := txs[i]
		go func(agentID integrationstore.ID, metadata string) {
			<-start
			_, refreshErr := store.Integrations().GetOrCreateIntegrationTargetForBindingTx(
				context.Background(),
				tx,
				integrationstore.CreateIntegrationTargetInput{
					ProjectID: testProjectID, AgentID: agentID,
					IntegrationInstallID: install.ID,
					ProviderRef:          target.ProviderRef,
					ProviderRefKind:      target.ProviderRefKind,
					ProviderMetadata:     json.RawMessage(metadata),
				},
			)
			if refreshErr == nil {
				refreshErr = tx.Commit(context.Background())
			} else {
				_ = tx.Rollback(context.Background())
			}
			results <- refreshErr
		}(agent.ID, fmt.Sprintf(`{"refresher":%d}`, i+1))
	}
	close(start)
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("concurrent receive target refresh: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent receive target refresh deadlocked")
		}
	}
}

func TestSecretRotationLocksInstallBeforeApp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin, agent, credential, app, install := createCredentialBackedChannelLifecycleFixture(
		t,
		ctx,
		store,
		"secret-rotation-lock-order",
	)

	appHolder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin app holder: %v", err)
	}
	t.Cleanup(func() { _ = appHolder.Rollback(ctx) })
	var appHolderPID int32
	if err := appHolder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&appHolderPID); err != nil {
		t.Fatalf("load app holder backend: %v", err)
	}
	if _, err := appHolder.Exec(
		ctx,
		`SELECT id FROM integration_apps WHERE id = $1 FOR UPDATE`,
		app.ID,
	); err != nil {
		t.Fatalf("lock integration app: %v", err)
	}

	targetTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin target authority holder: %v", err)
	}
	t.Cleanup(func() { _ = targetTx.Rollback(ctx) })
	var targetPID int32
	if err := targetTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&targetPID); err != nil {
		t.Fatalf("load target authority backend: %v", err)
	}
	targetDone := make(chan error, 1)
	go func() {
		qtx := dbsqlc.New(targetTx)
		_, err := qtx.LockIntegrationTargetCreateAuthority(
			context.Background(),
			dbsqlc.LockIntegrationTargetCreateAuthorityParams{
				ProjectID: testProjectID, AgentID: agent.ID,
				IntegrationInstallID: install.ID,
			},
		)
		if err == nil {
			_, err = qtx.InsertIntegrationTarget(
				context.Background(),
				dbsqlc.InsertIntegrationTargetParams{
					TargetRef:   "secret-rotation-lock-order-target",
					ProviderRef: "secret-rotation-lock-order-thread", ProviderRefKind: "thread",
					DisplayName: "Secret rotation lock order", ProviderMetadata: json.RawMessage(`{}`),
					ProjectID: testProjectID, AgentID: nil,
					IntegrationInstallID: install.ID,
				},
			)
		}
		if err == nil {
			err = targetTx.Commit(context.Background())
		}
		targetDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: LockIntegrationTargetCreateAuthority ",
		appHolderPID,
	)

	rotationDone := make(chan error, 1)
	go func() {
		_, _, err := store.Secrets().CreateSecretVersion(
			context.Background(),
			secretstore.CreateSecretVersionInput{
				OrgID: testOrgID, SecretID: credential.ID,
				Material: secrets.GenericMaterial{Value: "second-value"},
				Actor:    identitystore.NewUserPrincipal(admin.ID),
			},
		)
		rotationDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: SetSecretCurrentVersion ",
		targetPID,
	)
	if err := appHolder.Rollback(ctx); err != nil {
		t.Fatalf("release integration app: %v", err)
	}
	select {
	case err := <-targetDone:
		if err != nil {
			t.Fatalf("finish target authority transaction: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("target authority transaction did not finish")
	}
	select {
	case err := <-rotationDone:
		if err != nil {
			t.Fatalf("finish shared credential rotation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shared credential rotation did not finish")
	}
}

func TestOrganizationSecretRotationEntersOrganizationLifecycleBeforeSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "org-secret-rotation-lifecycle@example.com")
	credential, _, err := store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerOrg,
			Name:     "organization integration credential",
			Material: secrets.GenericMaterial{Value: "first-value"},
			Actor:    identitystore.NewUserPrincipal(admin.ID),
		},
	)
	if err != nil {
		t.Fatalf("create organization credential: %v", err)
	}
	if _, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, Provider: testChannelProvider,
			ProviderAppRef: "org-secret-rotation-lifecycle", DisplayName: "Organization lifecycle",
			ConnectorKey: testChannelConnector, CredentialSecretID: credential.ID,
			State: integrationstore.IntegrationAppStateActive,
		},
	); err != nil {
		t.Fatalf("create organization app: %v", err)
	}

	deletion, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin organization deletion holder: %v", err)
	}
	defer func() { _ = deletion.Rollback(context.Background()) }()
	var deletionPID int32
	if err := deletion.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&deletionPID); err != nil {
		t.Fatalf("load organization deletion backend: %v", err)
	}
	if _, err := deletion.Exec(
		ctx,
		`UPDATE orgs SET deleted_at = statement_timestamp() WHERE id = $1`,
		testOrgID,
	); err != nil {
		t.Fatalf("hold organization deletion row: %v", err)
	}

	rotationDone := make(chan error, 1)
	go func() {
		_, _, err := store.Secrets().CreateSecretVersion(
			context.Background(),
			secretstore.CreateSecretVersionInput{
				OrgID: testOrgID, SecretID: credential.ID,
				Material: secrets.GenericMaterial{Value: "second-value"},
				Actor:    identitystore.NewUserPrincipal(admin.ID),
			},
		)
		rotationDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: LockOrganizationLifecycleShared ",
		deletionPID,
	)
	if err := deletion.Rollback(ctx); err != nil {
		t.Fatalf("release organization deletion row: %v", err)
	}
	select {
	case err := <-rotationDone:
		if err != nil {
			t.Fatalf("finish organization credential rotation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("organization credential rotation did not finish after deletion row release")
	}
}

func TestProjectDeletionWaitsForSecretRotationLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin, _, credential, app, install := createCredentialBackedChannelLifecycleFixture(
		t,
		ctx,
		store,
		"secret-rotation-project-deletion",
	)

	appHolder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin app holder: %v", err)
	}
	t.Cleanup(func() { _ = appHolder.Rollback(ctx) })
	var appHolderPID int32
	if err := appHolder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&appHolderPID); err != nil {
		t.Fatalf("load app holder backend: %v", err)
	}
	if _, err := appHolder.Exec(
		ctx,
		`SELECT id FROM integration_apps WHERE id = $1 FOR UPDATE`,
		app.ID,
	); err != nil {
		t.Fatalf("lock integration app: %v", err)
	}

	rotationDone := make(chan error, 1)
	go func() {
		_, _, err := store.Secrets().CreateSecretVersion(
			context.Background(),
			secretstore.CreateSecretVersionInput{
				OrgID: testOrgID, SecretID: credential.ID,
				Material: secrets.GenericMaterial{Value: "second-value"},
				Actor:    identitystore.NewUserPrincipal(admin.ID),
			},
		)
		rotationDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: SetSecretCurrentVersion ",
		appHolderPID,
	)
	var rotationPID int32
	if err := pool.QueryRow(ctx, `
SELECT pid
FROM pg_stat_activity
WHERE datname = current_database()
  AND wait_event_type = 'Lock'
  AND query ILIKE '%-- name: SetSecretCurrentVersion %'
  AND $1::integer = ANY(pg_blocking_pids(pid))
ORDER BY pid
LIMIT 1
`, appHolderPID).Scan(&rotationPID); err != nil {
		t.Fatalf("load secret rotation backend: %v", err)
	}

	deleteDone := make(chan error, 1)
	go func() {
		_, err := store.Organizations().DeleteProject(
			context.Background(),
			testOrgID,
			testProjectID,
			identitystore.NewUserPrincipal(admin.ID),
		)
		deleteDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: LockProjectLifecycleExclusive ",
		rotationPID,
	)

	if err := appHolder.Rollback(ctx); err != nil {
		t.Fatalf("release integration app: %v", err)
	}
	select {
	case err := <-rotationDone:
		if err != nil {
			t.Fatalf("finish credential rotation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("credential rotation did not finish after app unlock")
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("finish project deletion: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("project deletion did not finish after credential rotation")
	}
	assertChannelLifecycleRowsDeleted(t, ctx, pool, app.ID, install.ID)
}

func createCredentialBackedChannelLifecycleFixture(
	t *testing.T,
	ctx context.Context,
	store *Store,
	suffix string,
) (
	identitystore.UserRecord,
	executionstore.AgentRecord,
	secretstore.SecretRecord,
	integrationstore.IntegrationAppRecord,
	integrationstore.IntegrationInstallRecord,
) {
	t.Helper()
	admin := createIntegrationProjectAdmin(t, ctx, store, suffix+"@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, suffix)
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, suffix)
	credential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerProject,
		OwnerProjectID: testProjectID, Name: "shared-integration-credential",
		Material: secrets.GenericMaterial{Value: "first-value"},
		Actor:    identitystore.NewUserPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create shared integration credential: %v", err)
	}
	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, OwnerProjectID: testProjectID,
			Provider: testChannelProvider, ProviderAppRef: suffix + "-app",
			DisplayName: suffix, ConnectorKey: testChannelConnector,
			CredentialSecretID:         credential.ID,
			InstallationCredentialKind: "generic",
			State:                      integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create shared-credential app: %v", err)
	}
	install, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
			InstalledByUserID: admin.ID,
			Provider:          testChannelProvider, IntegrationKind: "lock_order_test",
			ConnectionMode: "gateway", State: integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: suffix + "-tenant", ProviderAccountRef: suffix + "-account",
			CredentialSecretID: credential.ID,
		},
	)
	if err != nil {
		t.Fatalf("create shared-credential install: %v", err)
	}
	return admin, agent, credential, app, install
}
