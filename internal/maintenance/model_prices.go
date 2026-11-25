package maintenance

import (
	"context"
	"log/slog"
	"time"
)

const modelPriceRefreshInterval = time.Hour

func refreshModelPrices(
	log *slog.Logger,
	refresh func(context.Context) (string, int, error),
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		version, pricedModels, err := refresh(ctx)
		outcome := completedMaintenanceOutcome(ctx, err)
		if outcome.err != nil {
			log.Error("refresh model prices", "error", outcome.err)
		} else if version != "" && !outcome.interrupted {
			log.Info("stored new model prices", "version", version, "priced_models", pricedModels)
		}
		return maintenanceResult{worked: version != "" && outcome.err == nil, err: outcome.err}
	}
}
