//go:build integration

package executionstore_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

type receiptMaintenanceFixture struct {
	store     *Store
	appID     uuid.UUID
	installID uuid.UUID
}

func newReceiptMaintenanceFixture(t *testing.T) receiptMaintenanceFixture {
	t.Helper()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, _, app, install := createChannelLifecycleFixture(t, ctx, store, "receipt-maintenance")
	return receiptMaintenanceFixture{store: store, appID: app.ID, installID: install.ID}
}

func (f receiptMaintenanceFixture) receive(t *testing.T, eventID string) integrationstore.IntegrationEventReceipt {
	t.Helper()
	input := integrationstore.ReceiveIntegrationEventInput{
		ProjectID: testProjectID, IntegrationInstallID: f.installID, EventID: eventID,
		Payload:      json.RawMessage(`{"message":"retained incoming payload"}`),
		Capabilities: testChannelCapabilities(testChannelProvider),
	}
	receipt, err := f.store.Integrations().ReceiveIntegrationEvent(t.Context(), input)
	require.NoError(t, err)
	return receipt
}

func (f receiptMaintenanceFixture) claim(t *testing.T) integrationstore.IntegrationEventReceipt {
	t.Helper()
	input := integrationstore.ClaimNextIntegrationEventInput{
		Capability: testChannelCapability(testChannelProvider), LeaseDuration: time.Minute,
	}
	receipt, found, err := f.store.Integrations().ClaimNextIntegrationEvent(t.Context(), input)
	require.NoError(t, err)
	require.True(t, found)
	return receipt
}

func (f receiptMaintenanceFixture) finish(
	t *testing.T,
	lease integrationstore.IntegrationEventReceipt,
	state integrationstore.IntegrationEventState,
) integrationstore.IntegrationEventReceipt {
	t.Helper()
	input := integrationstore.FinishIntegrationEventInput{
		ProjectID: testProjectID, IntegrationInstallID: f.installID, ID: lease.ID,
		LeaseToken: lease.LeaseToken, LeaseGeneration: lease.LeaseGeneration, State: state,
		Capabilities: testChannelCapabilities(testChannelProvider),
	}
	if state != integrationstore.IntegrationEventCompleted {
		input.LastError = json.RawMessage(`{"code":"provider_failure","message":"provider is unavailable"}`)
	}
	receipt, err := f.store.Integrations().FinishIntegrationEvent(t.Context(), input)
	require.NoError(t, err)
	return receipt
}

func (f receiptMaintenanceFixture) expire(t *testing.T, id uuid.UUID) {
	t.Helper()
	_, err := f.store.pool.Exec(t.Context(), `UPDATE integration_event_receipts
SET available_at = statement_timestamp() - interval '1 second',
    lease_expires_at = statement_timestamp() - interval '1 second'
WHERE id = $1 AND state = 'processing'`, id)
	require.NoError(t, err)
}

func (f receiptMaintenanceFixture) read(t *testing.T, eventID string) dbsqlc.IntegrationEventReceipt {
	t.Helper()
	input := dbsqlc.GetIntegrationEventReceiptByIdentityParams{
		ProjectID: testProjectID, IntegrationInstallID: f.installID, EventID: eventID,
	}
	receipt, err := f.store.q.GetIntegrationEventReceiptByIdentity(t.Context(), input)
	require.NoError(t, err)
	return receipt
}

