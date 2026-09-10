package modal

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage"
)

var _ providers.RuntimeStateObserver = (*provider)(nil)

const runtimeObservationTimeout = 5 * time.Second

func (p *provider) ObserveRuntimeStates(
	ctx context.Context,
	targets []providers.RuntimeTarget,
) ([]providers.RuntimeObservation, error) {
	observations := make([]providers.RuntimeObservation, len(targets))
	resourceCounts := make(map[string]int, len(targets))
	machineCounts := make(map[storage.ID]int, len(targets))
	for index, target := range targets {
		observations[index] = target.UnknownObservation()
		resourceCounts[target.ProviderResourceID]++
		machineCounts[target.MachineID]++
	}
	if len(targets) == 0 {
		return observations, nil
	}
	api, err := p.apiClient()
	if err != nil {
		return nil, err
	}
	if p.api == nil {
		defer api.Close()
	}
	for index, target := range targets {
		if resourceCounts[target.ProviderResourceID] != 1 || machineCounts[target.MachineID] != 1 {
			continue
		}
		observationCtx, cancel := context.WithTimeout(ctx, runtimeObservationTimeout)
		observation, err := observeRuntimeState(observationCtx, api, target)
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
	api, err := p.apiClient()
	if err != nil {
		return target.UnknownObservation(), err
	}
	if p.api == nil {
		defer api.Close()
	}
	return observeRuntimeState(ctx, api, target)
}

func observeRuntimeState(
	ctx context.Context,
	api apiClient,
	target providers.RuntimeTarget,
) (providers.RuntimeObservation, error) {
	observation := target.UnknownObservation()
	if target.InstallationID == storage.NilID || target.MachineID == storage.NilID ||
		target.ProviderResourceID == "" ||
		target.ProviderResourceID != strings.TrimSpace(target.ProviderResourceID) {
		return observation, nil
	}
	current, found, err := api.GetSandboxByID(ctx, target.ProviderResourceID)
	if err != nil {
		return observation, fmt.Errorf("observe modal sandbox runtime: %w", err)
	}
	if !found {
		observation.State = providers.RuntimeStateTerminated
		return observation, nil
	}
	if current.ID != target.ProviderResourceID || !sandboxOwnedBy(current, target.InstallationID, target.MachineID) {
		return observation, nil
	}
	if current.Running {
		observation.State = providers.RuntimeStateRunning
	} else {
		observation.State = providers.RuntimeStateTerminated
	}
	return observation, nil
}
