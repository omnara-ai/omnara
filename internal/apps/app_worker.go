package apps

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
)

type AppInboxSchedulerStore interface {
	RecoverAppInbox(context.Context, int) (int64, error)
	OldestReadyAppInboxLag(context.Context) (time.Duration, error)
	ListReadyAppInboxApps(
		context.Context, appstore.AppInboxApp, int,
	) (appstore.AppInboxAppPage, error)
	ClaimAppInbox(
		context.Context,
		appstore.ClaimAppInboxInput,
	) (appstore.AppInboxRecord, bool, error)
	WithAppInboxLease(
		context.Context,
		appstore.AppInboxLease,
		func(*appstore.AppInboxLeaseTx) error,
	) error
}

type AppReceiptConsumer interface {
	Consume(context.Context, appstore.AppInboxLease) ([]AppSlotAdmission, error)
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
	ready   []appstore.AppInboxApp
	after   appstore.AppInboxApp

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
		w.options.Capacity > appstore.AppInboxMaxBatch {
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
	for attempt := range appstore.AppInboxMaxBatch {
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
		receipt, claimed, err := w.inbox.ClaimAppInbox(ctx, appstore.ClaimAppInboxInput{
			ProjectID:     appSetup.ProjectID,
			AppID:         appSetup.AppID,
			LeaseDuration: appstore.AppInboxMaxLease,
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
		lag, err := w.inbox.OldestReadyAppInboxLag(sampleCtx)
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
		count, err := w.inbox.RecoverAppInbox(recoveryCtx, appstore.AppInboxMaxBatch)
		if err != nil {
			return err
		}
		if count < appstore.AppInboxMaxBatch {
			return nil
		}
	}
}

func (w *AppInboxWorker) nextApp(
	ctx context.Context,
	discover bool,
) (appstore.AppInboxApp, bool, error) {
	w.readyMu.Lock()
	defer w.readyMu.Unlock()
	for scanned := 0; discover && len(w.ready) == 0 && scanned < appstore.AppInboxMaxBatch; scanned++ {
		page, err := w.inbox.ListReadyAppInboxApps(ctx, w.after, appstore.AppInboxMaxBatch)
		if err != nil {
			return appstore.AppInboxApp{}, false, err
		}
		w.ready = page.Apps
		w.after = page.NextCursor
		if w.after == (appstore.AppInboxApp{}) {
			break
		}
	}
	if len(w.ready) == 0 {
		return appstore.AppInboxApp{}, false, nil
	}
	appSetup := w.ready[0]
	w.ready = w.ready[1:]
	return appSetup, true, nil
}

func (w *AppInboxWorker) consume(ctx context.Context, receipt appstore.AppInboxRecord) error {
	// Leave lease time to record a timed-out attempt.
	deadline := time.Now().Add(appstore.AppInboxMaxLease - 15*time.Second)
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
		errors.Is(err, ErrScheduledActionFailed) || errors.Is(err, ErrAppInboundPermanent)
	if !errors.Is(err, appstore.ErrAppInboxLeaseLost) {
		retryCtx, retryCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer retryCancel()
		retryErr := w.inbox.WithAppInboxLease(
			retryCtx,
			receipt.Lease(),
			func(work *appstore.AppInboxLeaseTx) error {
				if terminal {
					return work.Fail(retryCtx, err.Error())
				}
				return work.Retry(retryCtx, appInboxRetryDelay(receipt.AttemptCount, err), err.Error())
			},
		)
		outcome = "retry_scheduled"
		if receipt.AttemptCount >= appstore.AppInboxMaxAttempts || terminal {
			outcome = "failed"
		}
		if retryErr != nil {
			outcome = "retry_not_recorded"
		}
		if retryErr != nil && !errors.Is(retryErr, appstore.ErrAppInboxLeaseLost) {
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

func appInboxRetryDelay(attempt int, err error) time.Duration {
	delay := 5 * time.Second
	for range min(max(attempt-1, 0), 6) {
		delay *= 2
	}
	return min(max(min(delay, 5*time.Minute), providerRetryDelay(err)), appstore.AppInboxMaxRetryDelay)
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
