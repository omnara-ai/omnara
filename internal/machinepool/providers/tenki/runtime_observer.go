package tenki

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

var _ providers.RuntimeStateObserver = (*provider)(nil)

func normalizeRuntimeState(state string) providers.RuntimeState {
	switch state {
	case sessionStateRunning:
		return providers.RuntimeStateRunning
	case sessionStatePaused, sessionStateUserShutdown:
		return providers.RuntimeStateInactive
	case sessionStateCreating, sessionStatePausing, sessionStateResuming, sessionStateTerminating:
		return providers.RuntimeStateTransitional
	case sessionStateTerminated:
		return providers.RuntimeStateTerminated
	default:
		return providers.RuntimeStateUnknown
	}
}

func validRuntimeTarget(target providers.RuntimeTarget) bool {
	return target.InstallationID != uuid.Nil && target.MachineID != uuid.Nil &&
		target.ProviderResourceID != "" &&
		strings.TrimSpace(target.ProviderResourceID) == target.ProviderResourceID
}

func (p *provider) ObserveRuntimeState(
	ctx context.Context,
	target providers.RuntimeTarget,
) (providers.RuntimeObservation, error) {
	observation := target.UnknownObservation()
	if !validRuntimeTarget(target) {
		return observation, nil
	}
	current, found, err := p.api.Get(ctx, target.ProviderResourceID)
	if err != nil {
		return observation, fmt.Errorf("get tenki session %q for runtime observation: %w", target.ProviderResourceID, err)
	}
	if !found {
		observation.State = providers.RuntimeStateTerminated
	} else if current.ID == target.ProviderResourceID && ownedBy(current, target.InstallationID, target.MachineID) {
		observation.State = normalizeRuntimeState(current.State)
	}
	return observation, nil
}

func (p *provider) ObserveRuntimeStates(
	ctx context.Context,
	targets []providers.RuntimeTarget,
) ([]providers.RuntimeObservation, error) {
	result := make([]providers.RuntimeObservation, len(targets))
	resourceCounts := make(map[string]int)
	machineCounts := make(map[uuid.UUID]int)
	for _, target := range targets {
		resourceCounts[target.ProviderResourceID]++
		machineCounts[target.MachineID]++
	}
	for i, target := range targets {
		result[i] = target.UnknownObservation()
		if resourceCounts[target.ProviderResourceID] != 1 || machineCounts[target.MachineID] != 1 {
			continue
		}
		observation, err := p.ObserveRuntimeState(ctx, target)
		if err != nil {
			return nil, err
		}
		result[i] = observation
	}
	return result, nil
}
