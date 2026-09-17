package arker

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func runtimeState(found bool) providers.RuntimeState {
	if found {
		return providers.RuntimeStateRunning
	}
	return providers.RuntimeStateTerminated
}

func (p *provider) ObserveRuntimeStates(
	ctx context.Context,
	targets []providers.RuntimeTarget,
) ([]providers.RuntimeObservation, error) {
	observations := make([]providers.RuntimeObservation, 0, len(targets))
	for _, target := range targets {
		observation, err := p.ObserveRuntimeState(ctx, target)
		if err != nil {
			observation = target.UnknownObservation()
		}
		observations = append(observations, observation)
	}
	return observations, nil
}

func (p *provider) ObserveRuntimeState(
	ctx context.Context,
	target providers.RuntimeTarget,
) (providers.RuntimeObservation, error) {
	observation := target.UnknownObservation()
	if target.ProviderResourceID == "" {
		return observation, nil
	}
	resourceID, found, err := p.InspectMachine(
		ctx,
		target.InstallationID,
		target.MachineID,
		target.MachineProvisioning,
		target.ProviderResourceID,
	)
	if err != nil {
		if errors.Is(err, errNotThisMachine) {
			return observation, nil
		}
		return observation, fmt.Errorf(
			"get arker vm %q for runtime observation: %w",
			target.ProviderResourceID,
			err,
		)
	}
	if found {
		observation.ProviderResourceID = resourceID
	}
	observation.State = runtimeState(found)
	return observation, nil
}
