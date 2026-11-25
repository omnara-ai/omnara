package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelretry"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	modelUsageRecoveryJob         = "recover_model_usage"
	modelUsageRecoveryLookback    = time.Hour
	modelUsageRecoveryBatch       = 64
	modelUsageRecoveryConcurrency = 16
	modelUsageRecoveryCooldown    = time.Minute
)

var modelUsageRecoverySchedule = []time.Duration{
	15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute,
	10 * time.Minute, 20 * time.Minute, 40 * time.Minute, time.Hour, 2 * time.Hour,
	4 * time.Hour, 8 * time.Hour, 16 * time.Hour, 24 * time.Hour,
}

var errModelUsageCoolingDown = errors.New("model provider credential is rate limited")

type modelUsageRecovery struct {
	log  *slog.Logger
	list func(
		ctx context.Context,
		after executionstore.ModelUsageRecoveryCursor,
		lookback time.Duration,
		limit int32,
		schedule []time.Duration,
	) ([]executionstore.ModelUsageRecovery, executionstore.ModelUsageRecoveryCursor, bool, error)
	lookupUsage func(
		ctx context.Context, orgID, providerConfigID uuid.UUID, generationID string,
	) (model.GenerationUsage, error)
	record func(context.Context, executionstore.RecordRecoveredModelCallUsageInput) (bool, error)
	now    func() time.Time

	cursor    executionstore.ModelUsageRecoveryCursor
	mu        sync.Mutex
	cooldowns map[uuid.UUID]time.Time
}

func recoverModelUsage(recovery *modelUsageRecovery) func(context.Context) maintenanceResult {
	recovery.cooldowns = map[uuid.UUID]time.Time{}
	return recovery.run
}

func (r *modelUsageRecovery) run(ctx context.Context) maintenanceResult {
	due, next, more, err := r.list(
		ctx, r.cursor, modelUsageRecoveryLookback, modelUsageRecoveryBatch, modelUsageRecoverySchedule,
	)
	if err != nil {
		outcome := completedMaintenanceOutcome(ctx, err)
		if outcome.err != nil {
			r.log.Error("list model usage recoveries", "error", outcome.err)
		}
		return maintenanceResult{err: outcome.err}
	}
	recovered := false
	var failures []error
	var group errgroup.Group
	group.SetLimit(modelUsageRecoveryConcurrency)
	for _, call := range due {
		group.Go(func() error {
			ok, err := r.recoverCall(ctx, call)
			r.mu.Lock()
			defer r.mu.Unlock()
			recovered = recovered || ok
			if err != nil {
				failures = append(failures, err)
			}
			return nil
		})
	}
	_ = group.Wait()
	r.cursor = next
	outcome := completedMaintenanceOutcome(ctx, errors.Join(failures...))
	if outcome.err != nil {
		r.log.Error("recover model usage", "error", outcome.err)
	}
	return maintenanceResult{worked: recovered, more: more, err: outcome.err}
}

func (r *modelUsageRecovery) recoverCall(
	ctx context.Context,
	call executionstore.ModelUsageRecovery,
) (bool, error) {
	usage, err := r.lookup(ctx, call)
	if err != nil {
		cause := "lookup"
		switch {
		case errors.Is(err, model.ErrGenerationUsageNotFound):
			cause = "not_recorded"
		case errors.Is(err, errModelUsageCoolingDown):
			cause = "cooling_down"
		}
		if cause == "lookup" || call.Final {
			reportMissed(ctx, call, cause, err)
		}
		if cause != "lookup" {
			return false, nil
		}
		return false, fmt.Errorf("look up generation for model call %s: %w", call.ModelCallContextID, err)
	}
	recorded, err := r.record(ctx, executionstore.RecordRecoveredModelCallUsageInput{
		ProjectID:               call.ProjectID,
		AgentID:                 call.AgentID,
		ModelCallContextID:      call.ModelCallContextID,
		ProviderResponseID:      call.GenerationID,
		Usage:                   usage.Usage,
		ProviderReportedCostUSD: usage.ProviderReportedCostUSD,
	})
	if err != nil {
		reportMissed(ctx, call, "record", err)
		return false, fmt.Errorf("record recovered usage for model call %s: %w", call.ModelCallContextID, err)
	}
	return recorded, nil
}

func reportMissed(ctx context.Context, call executionstore.ModelUsageRecovery, cause string, err error) {
	if completedMaintenanceOutcome(ctx, err).err != nil {
		logent.ModelUsageRecoveryMissed(ctx, call, cause, err)
	}
}

func (r *modelUsageRecovery) lookup(
	ctx context.Context,
	call executionstore.ModelUsageRecovery,
) (model.GenerationUsage, error) {
	if r.coolingDown(call.ModelProviderConfigID) {
		return model.GenerationUsage{}, errModelUsageCoolingDown
	}
	usage, err := r.lookupUsage(ctx, call.OrgID, call.ModelProviderConfigID, call.GenerationID)
	if providerErr, ok := model.ClassifyError(err); ok && providerErr.StatusCode == http.StatusTooManyRequests {
		r.coolDown(call.ModelProviderConfigID, providerErr.RetryAfter)
	}
	return usage, err
}

func (r *modelUsageRecovery) coolingDown(providerConfigID uuid.UUID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	until, ok := r.cooldowns[providerConfigID]
	if ok && !r.now().Before(until) {
		delete(r.cooldowns, providerConfigID)
		return false
	}
	return ok
}

func (r *modelUsageRecovery) coolDown(providerConfigID uuid.UUID, retryAfter *model.RetryAfter) {
	now := r.now()
	delay, ok := modelretry.RetryAfterDelay(retryAfter, now)
	if !ok || delay <= 0 {
		delay = modelUsageRecoveryCooldown
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cooldowns[providerConfigID] = now.Add(delay)
}
