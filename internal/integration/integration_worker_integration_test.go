//go:build integration

package integration

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
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func integrationWorkerFixture(t *testing.T) (*pgxpool.Pool, *storage.Store, storagefixture.ProjectIDs, uuid.UUID) {
	t.Helper()
	return integrationProviderFixture(t, "slack", "T123", "A123")
}

func integrationProviderFixture(
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
	integrationSetup := uuid.Must(uuid.NewV7())
	integrationTypes := integrationdefinition.IntegrationTypesForProvider(provider)
	require.Len(t, integrationTypes, 1, "fixture requires an explicit registered type for this transport")
	_, err := pool.Exec(
		t.Context(),
		`INSERT INTO project_integrations(id,org_id,project_id,installed_by_user_id,state,provider_tenant_id,provider_account_ref,name,integration_type,credential_secret_id,created_at,updated_at) VALUES($1,$2,$3,$4,'active',$6,$7,'chat',$8,$5,now(),now())`,
		integrationSetup,
		ids.OrgID,
		ids.ProjectID,
		ids.ProviderAdminUserID,
		ids.ProviderSecretID, tenant, account, integrationTypes[0],
	)
	require.NoError(t, err)
	return pool, store, ids, integrationSetup
}

type integrationWorkerFailureConsumer struct {
	integrationWorkerConsumerFunc
	finalize func(context.Context, uuid.UUID, uuid.UUID) error
}

func (c integrationWorkerFailureConsumer) FinalizeFailure(ctx context.Context, project, receipt uuid.UUID) error {
	return c.finalize(ctx, project, receipt)
}

func TestIntegrationInboxWorkerDurablePartialRetryAndExhaustion(t *testing.T) {
	pool, store, ids, integrationSetup := integrationWorkerFixture(t)
	inbox := store.Integrations()
	ctx := t.Context()
	receipt, _, err := inbox.AcceptIntegrationReceipt(
		ctx,
		integrationstore.VerifiedIntegrationReceipt{
			ProjectID:     ids.ProjectID,
			IntegrationID: integrationSetup,
			ReceiptKey:    "partial",
			Payload:       []byte(`{"event":"test"}`),
		},
	)
	require.NoError(t, err)
	transient := &discord.APIError{Code: discord.RateLimited, RetryAfter: time.Hour}
	consumer := integrationWorkerConsumerFunc(
		func(ctx context.Context, lease integrationstore.IntegrationInboxLease) ([]IntegrationSlotAdmission, error) {
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
	finalizations := 0
	worker := NewIntegrationInboxWorker(inbox, integrationWorkerFailureConsumer{integrationWorkerConsumerFunc: consumer,
		finalize: func(ctx context.Context, project, id uuid.UUID) error {
			stored, err := inbox.GetIntegrationInbox(ctx, project, id)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxFailed, stored.State)
			finalizations++
			return errors.New("notification unavailable")
		},
	}, IntegrationInboxWorkerOptions{})
	worked, err := worker.RunOnce(ctx)
	require.True(t, worked)
	require.ErrorIs(t, err, transient)
	first, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxPending, first.State)
	require.Contains(t, first.LastError, transient.Error())
	require.Contains(t, string(first.Progress), "committed-a")
	require.Zero(t, finalizations, "no failure notice during retry")
	require.WithinDuration(t, time.Now().Add(time.Hour), first.AvailableAt, 5*time.Second)
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
	require.Equal(t, 1, finalizations)
	require.NotNil(t, failed.CompletedAt)
	duplicate, created, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: ids.ProjectID, IntegrationID: integrationSetup, ReceiptKey: "partial", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, receipt.ID, duplicate.ID)
	worked, err = worker.RunOnce(ctx)
	require.False(t, worked)
	require.NoError(t, err)
	fresh, created, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: ids.ProjectID, IntegrationID: integrationSetup, ReceiptKey: "fresh", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, receipt.ID, fresh.ID)
	worked, err = worker.RunOnce(ctx)
	require.True(t, worked)
	require.ErrorIs(t, err, transient)
	fresh, err = inbox.GetIntegrationInbox(ctx, ids.ProjectID, fresh.ID)
	require.NoError(t, err)
	require.Equal(t, 1, fresh.AttemptCount)
	require.Equal(t, integrationstore.IntegrationInboxPending, fresh.State)
}

