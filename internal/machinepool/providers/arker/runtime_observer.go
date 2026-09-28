package arker

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

// Arker's `idle` means no command is in flight, not suspended, so presence is
// the signal: absence is the only terminal one.
func runtimeState(found bool) providers.RuntimeState {
	if found {
		return providers.RuntimeStateRunning
	}
	return providers.RuntimeStateTerminated
}

// A per-target failure is that machine's problem, not the scope's: one VM that
// 404s or answers oddly must not discard the observations already gathered for
// every other machine in the pool, which is what aborting the batch did. Such a
// target stays `unknown` and reconciliation simply learns nothing about it this
// pass. A failure that is genuinely the provider's -- every target erroring --
// is still returned, so discovery records the outage and backs the scope off
// instead of retrying each machine on every pass.
func (p *provider) ObserveRuntimeStates(
	ctx context.Context,
	targets []providers.RuntimeTarget,
) ([]providers.RuntimeObservation, error) {
	observations := make([]providers.RuntimeObservation, 0, len(targets))
	failed := 0
	var firstErr error
	for _, target := range targets {
		observation, err := p.ObserveRuntimeState(ctx, target)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			observations = append(observations, target.UnknownObservation())
			continue
		}
		observations = append(observations, observation)
	}
	if failed > 0 && failed == len(targets) {
		return nil, firstErr
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
