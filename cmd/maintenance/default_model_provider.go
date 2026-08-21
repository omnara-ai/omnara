package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/omnara-ai/omnara/internal/defaultprovider"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type defaultModelProviderProvisioningRunner interface {
	RunNext(context.Context) (bool, string, error)
}

func runDefaultModelProviderProvisioningLoop(
	ctx context.Context,
	log *slog.Logger,
	runner defaultModelProviderProvisioningRunner,
	interval time.Duration,
) {
	for {
		attempted, orgID, err := runDefaultModelProviderProvisioningTick(ctx, log, runner)
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

		timer := time.NewTimer(jitteredMaintenanceDelay(interval))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func runDefaultModelProviderProvisioningTick(
	ctx context.Context,
	log *slog.Logger,
	runner defaultModelProviderProvisioningRunner,
) (attempted bool, orgID string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("default model provider provisioning panicked: %v", recovered)
			log.Error(
				"default model provider provisioning tick panicked",
				"error",
				recovered,
				"stack",
				string(debug.Stack()),
			)
		}
	}()
	return runner.RunNext(ctx)
}
