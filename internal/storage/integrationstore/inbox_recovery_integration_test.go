//go:build integration

package integrationstore_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func freezeRecoverySelection(
	t *testing.T,
	f inboxFixture,
	name string,
) (integrationstore.IntegrationInboxRecord, integrationstore.InboxAppSelection) {
	t.Helper()
	var profileID uuid.UUID
	require.NoError(
		t,
		f.pool.QueryRow(f.ctx, `SELECT id FROM agent_profiles WHERE project_id=$1 LIMIT 1`, f.project).Scan(&profileID),
	)
	f.exec(t, `UPDATE project_apps SET provider_tenant_id='T123' WHERE id=$1`, f.appID)
	store := integrationstore.New(f.pool, executionstore.AppAccess{})
	app, err := store.UpdateProjectApp(f.ctx, f.appID, integrationstore.SaveProjectAppInput{
		OrgID: f.org, ProjectID: f.project, Name: "inbox-app", AppType: appdefinition.SlackThread,
		Settings: integrationstore.ProjectAppSettings{
			Launcher: &integrationstore.AppLauncher{Trigger: "mention", ScopeKind: "workspace", ScopeRef: "T123",
				Slots: []integrationstore.AppLaunchSlot{{Key: "a", AgentProfileID: &profileID}}},
		},
	})
	require.NoError(t, err)
	selection := integrationstore.InboxAppSelection{AppID: app.ID,
		Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}, Slot: "a"}
	plan, err := json.Marshal(
		map[string]any{"a": map[string]any{
			"agent_id": uuid.Must(
				uuid.NewV7(),
			),
			"selection": selection,
			"launch":    map[string]any{"ProfileID": profileID},
		}},
	)
	require.NoError(t, err)
	f.accept(t, name)
	receipt := f.claim(t)
	f.mutate(t, receipt, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		if err := work.FreezePlan(f.ctx, plan); err != nil {
			return err
		}
		return work.PrepareSlot(f.ctx, "a", json.RawMessage(`{"digest":"pinned-media-evidence"}`))
	})
	return f.read(t, receipt.ID), selection
}

func failRecoverySelection(
	t *testing.T,
	f inboxFixture,
	receipt integrationstore.IntegrationInboxRecord,
) integrationstore.IntegrationInboxRecord {
	t.Helper()
	f.mutate(t, receipt, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		return work.Fail(f.ctx, "operator recovery required")
	})
	f.exec(t, `UPDATE integration_inbox SET attempt_count=8 WHERE id=$1`, receipt.ID)
	return f.read(t, receipt.ID)
}

func TestInboxRecoveryRetryPreservesFrozenPartialWork(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	receipt, _ := freezeRecoverySelection(t, f, "partial")
	f.mutate(t, receipt, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		return work.CommitSlot(f.ctx, "a", json.RawMessage(`{"input_id":"already-committed"}`))
	})
	failed := failRecoverySelection(t, f, receipt)
	require.ErrorIs(
		t,
		f.store.DiscardFailedIntegrationInbox(f.ctx, f.project, receipt.ID),
		storeerr.ErrStateTransitionConflict,
	)
	require.Equal(t, failed, f.read(t, receipt.ID), "rejected discard must not rewrite anything")
	require.NoError(t, f.store.RetryFailedIntegrationInbox(f.ctx, f.project, receipt.ID))
	retried := f.read(t, receipt.ID)
	require.Equal(t, integrationstore.IntegrationInboxPending, retried.State)
	require.Zero(t, retried.AttemptCount)
	require.Equal(t, failed.Plan, retried.Plan)
	require.Equal(t, failed.Progress, retried.Progress)
	require.Equal(t, failed.Payload, retried.Payload)
	require.Equal(t, failed.LastError, retried.LastError)
}

