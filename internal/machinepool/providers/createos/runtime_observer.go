package createos

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

const runtimeObservationTimeout = 5 * time.Second

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
		if resourceCounts[target.ProviderResourceID] != 1 || machineCounts[target.MachineID] != 1 {
			continue
		}
		observationCtx, cancel := context.WithTimeout(ctx, runtimeObservationTimeout)
		observation, err := p.ObserveRuntimeState(observationCtx, target)
		cancel()
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
	targetSandbox, found, err := p.api.GetSandbox(ctx, target.ProviderResourceID)
	if err != nil {
		return observation, fmt.Errorf(
			"get createos sandbox %q for runtime observation: %w",
			target.ProviderResourceID,
			err,
		)
	}
	if !found {
		observation.State = providers.RuntimeStateTerminated
		return observation, nil
	}
	expectedName, err := allocationName(target.InstallationID, target.MachineID)
	if err != nil {
		return observation, fmt.Errorf("build createos allocation name: %w", err)
	}
	if targetSandbox.ID != target.ProviderResourceID || targetSandbox.Name != expectedName {
		return observation, nil
	}
	observation.State = createOSRuntimeState(targetSandbox.Status)
	return observation, nil
}

func createOSRuntimeState(status sandboxStatus) providers.RuntimeState {
	switch status {
	case sandboxStatusRunning:
		return providers.RuntimeStateRunning
	case sandboxStatusPaused, sandboxStatusError:
		return providers.RuntimeStateInactive
	case sandboxStatusCreating, sandboxStatusPausing, sandboxStatusResuming,
		sandboxStatusForking, sandboxStatusDestroying:
		return providers.RuntimeStateTransitional
	case sandboxStatusDestroyed, sandboxStatusFailed:
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

var _ providers.RuntimeStateObserver = (*provider)(nil)