func TestIntegrationEventMaintenanceBoundsRetriesAndCrashedClaims(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"retry", "crash", "complete final claim"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newReceiptMaintenanceFixture(t)
			original := f.receive(t, "poison")
			var lease integrationstore.IntegrationEventReceipt
			for attempt := 1; attempt <= integrationstore.MaxIntegrationEventAttempts; attempt++ {
				lease = f.claim(t)
				require.Equal(t, original.ID, lease.ID)
				require.Equal(t, attempt, lease.AttemptCount)
				require.Equal(t, int64(attempt), lease.LeaseGeneration)
				require.JSONEq(t, string(original.Payload), string(lease.Payload))
				if attempt == integrationstore.MaxIntegrationEventAttempts {
					failed, err := f.store.Integrations().FailUnprocessableIntegrationEvents(ctx, 10)
					require.NoError(t, err)
					require.Zero(t, failed, "an unexpired final claim may still complete successfully")
					if outcome == "complete final claim" {
						completed := f.finish(t, lease, integrationstore.IntegrationEventCompleted)
						require.Equal(t, integrationstore.IntegrationEventCompleted, completed.State)
						require.NotNil(t, completed.CompletedAt)
						return
					}
				}
				if outcome == "crash" {
					f.expire(t, lease.ID)
				} else {
					f.finish(t, lease, integrationstore.IntegrationEventPending)
					_, err := f.store.pool.Exec(ctx, `UPDATE integration_event_receipts
SET available_at = statement_timestamp() - interval '1 second' WHERE id = $1`, lease.ID)
					require.NoError(t, err)
				}
			}
			claimInput := integrationstore.ClaimNextIntegrationEventInput{
				Capability: testChannelCapability(testChannelProvider), LeaseDuration: time.Minute,
			}
			_, found, err := f.store.Integrations().ClaimNextIntegrationEvent(ctx, claimInput)
			require.NoError(t, err)
			require.False(t, found, "maintenance delays must not permit a claim beyond the hard budget")
			failed, err := f.store.Integrations().FailUnprocessableIntegrationEvents(ctx, 10)
			require.NoError(t, err)
			require.Equal(t, int64(1), failed)
			terminal := f.read(t, original.EventID)
			require.Equal(t, "failed", terminal.State)
			require.Equal(t, int32(integrationstore.MaxIntegrationEventAttempts), terminal.AttemptCount)
			require.Nil(t, terminal.LeaseToken)
			require.Nil(t, terminal.LeaseExpiresAt)
			require.NotNil(t, terminal.CompletedAt)
			require.Contains(t, string(terminal.LastError), `"retry_budget_exhausted"`)
			require.JSONEq(t, string(original.Payload), string(terminal.Payload))
			_, err = f.store.Integrations().FinishIntegrationEvent(ctx, integrationstore.FinishIntegrationEventInput{
				ProjectID: testProjectID, IntegrationInstallID: f.installID, ID: lease.ID,
				LeaseToken: lease.LeaseToken, LeaseGeneration: lease.LeaseGeneration,
				State: integrationstore.IntegrationEventCompleted, Capabilities: testChannelCapabilities(testChannelProvider),
			})
			require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict, "terminalized work cannot be reopened")
			failed, err = f.store.Integrations().FailUnprocessableIntegrationEvents(ctx, 10)
			require.NoError(t, err)
			require.Zero(t, failed)
			replayed := f.receive(t, original.EventID)
			require.Equal(t, original.ID, replayed.ID)
			require.Equal(t, integrationstore.IntegrationEventFailed, replayed.State)
			require.Equal(t, terminal.CompletedAt, replayed.CompletedAt, "replay does not restart terminal retention")
		})
	}
}

