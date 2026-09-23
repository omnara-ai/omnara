//go:build integration

package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/apps/discord"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
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
	appTypes := appdefinition.AppTypesForProvider(provider)
	require.Len(t, appTypes, 1, "fixture requires an explicit registered type for this transport")
	_, err := pool.Exec(
		t.Context(),
		`INSERT INTO project_apps(id,org_id,project_id,installed_by_user_id,state,provider_tenant_id,provider_account_ref,name,app_type,credential_secret_id,created_at,updated_at) VALUES($1,$2,$3,$4,'active',$6,$7,'chat',$8,$5,now(),now())`,
		appSetup,
		ids.OrgID,
		ids.ProjectID,
		ids.ProviderAdminUserID,
		ids.ProviderSecretID, tenant, account, appTypes[0],
	)
	require.NoError(t, err)
	return pool, store, ids, appSetup
}

type appWorkerFailureConsumer struct {
	appWorkerConsumerFunc
	finalize func(context.Context, uuid.UUID, uuid.UUID) error
}

func (c appWorkerFailureConsumer) FinalizeFailure(ctx context.Context, project, receipt uuid.UUID) error {
	return c.finalize(ctx, project, receipt)
}

func TestAppInboxWorkerDurablePartialRetryAndExhaustion(t *testing.T) {
	pool, store, ids, appSetup := appWorkerFixture(t)
	inbox := store.Apps()
	ctx := t.Context()
	receipt, _, err := inbox.AcceptAppReceipt(
		ctx,
		appstore.VerifiedAppReceipt{
			ProjectID:  ids.ProjectID,
			AppID:      appSetup,
			ReceiptKey: "partial",
			Payload:    []byte(`{"event":"test"}`),
		},
	)
	require.NoError(t, err)
	transient := &discord.APIError{Code: discord.RateLimited, RetryAfter: time.Hour}
	consumer := appWorkerConsumerFunc(
		func(ctx context.Context, lease appstore.AppInboxLease) ([]AppSlotAdmission, error) {
			err := inbox.WithAppInboxLease(
				ctx,
				lease,
				func(work *appstore.AppInboxLeaseTx) error {
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
	finalizations := 0
	worker := NewAppInboxWorker(inbox, appWorkerFailureConsumer{appWorkerConsumerFunc: consumer,
		finalize: func(ctx context.Context, project, id uuid.UUID) error {
			stored, err := inbox.GetAppInbox(ctx, project, id)
			require.NoError(t, err)
			require.Equal(t, appstore.AppInboxFailed, stored.State)
			finalizations++
			return errors.New("notification unavailable")
		},
	}, AppInboxWorkerOptions{})
	worked, err := worker.RunOnce(ctx)
	require.True(t, worked)
	require.ErrorIs(t, err, transient)
	first, err := inbox.GetAppInbox(ctx, ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, appstore.AppInboxPending, first.State)
	require.Contains(t, first.LastError, transient.Error())
	require.Contains(t, string(first.Progress), "committed-a")
	require.Zero(t, finalizations, "no failure notice during retry")
	require.WithinDuration(t, time.Now().Add(time.Hour), first.AvailableAt, 5*time.Second)
	_, err = pool.Exec(ctx, `UPDATE app_inbox SET attempt_count=7,available_at=now() WHERE id=$1`, receipt.ID)
	require.NoError(t, err)
	worked, err = worker.RunOnce(ctx)
	require.True(t, worked)
	require.ErrorIs(t, err, transient)
	failed, err := inbox.GetAppInbox(ctx, ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, appstore.AppInboxFailed, failed.State)
	require.Equal(t, 8, failed.AttemptCount)
	require.JSONEq(t, string(first.Plan), string(failed.Plan))
	require.JSONEq(t, string(first.Progress), string(failed.Progress))
	require.Contains(t, failed.LastError, transient.Error())
	require.Equal(t, 1, finalizations)
	require.NotNil(t, failed.CompletedAt)
	duplicate, created, err := inbox.AcceptAppReceipt(ctx, appstore.VerifiedAppReceipt{
		ProjectID: ids.ProjectID, AppID: appSetup, ReceiptKey: "partial", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, receipt.ID, duplicate.ID)
	worked, err = worker.RunOnce(ctx)
	require.False(t, worked)
	require.NoError(t, err)
	fresh, created, err := inbox.AcceptAppReceipt(ctx, appstore.VerifiedAppReceipt{
		ProjectID: ids.ProjectID, AppID: appSetup, ReceiptKey: "fresh", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, receipt.ID, fresh.ID)
	worked, err = worker.RunOnce(ctx)
	require.True(t, worked)
	require.ErrorIs(t, err, transient)
	fresh, err = inbox.GetAppInbox(ctx, ids.ProjectID, fresh.ID)
	require.NoError(t, err)
	require.Equal(t, 1, fresh.AttemptCount)
	require.Equal(t, appstore.AppInboxPending, fresh.State)
}

func TestAppInboxWorkerRecoversExpiredLeaseBeforeDiscovery(t *testing.T) {
	pool, store, ids, appSetup := appWorkerFixture(t)
	inbox := store.Apps()
	ctx := t.Context()
	_, _, err := inbox.AcceptAppReceipt(
		ctx,
		appstore.VerifiedAppReceipt{
			ProjectID:  ids.ProjectID,
			AppID:      appSetup,
			ReceiptKey: "expired",
			Payload:    []byte(`{}`),
		},
	)
	require.NoError(t, err)
	old, claimed, err := inbox.ClaimAppInbox(
		ctx,
		appstore.ClaimAppInboxInput{
			ProjectID:     ids.ProjectID,
			AppID:         appSetup,
			LeaseDuration: time.Minute,
		},
	)
	require.True(t, claimed)
	require.NoError(t, err)
	_, err = pool.Exec(
		ctx,
		`UPDATE app_inbox SET claim_expires_at=now()-interval '1 second' WHERE id=$1`,
		old.ID,
	)
	require.NoError(t, err)
	worker := NewAppInboxWorker(
		inbox,
		appWorkerConsumerFunc(
			func(ctx context.Context, lease appstore.AppInboxLease) ([]AppSlotAdmission, error) {
				require.Equal(t, old.ID, lease.ReceiptID)
				require.NotEqual(t, old.ClaimToken, lease.Token)
				err := inbox.WithAppInboxLease(
					ctx,
					old.Lease(),
					func(work *appstore.AppInboxLeaseTx) error { return work.Fail(ctx, "stale") },
				)
				require.ErrorIs(t, err, appstore.ErrAppInboxLeaseLost)
				return nil, inbox.WithAppInboxLease(
					ctx,
					lease,
					func(work *appstore.AppInboxLeaseTx) error {
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
	inbox := store.Apps()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	slow, _, err := inbox.AcceptAppReceipt(
		ctx,
		appstore.VerifiedAppReceipt{
			ProjectID:  ids.ProjectID,
			AppID:      appSetup,
			ReceiptKey: "slow-file",
			Payload:    []byte(`{}`),
		},
	)
	require.NoError(t, err)
	fast, _, err := inbox.AcceptAppReceipt(
		ctx,
		appstore.VerifiedAppReceipt{
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
		func(ctx context.Context, lease appstore.AppInboxLease) ([]AppSlotAdmission, error) {
			if lease.ReceiptID == slow.ID {
				close(slowStarted)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			<-slowStarted
			err := inbox.WithAppInboxLease(
				ctx,
				lease,
				func(work *appstore.AppInboxLeaseTx) error {
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
	completed, err := inbox.GetAppInbox(ctx, ids.ProjectID, fast.ID)
	require.NoError(t, err)
	require.Equal(t, appstore.AppInboxCompleted, completed.State)
	busy, err := inbox.GetAppInbox(ctx, ids.ProjectID, slow.ID)
	require.NoError(t, err)
	require.Equal(t, appstore.AppInboxProcessing, busy.State)
	cancel()
	require.NoError(t, <-done)
}

func seedIndependentApp(
	t *testing.T, pool *pgxpool.Pool, template appstore.ProjectAppRecord, name string,
) appstore.ProjectAppRecord {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	_, err := pool.Exec(t.Context(), `INSERT INTO project_apps
		(id,org_id,project_id,name,app_type,settings,installed_by_user_id,state,
		 provider_tenant_id,provider_account_ref,credential_secret_id,provider_config,provider_identity,
		 provider_metadata,setup_revision,created_at,updated_at)
		SELECT $2,org_id,project_id,$3,app_type,settings,installed_by_user_id,state,
		 provider_tenant_id,provider_account_ref,credential_secret_id,provider_config,provider_identity,
		 provider_metadata,setup_revision,now(),now()
		FROM project_apps WHERE id=$1`, template.ID, id, name)
	require.NoError(t, err)
	app, err := storage.NewStore(pool).Apps().GetProjectApp(t.Context(), template.ProjectID, id)
	require.NoError(t, err)
	return app
}

func TestAppInboxWorkerOnlyMarkedInboundFailuresAreTerminal(t *testing.T) {
	for _, test := range []struct {
		name     string
		cause    error
		terminal bool
	}{
		{"inaccessible channel", fmt.Errorf("expand: %w: %w", ErrAppInboundPermanent,
			&discord.APIError{Code: discord.PermanentFailure, StatusCode: http.StatusForbidden}), true},
		{"missing channel", fmt.Errorf("expand: %w: %w", ErrAppInboundPermanent,
			&discord.APIError{Code: discord.PermanentFailure, StatusCode: http.StatusNotFound}), true},
		{"revoked stored authority", fmt.Errorf("recipient: %w", storeerr.ErrUnauthorized), false},
		{"unmarked channel forbidden", &discord.APIError{
			Code: discord.PermanentFailure, StatusCode: http.StatusForbidden,
		}, false},
		{"unmarked channel missing", &discord.APIError{
			Code: discord.PermanentFailure, StatusCode: http.StatusNotFound,
		}, false},
		{"provider credential rejected", &discord.APIError{
			Code: discord.PermanentFailure, StatusCode: http.StatusUnauthorized,
		}, false},
		{"rate limited", &discord.APIError{Code: discord.RateLimited, RetryAfter: time.Minute}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, store, ids, appID := appProviderFixture(t, "discord", "11", "22")
			ctx := t.Context()
			inbox := store.Apps()
			input := appstore.VerifiedAppReceipt{
				ProjectID: ids.ProjectID, AppID: appID, ReceiptKey: "disposition", Payload: []byte(`{}`),
			}
			receipt, _, err := inbox.AcceptAppReceipt(ctx, input)
			require.NoError(t, err)
			attempts, finalized := 0, 0
			consumer := appWorkerFailureConsumer{
				appWorkerConsumerFunc: func(context.Context, appstore.AppInboxLease) ([]AppSlotAdmission, error) {
					attempts++
					return nil, test.cause
				},
				finalize: func(ctx context.Context, projectID, receiptID uuid.UUID) error {
					current, err := inbox.GetAppInbox(ctx, projectID, receiptID)
					require.NoError(t, err)
					require.Equal(t, appstore.AppInboxFailed, current.State,
						"finalization must run after the terminal commit")
					require.NotNil(t, current.CompletedAt)
					finalized++
					return nil
				},
			}
			worker := NewAppInboxWorker(inbox, consumer, AppInboxWorkerOptions{})
			worked, err := worker.RunOnce(ctx)
			require.True(t, worked)
			require.ErrorIs(t, err, test.cause)
			current, err := inbox.GetAppInbox(ctx, ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, 1, current.AttemptCount)
			require.Equal(t, 1, attempts)
			if test.terminal {
				require.Equal(t, appstore.AppInboxFailed, current.State)
				require.Equal(t, 1, finalized)
				duplicate, created, err := inbox.AcceptAppReceipt(ctx, input)
				require.NoError(t, err)
				require.False(t, created)
				require.Equal(t, receipt.ID, duplicate.ID)
				worked, err = worker.RunOnce(ctx)
				require.NoError(t, err)
				require.False(t, worked)
				require.Equal(t, 1, finalized, "duplicate intake must not finalize again")
				require.Equal(t, 1, attempts, "terminal input must not retry")
			} else {
				require.Equal(t, appstore.AppInboxPending, current.State)
				require.Nil(t, current.CompletedAt)
				require.True(t, current.AvailableAt.After(time.Now()))
				require.Zero(t, finalized)
			}
		})
	}
}
