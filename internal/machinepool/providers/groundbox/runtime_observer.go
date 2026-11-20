package groundbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

const runtimeObservationTimeout = 5 * time.Second

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
	current, found, err := p.api.GetSandbox(ctx, target.ProviderResourceID)
	if err != nil {
		return observation, fmt.Errorf(
			"get groundbox sandbox %q for runtime observation: %w",
			target.ProviderResourceID,
			err,
		)
	}
	switch {
	case !found:
		observation.State = providers.RuntimeStateTerminated
	case current.ID == target.ProviderResourceID && ownedBy(current, target.InstallationID, target.MachineID):
		observation.State = runtimeState(current.Status)
	}
	return observation, nil
}

func runtimeState(status sandboxStatus) providers.RuntimeState {
	switch status {
	case sandboxStatusRunning:
		return providers.RuntimeStateRunning
	case sandboxStatusPaused, sandboxStatusShutDown:
		return providers.RuntimeStateInactive
	case sandboxStatusQueued, sandboxStatusStarting, sandboxStatusPausing, sandboxStatusResuming,
		sandboxStatusShuttingDown, sandboxStatusStopping:
		return providers.RuntimeStateTransitional
	case sandboxStatusStopped, sandboxStatusFailed:
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
