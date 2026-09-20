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

// CreateCronTriggerAppLaunch atomically snapshots a live occurrence into the app
// inbox and completes its firing. False means no new handoff: a stale/canceled
// claim or a reported unavailable target. No provider I/O occurs here.
func (s *Store) CreateCronTriggerAppLaunch(ctx context.Context, claimed ClaimedCronTrigger) (bool, error) {
	if claimed.ProjectID == uuid.Nil || claimed.TriggerID == uuid.Nil ||
		claimed.ClaimToken == uuid.Nil || claimed.DueAt.IsZero() ||
		claimed.Target.Kind != CronTriggerTargetAppLaunch || claimed.Target.ID == uuid.Nil ||
		claimed.Target.AppLaunch == nil ||
		claimed.Target.AppLaunch.ProfileID == uuid.Nil {
		return false, storeerr.InvalidRequest(errors.New("claimed app launch occurrence is required"))
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
	// The app gate is taken before profile and cron, including on unavailable paths.
	unavailable := ""
	if err := integrationstore.LockAppsTx(ctx, tx, claimed.ProjectID, nil, claimed.Target.ID); err != nil {
		if !errors.Is(err, storeerr.ErrNotFound) && !errors.Is(err, storeerr.ErrUnauthorized) {
			return false, err
		}
		unavailable = "Scheduled app launch skipped: app is unavailable."
	}
	profile, err := lockAgentProfileTx(ctx, q, claimed.ProjectID, claimed.Target.AppLaunch.ProfileID)
	if err != nil {
		if !errors.Is(err, storeerr.ErrNotFound) {
			return false, err
		}
		unavailable = "Scheduled app launch skipped: profile is unavailable."
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
	if current.Target.Kind != CronTriggerTargetAppLaunch || current.Target.ID != claimed.Target.ID ||
		current.Target.AppLaunch.ProfileID != claimed.Target.AppLaunch.ProfileID {
		return false, storeerr.InvalidRequest(errors.New("claimed app launch identity does not match cron trigger"))
	}
	if !current.Enabled || current.NextFireAfter == nil || !current.NextFireAfter.Equal(claimed.DueAt) {
		if _, err := q.ReleaseCronTriggerClaim(ctx, dbsqlc.ReleaseCronTriggerClaimParams{
			ProjectID: claimed.ProjectID, ID: claimed.TriggerID, ClaimToken: &claimed.ClaimToken,
		}); err != nil {
			return false, err
		}
		return false, tx.Commit(ctx)
	}
	// A manually forged future claim must not launch early.
	now, err := q.DBNow(ctx)
	if err != nil {
		return false, err
	}
	if claimed.DueAt.After(now) {
		return false, nil
	}
	if unavailable != "" {
		return false, skipCronAppLaunchTx(ctx, tx, q, claimed, unavailable)
	}
	app, err := s.integrations.GetProjectAppByIDTx(ctx, tx, current.Target.ID)
	if err != nil {
		return false, err
	}
	if err := validateCronAppLaunchTarget(&current.Target, app, current.Name); err != nil {
		return false, skipCronAppLaunchTx(
			ctx,
			tx,
			q,
			claimed,
			"Scheduled app launch skipped: invalid destination or opening message template.",
		)
	}
	data, err := cronschedule.OccurrenceMessageData(
		current.Name,
		claimed.FiredAt,
		current.LastFiredAt,
		claimed.DueAt,
		current.Timezone,
	)
	if err != nil {
		return false, skipCronAppLaunchTx(ctx, tx, q, claimed, "Scheduled app launch skipped: invalid timezone.")
	}
	opening, err := cronschedule.RenderOpeningMessage(current.Target.AppLaunch.OpeningMessageTemplate, data)
	if err != nil {
		return false, skipCronAppLaunchTx(
			ctx,
			tx,
			q,
			claimed,
			"Scheduled app launch skipped: opening message template could not be rendered.",
		)
	}
	message, err := cronschedule.RenderMessage(current.MessageTemplate, data)
	if err != nil {
		return false, skipCronAppLaunchTx(
			ctx,
			tx,
			q,
			claimed,
			"Scheduled app launch skipped: task template could not be rendered.",
		)
	}
	receipt, _, err := s.integrations.AcceptScheduledAppLaunchTx(
		ctx,
		tx,
		integrationstore.AcceptScheduledAppLaunchInput{
			ProjectID: current.ProjectID, AppID: current.Target.ID,
			ReceiptKey: "cron_trigger:" + current.ID.String() + ":" + claimed.DueAt.UTC().Format(time.RFC3339),
			Launch: integrationstore.ScheduledAppLaunch{
				TriggerID: current.ID, ProfileID: profile.ID, ConfigID: profile.CurrentConfigID,
				TriggerName: current.Name, DueAt: claimed.DueAt, Destination: current.Target.AppLaunch.Destination,
				OpeningMessage: opening, Message: message,
			},
		},
	)
	if err != nil {
		if errors.Is(err, storeerr.ErrInvalidRequest) || errors.Is(err, storeerr.ErrIdempotencyConflict) {
			return false, skipCronAppLaunchTx(
				ctx,
				tx,
				q,
				claimed,
				"Scheduled app launch skipped: occurrence could not be accepted.",
			)
		}
		return false, fmt.Errorf("accept scheduled app launch: %w", err)
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
		return false, fmt.Errorf("commit scheduled app launch: %w", err)
	}
	return true, nil
}

// An unavailable setup is a failed occurrence, not an endless claim-lease retry.
// Record and completion share the lock and transaction; future firings still run.
func skipCronAppLaunchTx(
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
