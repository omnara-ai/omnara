package integration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

type IntegrationInboxSchedulerStore interface {
	RecoverIntegrationInbox(context.Context, int) (int64, error)
	OldestReadyIntegrationInboxLag(context.Context) (time.Duration, error)
	ListReadyIntegrationInboxIntegrations(
		context.Context, integrationstore.IntegrationInboxIntegration, int,
	) (integrationstore.IntegrationInboxIntegrationPage, error)
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

type IntegrationReceiptConsumer interface {
	Consume(context.Context, integrationstore.IntegrationInboxLease) ([]IntegrationSlotAdmission, error)
}

type IntegrationLaunchProvisioner interface {
	StartLaunchProvisioning(context.Context, *slog.Logger, uuid.UUID, []uuid.UUID)
}

type IntegrationInboxWorkerOptions struct {
	Log          *slog.Logger
	Capacity     int
	MachinePools IntegrationLaunchProvisioner
	Metrics      *metrics.IntegrationInboxRecorder
}

type IntegrationInboxWorker struct {
	inbox    IntegrationInboxSchedulerStore
	consumer IntegrationReceiptConsumer
	options  IntegrationInboxWorkerOptions

	// Share discovery so concurrent consumers cannot repeatedly favor the first ready integration.
	readyMu         sync.Mutex
	ready           []integrationstore.IntegrationInboxIntegration
	after           integrationstore.IntegrationInboxIntegration
	recoveryMu      sync.Mutex
	recoveryRunning bool
	nextRecovery    time.Time
	nextLagSample   time.Time
}

const (
	integrationInboxRecoveryInterval = 30 * time.Second
	integrationInboxRecoveryRetry    = time.Second
	integrationInboxRecoveryBudget   = time.Second
	integrationInboxRecoveryTimeout  = 5 * time.Second
	integrationInboxLagSampleTimeout = time.Second
)

func NewIntegrationInboxWorker(
	inbox IntegrationInboxSchedulerStore,
	consumer IntegrationReceiptConsumer,
	options IntegrationInboxWorkerOptions,
) *IntegrationInboxWorker {
	if options.Log == nil {
		options.Log = slog.Default()
	}
	if options.Capacity == 0 {
		options.Capacity = 4
	}
	return &IntegrationInboxWorker{inbox: inbox, consumer: consumer, options: options}
}

func (w *IntegrationInboxWorker) Run(ctx context.Context) error {
	if w.inbox == nil || w.consumer == nil || w.options.Capacity < 1 ||
		w.options.Capacity > integrationstore.IntegrationInboxMaxBatch {
		return errors.New("integration inbox worker requires stores and capacity between 1 and 100")
	}
	var workers sync.WaitGroup
	workers.Go(func() {
		ticker := time.NewTicker(integrationInboxRecoveryRetry)
		defer ticker.Stop()
		for {
			_ = w.recoverDue(ctx)
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
					w.options.Log.Warn("discover integration inbox work", "error", err)
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

func (w *IntegrationInboxWorker) RunOnce(ctx context.Context) (bool, error) {
	if w.inbox == nil || w.consumer == nil {
		return false, errors.New("integration inbox worker requires stores")
	}
	var failures []error
	if err := w.recoverDue(ctx); err != nil {
		failures = append(failures, err)
	}
	for attempt := range integrationstore.IntegrationInboxMaxBatch {
		if err := ctx.Err(); err != nil {
			return false, errors.Join(append(failures, err)...)
		}
		integrationSetup, found, err := w.nextIntegration(ctx, attempt == 0)
		if err != nil {
			return false, errors.Join(append(failures, err)...)
		}
		if !found {
			break
		}
		receipt, claimed, err := w.inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
			ProjectID:     integrationSetup.ProjectID,
			IntegrationID: integrationSetup.IntegrationID,
			LeaseDuration: integrationstore.IntegrationInboxMaxLease,
		})
		if err != nil {
			failures = append(
				failures,
				fmt.Errorf(
					"claim inbox integration %s project %s: %w",
					integrationSetup.IntegrationID,
					integrationSetup.ProjectID,
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

func (w *IntegrationInboxWorker) recoverDue(ctx context.Context) (err error) {
	w.recoveryMu.Lock()
	if w.recoveryRunning || time.Now().Before(w.nextRecovery) {
		w.recoveryMu.Unlock()
		return nil
	}
	w.recoveryRunning = true
	w.recoveryMu.Unlock()
	interval := integrationInboxRecoveryInterval
	defer func() {
		w.recoveryMu.Lock()
		w.nextRecovery = time.Now().Add(interval)
		w.recoveryRunning = false
		w.recoveryMu.Unlock()
		if err != nil && ctx.Err() == nil {
			w.options.Log.Warn("recover integration inbox", "error", err)
		}
	}()

	if w.options.Metrics != nil && !time.Now().Before(w.nextLagSample) {
		sampleCtx, cancel := context.WithTimeout(ctx, integrationInboxLagSampleTimeout)
		lag, err := w.inbox.OldestReadyIntegrationInboxLag(sampleCtx)
		cancel()
		w.options.Metrics.RecordOldestReadyLag(lag, err)
		w.nextLagSample = time.Now().Add(integrationInboxRecoveryInterval)
		if err != nil && ctx.Err() == nil {
			w.options.Log.Warn("sample integration inbox lag", "error", err)
		}
	}

	stopAt := time.Now().Add(integrationInboxRecoveryBudget)
	recoveryCtx, cancel := context.WithTimeout(ctx, integrationInboxRecoveryTimeout)
	defer cancel()
	for {
		if err := recoveryCtx.Err(); err != nil {
			return err
		}
		if !time.Now().Before(stopAt) {
			interval = integrationInboxRecoveryRetry
			return nil
		}
		count, err := w.inbox.RecoverIntegrationInbox(recoveryCtx, integrationstore.IntegrationInboxMaxBatch)
		if err != nil {
			return err
		}
		if count < integrationstore.IntegrationInboxMaxBatch {
			return nil
		}
	}
}

func (w *IntegrationInboxWorker) nextIntegration(
	ctx context.Context,
	discover bool,
) (integrationstore.IntegrationInboxIntegration, bool, error) {
	w.readyMu.Lock()
	defer w.readyMu.Unlock()
	for scanned := 0; discover && len(w.ready) == 0 && scanned < integrationstore.IntegrationInboxMaxBatch; scanned++ {
		page, err := w.inbox.ListReadyIntegrationInboxIntegrations(ctx, w.after, integrationstore.IntegrationInboxMaxBatch)
		if err != nil {
			return integrationstore.IntegrationInboxIntegration{}, false, err
		}
		w.ready = page.Integrations
		w.after = page.NextCursor
		if w.after == (integrationstore.IntegrationInboxIntegration{}) {
			break
		}
	}
	if len(w.ready) == 0 {
		return integrationstore.IntegrationInboxIntegration{}, false, nil
	}
	integrationSetup := w.ready[0]
	w.ready = w.ready[1:]
	return integrationSetup, true, nil
}

func (w *IntegrationInboxWorker) consume(ctx context.Context, receipt integrationstore.IntegrationInboxRecord) error {
	// Leave lease time to record a timed-out attempt.
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
				"completed delayed integration inbox receipt",
				"receipt_id",
				receipt.ID,
				"integration_id",
				receipt.IntegrationID,
				"project_id",
				receipt.ProjectID,
				"attempt",
				receipt.AttemptCount,
				"receipt_age",
				time.Since(receipt.CreatedAt),
			)
		}
		return nil
	}
	outcome := "lease_lost"
	terminal := (len(receipt.Events) != 0 && errors.Is(err, ErrIntegrationLaunchUnavailable)) ||
		errors.Is(err, ErrScheduledActionFailed) || errors.Is(err, ErrIntegrationInboundPermanent)
	if !errors.Is(err, integrationstore.ErrIntegrationInboxLeaseLost) {
		retryCtx, retryCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer retryCancel()
		retryErr := w.inbox.WithIntegrationInboxLease(
			retryCtx,
			receipt.Lease(),
			func(work *integrationstore.IntegrationInboxLeaseTx) error {
				if terminal {
					return work.Fail(retryCtx, err.Error())
				}
				return work.Retry(retryCtx, integrationInboxRetryDelay(receipt.AttemptCount, err), err.Error())
			},
		)
		outcome = "retry_scheduled"
		if receipt.AttemptCount >= integrationstore.IntegrationInboxMaxAttempts || terminal {
			outcome = "failed"
		}
		if retryErr != nil {
			outcome = "retry_not_recorded"
		}
		if retryErr != nil && !errors.Is(retryErr, integrationstore.ErrIntegrationInboxLeaseLost) {
			err = errors.Join(err, fmt.Errorf("schedule inbox retry: %w", retryErr))
		}
	}
	if outcome == "failed" {
		if finalizer, ok := w.consumer.(interface {
			FinalizeFailure(context.Context, uuid.UUID, uuid.UUID) error
		}); ok {
			finalCtx, finalCancel := context.WithTimeout(context.WithoutCancel(ctx), 40*time.Second)
			if finalErr := finalizer.FinalizeFailure(finalCtx, receipt.ProjectID, receipt.ID); finalErr != nil {
				w.options.Log.WarnContext(ctx, "finalize failed integration inbox", "receipt_id", receipt.ID, "error", finalErr)
			}
			finalCancel()
		}
	}
	w.options.Log.WarnContext(
		ctx,
		"integration inbox admission failed",
		"receipt_id",
		receipt.ID,
		"integration_id",
		receipt.IntegrationID,
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
		"receipt %s integration %s (attempt %d): %w",
		receipt.ID,
		receipt.IntegrationID,
		receipt.AttemptCount,
		err,
	)
}

func integrationInboxRetryDelay(attempt int, err error) time.Duration {
	delay := 5 * time.Second
	for range min(max(attempt-1, 0), 6) {
		delay *= 2
	}
	return min(max(min(delay, 5*time.Minute), providerRetryDelay(err)), integrationstore.IntegrationInboxMaxRetryDelay)
}

func providerRetryDelay(err error) time.Duration {
	var delay time.Duration
	if hint, ok := err.(interface{ RetryDelay() time.Duration }); ok {
		delay = max(delay, hint.RetryDelay())
	}
	if wrapped, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range wrapped.Unwrap() {
			delay = max(delay, providerRetryDelay(cause))
		}
	} else if cause := errors.Unwrap(err); cause != nil {
		delay = max(delay, providerRetryDelay(cause))
	}
	return delay
}
