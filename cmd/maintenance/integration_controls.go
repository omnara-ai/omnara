package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

const integrationControlBatchSize = 1000

type integrationControlMaintenanceStore interface {
	FailUnprocessableIntegrationControls(context.Context, int) (int64, error)
	DeleteRetainedIntegrationControls(
		context.Context, integrationstore.DeleteRetainedIntegrationControlsInput,
	) (int64, error)
	OldestPendingIntegrationControl(context.Context) (*time.Time, error)
}

func runIntegrationControlMaintenanceTick(
	ctx context.Context,
	log *slog.Logger,
	store integrationControlMaintenanceStore,
	recorder *metrics.IntegrationControlRecorder,
	retention time.Duration,
) (bool, error) {
	// Each operation runs once per tick. A full batch does not trigger an
	// unbounded drain, and a failed cleanup must not prevent the other work.
	failed, failErr := store.FailUnprocessableIntegrationControls(ctx, integrationControlBatchSize)
	failOutcome := completedMaintenanceOutcome(ctx, failErr)
	deleted, deleteErr := store.DeleteRetainedIntegrationControls(
		ctx,
		integrationstore.DeleteRetainedIntegrationControlsInput{
			Retention: retention, Limit: integrationControlBatchSize,
		},
	)
	deleteOutcome := completedMaintenanceOutcome(ctx, deleteErr)
	oldest, oldestErr := store.OldestPendingIntegrationControl(ctx)
	oldestOutcome := completedMaintenanceOutcome(ctx, oldestErr)
	if oldestErr == nil && !oldestOutcome.interrupted {
		recorder.RecordOldestUnapplied(oldest, time.Now())
	}
	if failOutcome.err != nil {
		log.Error("fail unprocessable integration controls", "error", failOutcome.err)
	}
	if deleteOutcome.err != nil {
		log.Error("delete retained integration controls", "error", deleteOutcome.err)
	}
	if oldestOutcome.err != nil {
		log.Error("observe oldest unapplied integration control", "error", oldestOutcome.err)
	}
	worked := failed > 0 || deleted > 0
	if !failOutcome.interrupted && !deleteOutcome.interrupted && worked {
		log.Info("maintained integration control receipts", "failed", failed, "deleted", deleted)
	}
	return worked, errors.Join(failOutcome.err, deleteOutcome.err, oldestOutcome.err)
}