func TestIntegrationEventMaintenanceTerminalizesUnavailableOwners(t *testing.T) {
	t.Parallel()
	for _, owner := range []string{
		"disabled app", "deleted app", "disabled install", "deleted install", "project", "org",
	} {
		t.Run(owner, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newReceiptMaintenanceFixture(t)
			f.receive(t, "expired")
			expired := f.claim(t)
			f.receive(t, "active")
			active := f.claim(t)
			pending := f.receive(t, "pending")
			f.expire(t, expired.ID)
			_, err := f.store.pool.Exec(ctx, `UPDATE integration_event_receipts
SET available_at = statement_timestamp() + interval '1 hour' WHERE id = $1`, pending.ID)
			require.NoError(t, err)
			switch owner {
			case "disabled app":
				_, err = f.store.pool.Exec(ctx, `UPDATE integration_apps SET state = 'disabled' WHERE id = $1`, f.appID)
			case "deleted app":
				_, err = f.store.pool.Exec(ctx,
					`UPDATE integration_apps SET deleted_at = statement_timestamp() WHERE id = $1`, f.appID)
			case "disabled install":
				_, err = f.store.pool.Exec(ctx, `UPDATE integration_installs SET state = 'disabled' WHERE id = $1`, f.installID)
			case "deleted install":
				_, err = f.store.pool.Exec(ctx,
					`UPDATE integration_installs SET deleted_at = statement_timestamp() WHERE id = $1`, f.installID)
			case "project":
				_, err = f.store.pool.Exec(ctx,
					`UPDATE projects SET deleted_at = statement_timestamp() WHERE id = $1`, testProjectID)
			case "org":
				_, err = f.store.pool.Exec(ctx, `UPDATE orgs SET deleted_at = statement_timestamp() WHERE id = $1`, testOrgID)
			}
			require.NoError(t, err)
			claimInput := integrationstore.ClaimNextIntegrationEventInput{
				Capability: testChannelCapability(testChannelProvider), LeaseDuration: time.Minute,
			}
			_, found, err := f.store.Integrations().ClaimNextIntegrationEvent(ctx, claimInput)
			require.NoError(t, err)
			require.False(t, found)
			failed, err := f.store.Integrations().FailUnprocessableIntegrationEvents(ctx, 100)
			require.NoError(t, err)
			require.Equal(t, int64(2), failed, "pending backoff and expired claims must not get stuck")
			for _, receipt := range []integrationstore.IntegrationEventReceipt{pending, expired} {
				terminal := f.read(t, receipt.EventID)
				require.Equal(t, "failed", terminal.State)
				require.Contains(t, string(terminal.LastError), `"owner_unavailable"`)
				require.NotNil(t, terminal.CompletedAt)
				require.Nil(t, terminal.LeaseToken)
				require.Nil(t, terminal.LeaseExpiresAt)
				require.JSONEq(t, string(receipt.Payload), string(terminal.Payload))
			}
			stillActive := f.read(t, active.EventID)
			require.Equal(t, "processing", stillActive.State)
			require.Equal(t, active.LeaseToken, *stillActive.LeaseToken)
			f.expire(t, active.ID)
			failed, err = f.store.Integrations().FailUnprocessableIntegrationEvents(ctx, 100)
			require.NoError(t, err)
			require.Equal(t, int64(1), failed)
		})
	}
}

func TestIntegrationEventMaintenanceBoundsBatchAndSkipsLockedReceipts(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newReceiptMaintenanceFixture(t)
	// Earlier healthy work must not prevent finding later exhausted receipts.
	healthy := f.receive(t, "healthy")
	var exhausted []integrationstore.IntegrationEventReceipt
	for i := range 3 {
		receipt := f.receive(t, fmt.Sprintf("exhausted-%d", i))
		exhausted = append(exhausted, receipt)
		_, err := f.store.pool.Exec(ctx, `UPDATE integration_event_receipts
SET attempt_count = $2, available_at = statement_timestamp() + interval '1 hour',
    last_error = jsonb_build_object('message', repeat('x', 262000)) WHERE id = $1`,
			receipt.ID, integrationstore.MaxIntegrationEventAttempts)
		require.NoError(t, err)
	}
	holder := integrationdb.BeginTx(t, ctx, f.store.pool)
	_, err := holder.Exec(ctx, `SELECT id FROM integration_event_receipts WHERE id = $1 FOR UPDATE`, exhausted[0].ID)
	require.NoError(t, err)
	failed, err := f.store.Integrations().FailUnprocessableIntegrationEvents(ctx, 1)
	require.NoError(t, err)
	require.Zero(t, failed, "healthy work consumes one candidate slot before any owner filtering")
	failed, err = f.store.Integrations().FailUnprocessableIntegrationEvents(ctx, 1)
	require.NoError(t, err)
	require.Zero(t, failed, "a locked row consumes a slot rather than scanning an unbounded locked prefix")
	failed, err = f.store.Integrations().FailUnprocessableIntegrationEvents(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), failed)
	require.Equal(t, "pending", f.read(t, exhausted[0].EventID).State)
	require.Equal(t, "failed", f.read(t, exhausted[1].EventID).State)
	require.Equal(t, "pending", f.read(t, exhausted[2].EventID).State, "LIMIT caps changes, not just returned rows")
	require.Equal(t, "pending", f.read(t, healthy.EventID).State)
	failed, err = f.store.Integrations().FailUnprocessableIntegrationEvents(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), failed)
	require.NoError(t, holder.Rollback(ctx))
	failed, err = f.store.Integrations().FailUnprocessableIntegrationEvents(ctx, 1)
	require.NoError(t, err)
	require.Zero(t, failed, "the next fixed cycle revisits earlier healthy work")
	failed, err = f.store.Integrations().FailUnprocessableIntegrationEvents(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), failed)
	for _, receipt := range exhausted {
		terminal := f.read(t, receipt.EventID)
		require.Less(t, len(terminal.LastError), 256, "maintenance must succeed even after a near-limit provider error")
	}
}

