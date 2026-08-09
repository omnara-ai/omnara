package blaxel

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage"
)

const (
	targetedRuntimeObservationLimit = 20
	runtimeObservationListPageSize  = 100
)

type sandboxLister interface {
	ListSandboxes(context.Context, string, int) (sandboxListPage, error)
}

var _ providers.RuntimeStateObserver = (*provider)(nil)

func (p *provider) ObserveRuntimeState(
	ctx context.Context,
	target providers.RuntimeTarget,
) (providers.RuntimeObservation, error) {
	return observeRuntimeState(ctx, p.apiClient(), target)
}

func (p *provider) ObserveRuntimeStates(
	ctx context.Context,
	targets []providers.RuntimeTarget,
) ([]providers.RuntimeObservation, error) {
	observations := make([]providers.RuntimeObservation, len(targets))
	resourceIndexes := make(map[string][]int, len(targets))
	resourceCounts := make(map[string]int, len(targets))
	machineCounts := make(map[storage.ID]int, len(targets))
	for index, target := range targets {
		observations[index] = unknownRuntimeObservation(target)
		resourceCounts[target.ProviderResourceID]++
		machineCounts[target.MachineID]++
		if _, valid := expectedSandboxName(target); !valid {
			continue
		}
		resourceIndexes[target.ProviderResourceID] = append(
			resourceIndexes[target.ProviderResourceID],
			index,
		)
	}

	validIndexes := make([]int, 0, len(targets))
	for index, target := range targets {
		indexes := resourceIndexes[target.ProviderResourceID]
		if resourceCounts[target.ProviderResourceID] != 1 ||
			len(indexes) != 1 || indexes[0] != index {
			continue
		}
		if machineCounts[target.MachineID] != 1 {
			continue
		}
		validIndexes = append(validIndexes, index)
	}
	if len(validIndexes) == 0 {
		return observations, nil
	}

	api := p.apiClient()
	lister, canList := api.(sandboxLister)
	if !canList || len(validIndexes) <= targetedRuntimeObservationLimit {
		for _, index := range validIndexes {
			observation, err := observeRuntimeState(ctx, api, targets[index])
			if err != nil {
				return nil, err
			}
			observations[index] = observation
		}
		return observations, nil
	}

	listed, err := listTargetSandboxes(ctx, lister, targets, validIndexes)
	if err != nil {
		return nil, err
	}
	for _, index := range validIndexes {
		target := targets[index]
		matches := listed[target.ProviderResourceID]
		if len(matches) != 1 {
			continue
		}
		expectedName, _ := expectedSandboxName(target)
		if !sandboxOwnedBy(
			matches[0], expectedName, target.InstallationID, target.MachineID,
		) {
			continue
		}
		state := normalizedSandboxRuntimeState(matches[0])
		if state != providers.RuntimeStateUnknown {
			observations[index].State = state
			continue
		}

		// A targeted GET is the safe fallback when a bulk row omits runtime
		// state; it repeats ownership validation.
		observation, err := observeRuntimeState(ctx, api, target)
		if err != nil {
			return nil, err
		}
		observations[index] = observation
	}
	return observations, nil
}

func observeRuntimeState(
	ctx context.Context,
	api apiClient,
	target providers.RuntimeTarget,
) (providers.RuntimeObservation, error) {
	observation := unknownRuntimeObservation(target)
	expectedName, valid := expectedSandboxName(target)
	if !valid {
		return observation, nil
	}
	sandbox, found, err := api.GetSandbox(ctx, target.ProviderResourceID)
	if err != nil {
		return observation, fmt.Errorf("observe blaxel sandbox runtime: %w", err)
	}
	if !found {
		observation.State = providers.RuntimeStateTerminated
		return observation, nil
	}
	if !sandboxOwnedBy(
		sandbox, expectedName, target.InstallationID, target.MachineID,
	) {
		return observation, nil
	}
	observation.State = normalizedSandboxRuntimeState(sandbox)
	return observation, nil
}

func listTargetSandboxes(
	ctx context.Context,
	lister sandboxLister,
	targets []providers.RuntimeTarget,
	validIndexes []int,
) (map[string][]sandbox, error) {
	targetNames := make(map[string]struct{}, len(validIndexes))
	for _, index := range validIndexes {
		targetNames[targets[index].ProviderResourceID] = struct{}{}
	}

	matches := make(map[string][]sandbox, len(validIndexes))
	seenCursors := map[string]struct{}{"": {}}
	cursor := ""
	pageSize := runtimeObservationListPageSize
	for {
		page, err := lister.ListSandboxes(ctx, cursor, pageSize)
		if errors.Is(err, providers.ErrResponseTooLarge) && pageSize > 1 {
			pageSize = max(1, pageSize/2)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("list blaxel sandbox runtimes: %w", err)
		}
		for _, candidate := range page.Sandboxes {
			if _, wanted := targetNames[candidate.Metadata.Name]; wanted {
				if len(matches[candidate.Metadata.Name]) < 2 {
					matches[candidate.Metadata.Name] = append(
						matches[candidate.Metadata.Name], candidate,
					)
				}
			}
		}
		if !page.HasMore {
			return matches, nil
		}
		if page.NextCursor == "" {
			return nil, errors.New("blaxel sandbox list has more pages without a cursor")
		}
		if _, duplicate := seenCursors[page.NextCursor]; duplicate {
			return nil, errors.New("blaxel sandbox list repeated a cursor")
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
}

func expectedSandboxName(target providers.RuntimeTarget) (string, bool) {
	expectedName, err := providers.MachineAllocationName(
		target.InstallationID,
		target.MachineID,
	)
	return expectedName, err == nil && target.ProviderResourceID == expectedName
}

func unknownRuntimeObservation(target providers.RuntimeTarget) providers.RuntimeObservation {
	return providers.RuntimeObservation{
		MachineID:          target.MachineID,
		ProviderResourceID: target.ProviderResourceID,
		State:              providers.RuntimeStateUnknown,
	}
}

func normalizedSandboxRuntimeState(target sandbox) providers.RuntimeState {
	switch normalizeSandboxDeploymentStatus(target.Status) {
	case sandboxDeploymentDeleting, sandboxDeploymentTerminated:
		return providers.RuntimeStateTerminated
	case sandboxDeploymentDeployed:
		// Only a ready deployment makes the runtime state authoritative.
	default:
		return providers.RuntimeStateUnknown
	}
	switch normalizeSandboxRuntimeState(target.State) {
	case sandboxRuntimeRunning:
		return providers.RuntimeStateRunning
	case sandboxRuntimeStandby:
		return providers.RuntimeStateInactive
	default:
		return providers.RuntimeStateUnknown
	}
}
