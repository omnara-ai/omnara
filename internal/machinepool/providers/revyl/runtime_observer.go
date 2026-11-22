package revyl

import (
	"context"
	"fmt"
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
	for index, target := range targets {
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
	if target.InstallationID == uuid.Nil || target.MachineID == uuid.Nil || target.ProviderResourceID == "" {
		return observation, nil
	}
	id, err := machineComputerID(target.InstallationID, target.MachineID)
	if err != nil {
		return observation, err
	}
	if target.ProviderResourceID != id.String() {
		return observation, nil
	}
	current, found, err := p.api.GetComputer(ctx, id)
	if err != nil {
		return observation, fmt.Errorf("get revyl computer %s for runtime observation: %w", id, err)
	}
	if !found {
		observation.State = providers.RuntimeStateTerminated
		return observation, nil
	}
	if !ownsComputer(current, id) {
		return observation, nil
	}
	switch current.State {
	case computerStateActive:
		observation.State = providers.RuntimeStateRunning
	case computerStateStarting, computerStateClosing:
		observation.State = providers.RuntimeStateTransitional
	case computerStateStopped, computerStateFailed, computerStateOwnerLost:
		observation.State = providers.RuntimeStateTerminated
	}
	return observation, nil
}

var _ providers.RuntimeStateObserver = (*provider)(nil)
