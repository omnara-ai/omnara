//go:build integration

package appstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestInboxRetentionUsesTerminalAgeAndSkipsLockedReceipts(t *testing.T) {
	t.Parallel()
	for _, state := range []appstore.AppInboxState{
		appstore.AppInboxCompleted, appstore.AppInboxFailed,
	} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			f := newInboxFixture(t)
			finish := func(key string) appstore.AppInboxRecord {
				f.accept(t, key)
				receipt := f.claim(t)
				f.mutate(t, receipt, func(work *appstore.AppInboxLeaseTx) error {
					if err := work.FreezePlan(f.ctx, json.RawMessage(`{"slot":{"planned":"identity"}}`)); err != nil {
						return err
					}
					if err := work.PrepareSlot(f.ctx, "slot", json.RawMessage(`{"digest":"frozen"}`)); err != nil {
						return err
					}
					if state == appstore.AppInboxFailed {
						return work.Fail(f.ctx, "launch failed")
					}
					if err := work.CommitSlot(f.ctx, "slot", json.RawMessage(`{"admitted":true}`)); err != nil {
						return err
					}
					return work.Complete(f.ctx)
				})
				return f.read(t, receipt.ID)
			}
			locked, old, recent := finish("locked"), finish("old"), finish("recent")
			f.exec(t, `UPDATE app_inbox SET completed_at=now()-interval '9 days' WHERE id=$1`, locked.ID)
			f.exec(t, `UPDATE app_inbox SET completed_at=now()-interval '8 days' WHERE id=$1`, old.ID)
			f.exec(t, `UPDATE app_inbox SET created_at=now()-interval '30 days',
 completed_at=now()-interval '6 days' WHERE id=$1`, recent.ID)
			recent = f.read(t, recent.ID)
			f.accept(t, "processing")
			processing := f.claim(t)
			pending := f.accept(t, "pending")
			tx, err := f.pool.Begin(f.ctx)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(f.ctx) }()
			_, err = tx.Exec(f.ctx, `SELECT id FROM app_inbox WHERE id=$1 FOR UPDATE`, locked.ID)
			require.NoError(t, err)
			bounded, cancel := context.WithTimeout(f.ctx, time.Second)
			defer cancel()
			count, err := f.store.CleanupTerminalAppInbox(bounded, 7*24*time.Hour, 1)
			require.NoError(t, err, "cleanup must skip a busy terminal receipt")
			require.EqualValues(t, 1, count)
			_, err = f.store.GetAppInbox(f.ctx, f.project, old.ID)
			require.ErrorIs(t, err, storeerr.ErrNotFound)
			require.Equal(t, state, f.read(t, locked.ID).State)
			require.NoError(t, tx.Rollback(f.ctx))
			for _, want := range []int64{1, 0} {
				count, err = f.store.CleanupTerminalAppInbox(f.ctx, 7*24*time.Hour, 1)
				require.NoError(t, err)
				require.Equal(t, want, count)
			}
			require.Equal(t, recent, f.read(t, recent.ID), "retained payload, plan and progress stay intact")
			require.Equal(t, processing, f.read(t, processing.ID))
			require.Equal(t, pending, f.read(t, pending.ID))
			replayed, created, err := f.store.AcceptAppReceipt(f.ctx, appstore.VerifiedAppReceipt{
				ProjectID: f.project, AppID: f.appID, ReceiptKey: "recent", Payload: []byte(`{"changed":true}`),
			})
			require.NoError(t, err)
			require.False(t, created)
			require.Equal(t, recent, replayed)
			reaccepted := f.accept(t, "old")
			require.NotEqual(t, old.ID, reaccepted.ID)
		})
	}
}