func TestInboxRecoveryDiscardRetainsDedupeAndReleasesSelection(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	receipt, _ := freezeRecoverySelection(t, f, "discard")
	failed := failRecoverySelection(t, f, receipt)
	require.NoError(t, f.store.DiscardFailedIntegrationInbox(f.ctx, f.project, receipt.ID))
	discarded := f.read(t, receipt.ID)
	require.Equal(t, integrationstore.IntegrationInboxDiscarded, discarded.State)
	require.NotNil(t, discarded.CompletedAt)
	require.Equal(t, failed.Plan, discarded.Plan)
	require.Equal(t, failed.Progress, discarded.Progress)
	require.Equal(t, failed.LastError, discarded.LastError)
	replay, created, err := f.store.AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID:  f.project,
		AppID:      f.appID,
		ReceiptKey: receipt.ReceiptKey,
		Payload:    []byte(`{"changed":true}`),
	})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, discarded.ID, replay.ID)
	require.Equal(t, discarded.Payload, replay.Payload)
	_, claimed, err := f.store.ClaimIntegrationInbox(f.ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: f.project, AppID: f.appID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.False(t, claimed)
	require.ErrorIs(
		t,
		f.store.RetryFailedIntegrationInbox(f.ctx, f.project, receipt.ID),
		storeerr.ErrStateTransitionConflict,
	)
	page, err := f.store.ListIntegrationInbox(f.ctx, integrationstore.ListIntegrationInboxInput{
		ProjectID: f.project, State: integrationstore.IntegrationInboxDiscarded, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Receipts, 1)
	f.accept(t, "fresh-event")
	fresh := f.claim(t)
	var plan map[string]map[string]any
	require.NoError(t, json.Unmarshal(receipt.Plan, &plan))
	plan["a"]["agent_id"] = uuid.Must(uuid.NewV7()).String()
	newPlan, err := json.Marshal(plan)
	require.NoError(t, err)
	f.mutate(
		t,
		fresh,
		func(work *integrationstore.IntegrationInboxLeaseTx) error { return work.FreezePlan(f.ctx, newPlan) },
	)
	require.NotEqual(t, discarded.Plan, f.read(t, fresh.ID).Plan)
	count, err := f.store.CleanupTerminalIntegrationInbox(f.ctx, 7*24*time.Hour, 100)
	require.NoError(t, err)
	require.Zero(t, count)
	f.exec(
		t,
		`UPDATE integration_inbox SET completed_at=statement_timestamp()-interval '8 days' WHERE id=$1`,
		receipt.ID,
	)
	count, err = f.store.CleanupTerminalIntegrationInbox(f.ctx, 7*24*time.Hour, 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	require.Equal(t, integrationstore.IntegrationInboxProcessing, f.read(t, fresh.ID).State)
}

func TestInboxRecoveryDiscardRejectsRetainedTargets(t *testing.T) {
	t.Parallel()
	for _, retired := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "retired"}[retired], func(t *testing.T) {
			t.Parallel()
			f := newInboxFixture(t)
			receipt, selection := freezeRecoverySelection(t, f, "retained")
			failed := failRecoverySelection(t, f, receipt)
			var configID uuid.UUID
			require.NoError(
				t,
				f.pool.QueryRow(
					f.ctx,
					`SELECT id FROM agent_configs WHERE project_id=$1 LIMIT 1`,
					f.project,
				).Scan(
					&configID,
				),
			)
			launch, err := executionstore.New(
				f.pool,
				executionstore.Config{},
			).LaunchAgent(
				f.ctx,
				executionstore.LaunchAgentInput{
					ProjectID: f.project, AgentConfigID: configID, LaunchedBy: identitystore.NewUserPrincipal(f.user),
				},
			)
			require.NoError(t, err)
			f.exec(t, `INSERT INTO integration_targets
 (project_id,agent_id,app_id,provider_ref_kind,provider_ref,
  routing_role,selection_slot,deleted_at,created_at,updated_at)
 VALUES($1,$2,$3,'thread','C123:1.2','selected','a',
  CASE WHEN $4 THEN now() ELSE NULL END,now(),now())`,
				f.project, launch.Agent.ID, selection.AppID, retired)
			require.ErrorIs(
				t,
				f.store.DiscardFailedIntegrationInbox(f.ctx, f.project, receipt.ID),
				storeerr.ErrStateTransitionConflict,
			)
			require.Equal(t, failed, f.read(t, receipt.ID))
		})
	}
}

func TestInboxRecoveryDisconnectedAppAllowsDiscardOnly(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	receipt, _ := freezeRecoverySelection(t, f, "disabled")
	failed := failRecoverySelection(t, f, receipt)
	f.exec(t, `UPDATE project_apps SET state='disconnected' WHERE id=$1`, f.appID)
	require.ErrorIs(t, f.store.RetryFailedIntegrationInbox(f.ctx, f.project, receipt.ID), storeerr.ErrUnauthorized)
	require.Equal(t, failed, f.read(t, receipt.ID))
	require.ErrorIs(t, f.store.DiscardFailedIntegrationInbox(f.ctx, uuid.New(), receipt.ID), storeerr.ErrNotFound)
	require.NoError(t, f.store.DiscardFailedIntegrationInbox(f.ctx, f.project, receipt.ID))
}

