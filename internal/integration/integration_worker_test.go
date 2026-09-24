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
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

type integrationWorkerConsumerFunc func(
	context.Context,
	integrationstore.IntegrationInboxLease,
) ([]IntegrationSlotAdmission, error)

func (f integrationWorkerConsumerFunc) Consume(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
) ([]IntegrationSlotAdmission, error) {
	return f(ctx, lease)
}

type integrationWorkerTestStore struct {
	mu sync.Mutex
	IntegrationInboxSchedulerStore
	claimed      int
	recovered    int
	ready        bool
	retried      []integrationstore.IntegrationInboxLease
	integrations []integrationstore.IntegrationInboxIntegration
	claimOrder   []uuid.UUID
	scans        int
	noClaim      bool
	discover     func(integrationstore.IntegrationInboxIntegration) integrationstore.IntegrationInboxIntegrationPage
	recoverBatch func(context.Context, int) (int64, error)
	sampleLag    func(context.Context) (time.Duration, error)
	sampled      int
}

func (s *integrationWorkerTestStore) RecoverIntegrationInbox(ctx context.Context, limit int) (int64, error) {
	s.mu.Lock()
	s.recovered++
	s.mu.Unlock()
	if s.recoverBatch != nil {
		return s.recoverBatch(ctx, limit)
	}
	return 0, nil
}

func (s *integrationWorkerTestStore) OldestReadyIntegrationInboxLag(ctx context.Context) (time.Duration, error) {
	s.mu.Lock()
	s.sampled++
	s.mu.Unlock()
	if s.sampleLag != nil {
		return s.sampleLag(ctx)
	}
	return 0, nil
}

func (s *integrationWorkerTestStore) ListReadyIntegrationInboxIntegrations(
	_ context.Context,
	after integrationstore.IntegrationInboxIntegration,
	_ int,
) (integrationstore.IntegrationInboxIntegrationPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scans++
	if s.discover != nil {
		return s.discover(after), nil
	}
	if !s.ready {
		return integrationstore.IntegrationInboxIntegrationPage{}, nil
	}
	if len(s.integrations) > 0 {
		return integrationstore.IntegrationInboxIntegrationPage{
			Integrations: append([]integrationstore.IntegrationInboxIntegration(nil), s.integrations...),
		}, nil
	}
	return integrationstore.IntegrationInboxIntegrationPage{
		Integrations: []integrationstore.IntegrationInboxIntegration{{ProjectID: uuid.New(), IntegrationID: uuid.New()}},
	}, nil
}

func (s *integrationWorkerTestStore) ClaimIntegrationInbox(
	_ context.Context,
	input integrationstore.ClaimIntegrationInboxInput,
) (integrationstore.IntegrationInboxRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimed++
	s.claimOrder = append(s.claimOrder, input.IntegrationID)
	if s.noClaim {
		return integrationstore.IntegrationInboxRecord{}, false, nil
	}
	return integrationstore.IntegrationInboxRecord{
		ID:            uuid.New(),
		ProjectID:     input.ProjectID,
		IntegrationID: input.IntegrationID,
		CreatedAt:     time.Now().Add(-2 * time.Minute),
		AttemptCount:  1,
		ClaimToken:    uuid.New(),
	}, true, nil
}

func TestIntegrationInboxWorkerRotatesIntegrationsBeforeRevisitingHotIntegration(t *testing.T) {
	integrations := []integrationstore.IntegrationInboxIntegration{
		{ProjectID: uuid.New(), IntegrationID: uuid.New()},
		{ProjectID: uuid.New(), IntegrationID: uuid.New()},
		{ProjectID: uuid.New(), IntegrationID: uuid.New()},
	}
	store := &integrationWorkerTestStore{ready: true, integrations: integrations}
	worker := NewIntegrationInboxWorker(
		store,
		integrationWorkerConsumerFunc(
			func(context.Context, integrationstore.IntegrationInboxLease) ([]IntegrationSlotAdmission, error) {
				return nil, nil
			},
		),
		IntegrationInboxWorkerOptions{},
	)
	var want []uuid.UUID
	for range 4 {
		for _, integrationSetup := range integrations {
			worked, err := worker.RunOnce(t.Context())
			require.NoError(t, err)
			require.True(t, worked)
			want = append(want, integrationSetup.IntegrationID)
		}
	}
	require.Equal(t, want, store.claimOrder)
	require.Equal(t, 4, store.scans, "one discovery per integration round, not per receipt")
	require.Equal(t, 1, store.recovered, "receipt traffic must not multiply recovery scans")
}

