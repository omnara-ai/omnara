//go:build integration

package integrationstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func freezeInboxSelection(
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

func failInboxSelection(
	t *testing.T,
	f inboxFixture,
	receipt integrationstore.IntegrationInboxRecord,
) integrationstore.IntegrationInboxRecord {
	t.Helper()
	f.mutate(t, receipt, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		return work.Fail(f.ctx, "launch failed")
	})
	return f.read(t, receipt.ID)
}

func TestInboxTerminalFailureRetainsDedupeAndReleasesSelection(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	receipt, _ := freezeInboxSelection(t, f, "failed")
	failed := failInboxSelection(t, f, receipt)
	require.Equal(t, integrationstore.IntegrationInboxFailed, failed.State)
	require.NotNil(t, failed.CompletedAt)
	require.Equal(t, receipt.Plan, failed.Plan)
	require.Equal(t, receipt.Progress, failed.Progress)
	replay, created, err := f.store.AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID:  f.project,
		AppID:      f.appID,
		ReceiptKey: receipt.ReceiptKey,
		Payload:    []byte(`{"changed":true}`),
	})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, failed.ID, replay.ID)
	require.Equal(t, failed.Payload, replay.Payload)
	_, claimed, err := f.store.ClaimIntegrationInbox(f.ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: f.project, AppID: f.appID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.False(t, claimed)
	err = f.store.WithIntegrationInboxLease(f.ctx, receipt.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.CommitSlot(f.ctx, "a", json.RawMessage(`{"admitted":true}`))
		})
	require.ErrorIs(t, err, integrationstore.ErrIntegrationInboxLeaseLost)
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
	require.NotEqual(t, failed.Plan, f.read(t, fresh.ID).Plan)
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

func TestInboxTerminalFailurePreservesRetainedTargets(t *testing.T) {
	t.Parallel()
	for _, retired := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "retired"}[retired], func(t *testing.T) {
			t.Parallel()
			f := newInboxFixture(t)
			receipt, selection := freezeInboxSelection(t, f, "retained")
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
  selection_slot,deleted_at,created_at,updated_at)
 VALUES($1,$2,$3,'thread','C123:1.2','a',
  CASE WHEN $4 THEN now() ELSE NULL END,now(),now())`,
				f.project, launch.Agent.ID, selection.AppID, retired)
			f.mutate(t, receipt, func(work *integrationstore.IntegrationInboxLeaseTx) error {
				return work.CommitSlot(f.ctx, "a", json.RawMessage(`{"admitted":true}`))
			})
			failed := failInboxSelection(t, f, receipt)
			f.accept(t, "replacement")
			replacement := f.claim(t)
			err = f.store.WithIntegrationInboxLease(f.ctx, replacement.Lease(),
				func(work *integrationstore.IntegrationInboxLeaseTx) error {
					return work.FreezePlan(f.ctx, failed.Plan)
				})
			require.ErrorIs(t, err, integrationstore.ErrAppSelectionSettled,
				"committed targets retain membership after the failed receipt releases its reservation")
			f.exec(t, `UPDATE integration_inbox SET completed_at=now()-interval '8 days' WHERE id=$1`, receipt.ID)
			count, err := f.store.CleanupTerminalIntegrationInbox(f.ctx, 7*24*time.Hour, 1)
			require.NoError(t, err)
			require.EqualValues(t, 1, count)
			var targets int
			require.NoError(t, f.pool.QueryRow(f.ctx,
				`SELECT count(*) FROM integration_targets WHERE project_id=$1 AND agent_id=$2`,
				f.project, launch.Agent.ID).Scan(&targets))
			require.Equal(t, 1, targets)
			_, err = executionstore.New(f.pool, executionstore.Config{}).GetAgentInProject(f.ctx, f.project, launch.Agent.ID)
			require.NoError(t, err)
			err = f.store.WithIntegrationInboxLease(f.ctx, replacement.Lease(),
				func(work *integrationstore.IntegrationInboxLeaseTx) error {
					return work.FreezePlan(f.ctx, failed.Plan)
				})
			require.ErrorIs(t, err, integrationstore.ErrAppSelectionSettled)
		})
	}
}

func TestInboxReservationsExcludeFailedOwnersBeforeLimit(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	pending, selection := freezeInboxSelection(t, f, "still-preparing")
	// Retained failed plans may coexist with a new request. They must be
	// filtered before LIMIT so the live owner is still found.
	f.exec(t, `INSERT INTO integration_inbox(id,project_id,app_id,receipt_key,payload,plan,state,completed_at)
 SELECT ('00000000-0000-7000-8000-'||lpad(n::text,12,'0'))::uuid,
        project_id,app_id,'failed-'||n,payload,plan,'failed',now()
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
	failInboxSelection(t, f, pending)
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

func TestInboxTerminalRecoverySkipsLockedAndBoundsBatches(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"expired-budget", "inactive-pending", "inactive-expired"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			f := newInboxFixture(t)
			var receipts []integrationstore.IntegrationInboxRecord
			for _, key := range []string{"locked", "second", "third"} {
				f.accept(t, key)
				receipt := f.claim(t)
				if path == "inactive-pending" {
					f.mutate(t, receipt, func(work *integrationstore.IntegrationInboxLeaseTx) error {
						return work.Retry(f.ctx, time.Hour, "temporarily unavailable")
					})
				} else {
					attempts := 1
					if path == "expired-budget" {
						attempts = integrationstore.IntegrationInboxMaxAttempts
					}
					f.exec(t, `UPDATE integration_inbox SET attempt_count=$2,
 claim_expires_at=now()-interval '1 hour' WHERE id=$1`, receipt.ID, attempts)
				}
				receipts = append(receipts, f.read(t, receipt.ID))
			}
			if path != "expired-budget" {
				f.exec(t, `UPDATE project_apps SET state='disconnected' WHERE id=$1`, f.appID)
			}
			tx, err := f.pool.Begin(f.ctx)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(f.ctx) }()
			_, err = tx.Exec(f.ctx, `SELECT id FROM integration_inbox WHERE id=$1 FOR UPDATE`, receipts[0].ID)
			require.NoError(t, err)
			bounded, cancel := context.WithTimeout(f.ctx, 5*time.Second)
			defer cancel()
			for _, receipt := range receipts[1:] {
				count, err := f.store.RecoverIntegrationInbox(bounded, 1)
				require.NoError(t, err)
				require.EqualValues(t, 1, count)
				failed := f.read(t, receipt.ID)
				require.Equal(t, integrationstore.IntegrationInboxFailed, failed.State)
				require.NotNil(t, failed.CompletedAt)
				require.Equal(t, receipt.AttemptCount, failed.AttemptCount)
				require.Equal(t, receipt.Payload, failed.Payload)
				require.Nil(t, failed.ClaimExpiresAt)
				require.Equal(t, uuid.Nil, failed.ClaimToken)
			}
			require.Equal(t, receipts[0], f.read(t, receipts[0].ID))
			require.NoError(t, tx.Rollback(f.ctx))
			for _, want := range []int64{1, 0} {
				count, err := f.store.RecoverIntegrationInbox(f.ctx, 1)
				require.NoError(t, err)
				require.Equal(t, want, count)
			}
			require.NotNil(t, f.read(t, receipts[0].ID).CompletedAt)
		})
	}
}
