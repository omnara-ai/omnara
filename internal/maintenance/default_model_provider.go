package maintenance

import (
	"context"
	"errors"
	"log/slog"

	"github.com/omnara-ai/omnara/internal/defaultprovider"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func provisionDefaultModelProviders(log *slog.Logger, runner *defaultprovider.Runner) func(context.Context) bool {
	return func(ctx context.Context) bool {
		attempted, orgID, err := runner.RunNext(ctx)
		switch {
		case err == nil && attempted:
			log.Info("provisioned default model provider", "org_id", orgID)
		case errors.Is(err, defaultprovider.ErrRetrySchedule):
			log.Error("schedule default model provider provisioning retry", "org_id", orgID, "error", err)
		case errors.Is(err, orglifecycle.ErrDefaultModelProviderProvisioningSuperseded):
			log.Warn("default model provider provisioning superseded", "org_id", orgID, "error", err)
		case errors.Is(err, storeerr.ErrStateTransitionConflict):
			log.Warn("default model provider provisioning claim superseded", "org_id", orgID)
		case errors.Is(err, storeerr.ErrNotFound):
			log.Info("default model provider provisioning canceled", "org_id", orgID)
		case errors.Is(err, modelprovider.ErrHostedCredentialPending):
			log.Info("default model provider provisioning pending", "org_id", orgID)
		case err != nil && ctx.Err() == nil:
			log.Error("provision default model provider", "org_id", orgID, "error", err)
		}
		return false
	}
}
