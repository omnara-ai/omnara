package freestyle

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

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
	for index, target := range targets {
		if !validRuntimeTarget(target) ||
			resourceCounts[target.ProviderResourceID] != 1 ||
			machineCounts[target.MachineID] != 1 {
			continue
		}
		observation, err := p.ObserveRuntimeState(ctx, target)
		if err != nil {
			return nil, err
		}
		observations[index] = observation
	}
	return observations, nil
}

func (p *provider) ObserveRuntimeState(
	ctx context.Context,
	target providers.RuntimeTarget,
) (providers.RuntimeObservation, error) {
	observation := target.UnknownObservation()
	if !validRuntimeTarget(target) {
		return observation, nil
	}
	current, found, err := p.api.GetVM(ctx, target.ProviderResourceID)
	if err != nil {
		return observation, fmt.Errorf(
			"get freestyle VM %q for runtime observation: %w",
			target.ProviderResourceID,
			err,
		)
	}
	if !found {
		observation.State = providers.RuntimeStateTerminated
		return observation, nil
	}
	expectedName, err := providers.MachineAllocationName(target.InstallationID, target.MachineID)
	if err != nil || validateOwnedVM(current, target.InstallationID, target.MachineID, expectedName) != nil {
		return observation, nil
	}
	observation.State = freestyleRuntimeState(current.State)
	return observation, nil
}

func validRuntimeTarget(target providers.RuntimeTarget) bool {
	return target.InstallationID != uuid.Nil &&
		target.MachineID != uuid.Nil &&
		target.ProviderResourceID != "" &&
		target.ProviderResourceID == strings.TrimSpace(target.ProviderResourceID)
}

func freestyleRuntimeState(value string) providers.RuntimeState {
	switch normalizeVMState(value) {
	case vmStateRunning:
		return providers.RuntimeStateRunning
	case vmStatePaused, vmStateStopped:
		return providers.RuntimeStateInactive
	case vmStateStarting, vmStatePausing:
		return providers.RuntimeStateTransitional
	default:
		return providers.RuntimeStateUnknown
	}
}

var _ providers.RuntimeStateObserver = (*provider)(nil)
