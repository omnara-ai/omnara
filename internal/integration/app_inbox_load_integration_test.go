//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

// Opt-in experiment, not a CI performance gate. The existing fixture creates
// and drops a guarded omnara_test_* database; the supplied database is never used
// for product writes. See plans/composable-agent-apps/inbox-load-results.md.
func TestAppInboxLocalLoad(t *testing.T) {
	if os.Getenv("OMNARA_INBOX_LOAD") != "1" {
		t.Skip("set OMNARA_INBOX_LOAD=1 and OMNARA_TEST_DATABASE_URL for the local load experiment")
	}
	seconds := 20
	if value := os.Getenv("OMNARA_INBOX_LOAD_SECONDS"); value != "" {
		var err error
		seconds, err = strconv.Atoi(value)
		require.NoError(t, err)
		require.True(t, seconds >= 1 && seconds <= 300, "load duration must be 1–300 seconds")
	}
	t.Logf("host_cpus=%d gomaxprocs=%d duration_s=%d "+
		"history=100000 apps=16 intake_clients=8 competing_readers=2 payload_bytes=1024",
		runtime.NumCPU(), runtime.GOMAXPROCS(0), seconds)
	for _, scenario := range []struct {
		name         string
		capacity     int
		poolSize     int32
		holdSnapshot bool
	}{
		{"workers4_pool10", 4, 10, false},
		{"workers16_pool10", 16, 10, false},
		{"workers16_pool20", 16, 20, false},
		{"workers16_pool10_snapshot", 16, 10, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			runInboxLoad(t, scenario.capacity, scenario.poolSize, scenario.holdSnapshot, time.Duration(seconds)*time.Second)
		})
	}
}

// Keep raw duration samples only for this short, opt-in run. No histogram or
// instrumentation framework is introduced into the product or ordinary tests.
type inboxLoadSamples struct {
	mu     sync.Mutex
	values []time.Duration
}

func (s *inboxLoadSamples) add(value time.Duration) {
	s.mu.Lock()
	s.values = append(s.values, value)
	s.mu.Unlock()
}

func (s *inboxLoadSamples) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.values)
}

func (s *inboxLoadSamples) report(t *testing.T, name string) {
	t.Helper()
	s.mu.Lock()
	values := slices.Clone(s.values)
	s.mu.Unlock()
	if len(values) == 0 {
		t.Logf("latency=%s samples=0", name)
		return
	}
	slices.Sort(values)
	ms := func(percent int) float64 {
		return float64(values[(len(values)-1)*percent/100]) / float64(time.Millisecond)
	}
	t.Logf("latency=%s samples=%d p50_ms=%.3f p95_ms=%.3f p99_ms=%.3f max_ms=%.3f",
		name, len(values), ms(50), ms(95), ms(99), ms(100))
}

