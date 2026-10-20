package e2b

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
	if target.InstallationID == uuid.Nil || target.MachineID == uuid.Nil || target.ProviderResourceID == "" ||
		target.ProviderResourceID != strings.TrimSpace(target.ProviderResourceID) {
		return observation, nil
	}
	current, found, err := p.api.GetSandbox(ctx, target.ProviderResourceID)
	if err != nil {
		return observation, fmt.Errorf(
			"get e2b sandbox %q for runtime observation: %w",
			target.ProviderResourceID,
			err,
		)
	}
	if !found {
		observation.State = providers.RuntimeStateTerminated
		return observation, nil
	}
	metadata, err := ownershipMetadata(target.InstallationID, target.MachineID)
	if err != nil {
		return observation, err
	}
	if current.SandboxID != target.ProviderResourceID || !ownedBy(current, metadata) {
		return observation, nil
	}
	switch current.State {
	case sandboxStateRunning:
		observation.State = providers.RuntimeStateRunning
	case sandboxStatePaused:
		observation.State = providers.RuntimeStateInactive
	}
	return observation, nil
}

var _ providers.RuntimeStateObserver = (*provider)(nil)
