package maintenance

import (
	"context"
	"log/slog"

	integrationruntime "github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/observability/metrics"
)

func reconcileGitHubChecks(
	log *slog.Logger,
	reconcile func(context.Context) (integrationruntime.GitHubCheckPass, error),
	recorder *metrics.GitHubCheckRecorder,
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		pass, err := reconcile(ctx)
		recorder.Record(pass.FullPass, pass.Retrying)
		outcome := completedMaintenanceOutcome(ctx, err)
		if outcome.err != nil {
			log.Error("reconcile GitHub checks", "error", outcome.err)
		}
		return maintenanceResult{worked: pass.Worked, err: outcome.err}
	}
}
