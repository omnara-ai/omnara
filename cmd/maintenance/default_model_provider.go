package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	defaultModelProviderPendingRetryDelay  = 2 * time.Minute
	defaultModelProviderConflictRetryDelay = 24 * time.Hour
	defaultModelProviderInitialRetryDelay  = 5 * time.Second
	defaultModelProviderMaximumRetryDelay  = 15 * time.Minute
	defaultModelProviderStateWriteTimeout  = 30 * time.Second
)

var errDefaultModelProviderRetrySchedule = errors.New(
	"schedule default model provider provisioning retry",
)

type defaultModelProviderProvisioningStore interface {
	ClaimDefaultModelProviderProvisioning(
		context.Context,
	) (orglifecycle.DefaultModelProviderProvisioningClaim, bool, error)
	CompleteDefaultModelProviderProvisioning(
		context.Context,
		orglifecycle.CompleteDefaultModelProviderProvisioningInput,
	) error
	RetryDefaultModelProviderProvisioning(
		context.Context,
		orglifecycle.RetryDefaultModelProviderProvisioningInput,
	) error
}

type defaultModelProviderProvisioningWorker struct {
	store       defaultModelProviderProvisioningStore
	provisioner modelprovider.HostedCredentialProvisioner
}

func runDefaultModelProviderProvisioningLoop(
	ctx context.Context,
	log *slog.Logger,
	worker defaultModelProviderProvisioningWorker,
	interval time.Duration,
) {
	for {
		attempted, orgID, err := runDefaultModelProviderProvisioningTick(ctx, log, worker)
		switch {
		case err == nil && attempted:
			log.Info("provisioned default model provider", "org_id", orgID)
		case errors.Is(err, errDefaultModelProviderRetrySchedule):
			log.Error("schedule default model provider provisioning retry", "org_id", orgID, "error", err)
		case errors.Is(err, orglifecycle.ErrDefaultModelProviderProvisioningSuperseded):
			log.Warn("default model provider provisioning superseded", "org_id", orgID, "error", err)
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
	worker defaultModelProviderProvisioningWorker,
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
	return worker.runOnce(ctx)
}

func (worker defaultModelProviderProvisioningWorker) runOnce(
	ctx context.Context,
) (bool, string, error) {
	claim, found, err := worker.store.ClaimDefaultModelProviderProvisioning(ctx)
	if err != nil {
		if !found {
			return false, "", err
		}
		orgID, encodeErr := publicid.Encode(publicid.KindOrganization, claim.OrgID)
		if encodeErr != nil {
			return true, "", worker.retry(ctx, claim, errors.Join(err, encodeErr))
		}
		return true, orgID, worker.retry(ctx, claim, err)
	}
	if !found {
		return false, "", err
	}
	orgID, err := publicid.Encode(publicid.KindOrganization, claim.OrgID)
	if err != nil {
		return true, "", worker.retry(ctx, claim, err)
	}
	creatorUserID, err := publicid.Encode(publicid.KindUser, claim.CreatorUserID)
	if err != nil {
		return true, orgID, worker.retry(ctx, claim, err)
	}

	provisionCtx, cancel := context.WithTimeout(ctx, modelprovider.HostedCredentialProvisionTimeout)
	response, provisionErr := worker.provisioner.ProvisionHostedCredential(
		provisionCtx,
		modelprovider.HostedCredentialRequest{
			OrgID:         orgID,
			CreatorUserID: creatorUserID,
			Template:      claim.Template,
		},
	)
	cancel()
	if provisionErr != nil {
		return true, orgID, worker.retry(ctx, claim, provisionErr)
	}

	stateCtx, stateCancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		defaultModelProviderStateWriteTimeout,
	)
	completeErr := worker.store.CompleteDefaultModelProviderProvisioning(
		stateCtx,
		orglifecycle.CompleteDefaultModelProviderProvisioningInput{
			Claim:           claim,
			CredentialValue: response.CredentialValue,
		},
	)
	stateCancel()
	if completeErr != nil {
		if errors.Is(completeErr, orglifecycle.ErrDefaultModelProviderProvisioningSuperseded) ||
			errors.Is(completeErr, storeerr.ErrNotFound) {
			return true, orgID, completeErr
		}
		return true, orgID, worker.retry(ctx, claim, completeErr)
	}
	return true, orgID, nil
}

func (worker defaultModelProviderProvisioningWorker) retry(
	ctx context.Context,
	claim orglifecycle.DefaultModelProviderProvisioningClaim,
	cause error,
) error {
	stateCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		defaultModelProviderStateWriteTimeout,
	)
	defer cancel()
	retryErr := worker.store.RetryDefaultModelProviderProvisioning(
		stateCtx,
		orglifecycle.RetryDefaultModelProviderProvisioningInput{
			Claim: claim,
			Delay: defaultModelProviderRetryDelay(claim.Attempt, cause),
		},
	)
	if retryErr != nil {
		return errors.Join(
			cause,
			fmt.Errorf("%w: %w", errDefaultModelProviderRetrySchedule, retryErr),
		)
	}
	return cause
}

func defaultModelProviderRetryDelay(attempt int32, err error) time.Duration {
	switch {
	case errors.Is(err, modelprovider.ErrHostedCredentialPending):
		return defaultModelProviderPendingRetryDelay
	case errors.Is(err, modelprovider.ErrHostedCredentialConflict):
		return defaultModelProviderConflictRetryDelay
	}
	delay := defaultModelProviderInitialRetryDelay
	for current := int32(1); current < attempt && delay < defaultModelProviderMaximumRetryDelay; current++ {
		delay *= 2
	}
	return min(delay, defaultModelProviderMaximumRetryDelay)
}
