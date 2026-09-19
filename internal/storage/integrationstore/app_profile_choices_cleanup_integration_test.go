//go:build integration

package integrationstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestAppProfileChoiceCleanupDeletedScopesPreservesLiveRecovery(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"active", "disabled", "connection", "project", "organization"} {
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
				return work.Fail(f.ctx, "operator recovery required")
			})
			failed := f.read(t, decided.ID)
			f.exec(t, `UPDATE app_profile_choices SET expires_at=now()-interval '8 days' WHERE id=$1`, choice.ID)
			choice = f.readChoice(t, choice.ID)
			input := f.input
			input.Address.Ref = "C123:456.789"
			input.SourceKey = "another-conversation"
			recent, _, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), input)
			require.NoError(t, err)
			require.NotEqual(t, choice.ID, recent.ID)
			switch scope {
			case "disabled":
				f.exec(t, `UPDATE integration_connections SET state='disabled' WHERE id=$1`, f.connection)
			case "connection":
				require.NoError(t, f.store.DeleteIntegrationConnection(f.ctx, f.project, f.connection))
			case "project":
				f.exec(t, `UPDATE projects SET deleted_at=now() WHERE id=$1`, f.project)
			case "organization":
				f.exec(t, `UPDATE orgs SET deleted_at=now() WHERE id=$1`, f.org)
			}
			deleted := scope != "active" && scope != "disabled"
			// Deleted-scope cleanup remains bounded and must skip a busy choice.
			tx, err := f.pool.Begin(f.ctx)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(f.ctx) }()
			_, err = tx.Exec(f.ctx, `SELECT id FROM app_profile_choices WHERE id=$1 FOR UPDATE`, choice.ID)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
			defer cancel()
			count, err := f.store.CleanupAppProfileChoices(ctx, integrationstore.AppProfileChoiceMinRetention, 1)
			require.NoError(t, err)
			if deleted {
				require.EqualValues(t, 1, count, "deleted scopes remove fresh payloads without waiting seven days")
				_, err = f.store.GetAppProfileChoice(f.ctx, f.project, f.connection, recent.ID)
				require.ErrorIs(t, err, storeerr.ErrNotFound)
			} else {
				require.Zero(t, count)
				require.Equal(t, recent, f.readChoice(t, recent.ID))
			}
			require.Equal(t, choice, f.readChoice(t, choice.ID), "busy choices are deferred")
			require.NoError(t, tx.Rollback(f.ctx))
			count, err = f.store.CleanupAppProfileChoices(f.ctx, integrationstore.AppProfileChoiceMinRetention, 1)
			require.NoError(t, err)
			if deleted {
				require.EqualValues(t, 1, count, "failed work cannot retain payloads for deleted scopes")
				_, err = f.store.GetAppProfileChoice(f.ctx, f.project, f.connection, choice.ID)
				require.ErrorIs(t, err, storeerr.ErrNotFound)
			} else {
				require.Zero(t, count)
				require.Equal(t, choice, f.readChoice(t, choice.ID), "live or disabled failed work stays recoverable")
			}
			require.Equal(t, failed, f.read(t, failed.ID), "chooser cleanup leaves inbox recovery data untouched")
			count, err = f.store.CleanupAppProfileChoices(f.ctx, integrationstore.AppProfileChoiceMinRetention, 1)
			require.NoError(t, err)
			require.Zero(t, count)
		})
	}
}

func TestAppProfileChoiceCleanupSharesBatchAcrossExpiryAndDeletedScopes(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	expired := f.menu(t)
	f.exec(t, `UPDATE app_profile_choices SET expires_at=now()-interval '8 days' WHERE id=$1`, expired.ID)
	input := f.input
	input.SourceKey = "recent"
	recent, _, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), input)
	require.NoError(t, err)
	deleted := uuid.New()
	f.exec(t, `INSERT INTO integration_connections
 (id,org_id,project_id,installed_by_user_id,provider,state,provider_tenant_id,provider_account_ref,
  deleted_at,created_at,updated_at)
 SELECT $1,org_id,project_id,installed_by_user_id,provider,'disabled',provider_tenant_id,
        ($1::uuid)::text,now(),now(),now()
 FROM integration_connections WHERE id=$2`, deleted, f.connection)
	// One deleted row overlaps the expiry path; another is still recent.
	f.exec(t, `INSERT INTO app_profile_choices
 (project_id,connection_id,app_id,owner_receipt_id,address_kind,address_ref,source_key,event,payload,options,expires_at)
 SELECT project_id,$1,app_id,owner_receipt_id,address_kind,address_ref,source_key,event,payload,options,expires_at
 FROM app_profile_choices WHERE connection_id=$2`, deleted, f.connection)
	for _, want := range []int64{2, 1, 0} {
		count, err := f.store.CleanupAppProfileChoices(f.ctx, integrationstore.AppProfileChoiceMinRetention, 2)
		require.NoError(t, err)
		require.Equal(t, want, count, "both paths share one limit over distinct rows")
	}
	require.Equal(t, recent, f.readChoice(t, recent.ID), "live recent data is outside both cleanup paths")
	var count int
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM app_profile_choices WHERE project_id=$1`, f.project).Scan(&count))
	require.Equal(t, 1, count)
}
