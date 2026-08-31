package machinepool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	DefaultReconcileBatchSize           = 40
	DefaultImmediateProvisioningTimeout = 2 * time.Minute
	DefaultImmediateDeletionTimeout     = 2 * time.Minute
	machineWakeAttempts                 = 3
	machineWakeTimeout                  = 2 * time.Minute
	providerInspectionTimeout           = 5 * time.Second
	providerDeletionTimeout             = 5 * time.Second
	providerFailureRetryDelay           = time.Minute
	providerProvisionRetryAttempts      = 4
	poolMachineReconcileConcurrency     = 8
)

var providerProvisionRetryDelays = [...]time.Duration{
	time.Second,
	2 * time.Second,
	4 * time.Second,
}

type Manager struct {
	Execution                  *executionstore.Store
	Identity                   *identitystore.Store
	Catalog                    Catalog
	Omnara                     providers.OmnaraURLs
	runtimeReconciliationState *runtimeReconciliationState
}

func NewManager(
	execution *executionstore.Store,
	identity *identitystore.Store,
	omnara providers.OmnaraURLs,
) *Manager {
	return &Manager{
		Execution:                  execution,
		Identity:                   identity,
		Catalog:                    DefaultCatalog(),
		Omnara:                     omnara,
		runtimeReconciliationState: newRuntimeReconciliationState(),
	}
}

func (m Manager) ValidateDefaultMachinePool(defaultPoolTemplate executionstore.DefaultMachinePoolTemplate) error {
	if err := executionstore.ValidateDefaultMachinePoolTemplate(
		defaultPoolTemplate,
		m.Catalog,
	); err != nil {
		return fmt.Errorf("default machine pool: %w", err)
	}
	return nil
}

func (m Manager) StartLaunchProvisioning(
	parent context.Context,
	logger *slog.Logger,
	orgID storage.ID,
	machineIDs []storage.ID,
) {
	if len(machineIDs) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(
			context.WithoutCancel(parent),
			DefaultImmediateProvisioningTimeout,
		)
		defer cancel()
		for _, machineID := range machineIDs {
			if err := m.ProvisionMachine(ctx, orgID, machineID); err != nil {
				logger.Warn(
					"launch machine provisioning failed",
					"org_id", orgID,
					"machine_id", machineID,
					"error", err,
				)
			}
		}
	}()
}

