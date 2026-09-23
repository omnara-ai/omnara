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

type AppInboxSchedulerStore interface {
	RecoverIntegrationInbox(context.Context, int) (int64, error)
	OldestReadyIntegrationInboxLag(context.Context) (time.Duration, error)
	ListReadyIntegrationInboxApps(
		context.Context, integrationstore.IntegrationInboxApp, int,
	) (integrationstore.IntegrationInboxAppPage, error)
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

type AppLaunchProvisioner interface {
	StartLaunchProvisioning(context.Context, *slog.Logger, uuid.UUID, []uuid.UUID)
}

type AppInboxWorkerOptions struct {
	Log          *slog.Logger
	Capacity     int
	MachinePools AppLaunchProvisioner
	Metrics      *metrics.AppInboxRecorder
}

type AppInboxWorker struct {
	inbox    AppInboxSchedulerStore
	consumer AppReceiptConsumer
	options  AppInboxWorkerOptions

	// Share discovery so concurrent consumers cannot repeatedly favor the first ready app.
	readyMu sync.Mutex
	ready   []integrationstore.IntegrationInboxApp
	after   integrationstore.IntegrationInboxApp

	recoveryMu      sync.Mutex
	recoveryRunning bool
	nextRecovery    time.Time
	nextLagSample   time.Time
}

const (
	appInboxRecoveryInterval = 30 * time.Second
	appInboxRecoveryRetry    = time.Second
	appInboxRecoveryBudget   = time.Second
	appInboxRecoveryTimeout  = 5 * time.Second
	appInboxLagSampleTimeout = time.Second
)

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

func (w *AppInboxWorker) Run(ctx context.Context) error {
	if w.inbox == nil || w.consumer == nil || w.options.Capacity < 1 ||
		w.options.Capacity > integrationstore.IntegrationInboxMaxBatch {
		return errors.New("app inbox worker requires stores and capacity between 1 and 100")
	}
	var workers sync.WaitGroup
	workers.Go(func() {
		ticker := time.NewTicker(appInboxRecoveryRetry)
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

func (w *AppInboxWorker) RunOnce(ctx context.Context) (bool, error) {
	if w.inbox == nil || w.consumer == nil {
		return false, errors.New("app inbox worker requires stores")
	}
	var failures []error
	if err := w.recoverDue(ctx); err != nil {
		failures = append(failures, err)
	}
	for attempt := range integrationstore.IntegrationInboxMaxBatch {
		if err := ctx.Err(); err != nil {
			return false, errors.Join(append(failures, err)...)
		}
		appSetup, found, err := w.nextApp(ctx, attempt == 0)
		if err != nil {
			return false, errors.Join(append(failures, err)...)
		}
		if !found {
			break
		}
		receipt, claimed, err := w.inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
			ProjectID:     appSetup.ProjectID,
			AppID:         appSetup.AppID,
			LeaseDuration: integrationstore.IntegrationInboxMaxLease,
		})
		if err != nil {
			failures = append(
				failures,
				fmt.Errorf(
					"claim inbox app %s project %s: %w",
					appSetup.AppID,
					appSetup.ProjectID,
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

func (w *AppInboxWorker) recoverDue(ctx context.Context) (err error) {
	w.recoveryMu.Lock()
	if w.recoveryRunning || time.Now().Before(w.nextRecovery) {
		w.recoveryMu.Unlock()
		return nil
	}
	w.recoveryRunning = true
	w.recoveryMu.Unlock()
	interval := appInboxRecoveryInterval
	defer func() {
		w.recoveryMu.Lock()
		w.nextRecovery = time.Now().Add(interval)
		w.recoveryRunning = false
		w.recoveryMu.Unlock()
		if err != nil && ctx.Err() == nil {
			w.options.Log.Warn("recover app inbox", "error", err)
		}
	}()

	if w.options.Metrics != nil && !time.Now().Before(w.nextLagSample) {
		sampleCtx, cancel := context.WithTimeout(ctx, appInboxLagSampleTimeout)
		lag, err := w.inbox.OldestReadyIntegrationInboxLag(sampleCtx)
		cancel()
		w.options.Metrics.RecordOldestReadyLag(lag, err)
		w.nextLagSample = time.Now().Add(appInboxRecoveryInterval)
		if err != nil && ctx.Err() == nil {
			w.options.Log.Warn("sample app inbox lag", "error", err)
		}
	}

	stopAt := time.Now().Add(appInboxRecoveryBudget)
	recoveryCtx, cancel := context.WithTimeout(ctx, appInboxRecoveryTimeout)
	defer cancel()
	for {
		if err := recoveryCtx.Err(); err != nil {
			return err
		}
		if !time.Now().Before(stopAt) {
			interval = appInboxRecoveryRetry
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

func (w *AppInboxWorker) nextApp(
	ctx context.Context,
	discover bool,
) (integrationstore.IntegrationInboxApp, bool, error) {
	w.readyMu.Lock()
	defer w.readyMu.Unlock()
	for scanned := 0; discover && len(w.ready) == 0 && scanned < integrationstore.IntegrationInboxMaxBatch; scanned++ {
		page, err := w.inbox.ListReadyIntegrationInboxApps(ctx, w.after, integrationstore.IntegrationInboxMaxBatch)
		if err != nil {
			return integrationstore.IntegrationInboxApp{}, false, err
		}
		w.ready = page.Apps
		w.after = page.NextCursor
		if w.after == (integrationstore.IntegrationInboxApp{}) {
			break
		}
	}
	if len(w.ready) == 0 {
		return integrationstore.IntegrationInboxApp{}, false, nil
	}
	appSetup := w.ready[0]
	w.ready = w.ready[1:]
	return appSetup, true, nil
}

func (w *AppInboxWorker) consume(ctx context.Context, receipt integrationstore.IntegrationInboxRecord) error {
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
				"completed delayed app inbox receipt",
				"receipt_id",
				receipt.ID,
				"app_id",
				receipt.AppID,
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
	terminal := (len(receipt.Events) != 0 && errors.Is(err, ErrAppLaunchUnavailable)) ||
		errors.Is(err, ErrScheduledActionFailed)
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
				return work.Retry(retryCtx, appInboxRetryDelay(receipt.AttemptCount), err.Error())
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
				w.options.Log.WarnContext(ctx, "finalize failed app inbox", "receipt_id", receipt.ID, "error", finalErr)
			}
			finalCancel()
		}
	}
	w.options.Log.WarnContext(
		ctx,
		"app inbox admission failed",
		"receipt_id",
		receipt.ID,
		"app_id",
		receipt.AppID,
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
		"receipt %s app %s (attempt %d): %w",
		receipt.ID,
		receipt.AppID,
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
