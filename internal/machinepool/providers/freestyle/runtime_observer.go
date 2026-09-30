package freestyle

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/publicid"
)

const runtimeListPageSize = 100

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
	targetIndexes := make(map[string]int, len(targets))
	installations := make(map[uuid.UUID]struct{})
	for index, target := range targets {
		if !validRuntimeTarget(target) ||
			resourceCounts[target.ProviderResourceID] != 1 ||
			machineCounts[target.MachineID] != 1 {
			continue
		}
		targetIndexes[target.ProviderResourceID] = index
		installations[target.InstallationID] = struct{}{}
	}
	listed := make(map[string][]vm, len(targetIndexes))
	for installationID := range installations {
		if err := p.listOwnedVMs(ctx, installationID, targetIndexes, listed); err != nil {
			return nil, err
		}
	}
	for resourceID, index := range targetIndexes {
		if matches := listed[resourceID]; len(matches) == 1 {
			observations[index] = runtimeObservation(targets[index], matches[0])
		}
	}
	return observations, nil
}

func (p *provider) listOwnedVMs(
	ctx context.Context,
	installationID uuid.UUID,
	targetIndexes map[string]int,
	listed map[string][]vm,
) error {
	installationOwner, err := publicid.Encode(publicid.KindInstallation, installationID)
	if err != nil {
		return err
	}
	filter := installationMarkerKey + ":" + installationOwner
	for offset := 0; ; {
		page, err := p.api.ListVMs(ctx, filter, runtimeListPageSize, offset)
		if err != nil {
			return fmt.Errorf("list freestyle VMs for runtime observation: %w", err)
		}
		for _, current := range page.VMs {
			if _, requested := targetIndexes[current.ID]; requested {
				listed[current.ID] = append(listed[current.ID], current)
			}
		}
		offset += len(page.VMs)
		if len(page.VMs) == 0 || offset >= page.TotalCount {
			return nil
		}
	}
}

func (p *provider) ObserveRuntimeState(
	ctx context.Context,
	target providers.RuntimeTarget,
) (providers.RuntimeObservation, error) {
	if !validRuntimeTarget(target) {
		return target.UnknownObservation(), nil
	}
	current, found, err := p.api.GetVM(ctx, target.ProviderResourceID)
	if err != nil {
		return target.UnknownObservation(), fmt.Errorf(
			"get freestyle VM %q for runtime observation: %w",
			target.ProviderResourceID,
			err,
		)
	}
	if !found {
		observation := target.UnknownObservation()
		observation.State = providers.RuntimeStateTerminated
		return observation, nil
	}
	return runtimeObservation(target, current), nil
}

func runtimeObservation(target providers.RuntimeTarget, current vm) providers.RuntimeObservation {
	observation := target.UnknownObservation()
	expectedName, err := providers.MachineAllocationName(target.InstallationID, target.MachineID)
	if err == nil && current.ID == target.ProviderResourceID &&
		validateOwnedVM(current, target.InstallationID, target.MachineID, expectedName) == nil {
		observation.State = freestyleRuntimeState(current.State)
	}
	return observation
}

func validRuntimeTarget(target providers.RuntimeTarget) bool {
	return target.InstallationID != uuid.Nil &&
		target.MachineID != uuid.Nil &&
		target.ProviderResourceID != "" &&
		target.ProviderResourceID == strings.TrimSpace(target.ProviderResourceID)
}

func freestyleRuntimeState(value string) providers.RuntimeState {
	switch normalizeVMState(value) {
	case vmStateRunning:
		return providers.RuntimeStateRunning
	case vmStatePaused, vmStateStopped:
		return providers.RuntimeStateInactive
	case vmStateStarting, vmStatePausing:
		return providers.RuntimeStateTransitional
	default:
		return providers.RuntimeStateUnknown
	}
}

var _ providers.RuntimeStateObserver = (*provider)(nil)
