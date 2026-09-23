package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

type appWorkerConsumerFunc func(context.Context, integrationstore.IntegrationInboxLease) ([]AppSlotAdmission, error)

func (f appWorkerConsumerFunc) Consume(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
) ([]AppSlotAdmission, error) {
	return f(ctx, lease)
}

type appWorkerTestStore struct {
	mu sync.Mutex
	AppInboxSchedulerStore
	claimed      int
	recovered    int
	ready        bool
	retried      []integrationstore.IntegrationInboxLease
	apps         []integrationstore.IntegrationInboxApp
	claimOrder   []uuid.UUID
	scans        int
	noClaim      bool
	discover     func(integrationstore.IntegrationInboxApp) integrationstore.IntegrationInboxAppPage
	recoverBatch func(context.Context, int) (int64, error)
	sampleLag    func(context.Context) (time.Duration, error)
	sampled      int
}

func (s *appWorkerTestStore) RecoverIntegrationInbox(ctx context.Context, limit int) (int64, error) {
	s.mu.Lock()
	s.recovered++
	s.mu.Unlock()
	if s.recoverBatch != nil {
		return s.recoverBatch(ctx, limit)
	}
	return 0, nil
}

func (s *appWorkerTestStore) OldestReadyIntegrationInboxLag(ctx context.Context) (time.Duration, error) {
	s.mu.Lock()
	s.sampled++
	s.mu.Unlock()
	if s.sampleLag != nil {
		return s.sampleLag(ctx)
	}
	return 0, nil
}

func (s *appWorkerTestStore) ListReadyIntegrationInboxApps(
	_ context.Context,
	after integrationstore.IntegrationInboxApp,
	_ int,
) (integrationstore.IntegrationInboxAppPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scans++
	if s.discover != nil {
		return s.discover(after), nil
	}
	if !s.ready {
		return integrationstore.IntegrationInboxAppPage{}, nil
	}
	if len(s.apps) > 0 {
		return integrationstore.IntegrationInboxAppPage{
			Apps: append([]integrationstore.IntegrationInboxApp(nil), s.apps...),
		}, nil
	}
	return integrationstore.IntegrationInboxAppPage{
		Apps: []integrationstore.IntegrationInboxApp{{ProjectID: uuid.New(), AppID: uuid.New()}},
	}, nil
}

func (s *appWorkerTestStore) ClaimIntegrationInbox(
	_ context.Context,
	input integrationstore.ClaimIntegrationInboxInput,
) (integrationstore.IntegrationInboxRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimed++
	s.claimOrder = append(s.claimOrder, input.AppID)
	if s.noClaim {
		return integrationstore.IntegrationInboxRecord{}, false, nil
	}
	return integrationstore.IntegrationInboxRecord{
		ID:           uuid.New(),
		ProjectID:    input.ProjectID,
		AppID:        input.AppID,
		CreatedAt:    time.Now().Add(-2 * time.Minute),
		AttemptCount: 1,
		ClaimToken:   uuid.New(),
	}, true, nil
}

func TestAppInboxWorkerRotatesAppsBeforeRevisitingHotApp(t *testing.T) {
	apps := []integrationstore.IntegrationInboxApp{
		{ProjectID: uuid.New(), AppID: uuid.New()},
		{ProjectID: uuid.New(), AppID: uuid.New()},
		{ProjectID: uuid.New(), AppID: uuid.New()},
	}
	store := &appWorkerTestStore{ready: true, apps: apps}
	worker := NewAppInboxWorker(
		store,
		appWorkerConsumerFunc(
			func(context.Context, integrationstore.IntegrationInboxLease) ([]AppSlotAdmission, error) {
				return nil, nil
			},
		),
		AppInboxWorkerOptions{},
	)
	var want []uuid.UUID
	for range 4 {
		for _, appSetup := range apps {
			worked, err := worker.RunOnce(t.Context())
			require.NoError(t, err)
			require.True(t, worked)
			want = append(want, appSetup.AppID)
		}
	}
	require.Equal(t, want, store.claimOrder)
	require.Equal(t, 4, store.scans, "one discovery per app round, not per receipt")
	require.Equal(t, 1, store.recovered, "receipt traffic must not multiply recovery scans")
}

