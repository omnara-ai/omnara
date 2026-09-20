//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func appWorkerFixture(t *testing.T) (*pgxpool.Pool, *storage.Store, storagefixture.ProjectIDs, uuid.UUID) {
	t.Helper()
	return appProviderFixture(t, "slack", "T123", "A123")
}

func appProviderFixture(
	t *testing.T, provider, tenant, account string,
) (*pgxpool.Pool, *storage.Store, storagefixture.ProjectIDs, uuid.UUID) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	pool := integrationdb.OpenMigratedPool(t, t.Context(), filepath.Join(filepath.Dir(file), "../../migrations"))
	ids := storagefixture.ProjectIDs{
		OrgID:                   uuid.New(),
		ProjectID:               uuid.New(),
		ProviderAdminUserID:     uuid.New(),
		ProviderSecretID:        uuid.New(),
		ProviderSecretVersionID: uuid.New(),
		ProviderConfigID:        uuid.New(),
	}
	storagefixture.SeedProject(t, t.Context(), pool, ids, time.Now())
	store := storage.NewStore(pool)
	appSetup := uuid.Must(uuid.NewV7())
	_, err := pool.Exec(
		t.Context(),
		`INSERT INTO project_apps(id,org_id,project_id,installed_by_user_id,provider,state,provider_tenant_id,provider_account_ref,name,definition_id,credential_secret_id,created_at,updated_at) VALUES($1,$2,$3,$4,$6,'active',$7,$8,'chat',$9,$5,now(),now())`,
		appSetup,
		ids.OrgID,
		ids.ProjectID,
		ids.ProviderAdminUserID,
		ids.ProviderSecretID, provider, tenant, account, "omnara."+provider,
	)
	require.NoError(t, err)
	return pool, store, ids, appSetup
}

func TestAppInboxWorkerDurablePartialRetryAndExhaustion(t *testing.T) {
	pool, store, ids, appSetup := appWorkerFixture(t)
	inbox := store.Integrations()
	ctx := t.Context()
	receipt, _, err := inbox.AcceptIntegrationReceipt(
		ctx,
		integrationstore.VerifiedIntegrationReceipt{
			ProjectID:  ids.ProjectID,
			AppID:      appSetup,
			ReceiptKey: "partial",
			Payload:    []byte(`{"event":"test"}`),
		},
	)
	require.NoError(t, err)
	transient := errors.New("recipient B temporarily unavailable")
	consumer := appWorkerConsumerFunc(
		func(ctx context.Context, lease integrationstore.IntegrationInboxLease) ([]AppSlotAdmission, error) {
			err := inbox.WithIntegrationInboxLease(
				ctx,
				lease,
				func(work *integrationstore.IntegrationInboxLeaseTx) error {
					if err := work.FreezePlan(
						ctx,
						json.RawMessage(`{"a":{"agent":"pinned-a"},"b":{"agent":"pinned-b"}}`),
					); err != nil {
						return err
					}
					if err := work.PrepareSlot(ctx, "a", json.RawMessage(`{"files":[]}`)); err != nil {
						return err
					}
					return work.CommitSlot(ctx, "a", json.RawMessage(`{"input":"committed-a"}`))
				},
			)
			return nil, errors.Join(err, transient)
		},
	)
	worker := NewAppInboxWorker(inbox, consumer, AppInboxWorkerOptions{})
	worked, err := worker.RunOnce(ctx)
	require.True(t, worked)
	require.ErrorIs(t, err, transient)
	first, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxPending, first.State)
	require.Contains(t, first.LastError, transient.Error())
	require.Contains(t, string(first.Progress), "committed-a")
	// Fast-forward the durable attempt budget, preserving exactly the frozen work.
	_, err = pool.Exec(ctx, `UPDATE integration_inbox SET attempt_count=7,available_at=now() WHERE id=$1`, receipt.ID)
	require.NoError(t, err)
	worked, err = worker.RunOnce(ctx)
	require.True(t, worked)
	require.ErrorIs(t, err, transient)
	failed, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxFailed, failed.State)
	require.Equal(t, 8, failed.AttemptCount)
	require.JSONEq(t, string(first.Plan), string(failed.Plan))
	require.JSONEq(t, string(first.Progress), string(failed.Progress))
	require.Contains(t, failed.LastError, transient.Error())
	// Explicit operator recovery grants a budget; it does not rebuild identities.
	require.NoError(t, inbox.RetryFailedIntegrationInbox(ctx, ids.ProjectID, receipt.ID))
	worker.consumer = appWorkerConsumerFunc(
		func(ctx context.Context, lease integrationstore.IntegrationInboxLease) ([]AppSlotAdmission, error) {
			return nil, inbox.WithIntegrationInboxLease(
				ctx,
				lease,
				func(work *integrationstore.IntegrationInboxLeaseTx) error {
					require.JSONEq(t, string(first.Plan), string(work.Receipt().Plan))
					if err := work.PrepareSlot(ctx, "b", json.RawMessage(`{}`)); err != nil {
						return err
					}
					if err := work.CommitSlot(ctx, "b", json.RawMessage(`{"input":"committed-b"}`)); err != nil {
						return err
					}
					return work.Complete(ctx)
				},
			)
		},
	)
	worked, err = worker.RunOnce(ctx)
	require.True(t, worked)
	require.NoError(t, err)
	completed, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxCompleted, completed.State)
	require.Contains(t, string(completed.Progress), "committed-a")
	require.Contains(t, string(completed.Progress), "committed-b")
	worked, err = worker.RunOnce(ctx)
	require.False(t, worked)
	require.NoError(t, err)
}