func TestIntegrationInboxWorkerAdvancesPastUnavailableIntegrationsAndWraps(t *testing.T) {
	first := integrationstore.IntegrationInboxIntegration{ProjectID: uuid.New(), IntegrationID: uuid.New()}
	last := integrationstore.IntegrationInboxIntegration{ProjectID: uuid.New(), IntegrationID: uuid.New()}
	var cursors []integrationstore.IntegrationInboxIntegration
	store := &integrationWorkerTestStore{discover: func(
		after integrationstore.IntegrationInboxIntegration,
	) integrationstore.IntegrationInboxIntegrationPage {
		cursors = append(cursors, after)
		if after == (integrationstore.IntegrationInboxIntegration{}) {
			return integrationstore.IntegrationInboxIntegrationPage{NextCursor: first}
		}
		require.Equal(t, first, after)
		return integrationstore.IntegrationInboxIntegrationPage{
			Integrations: []integrationstore.IntegrationInboxIntegration{last},
		}
	}}
	worker := NewIntegrationInboxWorker(store, integrationWorkerConsumerFunc(
		func(context.Context, integrationstore.IntegrationInboxLease) ([]IntegrationSlotAdmission, error) {
			return nil, nil
		},
	), IntegrationInboxWorkerOptions{})
	for range 2 {
		worked, err := worker.RunOnce(t.Context())
		require.NoError(t, err)
		require.True(t, worked, "empty eligible pages must not cause an idle poll before ready work")
	}
	require.Equal(t, []integrationstore.IntegrationInboxIntegration{{}, first, {}, first}, cursors)
	require.Equal(t, []uuid.UUID{last.IntegrationID, last.IntegrationID}, store.claimOrder)
}