func TestAppInboxWorkerAdvancesPastUnavailableAppsAndWraps(t *testing.T) {
	first := integrationstore.IntegrationInboxApp{ProjectID: uuid.New(), AppID: uuid.New()}
	last := integrationstore.IntegrationInboxApp{ProjectID: uuid.New(), AppID: uuid.New()}
	var cursors []integrationstore.IntegrationInboxApp
	store := &appWorkerTestStore{discover: func(
		after integrationstore.IntegrationInboxApp,
	) integrationstore.IntegrationInboxAppPage {
		cursors = append(cursors, after)
		if after == (integrationstore.IntegrationInboxApp{}) {
			return integrationstore.IntegrationInboxAppPage{NextCursor: first}
		}
		require.Equal(t, first, after)
		return integrationstore.IntegrationInboxAppPage{Apps: []integrationstore.IntegrationInboxApp{last}}
	}}
	worker := NewAppInboxWorker(store, appWorkerConsumerFunc(
		func(context.Context, integrationstore.IntegrationInboxLease) ([]AppSlotAdmission, error) {
			return nil, nil
		},
	), AppInboxWorkerOptions{})
	for range 2 {
		worked, err := worker.RunOnce(t.Context())
		require.NoError(t, err)
		require.True(t, worked, "empty eligible pages must not cause an idle poll before ready work")
	}
	require.Equal(t, []integrationstore.IntegrationInboxApp{{}, first, {}, first}, cursors)
	require.Equal(t, []uuid.UUID{last.AppID, last.AppID}, store.claimOrder)
}

func TestAppInboxWorkerBoundsUnavailableAppDiscovery(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprintf("complete=%t", complete), func(t *testing.T) {
			store := &appWorkerTestStore{discover: func(
				integrationstore.IntegrationInboxApp,
			) integrationstore.IntegrationInboxAppPage {
				if complete {
					return integrationstore.IntegrationInboxAppPage{}
				}
				return integrationstore.IntegrationInboxAppPage{
					NextCursor: integrationstore.IntegrationInboxApp{ProjectID: uuid.New(), AppID: uuid.New()},
				}
			}}
			worker := NewAppInboxWorker(store, appWorkerConsumerFunc(
				func(context.Context, integrationstore.IntegrationInboxLease) ([]AppSlotAdmission, error) {
					t.Fatal("no ready app must not invoke consumer")
					return nil, nil
				},
			), AppInboxWorkerOptions{})
			worked, err := worker.RunOnce(t.Context())
			require.NoError(t, err)
			require.False(t, worked)
			require.Zero(t, store.claimed)
			if complete {
				require.Equal(t, 1, store.scans, "stop when traversal wraps")
			} else {
				require.Equal(t, integrationstore.IntegrationInboxMaxBatch, store.scans, "bound a large traversal")
			}
		})
	}
}

func TestAppInboxWorkerStaleDiscoveryDoesNotSpin(t *testing.T) {
	store := &appWorkerTestStore{ready: true, noClaim: true}
	worker := NewAppInboxWorker(
		store,
		appWorkerConsumerFunc(
			func(context.Context, integrationstore.IntegrationInboxLease) ([]AppSlotAdmission, error) {
				t.Fatal("claim race must not invoke the consumer")
				return nil, nil
			},
		),
		AppInboxWorkerOptions{},
	)
	worked, err := worker.RunOnce(t.Context())
	require.NoError(t, err)
	require.False(t, worked)
	require.Equal(t, 1, store.scans)
	require.Equal(t, 1, store.claimed)
}

func TestAppInboxWorkerRecoveryContinuesWhileAllConsumersAreBusy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		appSetup := integrationstore.IntegrationInboxApp{ProjectID: uuid.New(), AppID: uuid.New()}
		store := &appWorkerTestStore{
			ready: true,
			apps:  []integrationstore.IntegrationInboxApp{appSetup},
		}
		started := make(chan struct{}, 1)
		consumer := appWorkerConsumerFunc(
			func(ctx context.Context, _ integrationstore.IntegrationInboxLease) ([]AppSlotAdmission, error) {
				started <- struct{}{}
				<-ctx.Done()
				return nil, ctx.Err()
			},
		)
		worker := NewAppInboxWorker(store, consumer, AppInboxWorkerOptions{
			Capacity: 1, Metrics: metrics.NewAppInboxRecorder(metrics.New()),
		})
		done := make(chan error, 1)
		go func() { done <- worker.Run(ctx) }()
		<-started
		synctest.Wait()
		store.mu.Lock()
		require.Equal(t, 1, store.recovered)
		store.mu.Unlock()
		time.Sleep(appInboxRecoveryInterval) //nolint:omnaralint // Advance synctest's virtual clock.
		synctest.Wait()
		store.mu.Lock()
		require.Equal(t, 2, store.recovered)
		require.Equal(t, 2, store.sampled, "lag sampling continues while consumers are busy")
		require.Equal(t, 1, store.claimed)
		store.mu.Unlock()
		cancel()
		require.NoError(t, <-done)
	})
}

