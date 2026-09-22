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

func TestAppProfileChoiceCleanupRespectsScopeAndTerminalRetention(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"active", "disconnected", "app", "project", "organization"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			f := newProfileChoiceFixture(t)
			choice := f.menu(t)
			_, err := f.store.ChooseAppProfile(f.ctx, f.chooseInput(t, choice, "support"))
			require.NoError(t, err)
			decided := f.claim(t)
			f.mutate(t, decided, func(work *integrationstore.IntegrationInboxLeaseTx) error {
				if err := work.FreezePlan(f.ctx, f.selectionPlan(t, f.app.ID, "support")); err != nil {
					return err
				}
				if err := work.PrepareSlot(f.ctx, "chosen", json.RawMessage(`{"digest":"frozen"}`)); err != nil {
					return err
				}
				return work.Fail(f.ctx, "launch failed")
			})
			failed := f.read(t, decided.ID)
			f.exec(t, `UPDATE app_states SET expires_at=now()-interval '8 days' WHERE id=$1`, choice.ID)
			choice = f.readChoice(t, choice.ID)
			input := f.input
			input.Address.Ref = "C123:456.789"
			input.SourceKey = "another-conversation"
			recent, _, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), input)
			require.NoError(t, err)
			require.NotEqual(t, choice.ID, recent.ID)
			switch scope {
			case "disconnected":
				f.exec(t, `UPDATE project_apps SET state='disconnected' WHERE id=$1`, f.appID)
			case "app":
				require.NoError(t, f.store.DeleteProjectApp(f.ctx, f.org, f.project, f.appID))
			case "project":
				f.exec(t, `UPDATE projects SET deleted_at=now() WHERE id=$1`, f.project)
			case "organization":
				f.exec(t, `UPDATE orgs SET deleted_at=now() WHERE id=$1`, f.org)
			}
			deleted := scope != "active" && scope != "disconnected"
			tx, err := f.pool.Begin(f.ctx)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(f.ctx) }()
			_, err = tx.Exec(f.ctx, `SELECT id FROM app_states WHERE id=$1 FOR UPDATE`, choice.ID)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
			defer cancel()
			count, err := f.store.CleanupAppStates(ctx, integrationstore.AppProfileChoiceMinRetention, 1)
			require.NoError(t, err)
			if deleted {
				require.EqualValues(t, 1, count, "deleted scopes remove fresh payloads without waiting seven days")
				_, err = f.store.GetAppProfileChoice(f.ctx, f.project, f.appID, recent.ID)
				require.ErrorIs(t, err, storeerr.ErrNotFound)
			} else {
				require.Zero(t, count)
				require.Equal(t, recent, f.readChoice(t, recent.ID))
			}
			require.Equal(t, choice, f.readChoice(t, choice.ID), "busy choices are deferred")
			require.NoError(t, tx.Rollback(f.ctx))
			count, err = f.store.CleanupAppStates(f.ctx, integrationstore.AppProfileChoiceMinRetention, 1)
			require.NoError(t, err)
			if deleted {
				require.EqualValues(t, 1, count, "failed work cannot retain payloads for deleted scopes")
				_, err = f.store.GetAppProfileChoice(f.ctx, f.project, f.appID, choice.ID)
				require.ErrorIs(t, err, storeerr.ErrNotFound)
			} else {
				require.Zero(t, count)
				require.Equal(t, choice, f.readChoice(t, choice.ID), "retained failures keep their source barrier")
			}
			require.Equal(t, failed, f.read(t, failed.ID), "chooser cleanup leaves the retained terminal receipt intact")
			count, err = f.store.CleanupAppStates(f.ctx, integrationstore.AppProfileChoiceMinRetention, 1)
			require.NoError(t, err)
			require.Zero(t, count)
			if !deleted {
				f.exec(t, `UPDATE integration_inbox SET completed_at=now()-interval '8 days' WHERE id=$1`, failed.ID)
				count, err = f.store.CleanupTerminalIntegrationInbox(f.ctx, 7*24*time.Hour, 1)
				require.NoError(t, err)
				require.EqualValues(t, 1, count)
				count, err = f.store.CleanupAppStates(f.ctx, integrationstore.AppProfileChoiceMinRetention, 1)
				require.NoError(t, err)
				require.EqualValues(t, 1, count, "receipt cleanup releases the old choice's source barrier")
				_, err = f.store.GetAppProfileChoice(f.ctx, f.project, f.appID, choice.ID)
				require.ErrorIs(t, err, storeerr.ErrNotFound)
				require.Equal(t, recent, f.readChoice(t, recent.ID))
			}
		})
	}
}

func TestAppProfileChoiceCleanupSharesBatchAcrossExpiryAndDeletedScopes(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	expired := f.menu(t)
	f.exec(t, `UPDATE app_states SET expires_at=now()-interval '8 days' WHERE id=$1`, expired.ID)
	input := f.input
	input.SourceKey = "another-expired"
	another, _, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), input)
	require.NoError(t, err)
	f.exec(t, `UPDATE app_states SET expires_at=now()-interval '8 days' WHERE id=$1`, another.ID)
	input.SourceKey = "recent"
	recent, _, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), input)
	require.NoError(t, err)
	deleted := f.addApp(t, "deleted", f.app.Settings).ID
	require.NoError(t, f.store.DeleteProjectApp(f.ctx, f.org, f.project, deleted))
	f.exec(t, `INSERT INTO app_states
 (project_id,app_id,kind,key,scope_kind,scope_ref,data,expires_at)
 SELECT project_id,$1,kind,key,scope_kind,scope_ref,data,expires_at
 FROM app_states WHERE id=$2`, deleted, expired.ID)
	for _, want := range []int64{2, 1, 0} {
		count, err := f.store.CleanupAppStates(f.ctx, integrationstore.AppProfileChoiceMinRetention, 2)
		require.NoError(t, err)
		require.Equal(t, want, count, "both paths share one limit over distinct rows")
	}
	require.Equal(t, recent, f.readChoice(t, recent.ID), "live recent data is outside both cleanup paths")
	var count int
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM app_states WHERE project_id=$1`, f.project).Scan(&count))
	require.Equal(t, 1, count)
}
