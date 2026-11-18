package docker

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

const runtimeListNameBatch = 100

var _ providers.RuntimeStateObserver = (*provider)(nil)

func (p *provider) ObserveRuntimeStates(
	ctx context.Context,
	targets []providers.RuntimeTarget,
) ([]providers.RuntimeObservation, error) {
	observations := make([]providers.RuntimeObservation, len(targets))
	resourceCounts := make(map[string]int, len(targets))
	machineCounts := make(map[uuid.UUID]int, len(targets))
	namesByOwner := make(map[string][]string, 1)
	for index, target := range targets {
		observations[index] = target.UnknownObservation()
		resourceCounts[target.ProviderResourceID]++
		machineCounts[target.MachineID]++
		if name, labels, err := machineIdentity(target.InstallationID, target.MachineID); err == nil {
			owner := labels[installationLabel]
			namesByOwner[owner] = append(namesByOwner[owner], name)
		}
	}
	listed := make(map[string][]containerSummary)
	for owner, names := range namesByOwner {
		for batch := range slices.Chunk(names, runtimeListNameBatch) {
			containers, err := p.api.ListContainers(ctx, map[string][]string{
				"label": {installationLabel + "=" + owner},
				"name":  batch,
			})
			if err != nil {
				return nil, fmt.Errorf("list docker containers for runtime observation: %w", err)
			}
			for _, current := range containers {
				for _, name := range current.Names {
					name = strings.TrimPrefix(name, "/")
					listed[name] = append(listed[name], current)
				}
			}
		}
	}
	for index, target := range targets {
		if resourceCounts[target.ProviderResourceID] != 1 || machineCounts[target.MachineID] != 1 {
			continue
		}
		name, labels, err := machineIdentity(target.InstallationID, target.MachineID)
		if err != nil || target.ProviderResourceID != name {
			continue
		}
		matches := listed[name]
		switch {
		case len(matches) == 0:
			observations[index].State = providers.RuntimeStateTerminated
		case len(matches) == 1 && ownedBy(matches[0].Labels, labels):
			observations[index].State = runtimeState(matches[0].State)
		}
	}
	return observations, nil
}

func (p *provider) ObserveRuntimeState(
	ctx context.Context,
	target providers.RuntimeTarget,
) (providers.RuntimeObservation, error) {
	observation := target.UnknownObservation()
	name, labels, err := machineIdentity(target.InstallationID, target.MachineID)
	if err != nil {
		return observation, err
	}
	if target.ProviderResourceID != name {
		return observation, nil
	}
	current, found, err := p.api.InspectContainer(ctx, name)
	if err != nil {
		return observation, fmt.Errorf("inspect docker container %q for runtime observation: %w", name, err)
	}
	if !found {
		observation.State = providers.RuntimeStateTerminated
		return observation, nil
	}
	if ownedBy(current.Config.Labels, labels) {
		observation.State = runtimeState(current.State.Status)
	}
	return observation, nil
}

func runtimeState(status string) providers.RuntimeState {
	switch status {
	case "running":
		return providers.RuntimeStateRunning
	case "paused":
		return providers.RuntimeStateInactive
	case "created", "restarting", "removing":
		return providers.RuntimeStateTransitional
	case "exited", "dead":
		return providers.RuntimeStateTerminated
	default:
		return providers.RuntimeStateUnknown
	}
}