func (m Manager) ProvisionMachine(ctx context.Context, orgID, machineID storage.ID) error {
	claim, claimed, err := m.Execution.ClaimPoolMachineForProvisioning(ctx, orgID, machineID)
	if err != nil || !claimed {
		return err
	}
	machine := claim.Machine
	machineProvisioning, err := executionstore.MachineProvisioningFromRecord(machine)
	if err != nil {
		return m.handleProvisionFailure(
			ctx,
			machine,
			provisionBackoff(machine.ProvisionAttempts),
			"provider_config_invalid",
			err.Error(),
			err,
		)
	}
	provider, reasonCode, reasonMessage, err := m.providerForMachine(ctx, machine, machineProvisioning, false)
	if err != nil {
		return m.handleProvisionFailure(ctx, machine, providerFailureRetryDelay, reasonCode, reasonMessage, err)
	}
	providerCtx, cancel := context.WithTimeout(ctx, provider.ProvisioningTimeout())
	facts, err := provider.PrepareProvisioning(providerCtx, machineProvisioning)
	cancel()
	if err != nil {
		reasonCode := "provider_preparation_error"
		if errors.Is(err, storeerr.ErrMachineProviderUnavailable) {
			reasonCode = "provider_unavailable"
		}
		return m.handleProvisionFailure(
			ctx,
			machine,
			provisionBackoff(machine.ProvisionAttempts),
			reasonCode,
			err.Error(),
			err,
		)
	}
	admission, err := m.Execution.AdmitPoolMachineProvisioning(
		ctx,
		executionstore.AdmitPoolMachineProvisioningInput{
			OrgID:            machine.OrgID,
			MachineID:        machine.ID,
			MachinePoolID:    machine.MachinePoolID,
			ProvisionAttempt: machine.ProvisionAttempts,
			Facts:            facts,
		},
	)
	if err != nil {
		return m.handleProvisionFailure(
			ctx,
			machine,
			provisionBackoff(machine.ProvisionAttempts),
			"provisioning_admission_failed",
			err.Error(),
			err,
		)
	}
	facts = admission.Facts
	machine.UpdatedAt = admission.UpdatedAt
	machineProvisioning.CPU = facts.CPU
	machineProvisioning.MemoryMB = facts.MemoryMB
	installationID, err := m.Identity.GetInstallationID(ctx)
	if err != nil {
		return err
	}
	machineEnv, err := m.Execution.ResolvePoolMachineProvisioningEnv(ctx, claim)
	if err != nil {
		if errors.Is(err, storeerr.ErrPermanentEnvironment) {
			return m.cleanupFailedProvision(ctx, machine, "machine_environment_invalid", err.Error(), err)
		}
		return m.handleProvisionFailure(
			ctx,
			machine,
			provisionBackoff(machine.ProvisionAttempts),
			"machine_environment_unavailable",
			err.Error(),
			err,
		)
	}
	providerProvisioning, err := m.Execution.BeginPoolMachineProviderProvisioning(
		ctx,
		executionstore.BeginPoolMachineProviderProvisioningInput{
			OrgID:            machine.OrgID,
			MachineID:        machine.ID,
			ProvisionAttempt: machine.ProvisionAttempts,
			TokenName:        "machine lifecycle bootstrap",
		},
	)
	if err != nil {
		return err
	}
	machine.ProviderProvisionAttemptedAt = &providerProvisioning.ProviderProvisionAttemptedAt
	machine.UpdatedAt = providerProvisioning.UpdatedAt
	providerCtx, cancel = context.WithTimeout(ctx, provider.ProvisioningTimeout())
	provisionResult, provisionErr := provisionMachineWithRetry(
		providerCtx,
		provider,
		installationID,
		machine.ID,
		machineProvisioning,
		providerProvisioning.DaemonToken.Token,
		machineEnv,
	)
	cancel()
	if provisionResult.ProviderResourceID != "" {
		observation, err := m.Execution.RecordPoolMachineProvisioningResource(
			ctx,
			executionstore.RecordPoolMachineProvisioningResourceInput{
				OrgID:              machine.OrgID,
				MachineID:          machine.ID,
				ProviderResourceID: provisionResult.ProviderResourceID,
				ProvisionAttempt:   machine.ProvisionAttempts,
			},
		)
		if err != nil {
			return err
		}
		machine.ProviderResourceID = observation.ProviderResourceID
		machine.UpdatedAt = observation.UpdatedAt
	}
	if provisionErr != nil {
		return m.handleProvisionFailure(
			ctx,
			machine,
			provisionBackoff(machine.ProvisionAttempts),
			"provider_error",
			provisionErr.Error(),
			provisionErr,
		)
	}
	if provisionResult.ProviderResourceID == "" {
		err := errors.New("provider did not return a resource id")
		return m.handleProvisionFailure(
			ctx,
			machine,
			provisionBackoff(machine.ProvisionAttempts),
			"provider_missing_resource_id",
			err.Error(),
			err,
		)
	}
	err = m.Execution.CompletePoolMachineProvisioning(
		ctx,
		machine.OrgID,
		machine.ID,
		provisionResult.ProviderResourceID,
		provisionResult.SandboxURL,
		machine.ProvisionAttempts,
	)
	return err
}

func provisionMachineWithRetry(
	ctx context.Context,
	provider providers.Provider,
	installationID, machineID storage.ID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	machineToken string,
	machineEnv map[string]string,
) (providers.ProvisionMachineResult, error) {
	var observed providers.ProvisionMachineResult
	var provisionErr error
	for attempt := range providerProvisionRetryAttempts {
		result, err := provider.ProvisionMachine(
			ctx,
			installationID,
			machineID,
			machineProvisioning,
			machineToken,
			machineEnv,
		)
		provisionErr = err
		if errors.Is(err, providers.ErrResourceReplaced) {
			observed = providers.ProvisionMachineResult{}
		}
		if result.ProviderResourceID != "" {
			if observed.ProviderResourceID != "" && observed.ProviderResourceID != result.ProviderResourceID {
				return observed, fmt.Errorf(
					"provider returned conflicting resource ids %q and %q",
					observed.ProviderResourceID,
					result.ProviderResourceID,
				)
			}
			observed.ProviderResourceID = result.ProviderResourceID
		}
		if result.SandboxURL != "" {
			observed.SandboxURL = result.SandboxURL
		}
		if err == nil {
			return observed, nil
		}
		if ctx.Err() != nil {
			return observed, err
		}
		if attempt == providerProvisionRetryAttempts-1 {
			break
		}
		if err := waitForProviderProvisionRetry(ctx, providerProvisionRetryDelay(attempt, err)); err != nil {
			return observed, provisionErr
		}
	}
	return observed, provisionErr
}