func TestInboxRecoveryConcurrentActionsRespectGateOrder(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	receipt, selection := freezeRecoverySelection(t, f, "concurrent")
	failed := failRecoverySelection(t, f, receipt)
	appGate, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = appGate.Rollback(f.ctx) }()
	require.NoError(t, dbsqlc.New(appGate).LockProjectAppLifecycleExclusive(f.ctx,
		dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: f.appID}))
	conversationGate, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = conversationGate.Rollback(f.ctx) }()
	require.NoError(
		t,
		integrationstore.LockConversationTx(f.ctx, conversationGate, f.project, f.appID, selection.Address),
	)
	done := make(chan error, 2)
	go func() { done <- f.store.RetryFailedIntegrationInbox(f.ctx, f.project, receipt.ID) }()
	go func() { done <- f.store.DiscardFailedIntegrationInbox(f.ctx, f.project, receipt.ID) }()
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockProjectAppLifecycleShared", 2)
	probe, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = probe.Rollback(f.ctx) }()
	_, err = probe.Exec(f.ctx, `SELECT id FROM integration_inbox WHERE id=$1 FOR UPDATE NOWAIT`, receipt.ID)
	require.NoError(t, err, "receipt must remain unlocked while app gate is unavailable")
	require.NoError(t, probe.Rollback(f.ctx))
	require.NoError(t, appGate.Commit(f.ctx))
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockAppConversation", 1)
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockIntegrationInboxReceipt", 1)
	require.Equal(t, failed, f.read(t, receipt.ID))
	require.NoError(t, conversationGate.Commit(f.ctx))
	first, second := <-done, <-done
	wonFirst := first == nil && errors.Is(second, storeerr.ErrStateTransitionConflict)
	wonSecond := second == nil && errors.Is(first, storeerr.ErrStateTransitionConflict)
	require.True(t, wonFirst || wonSecond, "retry/discard outcomes: %v, %v", first, second)
	result := f.read(t, receipt.ID)
	require.Equal(t, failed.Plan, result.Plan)
	require.Equal(t, failed.Progress, result.Progress)
}

func TestInboxRecoveryProbeFiltersFailedOwnersBeforeLimit(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	pending, selection := freezeRecoverySelection(t, f, "still-preparing")
	// Retained failed plans may coexist during explicit recovery. They must be
	// filtered before LIMIT so the live owner is still found.
	f.exec(t, `INSERT INTO integration_inbox(id,project_id,app_id,receipt_key,payload,plan,state)
 SELECT ('00000000-0000-7000-8000-'||lpad(n::text,12,'0'))::uuid,
        project_id,app_id,'failed-'||n,payload,plan,'failed'
 FROM integration_inbox CROSS JOIN generate_series(1,2) n WHERE id=$1`, pending.ID)
	f.accept(t, "follow")
	follow := f.claim(t)
	err := f.store.WithIntegrationInboxLease(
		f.ctx,
		follow.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.CheckNoUnsettledAppSelection(f.ctx, selection.Address)
		},
	)
	var reservation *integrationstore.AppSelectionReservationError
	require.ErrorAs(t, err, &reservation)
	require.Equal(t, pending.ID, reservation.ReceiptID)
	failRecoverySelection(t, f, pending)
	f.mutate(t, follow, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		if err := work.CheckNoUnsettledAppSelection(f.ctx, selection.Address); err != nil {
			return err
		}
		if err := work.FreezePlan(f.ctx, json.RawMessage(`{}`)); err != nil {
			return err
		}
		return work.Complete(f.ctx)
	})
	require.Equal(t, integrationstore.IntegrationInboxCompleted, f.read(t, follow.ID).State)
}

func TestInboxRecoveryRetrySerializesWithEmptyPlanDecision(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	receipt, selection := freezeRecoverySelection(t, f, "retry-boundary")
	failed := failRecoverySelection(t, f, receipt)
	f.accept(t, "before-retry")
	follow := f.claim(t)
	gateHeld := make(chan struct{})
	finishEmpty := make(chan struct{})
	emptyDone := make(chan error, 1)
	go func() {
		emptyDone <- f.store.WithIntegrationInboxLease(
			f.ctx, follow.Lease(), func(work *integrationstore.IntegrationInboxLeaseTx) error {
				if err := work.CheckNoUnsettledAppSelection(f.ctx, selection.Address); err != nil {
					return err
				}
				close(gateHeld)
				select {
				case <-finishEmpty:
				case <-f.ctx.Done():
					return f.ctx.Err()
				}
				if err := work.FreezePlan(f.ctx, json.RawMessage(`{}`)); err != nil {
					return err
				}
				return work.Complete(f.ctx)
			})
	}()
	select {
	case <-gateHeld:
	case err := <-emptyDone:
		t.Fatalf("empty decision failed before conversation gate: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("empty decision did not reach conversation gate")
	}
	// Always release a blocked callback if an assertion aborts this test.
	defer func() {
		select {
		case <-finishEmpty:
		default:
			close(finishEmpty)
		}
	}()
	retryDone := make(chan error, 1)
	go func() { retryDone <- f.store.RetryFailedIntegrationInbox(f.ctx, f.project, receipt.ID) }()
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockAppConversation", 1)
	require.Equal(t, failed, f.read(t, receipt.ID))
	close(finishEmpty)
	require.NoError(t, <-emptyDone)
	require.NoError(t, <-retryDone)
	require.Equal(t, integrationstore.IntegrationInboxCompleted, f.read(t, follow.ID).State)
	owner := f.claim(t)
	require.Equal(t, receipt.ID, owner.ID)
	require.Equal(t, failed.Plan, owner.Plan)
	f.accept(t, "after-retry")
	next := f.claim(t)
	err := f.store.WithIntegrationInboxLease(
		f.ctx,
		next.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.CheckNoUnsettledAppSelection(f.ctx, selection.Address)
		},
	)
	require.ErrorIs(t, err, integrationstore.ErrAppSelectionReserved)
}