func TestIntegrationEventMaintenanceFixedCycleRevisitsEarlierWorkAcrossStoreRestart(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newReceiptMaintenanceFixture(t)
	first := f.receive(t, "first")
	middle := f.receive(t, "middle")
	end := f.receive(t, "cycle end")
	readCursor := func() (uuid.UUID, *uuid.UUID) {
		t.Helper()
		var last uuid.UUID
		var cycleEnd *uuid.UUID
		require.NoError(t, f.store.pool.QueryRow(ctx, `SELECT last_item_id, cycle_end_id
FROM integration_sweep_cursors WHERE sweep_kind = 'event_unprocessable'`).Scan(&last, &cycleEnd))
		return last, cycleEnd
	}
	failed, err := f.store.Integrations().FailUnprocessableIntegrationEvents(ctx, 1)
	require.NoError(t, err)
	require.Zero(t, failed)
	last, cycleEnd := readCursor()
	require.Equal(t, first.ID, last)
	require.Equal(t, end.ID, *cycleEnd)
	_, err = f.store.pool.Exec(ctx, `UPDATE integration_event_receipts SET attempt_count = $2 WHERE id = $1`,
		first.ID, integrationstore.MaxIntegrationEventAttempts)
	require.NoError(t, err)
	f.receive(t, "tail after first page")
	// Recreating the store must resume the persisted page, not start at the front.
	restarted := integrationstore.New(f.store.pool, nil)
	failed, err = restarted.FailUnprocessableIntegrationEvents(ctx, 1)
	require.NoError(t, err)
	require.Zero(t, failed)
	last, cycleEnd = readCursor()
	require.Equal(t, middle.ID, last)
	require.Equal(t, end.ID, *cycleEnd, "new ingress cannot extend an in-progress cycle")
	f.receive(t, "tail after second page")
	failed, err = restarted.FailUnprocessableIntegrationEvents(ctx, 1)
	require.NoError(t, err)
	require.Zero(t, failed)
	last, cycleEnd = readCursor()
	require.Equal(t, uuid.Nil, last)
	require.Nil(t, cycleEnd)
	failed, err = restarted.FailUnprocessableIntegrationEvents(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), failed, "new tail receipts must not starve an earlier exhausted receipt")
	require.Equal(t, "failed", f.read(t, first.EventID).State)
}

