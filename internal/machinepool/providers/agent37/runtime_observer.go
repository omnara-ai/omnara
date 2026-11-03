package agent37

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

var _ providers.RuntimeStateObserver = (*provider)(nil)

func (p *provider) ObserveRuntimeStates(
	ctx context.Context,
	targets []providers.RuntimeTarget,
) ([]providers.RuntimeObservation, error) {
	observations := make([]providers.RuntimeObservation, len(targets))
	resourceCounts := make(map[string]int, len(targets))
	machineCounts := make(map[uuid.UUID]int, len(targets))
	for index, target := range targets {
		observations[index] = target.UnknownObservation()
		resourceCounts[target.ProviderResourceID]++
		machineCounts[target.MachineID]++
	}
	if len(targets) == 0 {
		return observations, nil
	}
	instances, err := p.api.ListInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("list agent37 instances for runtime observation: %w", err)
	}
	byID := make(map[string]instance, len(instances))
	for _, current := range instances {
		byID[current.ID] = current
	}
	for index, target := range targets {
		if resourceCounts[target.ProviderResourceID] != 1 || machineCounts[target.MachineID] != 1 {
			continue
		}
		current, found := byID[target.ProviderResourceID]
		observations[index] = runtimeObservation(target, current, found)
	}
	return observations, nil
}

func (p *provider) ObserveRuntimeState(
	ctx context.Context,
	target providers.RuntimeTarget,
) (providers.RuntimeObservation, error) {
	if !validRuntimeTarget(target) {
		return target.UnknownObservation(), nil
	}
	current, found, err := p.api.GetInstance(ctx, target.ProviderResourceID)
	if err != nil {
		return target.UnknownObservation(), fmt.Errorf(
			"get agent37 instance %q for runtime observation: %w",
			target.ProviderResourceID,
			err,
		)
	}
	return runtimeObservation(target, current, found), nil
}

func runtimeObservation(
	target providers.RuntimeTarget,
	current instance,
	found bool,
) providers.RuntimeObservation {
	observation := target.UnknownObservation()
	switch {
	case !validRuntimeTarget(target):
	case !found:
		observation.State = providers.RuntimeStateTerminated
	case current.ID == target.ProviderResourceID && ownedBy(current, target.InstallationID, target.MachineID):
		observation.State = runtimeState(current.Status)
	}
	return observation
}

func runtimeState(status instanceStatus) providers.RuntimeState {
	switch status {
	case instanceStatusRunning:
		return providers.RuntimeStateRunning
	case instanceStatusStopped, instanceStatusSleeping:
		return providers.RuntimeStateInactive
	case instanceStatusProvisioning, instanceStatusStarting, instanceStatusStopping, instanceStatusRestarting,
		instanceStatusUpdating, instanceStatusWaking, instanceStatusDeleting:
		return providers.RuntimeStateTransitional
	case instanceStatusFailed, instanceStatusDeleted:
		return providers.RuntimeStateTerminated
	default:
		return providers.RuntimeStateUnknown
	}
}

func validRuntimeTarget(target providers.RuntimeTarget) bool {
	return target.InstallationID != uuid.Nil && target.MachineID != uuid.Nil &&
		target.ProviderResourceID != "" &&
		target.ProviderResourceID == strings.TrimSpace(target.ProviderResourceID)
}