func providerProvisionRetryDelay(attempt int, err error) time.Duration {
	delay := providerProvisionRetryDelays[attempt]
	if retryAfter, ok := providers.RetryAfter(err); ok {
		delay = max(delay, retryAfter)
	}
	return delay
}

func waitForProviderProvisionRetry(ctx context.Context, delay time.Duration) error {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
		return context.DeadlineExceeded
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (m Manager) WakeMachine(ctx context.Context, orgID, machineID storage.ID) (bool, error) {
	machine, err := m.Execution.GetMachine(ctx, orgID, machineID)
	if err != nil {
		return false, err
	}
	if machine.ConnectionState == executionstore.MachineConnectionStateOnline {
		return true, nil
	}
	if machine.ConnectionState != executionstore.MachineConnectionStateAsleep {
		return false, nil
	}
	machineProvisioning, err := executionstore.MachineProvisioningFromRecord(machine)
	if err != nil {
		return false, err
	}
	provider, _, _, err := m.providerForMachine(ctx, machine, machineProvisioning, false)
	if err != nil {
		return false, err
	}
	waker, ok := provider.(providers.MachineWaker)
	if !ok {
		return false, storeerr.ErrMachineNotWakeCapable
	}
	if machine.SourceKind == executionstore.MachineSourceKindPool {
		disposition, err := m.Execution.BeginMachineWake(
			ctx,
			machine.OrgID,
			machine.ID,
			machine.MachinePoolID,
			machineWakeTimeout,
		)
		if err != nil {
			return false, err
		}
		switch disposition {
		case executionstore.MachineWakeOnline, executionstore.MachineWakePending:
			return true, nil
		case executionstore.MachineWakeUnavailable:
			return false, nil
		case executionstore.MachineWakeUnresolved:
			return false, storeerr.ErrMachineWakeUnresolved
		case executionstore.MachineWakeReady:
		default:
			return false, fmt.Errorf("unsupported machine wake disposition %d", disposition)
		}
	}
	input := providers.WakeMachineInput{
		ProviderResourceID: machine.ProviderResourceID,
		SandboxURL:         machine.SandboxURL,
	}
	for range machineWakeAttempts {
		if err = waker.WakeMachine(ctx, input); err == nil {
			return true, nil
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
	}
	return false, err
}

func (m Manager) ReconcileProvisioning(ctx context.Context, limit int32) (int, error) {
	machines, err := m.Execution.ListPoolMachinesForProvisioning(ctx, limit)
	if err != nil {
		return 0, err
	}
	return runBoundedReconcile(
		ctx,
		len(machines),
		poolMachineReconcileConcurrency,
		func(ctx context.Context, i int) error {
			machine := machines[i]
			return m.ProvisionMachine(ctx, machine.OrgID, machine.ID)
		},
	)
}

func (m Manager) ReconcileCleanup(ctx context.Context, limit int32) (int, error) {
	candidates, err := m.Execution.ListPoolMachinesForCleanup(
		ctx,
		executionstore.DefaultPoolMachineProvisionFailureLimit,
		limit,
	)
	if err != nil {
		return 0, err
	}
	return runBoundedReconcile(
		ctx,
		len(candidates),
		poolMachineReconcileConcurrency,
		func(ctx context.Context, i int) error {
			return m.DeleteMachine(ctx, candidates[i])
		},
	)
}

func (m Manager) ReconcileIdleDeletion(ctx context.Context, limit int32) (int, error) {
	candidates, err := m.Execution.ListExpiredIdlePoolMachines(ctx, limit)
	if err != nil {
		return 0, err
	}
	_, err = runBoundedReconcile(
		ctx,
		len(candidates),
		poolMachineReconcileConcurrency,
		func(ctx context.Context, i int) error {
			candidate := candidates[i]
			claim, claimed, err := m.Execution.ClaimExpiredIdlePoolMachineDeletion(
				ctx,
				candidate.OrgID,
				candidate.MachineID,
			)
			if err != nil || !claimed {
				return err
			}
			_, err = m.deleteClaimedMachine(ctx, claim, nil)
			return err
		},
	)
	return len(candidates), err
}

func (m Manager) DeleteMachines(ctx context.Context, machines []executionstore.MachineRecord) (int, error) {
	return runBoundedReconcile(
		ctx,
		len(machines),
		poolMachineReconcileConcurrency,
		func(ctx context.Context, i int) error {
			machine := machines[i]
			return m.DeleteMachine(
				ctx,
				executionstore.PoolMachineCleanupCandidate{
					Machine:       machine,
					ReasonCode:    machine.LifecycleReasonCode,
					ReasonMessage: machine.LifecycleReasonMessage,
				},
			)
		},
	)
}

func (m Manager) DeleteMachine(ctx context.Context, candidate executionstore.PoolMachineCleanupCandidate) error {
	preClaimMachine := candidate.Machine
	claim, claimed, err := m.Execution.ClaimPoolMachineDeletion(ctx, executionstore.MachineDeletingInput{
		OrgID:                    preClaimMachine.OrgID,
		MachineID:                preClaimMachine.ID,
		LifecycleReasonCode:      candidate.ReasonCode,
		LifecycleReasonMessage:   candidate.ReasonMessage,
		ExpectedLifecycleVersion: preClaimMachine.LifecycleVersion,
	})
	if err != nil || !claimed {
		return err
	}
	_, err = m.deleteClaimedMachine(ctx, claim, nil)
	return err
}

func (m Manager) deleteClaimedMachine(
	ctx context.Context,
	claim executionstore.PoolMachineDeletionClaim,
	confirmedDeleter providers.MachineDeleter,
) (bool, error) {
	machine := claim.Machine
	if machine.ProviderResourceID == "" && machine.ProviderProvisionAttemptedAt == nil {
		return false, m.Execution.CompletePoolMachineDeletion(
			ctx,
			machine.OrgID,
			machine.ID,
			machine.DeleteAttempts,
		)
	}
	machineProvisioning, err := executionstore.MachineProvisioningFromRecord(machine)
	if err != nil {
		return false, m.markDeleteFailed(
			ctx,
			machine,
			deleteBackoff(machine.DeleteAttempts),
			"provider_config_invalid",
			"machine config is invalid",
			err,
		)
	}
	deleter := confirmedDeleter
	var provider providers.Provider
	if deleter == nil {
		var reasonCode, reasonMessage string
		provider, reasonCode, reasonMessage, err = m.providerForMachine(
			ctx,
			machine,
			machineProvisioning,
			true,
		)
		if err != nil {
			return false, m.markDeleteFailed(
				ctx,
				machine,
				deleteBackoff(machine.DeleteAttempts),
				reasonCode,
				reasonMessage,
				err,
			)
		}
		deleter = provider
	}
	installationID, err := m.Identity.GetInstallationID(ctx)
	if err != nil {
		return false, err
	}
	resourceID := machine.ProviderResourceID
	if resourceID == "" {
		if provider == nil {
			return false, errors.New("confirmed machine deletion is missing a provider resource id")
		}
		providerCtx, cancel := context.WithTimeout(ctx, providerInspectionTimeout)
		providerResourceID, found, err := provider.InspectMachine(
			providerCtx,
			installationID,
			machine.ID,
			machineProvisioning,
			"",
		)
		cancel()
		if err != nil {
			return true, m.markDeleteFailed(
				ctx,
				machine,
				deleteBackoff(machine.DeleteAttempts),
				"provider_inspect_error",
				err.Error(),
				err,
			)
		}
		if found {
			if providerResourceID == "" {
				err := errors.New("provider inspection returned an empty resource id")
				return true, m.markDeleteFailed(
					ctx,
					machine,
					deleteBackoff(machine.DeleteAttempts),
					"provider_inspect_error",
					err.Error(),
					err,
				)
			}
			observation, err := m.Execution.RecordPoolMachineDeletionResource(
				ctx,
				executionstore.RecordPoolMachineDeletionResourceInput{
					OrgID:              machine.OrgID,
					MachineID:          machine.ID,
					ProviderResourceID: providerResourceID,
					DeleteAttempt:      machine.DeleteAttempts,
				},
			)
			if err != nil {
				return false, err
			}
			resourceID = observation.ProviderResourceID
			machine.ProviderResourceID = observation.ProviderResourceID
			machine.UpdatedAt = observation.UpdatedAt
		} else if claim.CanFinalizeMissingProviderResource {
			return false, m.Execution.CompletePoolMachineDeletion(
				ctx,
				machine.OrgID,
				machine.ID,
				machine.DeleteAttempts,
			)
		} else {
			err := errors.New("provider resource was not found by allocation name")
			return false, m.markDeleteFailed(
				ctx,
				machine,
				deleteBackoff(machine.DeleteAttempts),
				"provider_resource_not_found",
				err.Error(),
				err,
			)
		}
	}
	providerCtx, cancel := context.WithTimeout(ctx, providerDeletionTimeout)
	err = deleter.DeleteMachine(
		providerCtx,
		installationID,
		machine.ID,
		machineProvisioning,
		resourceID,
	)
	cancel()
	if err != nil {
		return true, m.markDeleteFailed(
			ctx,
			machine,
			deleteBackoff(machine.DeleteAttempts),
			"provider_delete_error",
			err.Error(),
			err,
		)
	}
	return false, m.Execution.CompletePoolMachineDeletion(
		ctx,
		machine.OrgID,
		machine.ID,
		machine.DeleteAttempts,
	)
}

func (m Manager) providerForMachine(
	ctx context.Context,
	machine executionstore.MachineRecord,
	machineProvisioning executionstore.MachineProvisioningConfig,
	includeDeletedPool bool,
) (providers.Provider, string, string, error) {
	if machine.MachinePoolID == storage.NilID {
		err := errors.New("pool machine is missing machine pool")
		return nil, "provider_config_invalid", err.Error(), err
	}
	var pool executionstore.MachinePoolRecord
	var err error
	if includeDeletedPool {
		pool, err = m.Execution.GetMachinePoolForLifecycle(ctx, machine.OrgID, machine.MachinePoolID)
	} else {
		pool, err = m.Execution.GetMachinePool(ctx, machine.OrgID, machine.MachinePoolID)
	}
	if err != nil {
		err = fmt.Errorf("get machine pool provider config: %w", err)
		return nil, "provider_config_invalid", "machine pool provider config is unavailable", err
	}
	if machine.Provider != pool.Provider {
		err := fmt.Errorf(
			"machine provider %q does not match machine pool provider %q",
			machine.Provider,
			pool.Provider,
		)
		return nil, "provider_config_invalid", "machine provider does not match machine pool provider", err
	}
	definition, ok := m.Catalog.definition(machine.Provider)
	if !ok {
		err := fmt.Errorf("machine provider %q is not configured", machine.Provider)
		return nil, "provider_not_configured", "machine provider is not configured", err
	}
	if !includeDeletedPool {
		policy, err := pool.ProviderPolicy()
		if err != nil {
			return nil, "provider_config_invalid", "machine pool default config is invalid", err
		}
		if err := definition.ValidateMachineProvisioning(policy, machineProvisioning); err != nil {
			return nil, "provider_config_invalid", err.Error(), err
		}
	}
	providerAuthToken, err := m.Execution.ResolveMachineProviderAuthToken(
		ctx,
		pool.OrgID,
		pool.ManagementKind,
		pool.ProviderAuthSecretID,
		pool.ProviderAuthEnvVar,
	)
	if err != nil {
		return nil, "provider_config_invalid", "machine provider credential is unavailable", err
	}
	runtimeConfig := providers.RuntimeConfig{
		Omnara:            m.Omnara,
		ProviderAuthToken: providerAuthToken,
	}
	provider, err := definition.NewProvider(pool.ProviderConfig, runtimeConfig)
	if err != nil {
		return nil, "provider_config_invalid", "machine provider config is invalid", err
	}
	return provider, "", "", nil
}

func (m Manager) markProvisionFailed(
	ctx context.Context,
	machine executionstore.MachineRecord,
	retryDelay time.Duration,
	reasonCode, reasonMessage string,
	cause error,
) error {
	err := m.Execution.MarkPoolMachineProvisionFailed(ctx, executionstore.PoolMachineProvisionFailureInput{
		OrgID:                  machine.OrgID,
		MachineID:              machine.ID,
		ProvisionAttempt:       machine.ProvisionAttempts,
		LifecycleReasonCode:    reasonCode,
		LifecycleReasonMessage: reasonMessage,
		RetryDelay:             retryDelay,
	})
	if err != nil {
		return err
	}
	return cause
}

func (m Manager) handleProvisionFailure(
	ctx context.Context,
	machine executionstore.MachineRecord,
	retryDelay time.Duration,
	reasonCode, reasonMessage string,
	cause error,
) error {
	if machine.ProvisionAttempts < executionstore.DefaultPoolMachineProvisionFailureLimit {
		return m.markProvisionFailed(ctx, machine, retryDelay, reasonCode, reasonMessage, cause)
	}
	return m.cleanupFailedProvision(
		ctx,
		machine,
		"provision_failed_cleanup",
		"cleaning up machine after provisioning failed",
		cause,
	)
}

func (m Manager) cleanupFailedProvision(
	ctx context.Context,
	machine executionstore.MachineRecord,
	reasonCode, reasonMessage string,
	cause error,
) error {
	deleting, marked, err := m.Execution.MarkPoolMachineDeleting(ctx, executionstore.MachineDeletingInput{
		OrgID:                    machine.OrgID,
		MachineID:                machine.ID,
		LifecycleReasonCode:      reasonCode,
		LifecycleReasonMessage:   reasonMessage,
		ExpectedLifecycleVersion: machine.LifecycleVersion,
	})
	if err != nil {
		return err
	}
	if !marked {
		return cause
	}
	if err := m.DeleteMachine(ctx, executionstore.PoolMachineCleanupCandidate{
		Machine:       deleting,
		ReasonCode:    deleting.LifecycleReasonCode,
		ReasonMessage: deleting.LifecycleReasonMessage,
	}); err != nil {
		return err
	}
	return cause
}

func (m Manager) markDeleteFailed(
	ctx context.Context,
	machine executionstore.MachineRecord,
	retryDelay time.Duration,
	reasonCode, reasonMessage string,
	cause error,
) error {
	err := m.Execution.MarkMachineDeleteFailed(ctx, executionstore.MachineDeleteFailureInput{
		OrgID:                  machine.OrgID,
		MachineID:              machine.ID,
		LifecycleReasonCode:    reasonCode,
		LifecycleReasonMessage: reasonMessage,
		RetryDelay:             retryDelay,
		DeleteAttempt:          machine.DeleteAttempts,
	})
	if err != nil {
		return err
	}
	return cause
}

func runBoundedReconcile(
	ctx context.Context,
	count, concurrency int,
	run func(context.Context, int) error,
) (int, error) {
	if count == 0 {
		return 0, nil
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > count {
		concurrency = count
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	started := 0

	for i := range count {
		select {
		case <-ctx.Done():
			mu.Lock()
			if firstErr == nil {
				firstErr = ctx.Err()
			}
			mu.Unlock()
			wg.Wait()
			return started, firstErr
		case sem <- struct{}{}:
		}

		started++
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if recovered := recover(); recovered != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf(
							"machine reconciliation %d panicked: %v",
							i,
							recovered,
						)
					}
					mu.Unlock()
				}
			}()
			if err := run(ctx, i); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	return started, firstErr
}

func provisionBackoff(attempts int32) time.Duration {
	if attempts <= 1 {
		return 10 * time.Second
	}
	if attempts == 2 {
		return 30 * time.Second
	}
	return 90 * time.Second
}

func deleteBackoff(attempts int32) time.Duration {
	if attempts <= 1 {
		return time.Minute
	}
	if attempts == 2 {
		return 5 * time.Minute
	}
	if attempts == 3 {
		return 30 * time.Minute
	}
	return 24 * time.Hour
}