func TestIntegrationEventMaintenanceIdleAndConcurrentSweepDoNotAdvanceCursor(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newReceiptMaintenanceFixture(t)
	version := func() int64 {
		t.Helper()
		var xmin int64
		require.NoError(t, f.store.pool.QueryRow(ctx, `SELECT xmin::text::bigint
FROM integration_sweep_cursors WHERE sweep_kind = 'event_unprocessable'`).Scan(&xmin))
		return xmin
	}
	before := version()
	failed, err := f.store.Integrations().FailUnprocessableIntegrationEvents(ctx, 1)
	require.NoError(t, err)
	require.Zero(t, failed)
	require.Equal(t, before, version(), "empty sweeps should not create cursor churn")
	receipt := f.receive(t, "waiting for sweep")
	_, err = f.store.pool.Exec(ctx, `UPDATE integration_event_receipts SET attempt_count = $2 WHERE id = $1`,
		receipt.ID, integrationstore.MaxIntegrationEventAttempts)
	require.NoError(t, err)
	holder := integrationdb.BeginTx(t, ctx, f.store.pool)
	_, err = holder.Exec(ctx, `SELECT sweep_kind FROM integration_sweep_cursors
WHERE sweep_kind = 'event_unprocessable' FOR UPDATE`)
	require.NoError(t, err)
	failed, err = f.store.Integrations().FailUnprocessableIntegrationEvents(ctx, 1)
	require.NoError(t, err)
	require.Zero(t, failed)
	require.Equal(t, before, version())
	require.Equal(t, "pending", f.read(t, receipt.EventID).State)
	require.NoError(t, holder.Rollback(ctx))
	failed, err = f.store.Integrations().FailUnprocessableIntegrationEvents(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), failed)
}

func TestIntegrationEventRetentionUsesCompletionAndSkipsLockedRows(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newReceiptMaintenanceFixture(t)
	var terminal []integrationstore.IntegrationEventReceipt
	for _, state := range []integrationstore.IntegrationEventState{
		integrationstore.IntegrationEventCompleted,
		integrationstore.IntegrationEventFailed,
		integrationstore.IntegrationEventCompleted,
	} {
		f.receive(t, fmt.Sprintf("terminal-%d", len(terminal)))
		terminal = append(terminal, f.finish(t, f.claim(t), state))
	}
	f.receive(t, "recent terminal")
	recent := f.finish(t, f.claim(t), integrationstore.IntegrationEventFailed)
	f.receive(t, "processing")
	processing := f.claim(t)
	pending := f.receive(t, "pending")
	for _, receipt := range terminal {
		_, err := f.store.pool.Exec(ctx, `UPDATE integration_event_receipts
SET completed_at = statement_timestamp() - interval '8 days', updated_at = statement_timestamp()
WHERE id = $1`, receipt.ID)
		require.NoError(t, err)
	}
	_, err := f.store.pool.Exec(ctx, `UPDATE integration_event_receipts
SET updated_at = statement_timestamp() - interval '30 days' WHERE id = ANY($1::uuid[])`,
		[]uuid.UUID{recent.ID, processing.ID, pending.ID})
	require.NoError(t, err)
	f.expire(t, processing.ID)
	holder := integrationdb.BeginTx(t, ctx, f.store.pool)
	_, err = holder.Exec(ctx, `SELECT id FROM integration_event_receipts WHERE id = $1 FOR UPDATE`, terminal[0].ID)
	require.NoError(t, err)
	input := integrationstore.DeleteRetainedIntegrationEventsInput{Retention: 7 * 24 * time.Hour, Limit: 1}
	for range 2 {
		deleted, err := f.store.Integrations().DeleteRetainedIntegrationEvents(ctx, input)
		require.NoError(t, err)
		require.Equal(t, int64(1), deleted)
	}
	deleted, err := f.store.Integrations().DeleteRetainedIntegrationEvents(ctx, input)
	require.NoError(t, err)
	require.Zero(t, deleted)
	require.Equal(t, terminal[0].ID, f.read(t, terminal[0].EventID).ID)
	require.Equal(t, recent.ID, f.read(t, recent.EventID).ID, "recent completion survives an old updated_at")
	require.Equal(t, "processing", f.read(t, processing.EventID).State)
	require.Equal(t, "pending", f.read(t, pending.EventID).State)
	require.NoError(t, holder.Rollback(ctx))
	deleted, err = f.store.Integrations().DeleteRetainedIntegrationEvents(ctx, input)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
	// Receipt retention has a finite deduplication window. Deleting terminal
	// receipt metadata does not delete or rewrite previously admitted inputs.
	receivedAgain := f.receive(t, terminal[0].EventID)
	require.NotEqual(t, terminal[0].ID, receivedAgain.ID)
	require.Equal(t, integrationstore.IntegrationEventPending, receivedAgain.State)
}
