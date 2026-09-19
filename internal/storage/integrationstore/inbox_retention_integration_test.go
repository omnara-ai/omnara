//go:build integration

package integrationstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestInboxRetentionUsesCompletionAgeAndSkipsLockedReceipts(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	complete := func(key string) integrationstore.IntegrationInboxRecord {
		f.accept(t, key)
		receipt := f.claim(t)
		f.mutate(t, receipt, func(work *integrationstore.IntegrationInboxLeaseTx) error {
			if err := work.FreezePlan(f.ctx, json.RawMessage(`{}`)); err != nil {
				return err
			}
			return work.Complete(f.ctx)
		})
		return f.read(t, receipt.ID)
	}
	locked, old, recent := complete("locked"), complete("old"), complete("recent")
	f.exec(t, `UPDATE integration_inbox SET completed_at=statement_timestamp()-interval '9 days' WHERE id=$1`, locked.ID)
	f.exec(t, `UPDATE integration_inbox SET completed_at=statement_timestamp()-interval '8 days' WHERE id=$1`, old.ID)
	// Receipt creation age does not replace completion age as the retention clock.
	f.exec(t, `UPDATE integration_inbox SET created_at=statement_timestamp()-interval '30 days',
 completed_at=statement_timestamp()-interval '6 days' WHERE id=$1`, recent.ID)
	f.accept(t, "failed")
	failed := f.claim(t)
	f.mutate(t, failed, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		if err := work.FreezePlan(f.ctx, json.RawMessage(`{"slot":{"planned":"identity"}}`)); err != nil {
			return err
		}
		if err := work.PrepareSlot(f.ctx, "slot", json.RawMessage(`{"digest":"frozen"}`)); err != nil {
			return err
		}
		return work.Fail(f.ctx, "operator recovery required")
	})
	failed = f.read(t, failed.ID)
	f.accept(t, "processing")
	processing := f.claim(t)
	pending := f.accept(t, "pending")
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	_, err = tx.Exec(f.ctx, `SELECT id FROM integration_inbox WHERE id=$1 FOR UPDATE`, locked.ID)
	require.NoError(t, err)
	bounded, cancel := context.WithTimeout(f.ctx, time.Second)
	defer cancel()
	count, err := f.store.CleanupTerminalIntegrationInbox(bounded, 7*24*time.Hour, 1)
	require.NoError(t, err, "cleanup must skip a busy completed receipt, not wait for its lock")
	require.EqualValues(t, 1, count)
	_, err = f.store.GetIntegrationInbox(f.ctx, f.project, old.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.Equal(t, integrationstore.IntegrationInboxCompleted, f.read(t, locked.ID).State)
	require.NoError(t, tx.Rollback(f.ctx))
	count, err = f.store.CleanupTerminalIntegrationInbox(f.ctx, 7*24*time.Hour, 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	count, err = f.store.CleanupTerminalIntegrationInbox(f.ctx, 7*24*time.Hour, 100)
	require.NoError(t, err)
	require.Zero(t, count)
	require.Equal(t, failed, f.read(t, failed.ID), "failed plan/progress remains available for recovery")
	require.Equal(t, processing, f.read(t, processing.ID))
	require.Equal(t, pending, f.read(t, pending.ID))
	recent = f.read(t, recent.ID)
	replayed, created, err := f.store.AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: f.project, ConnectionID: f.connection, ReceiptKey: "recent", Payload: recent.Payload,
	})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, recent.ID, replayed.ID)
	// Deleting the receipt ends only transport dedupe. Late delivery can become a
	// new receipt; accepted agent history and its semantic dedupe are separate.
	reaccepted := f.accept(t, "old")
	require.NotEqual(t, old.ID, reaccepted.ID)
}