func TestAppInboxWorkerSameAppCanUseConcurrentConsumers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		store := &appWorkerTestStore{
			ready: true,
			apps: []integrationstore.IntegrationInboxApp{
				{ProjectID: uuid.New(), AppID: uuid.New()},
			},
		}
		started := make(chan struct{}, 2)
		worker := NewAppInboxWorker(
			store,
			appWorkerConsumerFunc(
				func(ctx context.Context, _ integrationstore.IntegrationInboxLease) ([]AppSlotAdmission, error) {
					started <- struct{}{}
					<-ctx.Done()
					return nil, ctx.Err()
				},
			),
			AppInboxWorkerOptions{Capacity: 2},
		)
		done := make(chan error, 1)
		go func() { done <- worker.Run(ctx) }()
		<-started
		<-started
		synctest.Wait()
		store.mu.Lock()
		require.Equal(t, 2, store.claimed, "a slow file must not serialize the entire app")
		store.mu.Unlock()
		cancel()
		require.NoError(t, <-done)
	})
}

func (s *appWorkerTestStore) WithIntegrationInboxLease(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	_ func(*integrationstore.IntegrationInboxLeaseTx) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retried = append(s.retried, lease)
	return nil
}

func TestAppInboxWorkerBoundedConcurrencyAndShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store := &appWorkerTestStore{ready: true}
	started := make(chan integrationstore.IntegrationInboxLease, 4)
	consumer := appWorkerConsumerFunc(
		func(ctx context.Context, lease integrationstore.IntegrationInboxLease) ([]AppSlotAdmission, error) {
			started <- lease
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)
	worker := NewAppInboxWorker(store, consumer, AppInboxWorkerOptions{Capacity: 2})
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("worker did not fill its bounded capacity")
		}
	}
	store.mu.Lock()
	require.Equal(t, 2, store.claimed, "claims must not run ahead of available consumers")
	store.mu.Unlock()
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not release claims on shutdown")
	}
	store.mu.Lock()
	require.Len(t, store.retried, 2)
	require.NotEqual(t, store.retried[0].ReceiptID, store.retried[1].ReceiptID)
	require.Equal(t, 2, store.claimed)
	store.mu.Unlock()
}

func TestAppInboxWorkerRecoversWithoutReadyApps(t *testing.T) {
	store := &appWorkerTestStore{}
	consumer := appWorkerConsumerFunc(
		func(context.Context, integrationstore.IntegrationInboxLease) ([]AppSlotAdmission, error) {
			t.Fatal("no receipt was claimed")
			return nil, nil
		},
	)
	worked, err := NewAppInboxWorker(store, consumer, AppInboxWorkerOptions{}).RunOnce(t.Context())
	require.NoError(t, err)
	require.False(t, worked)
	require.Equal(t, 1, store.recovered)
}

type appWorkerTestProvisioner struct{ machines []uuid.UUID }

func (p *appWorkerTestProvisioner) StartLaunchProvisioning(
	_ context.Context,
	_ *slog.Logger,
	_ uuid.UUID,
	ids []uuid.UUID,
) {
	p.machines = append(p.machines, ids...)
}

func TestAppInboxWorkerProvisionsPartialSuccessAndDoesNotRewriteLostLease(t *testing.T) {
	store := &appWorkerTestStore{ready: true}
	machine := uuid.New()
	consumer := appWorkerConsumerFunc(
		func(context.Context, integrationstore.IntegrationInboxLease) ([]AppSlotAdmission, error) {
			return []AppSlotAdmission{
				{Launch: &executionstore.LaunchAgentResult{ProvisionMachineIDs: []uuid.UUID{machine}}},
			}, errors.Join(
				integrationstore.ErrIntegrationInboxLeaseLost,
				errors.New("another slot failed"),
			)
		},
	)
	provisioner := &appWorkerTestProvisioner{}
	worker := NewAppInboxWorker(
		store,
		consumer,
		AppInboxWorkerOptions{MachinePools: provisioner, Log: slog.New(slog.NewTextHandler(io.Discard, nil))},
	)
	worked, err := worker.RunOnce(t.Context())
	require.True(t, worked)
	require.ErrorIs(t, err, integrationstore.ErrIntegrationInboxLeaseLost)
	require.Equal(t, []uuid.UUID{machine}, provisioner.machines)
	require.Empty(t, store.retried)
}