func TestIntegrationInboxWorkerBoundsUnavailableIntegrationDiscovery(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprintf("complete=%t", complete), func(t *testing.T) {
			store := &integrationWorkerTestStore{discover: func(
				integrationstore.IntegrationInboxIntegration,
			) integrationstore.IntegrationInboxIntegrationPage {
				if complete {
					return integrationstore.IntegrationInboxIntegrationPage{}
				}
				return integrationstore.IntegrationInboxIntegrationPage{
					NextCursor: integrationstore.IntegrationInboxIntegration{ProjectID: uuid.New(), IntegrationID: uuid.New()},
				}
			}}
			worker := NewIntegrationInboxWorker(store, integrationWorkerConsumerFunc(
				func(context.Context, integrationstore.IntegrationInboxLease) ([]IntegrationSlotAdmission, error) {
					t.Fatal("no ready integration must not invoke consumer")
					return nil, nil
				},
			), IntegrationInboxWorkerOptions{})
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

func TestIntegrationInboxWorkerStaleDiscoveryDoesNotSpin(t *testing.T) {
	store := &integrationWorkerTestStore{ready: true, noClaim: true}
	worker := NewIntegrationInboxWorker(
		store,
		integrationWorkerConsumerFunc(
			func(context.Context, integrationstore.IntegrationInboxLease) ([]IntegrationSlotAdmission, error) {
				t.Fatal("claim race must not invoke the consumer")
				return nil, nil
			},
		),
		IntegrationInboxWorkerOptions{},
	)
	worked, err := worker.RunOnce(t.Context())
	require.NoError(t, err)
	require.False(t, worked)
	require.Equal(t, 1, store.scans)
	require.Equal(t, 1, store.claimed)
}

func TestIntegrationInboxWorkerRecoveryContinuesWhileAllConsumersAreBusy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		integrationSetup := integrationstore.IntegrationInboxIntegration{ProjectID: uuid.New(), IntegrationID: uuid.New()}
		store := &integrationWorkerTestStore{
			ready:        true,
			integrations: []integrationstore.IntegrationInboxIntegration{integrationSetup},
		}
		started := make(chan struct{}, 1)
		consumer := integrationWorkerConsumerFunc(
			func(ctx context.Context, _ integrationstore.IntegrationInboxLease) ([]IntegrationSlotAdmission, error) {
				started <- struct{}{}
				<-ctx.Done()
				return nil, ctx.Err()
			},
		)
		worker := NewIntegrationInboxWorker(store, consumer, IntegrationInboxWorkerOptions{
			Capacity: 1, Metrics: metrics.NewIntegrationInboxRecorder(metrics.New()),
		})
		done := make(chan error, 1)
		go func() { done <- worker.Run(ctx) }()
		<-started
		synctest.Wait()
		store.mu.Lock()
		require.Equal(t, 1, store.recovered)
		store.mu.Unlock()
		time.Sleep(integrationInboxRecoveryInterval) //nolint:omnaralint // Advance synctest's virtual clock.
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

func TestIntegrationInboxWorkerSameIntegrationCanUseConcurrentConsumers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		store := &integrationWorkerTestStore{
			ready: true,
			integrations: []integrationstore.IntegrationInboxIntegration{
				{ProjectID: uuid.New(), IntegrationID: uuid.New()},
			},
		}
		started := make(chan struct{}, 2)
		worker := NewIntegrationInboxWorker(
			store,
			integrationWorkerConsumerFunc(
				func(ctx context.Context, _ integrationstore.IntegrationInboxLease) ([]IntegrationSlotAdmission, error) {
					started <- struct{}{}
					<-ctx.Done()
					return nil, ctx.Err()
				},
			),
			IntegrationInboxWorkerOptions{Capacity: 2},
		)
		done := make(chan error, 1)
		go func() { done <- worker.Run(ctx) }()
		<-started
		<-started
		synctest.Wait()
		store.mu.Lock()
		require.Equal(t, 2, store.claimed, "a slow file must not serialize the entire integration")
		store.mu.Unlock()
		cancel()
		require.NoError(t, <-done)
	})
}

