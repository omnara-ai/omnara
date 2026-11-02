package maintenance

import (
	"context"
	"log/slog"
)

func reconcileGitHubChecks(
	log *slog.Logger,
	reconcile func(context.Context) (bool, error),
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		worked, err := reconcile(ctx)
		outcome := completedMaintenanceOutcome(ctx, err)
		if outcome.err != nil {
			log.Error("reconcile GitHub checks", "error", outcome.err)
		}
		return maintenanceResult{worked: worked, err: outcome.err}
	}
}
