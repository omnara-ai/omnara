package executionstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/cronschedule"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// CreateCronTriggerAppEvent atomically copies a live occurrence into the app
// inbox and completes its firing. False means no new handoff: a stale/canceled
// claim or a reported unavailable target. No provider I/O occurs here.
func (s *Store) CreateCronTriggerAppEvent(ctx context.Context, claimed ClaimedCronTrigger) (bool, error) {
	if claimed.ProjectID == uuid.Nil || claimed.TriggerID == uuid.Nil ||
		claimed.ClaimToken == uuid.Nil || claimed.DueAt.IsZero() || claimed.FiredAt.IsZero() ||
		claimed.Target.Kind != CronTriggerTargetApp || claimed.Target.ID == uuid.Nil {
		return false, storeerr.InvalidRequest(errors.New("claimed app occurrence is required"))
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	project, err := loadProjectTx(ctx, q, claimed.ProjectID)
	if errors.Is(err, storeerr.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, project.OrgID, claimed.ProjectID); err != nil {
		if errors.Is(err, storeerr.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	// The app gate is taken before cron, including on unavailable paths. Resource
	// references inside settings belong to the app and grant no authority here.
	unavailable := ""
	if err := integrationstore.LockAppsTx(ctx, tx, claimed.ProjectID, nil, claimed.Target.ID); err != nil {
		if !errors.Is(err, storeerr.ErrNotFound) && !errors.Is(err, storeerr.ErrUnauthorized) {
			return false, err
		}
		unavailable = "Scheduled app action skipped: app is unavailable."
	}
	current, err := lockCronTriggerTx(ctx, q, claimed.ProjectID, claimed.TriggerID)
	if errors.Is(err, storeerr.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	live, err := q.CronTriggerClaimIsLive(ctx, dbsqlc.CronTriggerClaimIsLiveParams{
		ProjectID: claimed.ProjectID, ID: claimed.TriggerID, ClaimToken: &claimed.ClaimToken,
	})
	if err != nil {
		return false, err
	}
	if !live {
		return false, nil
	}
	// Never acquire a new identity's gates under cron, even for malformed claims.
	if current.Target.Kind != CronTriggerTargetApp || current.Target.ID != claimed.Target.ID {
		return false, storeerr.InvalidRequest(errors.New("claimed app identity does not match cron trigger"))
	}
	if !current.Enabled || current.NextFireAfter == nil || !current.NextFireAfter.Equal(claimed.DueAt) {
		if _, err := q.ReleaseCronTriggerClaim(ctx, dbsqlc.ReleaseCronTriggerClaimParams{
			ProjectID: claimed.ProjectID, ID: claimed.TriggerID, ClaimToken: &claimed.ClaimToken,
		}); err != nil {
			return false, err
		}
		return false, tx.Commit(ctx)
	}
	// A manually forged future claim must not fire early.
	now, err := q.DBNow(ctx)
	if err != nil {
		return false, err
	}
	if claimed.DueAt.After(now) {
		return false, nil
	}
	if unavailable != "" {
		return false, skipCronAppEventTx(ctx, tx, q, claimed, unavailable)
	}
	app, err := s.integrations.GetProjectAppByIDTx(ctx, tx, current.Target.ID)
	if err != nil {
		return false, err
	}
	if err := validateCronAppTarget(&current.Target, app); err != nil {
		return false, skipCronAppEventTx(
			ctx,
			tx,
			q,
			claimed,
			"Scheduled app action skipped: invalid settings.",
		)
	}
	if _, err := time.LoadLocation(current.Timezone); err != nil {
		return false, skipCronAppEventTx(ctx, tx, q, claimed, "Scheduled app action skipped: invalid timezone.")
	}
	receipt, _, err := s.integrations.AcceptScheduledAppEventTx(
		ctx,
		tx,
		integrationstore.AcceptScheduledAppEventInput{
			ProjectID: current.ProjectID, AppID: current.Target.ID,
			ReceiptKey: "cron_trigger:" + current.ID.String() + ":" + claimed.DueAt.UTC().Format(time.RFC3339),
			Event: integrationstore.ScheduledAppEvent{
				TriggerID: current.ID,
				Occurrence: cronschedule.Occurrence{
					Name: current.Name, DueAt: claimed.DueAt, FiredAt: claimed.FiredAt,
					LastFiredAt: current.LastFiredAt, Timezone: current.Timezone,
				},
				Settings: current.Target.Settings,
			},
		},
	)
	if err != nil {
		if errors.Is(err, storeerr.ErrInvalidRequest) || errors.Is(err, storeerr.ErrIdempotencyConflict) {
			return false, skipCronAppEventTx(
				ctx,
				tx,
				q,
				claimed,
				"Scheduled app action skipped: occurrence could not be accepted.",
			)
		}
		return false, fmt.Errorf("accept scheduled app event: %w", err)
	}
	rows, err := q.SetCronTriggerAppReceipt(ctx, dbsqlc.SetCronTriggerAppReceiptParams{
		ProjectID: current.ProjectID, ID: current.ID, ClaimToken: &claimed.ClaimToken, ReceiptID: &receipt.ID,
	})
	if err != nil {
		return false, err
	}
	if rows != 1 {
		// The lease may expire while accepting the receipt. Roll back the entire
		// handoff and let a fresh claim retry without recording a stale failure.
		return false, nil
	}
	if err := completeCronTriggerFiringTx(ctx, q, CompleteCronTriggerFiringInput{
		ProjectID: current.ProjectID, TriggerID: current.ID, ClaimToken: claimed.ClaimToken, Fired: true,
	}); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit scheduled app event: %w", err)
	}
	return true, nil
}

// An unavailable setup is a failed occurrence, not an endless claim-lease retry.
// Record and completion share the lock and transaction; future firings still run.
func skipCronAppEventTx(
	ctx context.Context,
	tx pgx.Tx,
	q *dbsqlc.Queries,
	claimed ClaimedCronTrigger,
	message string,
) error {
	live, err := q.CronTriggerClaimIsLive(
		ctx,
		dbsqlc.CronTriggerClaimIsLiveParams{
			ProjectID:  claimed.ProjectID,
			ID:         claimed.TriggerID,
			ClaimToken: &claimed.ClaimToken,
		},
	)
	if err != nil {
		return err
	}
	if !live {
		return nil
	}
	if _, err := q.RecordCronTriggerFailure(ctx, dbsqlc.RecordCronTriggerFailureParams{
		ProjectID:      claimed.ProjectID,
		ID:             claimed.TriggerID,
		ClaimToken:     &claimed.ClaimToken,
		FailureMessage: message,
		WillRetry:      false,
	}); err != nil {
		return err
	}
	if err := completeCronTriggerFiringTx(ctx, q, CompleteCronTriggerFiringInput{
		ProjectID: claimed.ProjectID, TriggerID: claimed.TriggerID, ClaimToken: claimed.ClaimToken,
	}); err != nil {
		if !errors.Is(err, storeerr.ErrInvalidRequest) {
			return err
		}
		// A corrupt schedule cannot compute a next firing. Match claiming's
		// existing disable behavior instead of retrying this occurrence forever.
		if _, err := q.DisableCronTrigger(ctx, dbsqlc.DisableCronTriggerParams{
			ProjectID: claimed.ProjectID, ID: claimed.TriggerID, FailureMessage: message,
		}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
