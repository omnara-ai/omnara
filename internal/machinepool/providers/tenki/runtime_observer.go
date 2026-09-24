package tenki

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func normalizeRuntimeState(state string) providers.RuntimeState {
	switch state {
	case "RUNNING":
		return providers.RuntimeStateRunning
	case "PAUSED", "USER_SHUTDOWN":
		return providers.RuntimeStateInactive
	case "CREATING", "PAUSING", "RESUMING", "TERMINATING":
		return providers.RuntimeStateTransitional
	case "TERMINATED":
		return providers.RuntimeStateTerminated
	default:
		return providers.RuntimeStateUnknown
	}
}

func validRuntimeTarget(target providers.RuntimeTarget) bool {
	return target.InstallationID != uuid.Nil && target.MachineID != uuid.Nil &&
		target.ProviderResourceID != "" &&
		strings.TrimSpace(target.ProviderResourceID) == target.ProviderResourceID
}

func (p *provider) ObserveRuntimeState(
	ctx context.Context,
	target providers.RuntimeTarget,
) (providers.RuntimeObservation, error) {
	observation := target.UnknownObservation()
	if !validRuntimeTarget(target) {
		return observation, nil
	}
	current, found, err := p.api.Get(ctx, target.ProviderResourceID)
	if err != nil {
		return observation, err
	}
	if !found {
		observation.State = providers.RuntimeStateTerminated
	} else if current.ID == target.ProviderResourceID && ownedBy(current, target.InstallationID, target.MachineID) {
		observation.State = normalizeRuntimeState(current.State)
	}
	return observation, nil
}

func (p *provider) ObserveRuntimeStates(
	ctx context.Context,
	targets []providers.RuntimeTarget,
) ([]providers.RuntimeObservation, error) {
	result := make([]providers.RuntimeObservation, len(targets))
	if len(targets) == 0 {
		return result, nil
	}
	sessions, err := p.api.List(ctx)
	if err != nil {
		return nil, err
	}
	resourceCounts := make(map[string]int)
	machineCounts := make(map[uuid.UUID]int)
	matches := make(map[string][]sandbox)
	for _, target := range targets {
		resourceCounts[target.ProviderResourceID]++
		machineCounts[target.MachineID]++
	}
	for _, session := range sessions {
		matches[session.ID] = append(matches[session.ID], session)
	}
	for i, target := range targets {
		result[i] = target.UnknownObservation()
		if !validRuntimeTarget(target) || resourceCounts[target.ProviderResourceID] != 1 ||
			machineCounts[target.MachineID] != 1 {
			continue
		}
		found := matches[target.ProviderResourceID]
		if len(found) == 1 && ownedBy(found[0], target.InstallationID, target.MachineID) {
			result[i].State = normalizeRuntimeState(found[0].State)
		}
	}
	return result, nil
}