func TestAppInboxWorkerLogsReceiptAndRecoveryOutcome(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	transient := errors.New("provider temporarily unavailable")
	store := &appWorkerTestStore{ready: true}
	worker := NewAppInboxWorker(
		store,
		appWorkerConsumerFunc(
			func(context.Context, integrationstore.IntegrationInboxLease) ([]AppSlotAdmission, error) {
				return nil, transient
			},
		),
		AppInboxWorkerOptions{Log: logger},
	)
	worked, err := worker.RunOnce(t.Context())
	require.True(t, worked)
	require.ErrorIs(t, err, transient)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "app inbox admission failed", entry["msg"])
	require.NotEmpty(t, entry["receipt_id"])
	require.Equal(t, store.claimOrder[0].String(), entry["app_id"])
	require.Equal(t, float64(1), entry["attempt"])
	require.GreaterOrEqual(t, entry["receipt_age"], float64(2*time.Minute))
	require.Equal(t, "retry_scheduled", entry["outcome"])
}

func TestAppInboxWorkerContinuesFullRecoveryBatches(t *testing.T) {
	store := &appWorkerTestStore{}
	store.recoverBatch = func(_ context.Context, limit int) (int64, error) {
		if store.recovered <= 3 {
			return int64(limit), nil
		}
		return 1, nil
	}
	store.sampleLag = func(context.Context) (time.Duration, error) {
		return 0, errors.New("sample unavailable")
	}
	worker := NewAppInboxWorker(store, nil, AppInboxWorkerOptions{
		Metrics: metrics.NewAppInboxRecorder(metrics.New()),
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, worker.recoverDue(t.Context()))
	require.Equal(t, 4, store.recovered, "full batches drain without a 30-second pause")
	require.Equal(t, 1, store.sampled)
	require.NoError(t, worker.recoverDue(t.Context()))
	require.Equal(t, 4, store.recovered, "a drained queue returns to the normal polling interval")
}

func TestAppInboxWorkerRecoveryBudgetAndLagSampleDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &appWorkerTestStore{}
		store.sampleLag = func(ctx context.Context) (time.Duration, error) {
			<-ctx.Done()
			return 0, ctx.Err()
		}
		store.recoverBatch = func(ctx context.Context, limit int) (int64, error) {
			if store.recovered == 1 {
				return int64(limit), nil
			}
			if store.recovered == 2 {
				time.Sleep(2 * appInboxRecoveryBudget) //nolint:omnaralint // Advance synctest's virtual clock.
				require.NoError(t, ctx.Err(), "soft budget must not cancel healthy recovery SQL")
				return int64(limit), nil
			}
			return 0, nil
		}
		worker := NewAppInboxWorker(store, nil, AppInboxWorkerOptions{
			Metrics: metrics.NewAppInboxRecorder(metrics.New()),
			Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		start := time.Now()
		require.NoError(t, worker.recoverDue(t.Context()))
		require.Equal(t, 2*appInboxRecoveryBudget+appInboxLagSampleTimeout, time.Since(start))
		require.Equal(t, 2, store.recovered)
		require.NoError(t, worker.recoverDue(t.Context()))
		require.Equal(t, 2, store.recovered, "budget exhaustion must not spin")
		time.Sleep(appInboxRecoveryRetry) //nolint:omnaralint // Advance synctest's virtual clock.
		require.NoError(t, worker.recoverDue(t.Context()))
		require.Equal(t, 3, store.recovered, "resume backlog recovery promptly")
		require.Equal(t, 1, store.sampled, "recovery continuation must not multiply lag samples")
	})
}

func TestAppInboxWorkerRecoveryHardDeadlineAndParentCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &appWorkerTestStore{}
		store.recoverBatch = func(ctx context.Context, _ int) (int64, error) {
			<-ctx.Done()
			return 0, ctx.Err()
		}
		var logs bytes.Buffer
		worker := NewAppInboxWorker(store, nil, AppInboxWorkerOptions{
			Log: slog.New(slog.NewJSONHandler(&logs, nil)),
		})
		start := time.Now()
		require.ErrorIs(t, worker.recoverDue(t.Context()), context.DeadlineExceeded)
		require.Contains(t, logs.String(), "recover app inbox")
		require.Contains(t, logs.String(), context.DeadlineExceeded.Error())
		require.Equal(t, appInboxRecoveryTimeout, time.Since(start))
		require.Equal(t, 1, store.recovered)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		worker = NewAppInboxWorker(store, nil, AppInboxWorkerOptions{})
		require.ErrorIs(t, worker.recoverDue(ctx), context.Canceled)
		require.Equal(t, 1, store.recovered, "parent cancellation must prevent another batch")
	})
}