func TestAppInboxWorkerRecoversExpiredLeaseBeforeDiscovery(t *testing.T) {
	pool, store, ids, appSetup := appWorkerFixture(t)
	inbox := store.Integrations()
	ctx := t.Context()
	_, _, err := inbox.AcceptIntegrationReceipt(
		ctx,
		integrationstore.VerifiedIntegrationReceipt{
			ProjectID:  ids.ProjectID,
			AppID:      appSetup,
			ReceiptKey: "expired",
			Payload:    []byte(`{}`),
		},
	)
	require.NoError(t, err)
	old, claimed, err := inbox.ClaimIntegrationInbox(
		ctx,
		integrationstore.ClaimIntegrationInboxInput{
			ProjectID:     ids.ProjectID,
			AppID:         appSetup,
			LeaseDuration: time.Minute,
		},
	)
	require.True(t, claimed)
	require.NoError(t, err)
	_, err = pool.Exec(
		ctx,
		`UPDATE integration_inbox SET claim_expires_at=now()-interval '1 second' WHERE id=$1`,
		old.ID,
	)
	require.NoError(t, err)
	worker := NewAppInboxWorker(
		inbox,
		appWorkerConsumerFunc(
			func(ctx context.Context, lease integrationstore.IntegrationInboxLease) ([]AppSlotAdmission, error) {
				require.Equal(t, old.ID, lease.ReceiptID)
				require.NotEqual(t, old.ClaimToken, lease.Token)
				err := inbox.WithIntegrationInboxLease(
					ctx,
					old.Lease(),
					func(work *integrationstore.IntegrationInboxLeaseTx) error { return work.Fail(ctx, "stale") },
				)
				require.ErrorIs(t, err, integrationstore.ErrIntegrationInboxLeaseLost)
				return nil, inbox.WithIntegrationInboxLease(
					ctx,
					lease,
					func(work *integrationstore.IntegrationInboxLeaseTx) error {
						if err := work.FreezePlan(ctx, json.RawMessage(`{}`)); err != nil {
							return err
						}
						return work.Complete(ctx)
					},
				)
			},
		),
		AppInboxWorkerOptions{},
	)
	worked, err := worker.RunOnce(ctx)
	require.True(t, worked)
	require.NoError(t, err)
}

func TestAppInboxWorkerSlowReceiptDoesNotBlockSameApp(t *testing.T) {
	_, store, ids, appSetup := appWorkerFixture(t)
	inbox := store.Integrations()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	slow, _, err := inbox.AcceptIntegrationReceipt(
		ctx,
		integrationstore.VerifiedIntegrationReceipt{
			ProjectID:  ids.ProjectID,
			AppID:      appSetup,
			ReceiptKey: "slow-file",
			Payload:    []byte(`{}`),
		},
	)
	require.NoError(t, err)
	fast, _, err := inbox.AcceptIntegrationReceipt(
		ctx,
		integrationstore.VerifiedIntegrationReceipt{
			ProjectID:  ids.ProjectID,
			AppID:      appSetup,
			ReceiptKey: "other-conversation",
			Payload:    []byte(`{}`),
		},
	)
	require.NoError(t, err)
	slowStarted := make(chan struct{})
	fastCompleted := make(chan error, 1)
	consumer := appWorkerConsumerFunc(
		func(ctx context.Context, lease integrationstore.IntegrationInboxLease) ([]AppSlotAdmission, error) {
			if lease.ReceiptID == slow.ID {
				close(slowStarted)
				<-ctx.Done() // Simulate a provider file read awaiting its context.
				return nil, ctx.Err()
			}
			<-slowStarted
			err := inbox.WithIntegrationInboxLease(
				ctx,
				lease,
				func(work *integrationstore.IntegrationInboxLeaseTx) error {
					if err := work.FreezePlan(ctx, json.RawMessage(`{}`)); err != nil {
						return err
					}
					return work.Complete(ctx)
				},
			)
			fastCompleted <- err
			return nil, err
		},
	)
	worker := NewAppInboxWorker(inbox, consumer, AppInboxWorkerOptions{Capacity: 2})
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case err := <-fastCompleted:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("slow receipt blocked another conversation on the same app")
	}
	completed, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, fast.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxCompleted, completed.State)
	busy, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, slow.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxProcessing, busy.State)
	cancel()
	require.NoError(t, <-done)
}

// seedIndependentApp gives a second saved app its own lifecycle and receipt
// identity while deliberately using the same physical provider credentials.
func seedIndependentApp(
	t *testing.T, pool *pgxpool.Pool, template integrationstore.ProjectAppRecord, name string,
) integrationstore.ProjectAppRecord {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	_, err := pool.Exec(t.Context(), `INSERT INTO project_apps
		(id,org_id,project_id,name,definition_id,settings,installed_by_user_id,provider,state,
		 provider_tenant_id,provider_account_ref,credential_secret_id,provider_config,provider_identity,
		 provider_metadata,setup_revision,created_at,updated_at)
		SELECT $2,org_id,project_id,$3,definition_id,settings,installed_by_user_id,provider,state,
		 provider_tenant_id,provider_account_ref,credential_secret_id,provider_config,provider_identity,
		 provider_metadata,setup_revision,now(),now()
		FROM project_apps WHERE id=$1`, template.ID, id, name)
	require.NoError(t, err)
	app, err := storage.NewStore(pool).Integrations().GetProjectApp(t.Context(), template.ProjectID, id)
	require.NoError(t, err)
	return app
}
