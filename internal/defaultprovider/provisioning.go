package defaultprovider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	pendingRetryDelay  = 2 * time.Minute
	conflictRetryDelay = 24 * time.Hour
	initialRetryDelay  = 5 * time.Second
	maximumRetryDelay  = 15 * time.Minute
	stateWriteTimeout  = 30 * time.Second
)

var ErrRetrySchedule = errors.New("schedule default model provider provisioning retry")

type Store interface {
	ClaimDefaultModelProviderProvisioning(
		context.Context,
	) (orglifecycle.DefaultModelProviderProvisioningClaim, bool, error)
	ClaimDefaultModelProviderProvisioningForOrganization(
		context.Context,
		uuid.UUID,
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

type Runner struct {
	store       Store
	provisioner modelprovider.HostedCredentialProvisioner
	template    modelstore.DefaultModelProviderTemplate
}

func NewRunner(
	store Store,
	provisioner modelprovider.HostedCredentialProvisioner,
	template modelstore.DefaultModelProviderTemplate,
) *Runner {
	return &Runner{store: store, provisioner: provisioner, template: template}
}

func (runner *Runner) RunNext(ctx context.Context) (bool, string, error) {
	claim, found, err := runner.store.ClaimDefaultModelProviderProvisioning(ctx)
	if err != nil || !found {
		return false, "", err
	}
	return runner.runClaim(ctx, claim)
}

func (runner *Runner) RunOrganization(
	ctx context.Context,
	organizationID uuid.UUID,
) (bool, string, error) {
	claim, found, err := runner.store.ClaimDefaultModelProviderProvisioningForOrganization(
		ctx,
		organizationID,
	)
	if err != nil || !found {
		return false, "", err
	}
	return runner.runClaim(ctx, claim)
}

func (runner *Runner) runClaim(
	ctx context.Context,
	claim orglifecycle.DefaultModelProviderProvisioningClaim,
) (bool, string, error) {
	organizationID, err := publicid.Encode(publicid.KindOrganization, claim.OrgID)
	if err != nil {
		return true, "", runner.retry(ctx, claim, err)
	}
	creatorUserID, err := publicid.Encode(publicid.KindUser, claim.CreatorUserID)
	if err != nil {
		return true, organizationID, runner.retry(ctx, claim, err)
	}

	provisionCtx, cancel := context.WithTimeout(ctx, modelprovider.HostedCredentialProvisionTimeout)
	response, provisionErr := runner.provisioner.ProvisionHostedCredential(
		provisionCtx,
		modelprovider.HostedCredentialRequest{
			OrgID:         organizationID,
			CreatorUserID: creatorUserID,
			Template:      runner.template,
		},
	)
	cancel()
	if provisionErr != nil {
		return true, organizationID, runner.retry(ctx, claim, provisionErr)
	}

	stateCtx, stateCancel := context.WithTimeout(context.WithoutCancel(ctx), stateWriteTimeout)
	completeErr := runner.store.CompleteDefaultModelProviderProvisioning(
		stateCtx,
		orglifecycle.CompleteDefaultModelProviderProvisioningInput{
			Claim:           claim,
			Template:        runner.template,
			CredentialValue: response.CredentialValue,
		},
	)
	stateCancel()
	if completeErr != nil {
		if errors.Is(completeErr, orglifecycle.ErrDefaultModelProviderProvisioningSuperseded) ||
			errors.Is(completeErr, storeerr.ErrStateTransitionConflict) ||
			errors.Is(completeErr, storeerr.ErrNotFound) {
			return true, organizationID, completeErr
		}
		return true, organizationID, runner.retry(ctx, claim, completeErr)
	}
	return true, organizationID, nil
}

func (runner *Runner) retry(
	ctx context.Context,
	claim orglifecycle.DefaultModelProviderProvisioningClaim,
	cause error,
) error {
	stateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stateWriteTimeout)
	defer cancel()
	retryErr := runner.store.RetryDefaultModelProviderProvisioning(
		stateCtx,
		orglifecycle.RetryDefaultModelProviderProvisioningInput{
			Claim: claim,
			Delay: retryDelay(claim.Attempt, cause),
		},
	)
	if retryErr != nil {
		return errors.Join(cause, fmt.Errorf("%w: %w", ErrRetrySchedule, retryErr))
	}
	return cause
}

func retryDelay(attempt int32, err error) time.Duration {
	switch {
	case errors.Is(err, modelprovider.ErrHostedCredentialPending):
		return pendingRetryDelay
	case errors.Is(err, modelprovider.ErrHostedCredentialConflict):
		return conflictRetryDelay
	}
	delay := initialRetryDelay
	for current := int32(1); current < attempt && delay < maximumRetryDelay; current++ {
		delay *= 2
	}
	return min(delay, maximumRetryDelay)
}
