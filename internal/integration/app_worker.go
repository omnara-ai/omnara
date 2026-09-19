package integration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

type AppInboxSchedulerStore interface {
	RecoverIntegrationInbox(context.Context, int) (int64, error)
	ListReadyIntegrationInboxConnections(context.Context, int) ([]integrationstore.IntegrationInboxConnection, error)
	ClaimIntegrationInbox(
		context.Context,
		integrationstore.ClaimIntegrationInboxInput,
	) (integrationstore.IntegrationInboxRecord, bool, error)
	WithIntegrationInboxLease(
		context.Context,
		integrationstore.IntegrationInboxLease,
		func(*integrationstore.IntegrationInboxLeaseTx) error,
	) error
}

type AppReceiptConsumer interface {
	Consume(context.Context, integrationstore.IntegrationInboxLease) ([]AppSlotAdmission, error)
}

// AppLaunchProvisioner is implemented by machinepool.Manager. Provisioning has
// its own durable recovery; it is not part of receipt completion or slot commit.
type AppLaunchProvisioner interface {
	StartLaunchProvisioning(context.Context, *slog.Logger, uuid.UUID, []uuid.UUID)
}

type AppInboxWorkerOptions struct {
	Log          *slog.Logger
	Capacity     int
	MachinePools AppLaunchProvisioner
}

type AppInboxWorker struct {
	inbox    AppInboxSchedulerStore
	consumer AppReceiptConsumer
	options  AppInboxWorkerOptions

	// One shared discovery round prevents each consumer from repeatedly taking
	// the first connection. Entries keep the store's oldest-ready order.
	readyMu sync.Mutex
	ready   []integrationstore.IntegrationInboxConnection

	recoveryMu      sync.Mutex
	recoveryRunning bool
	nextRecovery    time.Time
}

const appInboxRecoveryInterval = 30 * time.Second

func NewAppInboxWorker(
	inbox AppInboxSchedulerStore,
	consumer AppReceiptConsumer,
	options AppInboxWorkerOptions,
) *AppInboxWorker {
	if options.Log == nil {
		options.Log = slog.Default()
	}
	if options.Capacity == 0 {
		options.Capacity = 4
	}
	return &AppInboxWorker{inbox: inbox, consumer: consumer, options: options}
}

