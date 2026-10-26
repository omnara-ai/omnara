package maintenance

import (
	"context"
	"log/slog"
)

func reconcileGitHubChecks(
	log *slog.Logger,
	reconcile func(context.Context) error,
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		outcome := completedMaintenanceOutcome(ctx, reconcile(ctx))
		if outcome.err != nil {
			log.Error("reconcile GitHub checks", "error", outcome.err)
		}
		return maintenanceResult{err: outcome.err}
	}
}
