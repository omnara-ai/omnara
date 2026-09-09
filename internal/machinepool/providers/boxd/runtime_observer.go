package boxd

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func (p *provider) ObserveRuntimeStates(
	ctx context.Context,
	targets []providers.RuntimeTarget,
) ([]providers.RuntimeObservation, error) {
	if len(targets) == 0 {
		return []providers.RuntimeObservation{}, nil
	}
	requestedResourceIDs := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if validRuntimeTarget(target) {
			requestedResourceIDs[target.ProviderResourceID] = struct{}{}
		}
	}
	if len(requestedResourceIDs) == 0 {
		return observationsForMatches(targets, nil), nil
	}
	// ListVMs returns the whole org fleet in one response; boxd exposes no
	// pagination or state filters.
	vms, err := p.api.ListVMs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list boxd machines for runtime observation: %w", err)
	}
	matches := make(map[string][]vm, len(requestedResourceIDs))
	for _, current := range vms {
		if _, requested := requestedResourceIDs[current.ID]; requested {
			if len(matches[current.ID]) < 2 {
				matches[current.ID] = append(matches[current.ID], current)
			}
		}
	}
	return observationsForMatches(targets, matches), nil
}

func (p *provider) ObserveRuntimeState(
	ctx context.Context,
	target providers.RuntimeTarget,
) (providers.RuntimeObservation, error) {
	observation := target.UnknownObservation()
	if !validRuntimeTarget(target) {
		return observation, nil
	}
	current, found, err := p.api.GetVM(ctx, target.ProviderResourceID)
	if err != nil {
		return observation, fmt.Errorf(
			"get boxd machine %q for runtime observation: %w",
			target.ProviderResourceID,
			err,
		)
	}
	if !found {
		observation.State = providers.RuntimeStateTerminated
		return observation, nil
	}
	return observationForVM(target, current), nil
}

func observationsForMatches(
	targets []providers.RuntimeTarget,
	matches map[string][]vm,
) []providers.RuntimeObservation {
	resourceCounts := make(map[string]int, len(targets))
	machineCounts := make(map[uuid.UUID]int, len(targets))
	for _, target := range targets {
		resourceCounts[target.ProviderResourceID]++
		machineCounts[target.MachineID]++
	}
	observations := make([]providers.RuntimeObservation, len(targets))
	for index, target := range targets {
		observations[index] = target.UnknownObservation()
		if !validRuntimeTarget(target) || resourceCounts[target.ProviderResourceID] != 1 ||
			machineCounts[target.MachineID] != 1 {
			continue
		}
		matched := matches[target.ProviderResourceID]
		if len(matched) != 1 {
			continue
		}
		observations[index] = observationForVM(target, matched[0])
	}
	return observations
}

func observationForVM(target providers.RuntimeTarget, current vm) providers.RuntimeObservation {
	observation := target.UnknownObservation()
	expectedName, err := providers.MachineAllocationName(target.InstallationID, target.MachineID)
	if err != nil || current.ID != target.ProviderResourceID || !vmOwnedBy(current, expectedName) {
		return observation
	}
	observation.State = runtimeState(current.Status)
	return observation
}

func runtimeState(value vmStatus) providers.RuntimeState {
	switch normalizeVMStatus(value) {
	case vmStatusRunning:
		return providers.RuntimeStateRunning
	case vmStatusStopped, vmStatusSuspended, vmStatusStandby, vmStatusHibernated:
		return providers.RuntimeStateInactive
	case vmStatusDestroyed:
		return providers.RuntimeStateTerminated
	case vmStatusPending, vmStatusStarting, vmStatusStopping, vmStatusHibernating,
		vmStatusRebooting, vmStatusMigrating, vmStatusDestroying:
		return providers.RuntimeStateTransitional
	case vmStatusFailed:
		return providers.RuntimeStateUnknown
	default:
		return providers.RuntimeStateUnknown
	}
}

func validRuntimeTarget(target providers.RuntimeTarget) bool {
	return target.InstallationID != uuid.Nil && target.MachineID != uuid.Nil &&
		target.ProviderResourceID != "" &&
		target.ProviderResourceID == strings.TrimSpace(target.ProviderResourceID)
}

var (
	_ providers.RuntimeStateObserver = (*provider)(nil)
	_ providers.MachineWaker         = (*provider)(nil)
)
