package arker

import (
	"context"
	"errors"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func (p *provider) ObserveRuntimeStates(
	ctx context.Context,
	targets []providers.RuntimeTarget,
) ([]providers.RuntimeObservation, error) {
	observations := make([]providers.RuntimeObservation, len(targets))
	for index, target := range targets {
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
	if target.ProviderResourceID == "" {
		return observation, nil
	}
	machine, found, err := p.inspectVM(
		ctx,
		target.InstallationID,
		target.MachineID,
		target.MachineProvisioning,
		target.ProviderResourceID,
	)
	if errors.Is(err, errNotThisMachine) {
		return observation, nil
	}
	if err != nil {
		return observation, err
	}
	switch {
	case !found:
		observation.State = providers.RuntimeStateTerminated
	case machine.State == "running":
		observation.State = providers.RuntimeStateRunning
	case machine.State == "idle":
		observation.State = providers.RuntimeStateInactive
	}
	return observation, nil
}