func (s *integrationWorkerTestStore) WithIntegrationInboxLease(
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

func TestIntegrationInboxWorkerBoundedConcurrencyAndShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store := &integrationWorkerTestStore{ready: true}
	started := make(chan integrationstore.IntegrationInboxLease, 4)
	consumer := integrationWorkerConsumerFunc(
		func(ctx context.Context, lease integrationstore.IntegrationInboxLease) ([]IntegrationSlotAdmission, error) {
			started <- lease
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)
	worker := NewIntegrationInboxWorker(store, consumer, IntegrationInboxWorkerOptions{Capacity: 2})
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

func TestIntegrationInboxWorkerRecoversWithoutReadyIntegrations(t *testing.T) {
	store := &integrationWorkerTestStore{}
	consumer := integrationWorkerConsumerFunc(
		func(context.Context, integrationstore.IntegrationInboxLease) ([]IntegrationSlotAdmission, error) {
			t.Fatal("no receipt was claimed")
			return nil, nil
		},
	)
	worked, err := NewIntegrationInboxWorker(store, consumer, IntegrationInboxWorkerOptions{}).RunOnce(t.Context())
	require.NoError(t, err)
	require.False(t, worked)
	require.Equal(t, 1, store.recovered)
}

type integrationWorkerTestProvisioner struct{ machines []uuid.UUID }

func (p *integrationWorkerTestProvisioner) StartLaunchProvisioning(
	_ context.Context,
	_ *slog.Logger,
	_ uuid.UUID,
	ids []uuid.UUID,
) {
	p.machines = append(p.machines, ids...)
}

func TestIntegrationInboxWorkerProvisionsPartialSuccessAndDoesNotRewriteLostLease(t *testing.T) {
	store := &integrationWorkerTestStore{ready: true}
	machine := uuid.New()
	consumer := integrationWorkerConsumerFunc(
		func(context.Context, integrationstore.IntegrationInboxLease) ([]IntegrationSlotAdmission, error) {
			return []IntegrationSlotAdmission{
				{Launch: &executionstore.LaunchAgentResult{ProvisionMachineIDs: []uuid.UUID{machine}}},
			}, errors.Join(
				integrationstore.ErrIntegrationInboxLeaseLost,
				errors.New("another slot failed"),
			)
		},
	)
	provisioner := &integrationWorkerTestProvisioner{}
	worker := NewIntegrationInboxWorker(
		store,
		consumer,
		IntegrationInboxWorkerOptions{MachinePools: provisioner, Log: slog.New(slog.NewTextHandler(io.Discard, nil))},
	)
	worked, err := worker.RunOnce(t.Context())
	require.True(t, worked)
	require.ErrorIs(t, err, integrationstore.ErrIntegrationInboxLeaseLost)
	require.Equal(t, []uuid.UUID{machine}, provisioner.machines)
	require.Empty(t, store.retried)
}

func TestIntegrationInboxWorkerLogsReceiptAndRecoveryOutcome(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	transient := errors.New("provider temporarily unavailable")
	store := &integrationWorkerTestStore{ready: true}
	worker := NewIntegrationInboxWorker(
		store,
		integrationWorkerConsumerFunc(
			func(context.Context, integrationstore.IntegrationInboxLease) ([]IntegrationSlotAdmission, error) {
				return nil, transient
			},
		),
		IntegrationInboxWorkerOptions{Log: logger},
	)
	worked, err := worker.RunOnce(t.Context())
	require.True(t, worked)
	require.ErrorIs(t, err, transient)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "integration inbox admission failed", entry["msg"])
	require.NotEmpty(t, entry["receipt_id"])
	require.Equal(t, store.claimOrder[0].String(), entry["integration_id"])
	require.Equal(t, float64(1), entry["attempt"])
	require.GreaterOrEqual(t, entry["receipt_age"], float64(2*time.Minute))
	require.Equal(t, "retry_scheduled", entry["outcome"])
}

func TestIntegrationInboxWorkerContinuesFullRecoveryBatches(t *testing.T) {
	store := &integrationWorkerTestStore{}
	store.recoverBatch = func(_ context.Context, limit int) (int64, error) {
		if store.recovered <= 3 {
			return int64(limit), nil
		}
		return 1, nil
	}
	store.sampleLag = func(context.Context) (time.Duration, error) {
		return 0, errors.New("sample unavailable")
	}
	worker := NewIntegrationInboxWorker(store, nil, IntegrationInboxWorkerOptions{
		Metrics: metrics.NewIntegrationInboxRecorder(metrics.New()),
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, worker.recoverDue(t.Context()))
	require.Equal(t, 4, store.recovered, "full batches drain without a 30-second pause")
	require.Equal(t, 1, store.sampled)
	require.NoError(t, worker.recoverDue(t.Context()))
	require.Equal(t, 4, store.recovered, "a drained queue returns to the normal polling interval")
}

func TestIntegrationInboxWorkerRecoveryBudgetAndLagSampleDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &integrationWorkerTestStore{}
		store.sampleLag = func(ctx context.Context) (time.Duration, error) {
			<-ctx.Done()
			return 0, ctx.Err()
		}
		store.recoverBatch = func(ctx context.Context, limit int) (int64, error) {
			if store.recovered == 1 {
				return int64(limit), nil
			}
			if store.recovered == 2 {
				time.Sleep(2 * integrationInboxRecoveryBudget) //nolint:omnaralint // Advance synctest's virtual clock.
				require.NoError(t, ctx.Err(), "soft budget must not cancel healthy recovery SQL")
				return int64(limit), nil
			}
			return 0, nil
		}
		worker := NewIntegrationInboxWorker(store, nil, IntegrationInboxWorkerOptions{
			Metrics: metrics.NewIntegrationInboxRecorder(metrics.New()),
			Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		start := time.Now()
		require.NoError(t, worker.recoverDue(t.Context()))
		require.Equal(t, 2*integrationInboxRecoveryBudget+integrationInboxLagSampleTimeout, time.Since(start))
		require.Equal(t, 2, store.recovered)
		require.NoError(t, worker.recoverDue(t.Context()))
		require.Equal(t, 2, store.recovered, "budget exhaustion must not spin")
		time.Sleep(integrationInboxRecoveryRetry) //nolint:omnaralint // Advance synctest's virtual clock.
		require.NoError(t, worker.recoverDue(t.Context()))
		require.Equal(t, 3, store.recovered, "resume backlog recovery promptly")
		require.Equal(t, 1, store.sampled, "recovery continuation must not multiply lag samples")
	})
}

func TestIntegrationInboxWorkerRecoveryHardDeadlineAndParentCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &integrationWorkerTestStore{}
		store.recoverBatch = func(ctx context.Context, _ int) (int64, error) {
			<-ctx.Done()
			return 0, ctx.Err()
		}
		var logs bytes.Buffer
		worker := NewIntegrationInboxWorker(store, nil, IntegrationInboxWorkerOptions{
			Log: slog.New(slog.NewJSONHandler(&logs, nil)),
		})
		start := time.Now()
		require.ErrorIs(t, worker.recoverDue(t.Context()), context.DeadlineExceeded)
		require.Contains(t, logs.String(), "recover integration inbox")
		require.Contains(t, logs.String(), context.DeadlineExceeded.Error())
		require.Equal(t, integrationInboxRecoveryTimeout, time.Since(start))
		require.Equal(t, 1, store.recovered)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		worker = NewIntegrationInboxWorker(store, nil, IntegrationInboxWorkerOptions{})
		require.ErrorIs(t, worker.recoverDue(ctx), context.Canceled)
		require.Equal(t, 1, store.recovered, "parent cancellation must prevent another batch")
	})
}

