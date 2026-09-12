package arker

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

// Arker's "idle" means no command is in flight, not that the machine is down,
// so a vm that exists is running and only a vm that is gone is terminated.
// Reporting idle as inactive would retire working machines.
func runtimeState(found bool) providers.RuntimeState {
	if found {
		return providers.RuntimeStateRunning
	}
	return providers.RuntimeStateTerminated
}

// One machine at a time, not from the org-wide listing: a live machine can be
// missing from that aggregate, and a false absence retires it.
func (p *provider) ObserveRuntimeStates(
	ctx context.Context,
	targets []providers.RuntimeTarget,
) ([]providers.RuntimeObservation, error) {
	observations := make([]providers.RuntimeObservation, 0, len(targets))
	for _, target := range targets {
		// A bulk observation is a discovery hint, so one unreadable machine does
		// not lose the sweep.
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
		// An id that is not this machine's is a fact about one machine, not a
		// provider outage: reconciliation treats any error here as a scope
		// failure and would put the whole pool on cooldown for it.
		if errors.Is(err, errNotThisMachine) {
			return observation, nil
		}
		// Unknown, not terminated: a failed lookup is not evidence of absence.
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