func runInboxLoad(t *testing.T, capacity int, poolSize int32, holdSnapshot bool, duration time.Duration) {
	t.Helper()
	fixturePool, _, ids, firstApp := appWorkerFixture(t)
	config := fixturePool.Config()
	config.MaxConns = poolSize
	fixturePool.Close()
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	require.NoError(t, err)
	t.Cleanup(pool.Close) // Runs before the fixture drops its isolated database.
	store := storage.NewStore(pool)
	inbox := store.Integrations()
	ctx, cancel := context.WithTimeout(t.Context(), duration+2*time.Minute)
	defer cancel()
	apps := []uuid.UUID{firstApp}
	for i := 1; i < 16; i++ {
		id := uuid.Must(uuid.NewV7())
		_, err := pool.Exec(ctx, `INSERT INTO project_apps
 (id,org_id,project_id,installed_by_user_id,state,provider_tenant_id,provider_account_ref,
  name,app_type,credential_secret_id,created_at,updated_at)
 SELECT $2,org_id,project_id,installed_by_user_id,state,provider_tenant_id,provider_account_ref,
  $3,app_type,credential_secret_id,now(),now() FROM project_apps WHERE id=$1`,
			firstApp, id, fmt.Sprintf("load-%d", i))
		require.NoError(t, err)
		apps = append(apps, id)
	}
	// A valid JSON envelope, below TOAST's ordinary threshold, with nontrivial
	// content rather than an easily compressed multi-kilobyte repeated string.
	noise := make([]byte, 744)
	_, err = rand.Read(noise)
	require.NoError(t, err)
	payload := []byte(`{"type":"load","body":"` + base64.StdEncoding.EncodeToString(noise) + `"}`)
	payload = append(payload, []byte("       ")...)
	require.Equal(t, 1024, len(payload))
	require.True(t, json.Valid(payload))
	_, err = pool.Exec(ctx, `INSERT INTO integration_inbox
 (project_id,app_id,receipt_key,payload,state,plan,created_at,updated_at,completed_at)
 SELECT $1,($2::uuid[])[1+g%16],'history:'||g,$3,
 CASE WHEN g%10=9 THEN 'failed' ELSE 'completed' END,'{}',
 now()-interval '9 days',now()-interval '9 days',
 CASE WHEN g%10=9 THEN NULL WHEN g%10<4 THEN now()-interval '8 days' ELSE now()-interval '6 days' END
 FROM generate_series(1,100000) g`, ids.ProjectID, apps, payload)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "VACUUM (ANALYZE) integration_inbox")
	require.NoError(t, err)
	logInboxLoadStorage(t, ctx, pool, "seeded")
	// Remove connection creation from the timed comparison.
	connections := make([]*pgxpool.Conn, 0, poolSize)
	for range poolSize {
		connection, err := pool.Acquire(ctx)
		require.NoError(t, err)
		connections = append(connections, connection)
	}
	for _, connection := range connections {
		connection.Release()
	}
	var snapshot pgx.Tx
	if holdSnapshot {
		snapshot, err = pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		require.NoError(t, err)
		defer func() { _ = snapshot.Rollback(context.WithoutCancel(ctx)) }()
		var rows int
		require.NoError(t, snapshot.QueryRow(ctx, "SELECT count(*) FROM integration_inbox").Scan(&rows))
		require.Equal(t, 100000, rows)
	}
	var admission, completion, age, competing inboxLoadSamples
	var sequence, deleted atomic.Int64
	var cleanupCalls int64
	var cleanupBusy time.Duration
	var cleanupDrainedAt time.Duration
	consumer := appWorkerConsumerFunc(func(
		callCtx context.Context, lease integrationstore.IntegrationInboxLease,
	) ([]AppSlotAdmission, error) {
		started := time.Now()
		var created time.Time
		err := inbox.WithIntegrationInboxLease(callCtx, lease, func(work *integrationstore.IntegrationInboxLeaseTx) error {
			created = work.Receipt().CreatedAt
			// A real, deliberately empty frozen plan: no synthetic agent admission.
			if err := work.FreezePlan(callCtx, json.RawMessage(`{}`)); err != nil {
				return err
			}
			return work.Complete(callCtx)
		})
		if err == nil {
			completion.add(time.Since(started))
			age.add(time.Since(created))
		}
		return nil, err
	})
	worker := NewAppInboxWorker(inbox, consumer, AppInboxWorkerOptions{
		Capacity: capacity, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	before := pool.Stat()
	started := time.Now()
	phase, stop := context.WithTimeout(ctx, duration)
	defer stop()
	group, workCtx := errgroup.WithContext(ctx)
	// RunOnce is the production discovery/claim/consume path. Like Run, share
	// one worker and pause empty polls; surface any errors instead of retrying
	// silently. Finish in-flight transactions after the measurement timer fires.
	for range capacity {
		group.Go(func() error {
			for phase.Err() == nil && workCtx.Err() == nil {
				worked, err := worker.RunOnce(workCtx)
				if err != nil {
					return err
				}
				if !worked {
					if err := waitInboxLoad(phase, workCtx, time.Second); err != nil {
						return err
					}
				}
			}
			return nil
		})
	}
	for range 8 {
		group.Go(func() error {
			for phase.Err() == nil && workCtx.Err() == nil {
				n := sequence.Add(1)
				begin := time.Now()
				_, created, err := inbox.AcceptIntegrationReceipt(workCtx, integrationstore.VerifiedIntegrationReceipt{
					ProjectID: ids.ProjectID, AppID: apps[n%int64(len(apps))], ReceiptKey: fmt.Sprintf("live:%d", n), Payload: payload,
				})
				if err != nil {
					return err
				}
				if !created {
					return fmt.Errorf("unexpected duplicate receipt %d", n)
				}
				admission.add(time.Since(begin))
			}
			return nil
		})
	}
	group.Go(func() error {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for phase.Err() == nil && workCtx.Err() == nil {
			// Match maintenance's 250 ms work budget per one-second tick,
			// allowing the last batch to finish so measured deletes are exact.
			until := time.Now().Add(250 * time.Millisecond)
			for time.Now().Before(until) && phase.Err() == nil {
				begin := time.Now()
				n, err := inbox.CleanupTerminalIntegrationInbox(workCtx, 7*24*time.Hour, 100)
				if err != nil {
					return err
				}
				cleanupCalls++
				if n > 0 {
					cleanupBusy += time.Since(begin)
					deleted.Add(n)
				}
				if n < 100 {
					if cleanupDrainedAt == 0 {
						cleanupDrainedAt = time.Since(started)
					}
					break
				}
			}
			select {
			case <-phase.Done():
				return nil
			case <-workCtx.Done():
				return workCtx.Err()
			case <-tick.C:
			}
		}
		return nil
	})
	for range 2 {
		group.Go(func() error {
			for phase.Err() == nil && workCtx.Err() == nil {
				begin := time.Now()
				// Competing ordinary dashboard metadata reads share this exact pool.
				_, err := inbox.GetProjectApp(workCtx, ids.ProjectID, firstApp)
				if err != nil {
					return err
				}
				competing.add(time.Since(begin))
				if err := waitInboxLoad(phase, workCtx, 10*time.Millisecond); err != nil {
					return err
				}
			}
			return nil
		})
	}
	group.Go(func() error {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-phase.Done():
				return nil
			case <-workCtx.Done():
				return workCtx.Err()
			case <-ticker.C:
				oldest, err := inbox.OldestReadyIntegrationInboxLag(workCtx)
				if err != nil {
					return err
				}
				admitted, completed := admission.count(), completion.count()
				t.Logf("sample elapsed_s=%.2f admitted=%d completed=%d backlog=%d oldest_ready_s=%.3f acquired=%d deleted=%d",
					time.Since(started).Seconds(), admitted, completed,
					admitted-completed, oldest.Seconds(), pool.Stat().AcquiredConns(), deleted.Load())
			}
		}
	})
	require.NoError(t, group.Wait())
	elapsed := time.Since(started).Seconds()
	after := pool.Stat()
	acquires := after.AcquireCount() - before.AcquireCount()
	t.Logf("result elapsed_s=%.3f admission_s=%.2f completion_s=%.2f admitted=%d completed=%d backlog=%d competing_s=%.2f",
		elapsed, float64(admission.count())/elapsed, float64(completion.count())/elapsed,
		admission.count(), completion.count(), admission.count()-completion.count(), float64(competing.count())/elapsed)
	t.Logf("pool max=%d acquire_count=%d empty_acquires=%d "+
		"acquire_wait_total_s=%.3f acquire_mean_ms=%.3f canceled_acquires=%d",
		poolSize, acquires, after.EmptyAcquireCount()-before.EmptyAcquireCount(),
		(after.EmptyAcquireWaitTime() - before.EmptyAcquireWaitTime()).Seconds(),
		float64(after.AcquireDuration()-before.AcquireDuration())/float64(time.Millisecond)/float64(max(acquires, 1)),
		after.CanceledAcquireCount()-before.CanceledAcquireCount())
	t.Logf("cleanup deleted=%d calls=%d busy_s=%.3f drained_at_s=%.3f window_rows_s=%.2f busy_rows_s=%.2f",
		deleted.Load(), cleanupCalls, cleanupBusy.Seconds(), cleanupDrainedAt.Seconds(), float64(deleted.Load())/elapsed,
		float64(deleted.Load())/max(cleanupBusy.Seconds(), 0.001))
	admission.report(t, "admission")
	completion.report(t, "completion_transaction")
	age.report(t, "receipt_to_completion")
	competing.report(t, "competing_read")
	var live, done, pending, expired int
	require.NoError(t, pool.QueryRow(ctx, `SELECT
 count(*) FILTER (WHERE receipt_key LIKE 'live:%'),
 count(*) FILTER (WHERE receipt_key LIKE 'live:%' AND state='completed'),
 count(*) FILTER (WHERE receipt_key LIKE 'live:%' AND state='pending'),
 count(*) FILTER (WHERE state='completed' AND completed_at<now()-interval '7 days')
 FROM integration_inbox`).Scan(&live, &done, &pending, &expired))
	require.Equal(t, admission.count(), live)
	require.Equal(t, completion.count(), done)
	require.Equal(t, live-done, pending, "all in-flight claims must finish; backlog remains durable")
	require.EqualValues(t, 40000-deleted.Load(), expired)
	oldest, err := inbox.OldestReadyIntegrationInboxLag(ctx)
	require.NoError(t, err)
	t.Logf("durable live=%d completed=%d pending=%d expired_remaining=%d oldest_ready_s=%.3f",
		live, done, pending, expired, oldest.Seconds())
	if snapshot != nil {
		var visible int
		require.NoError(t, snapshot.QueryRow(ctx, "SELECT count(*) FROM integration_inbox").Scan(&visible))
		require.Equal(t, 100000, visible, "the held snapshot still sees deleted historical receipts")
		_, err = pool.Exec(ctx, "VACUUM (ANALYZE) integration_inbox")
		require.NoError(t, err)
		logInboxLoadStorage(t, ctx, pool, "vacuum_snapshot_held")
		require.NoError(t, snapshot.Rollback(ctx))
	}
	_, err = pool.Exec(ctx, "VACUUM (ANALYZE) integration_inbox")
	require.NoError(t, err)
	logInboxLoadStorage(t, ctx, pool, "vacuum_no_snapshot")
}

func waitInboxLoad(phase, ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-phase.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func logInboxLoadStorage(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) {
	t.Helper()
	var heap, indexes, total, live, dead int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT pg_relation_size(relid),pg_indexes_size(relid),
 pg_total_relation_size(relid),n_live_tup,n_dead_tup FROM pg_stat_user_tables WHERE relname='integration_inbox'`).
		Scan(&heap, &indexes, &total, &live, &dead))
	t.Logf("storage stage=%s heap_bytes=%d index_bytes=%d total_bytes=%d estimated_live=%d estimated_dead=%d",
		label, heap, indexes, total, live, dead)
}