func TestIntegrationInboxRetryDelayHonorsProviderHints(t *testing.T) {
	for _, test := range []struct {
		name    string
		attempt int
		err     error
		want    time.Duration
	}{
		{"initial", 1, nil, 5 * time.Second},
		{"local wins", 6, &discord.APIError{RetryAfter: time.Second}, 160 * time.Second},
		{"local cap", 100, nil, 5 * time.Minute},
		{"wrapped discord", 1, fmt.Errorf("prepare: %w", &discord.APIError{RetryAfter: time.Hour}), time.Hour},
		{"joined max", 1, errors.Join(&discord.APIError{RetryAfter: time.Minute},
			fmt.Errorf("Slack: %w", &slack.APIError{Result: slack.APIResult{RetryAfter: 2 * time.Hour}})), 2 * time.Hour},
		{"nested GitHub max", 1, errors.Join(&discord.APIError{RetryAfter: time.Minute},
			errors.Join(&slack.APIError{Result: slack.APIResult{RetryAfter: 2 * time.Hour}},
				fmt.Errorf("GitHub: %w", &github.APIError{Code: github.RateLimited, RetryAfter: 3 * time.Hour}))),
			3 * time.Hour},
		{"negative ignored", 1, &discord.APIError{RetryAfter: -time.Hour}, 5 * time.Second},
		{"storage bound", 1, &discord.APIError{RetryAfter: 48 * time.Hour}, 24 * time.Hour},
	} {
		t.Run(
			test.name,
			func(t *testing.T) { require.Equal(t, test.want, integrationInboxRetryDelay(test.attempt, test.err)) },
		)
	}
}