func TestIntegrationInboxWorkerRecoversExpiredLeaseBeforeDiscovery(t *testing.T) {
	pool, store, ids, integrationSetup := integrationWorkerFixture(t)
	inbox := store.Integrations()
	ctx := t.Context()
	_, _, err := inbox.AcceptIntegrationReceipt(
		ctx,
		integrationstore.VerifiedIntegrationReceipt{
			ProjectID:     ids.ProjectID,
			IntegrationID: integrationSetup,
			ReceiptKey:    "expired",
			Payload:       []byte(`{}`),
		},
	)
	require.NoError(t, err)
	old, claimed, err := inbox.ClaimIntegrationInbox(
		ctx,
		integrationstore.ClaimIntegrationInboxInput{
			ProjectID:     ids.ProjectID,
			IntegrationID: integrationSetup,
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
	worker := NewIntegrationInboxWorker(
		inbox,
		integrationWorkerConsumerFunc(
			func(ctx context.Context, lease integrationstore.IntegrationInboxLease) ([]IntegrationSlotAdmission, error) {
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
		IntegrationInboxWorkerOptions{},
	)
	worked, err := worker.RunOnce(ctx)
	require.True(t, worked)
	require.NoError(t, err)
}

func TestIntegrationInboxWorkerSlowReceiptDoesNotBlockSameIntegration(t *testing.T) {
	_, store, ids, integrationSetup := integrationWorkerFixture(t)
	inbox := store.Integrations()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	slow, _, err := inbox.AcceptIntegrationReceipt(
		ctx,
		integrationstore.VerifiedIntegrationReceipt{
			ProjectID:     ids.ProjectID,
			IntegrationID: integrationSetup,
			ReceiptKey:    "slow-file",
			Payload:       []byte(`{}`),
		},
	)
	require.NoError(t, err)
	fast, _, err := inbox.AcceptIntegrationReceipt(
		ctx,
		integrationstore.VerifiedIntegrationReceipt{
			ProjectID:     ids.ProjectID,
			IntegrationID: integrationSetup,
			ReceiptKey:    "other-conversation",
			Payload:       []byte(`{}`),
		},
	)
	require.NoError(t, err)
	slowStarted := make(chan struct{})
	fastCompleted := make(chan error, 1)
	consumer := integrationWorkerConsumerFunc(
		func(ctx context.Context, lease integrationstore.IntegrationInboxLease) ([]IntegrationSlotAdmission, error) {
			if lease.ReceiptID == slow.ID {
				close(slowStarted)
				<-ctx.Done()
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
	worker := NewIntegrationInboxWorker(inbox, consumer, IntegrationInboxWorkerOptions{Capacity: 2})
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case err := <-fastCompleted:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("slow receipt blocked another conversation on the same integration")
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

func seedIndependentIntegration(
	t *testing.T, pool *pgxpool.Pool, template integrationstore.ProjectIntegrationRecord, name string,
) integrationstore.ProjectIntegrationRecord {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	_, err := pool.Exec(t.Context(), `INSERT INTO project_integrations
		(id,org_id,project_id,name,integration_type,settings,installed_by_user_id,state,
		 provider_tenant_id,provider_account_ref,credential_secret_id,provider_config,provider_identity,
		 provider_metadata,setup_revision,created_at,updated_at)
		SELECT $2,org_id,project_id,$3,integration_type,settings,installed_by_user_id,state,
		 provider_tenant_id,provider_account_ref,credential_secret_id,provider_config,provider_identity,
		 provider_metadata,setup_revision,now(),now()
		FROM project_integrations WHERE id=$1`, template.ID, id, name)
	require.NoError(t, err)
	integration, err := storage.NewStore(pool).Integrations().GetProjectIntegration(t.Context(), template.ProjectID, id)
	require.NoError(t, err)
	return integration
}

func TestIntegrationInboxWorkerOnlyMarkedInboundFailuresAreTerminal(t *testing.T) {
	for _, test := range []struct {
		name     string
		cause    error
		terminal bool
	}{
		{"inaccessible channel", fmt.Errorf("expand: %w: %w", ErrIntegrationInboundPermanent,
			&discord.APIError{Code: discord.PermanentFailure, StatusCode: http.StatusForbidden}), true},
		{"missing channel", fmt.Errorf("expand: %w: %w", ErrIntegrationInboundPermanent,
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
			_, store, ids, integrationID := integrationProviderFixture(t, "discord", "11", "22")
			ctx := t.Context()
			inbox := store.Integrations()
			input := integrationstore.VerifiedIntegrationReceipt{
				ProjectID: ids.ProjectID, IntegrationID: integrationID, ReceiptKey: "disposition", Payload: []byte(`{}`),
			}
			receipt, _, err := inbox.AcceptIntegrationReceipt(ctx, input)
			require.NoError(t, err)
			attempts, finalized := 0, 0
			consumer := integrationWorkerFailureConsumer{
				integrationWorkerConsumerFunc: func(
					context.Context,
					integrationstore.IntegrationInboxLease,
				) ([]IntegrationSlotAdmission, error) {
					attempts++
					return nil, test.cause
				},
				finalize: func(ctx context.Context, projectID, receiptID uuid.UUID) error {
					current, err := inbox.GetIntegrationInbox(ctx, projectID, receiptID)
					require.NoError(t, err)
					require.Equal(t, integrationstore.IntegrationInboxFailed, current.State,
						"finalization must run after the terminal commit")
					require.NotNil(t, current.CompletedAt)
					finalized++
					return nil
				},
			}
			worker := NewIntegrationInboxWorker(inbox, consumer, IntegrationInboxWorkerOptions{})
			worked, err := worker.RunOnce(ctx)
			require.True(t, worked)
			require.ErrorIs(t, err, test.cause)
			current, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, 1, current.AttemptCount)
			require.Equal(t, 1, attempts)
			if test.terminal {
				require.Equal(t, integrationstore.IntegrationInboxFailed, current.State)
				require.Equal(t, 1, finalized)
				duplicate, created, err := inbox.AcceptIntegrationReceipt(ctx, input)
				require.NoError(t, err)
				require.False(t, created)
				require.Equal(t, receipt.ID, duplicate.ID)
				worked, err = worker.RunOnce(ctx)
				require.NoError(t, err)
				require.False(t, worked)
				require.Equal(t, 1, finalized, "duplicate intake must not finalize again")
				require.Equal(t, 1, attempts, "terminal input must not retry")
			} else {
				require.Equal(t, integrationstore.IntegrationInboxPending, current.State)
				require.Nil(t, current.CompletedAt)
				require.True(t, current.AvailableAt.After(time.Now()))
				require.Zero(t, finalized)
			}
		})
	}
}