// Run drains ready receipts with bounded concurrency. Claims fence replicas;
// expired-lease recovery also runs while there is no ready work. Cancellation
// stops claiming and waits for in-flight consumers to return and release leases.
func (w *AppInboxWorker) Run(ctx context.Context) error {
	if w.inbox == nil || w.consumer == nil || w.options.Capacity < 1 ||
		w.options.Capacity > integrationstore.IntegrationInboxMaxBatch {
		return errors.New("app inbox worker requires stores and capacity between 1 and 100")
	}
	var workers sync.WaitGroup
	// Recovery is independent of traffic and consumer occupancy, including when
	// every slot is waiting on provider I/O. RunOnce shares this same throttle.
	workers.Go(func() {
		ticker := time.NewTicker(appInboxRecoveryInterval)
		defer ticker.Stop()
		for {
			if err := w.recoverDue(ctx); err != nil && ctx.Err() == nil {
				w.options.Log.Warn("recover app inbox", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
	for range w.options.Capacity {
		workers.Go(func() {
			for ctx.Err() == nil {
				worked, err := w.RunOnce(ctx)
				if ctx.Err() != nil {
					return
				}
				if err != nil && !worked {
					w.options.Log.Warn("discover app inbox work", "error", err)
				}
				if worked && err == nil {
					continue
				}
				timer := time.NewTimer(time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		})
	}
	workers.Wait()
	return nil
}

// RunOnce recovers expired claims when due and consumes at most one newly claimed receipt.
// The bool reports a claim, including partial/failed work. Errors never turn a
// reservation into an empty completed plan; Retry retains its plan and progress
// and the store fails it durably when its eight-attempt budget is exhausted.
func (w *AppInboxWorker) RunOnce(ctx context.Context) (bool, error) {
	if w.inbox == nil || w.consumer == nil {
		return false, errors.New("app inbox worker requires stores")
	}
	var failures []error
	if err := w.recoverDue(ctx); err != nil {
		failures = append(failures, err)
	}
	// A bounded round avoids spinning on stale discovery or claim races. A hot
	// connection receives only one turn while other discovered entries wait.
	for attempt := range integrationstore.IntegrationInboxMaxBatch {
		if err := ctx.Err(); err != nil {
			return false, errors.Join(append(failures, err)...)
		}
		connection, found, err := w.nextConnection(ctx, attempt == 0)
		if err != nil {
			return false, errors.Join(append(failures, err)...)
		}
		if !found {
			break
		}
		receipt, claimed, err := w.inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
			ProjectID:     connection.ProjectID,
			ConnectionID:  connection.ConnectionID,
			LeaseDuration: integrationstore.IntegrationInboxMaxLease,
		})
		if err != nil {
			failures = append(
				failures,
				fmt.Errorf(
					"claim inbox connection %s project %s: %w",
					connection.ConnectionID,
					connection.ProjectID,
					err,
				),
			)
			continue
		}
		if claimed {
			err := w.consume(ctx, receipt)
			return true, errors.Join(append(failures, err)...)
		}
	}
	return false, errors.Join(failures...)
}

func (w *AppInboxWorker) recoverDue(ctx context.Context) error {
	w.recoveryMu.Lock()
	if w.recoveryRunning || time.Now().Before(w.nextRecovery) {
		w.recoveryMu.Unlock()
		return nil
	}
	w.recoveryRunning = true
	w.nextRecovery = time.Now().Add(appInboxRecoveryInterval)
	w.recoveryMu.Unlock()
	_, err := w.inbox.RecoverIntegrationInbox(ctx, integrationstore.IntegrationInboxMaxBatch)
	w.recoveryMu.Lock()
	w.recoveryRunning = false
	w.recoveryMu.Unlock()
	return err
}

func (w *AppInboxWorker) nextConnection(
	ctx context.Context,
	discover bool,
) (integrationstore.IntegrationInboxConnection, bool, error) {
	w.readyMu.Lock()
	defer w.readyMu.Unlock()
	if len(w.ready) == 0 && discover {
		connections, err := w.inbox.ListReadyIntegrationInboxConnections(ctx, integrationstore.IntegrationInboxMaxBatch)
		if err != nil {
			return integrationstore.IntegrationInboxConnection{}, false, err
		}
		seen := make(map[integrationstore.IntegrationInboxConnection]bool, len(connections))
		for _, connection := range connections {
			if !seen[connection] {
				w.ready = append(w.ready, connection)
				seen[connection] = true
			}
		}
	}
	if len(w.ready) == 0 {
		return integrationstore.IntegrationInboxConnection{}, false, nil
	}
	connection := w.ready[0]
	w.ready = w.ready[1:]
	return connection, true, nil
}

func (w *AppInboxWorker) consume(ctx context.Context, receipt integrationstore.IntegrationInboxRecord) error {
	// Leave time within the five-minute lease to record a timed-out attempt.
	// A crashed or uncooperative consumer is still fenced by admission itself.
	deadline := time.Now().Add(integrationstore.IntegrationInboxMaxLease - 15*time.Second)
	if receipt.ClaimExpiresAt != nil && receipt.ClaimExpiresAt.Add(-15*time.Second).Before(deadline) {
		deadline = receipt.ClaimExpiresAt.Add(-15 * time.Second)
	}
	workCtx, cancel := context.WithDeadline(ctx, deadline)
	results, err := w.consumer.Consume(workCtx, receipt.Lease())
	cancel()
	for _, result := range results {
		if result.Launch != nil && w.options.MachinePools != nil {
			w.options.MachinePools.StartLaunchProvisioning(
				ctx,
				w.options.Log,
				result.Launch.Agent.OrgID,
				result.Launch.ProvisionMachineIDs,
			)
		}
	}
	if err == nil {
		if !receipt.CreatedAt.IsZero() && time.Since(receipt.CreatedAt) > time.Minute {
			w.options.Log.InfoContext(
				ctx,
				"completed delayed app inbox receipt",
				"receipt_id",
				receipt.ID,
				"connection_id",
				receipt.ConnectionID,
				"project_id",
				receipt.ProjectID,
				"attempt",
				receipt.AttemptCount,
				"receipt_age",
				time.Since(receipt.CreatedAt),
			)
		}
		return nil // Consumer completed the receipt atomically after all slot commits.
	}
	outcome := "lease_lost"
	unavailableChoice := len(receipt.Events) != 0 && errors.Is(err, ErrAppLaunchUnavailable)
	if !errors.Is(err, integrationstore.ErrIntegrationInboxLeaseLost) {
		// Shutdown must release still-owned work, too. This short transaction has
		// no provider I/O; an expired/stolen lease cannot overwrite its successor.
		retryCtx, retryCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer retryCancel()
		retryErr := w.inbox.WithIntegrationInboxLease(
			retryCtx,
			receipt.Lease(),
			func(work *integrationstore.IntegrationInboxLeaseTx) error {
				if unavailableChoice {
					return work.Fail(retryCtx, err.Error())
				}
				return work.Retry(retryCtx, appInboxRetryDelay(receipt.AttemptCount), err.Error())
			},
		)
		outcome = "retry_scheduled"
		if receipt.AttemptCount >= integrationstore.IntegrationInboxMaxAttempts || unavailableChoice {
			outcome = "failed"
		}
		if retryErr != nil {
			outcome = "retry_not_recorded"
		}
		if retryErr != nil && !errors.Is(retryErr, integrationstore.ErrIntegrationInboxLeaseLost) {
			err = errors.Join(err, fmt.Errorf("schedule inbox retry: %w", retryErr))
		}
	}
	w.options.Log.WarnContext(
		ctx,
		"app inbox admission failed",
		"receipt_id",
		receipt.ID,
		"connection_id",
		receipt.ConnectionID,
		"project_id",
		receipt.ProjectID,
		"attempt",
		receipt.AttemptCount,
		"receipt_age",
		time.Since(receipt.CreatedAt),
		"outcome",
		outcome,
		"error",
		err,
	)
	return fmt.Errorf(
		"receipt %s connection %s (attempt %d): %w",
		receipt.ID,
		receipt.ConnectionID,
		receipt.AttemptCount,
		err,
	)
}

func appInboxRetryDelay(attempt int) time.Duration {
	delay := 5 * time.Second
	for range min(max(attempt-1, 0), 6) {
		delay *= 2
	}
	return min(delay, 5*time.Minute)
}
