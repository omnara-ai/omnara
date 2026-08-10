package daytona

import (
	"context"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage"
)

const daytonaRuntimeListPageSize = 100

// Filtering to states that can change reconciliation state keeps account-wide
// listings small. Transitional and error states may still be returned by an
// eventually consistent list and are normalized conservatively below.
var daytonaObservableStates = [...]sandboxState{
	sandboxStateStarted,
	sandboxStateStopped,
	sandboxStatePaused,
	sandboxStateArchived,
}

func (p *provider) ObserveRuntimeStates(
	ctx context.Context,
	targets []providers.RuntimeTarget,
) ([]providers.RuntimeObservation, error) {
	if len(targets) == 0 {
		return []providers.RuntimeObservation{}, nil
	}

	return p.observeRuntimeStatesByList(ctx, targets)
}

func (p *provider) observeRuntimeStatesByList(
	ctx context.Context,
	targets []providers.RuntimeTarget,
) ([]providers.RuntimeObservation, error) {
	requestedResourceIDs := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if validRuntimeTarget(target) {
			requestedResourceIDs[target.ProviderResourceID] = struct{}{}
		}
	}
	if len(requestedResourceIDs) == 0 {
		return daytonaObservationsForMatches(targets, nil), nil
	}

	matches := make(map[string][]sandbox, len(requestedResourceIDs))
	seenCursors := map[string]struct{}{}
	cursor := ""
	for {
		page, err := p.api.ListSandboxes(ctx, listSandboxesQuery{
			Cursor: cursor,
			Limit:  daytonaRuntimeListPageSize,
			States: daytonaObservableStates[:],
		})
		if err != nil {
			return nil, fmt.Errorf("list daytona sandboxes for runtime observation: %w", err)
		}
		for _, target := range page.Items {
			if _, requested := requestedResourceIDs[target.ID]; requested {
				if len(matches[target.ID]) < 2 {
					matches[target.ID] = append(matches[target.ID], target)
				}
			}
		}

		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		nextCursor := *page.NextCursor
		if strings.TrimSpace(nextCursor) == "" {
			return nil, fmt.Errorf("daytona sandbox list returned a blank pagination cursor")
		}
		if _, duplicate := seenCursors[nextCursor]; duplicate {
			return nil, fmt.Errorf("daytona sandbox list repeated pagination cursor")
		}
		seenCursors[nextCursor] = struct{}{}
		cursor = nextCursor
	}

	return daytonaObservationsForMatches(targets, matches), nil
}

func (p *provider) ObserveRuntimeState(
	ctx context.Context,
	target providers.RuntimeTarget,
) (providers.RuntimeObservation, error) {
	observation := target.UnknownObservation()
	if !validRuntimeTarget(target) {
		return observation, nil
	}

	current, found, err := p.api.GetSandbox(ctx, target.ProviderResourceID)
	if err != nil {
		return observation, fmt.Errorf(
			"get daytona sandbox %q for runtime observation: %w",
			target.ProviderResourceID,
			err,
		)
	}
	if !found {
		observation.State = providers.RuntimeStateTerminated
		return observation, nil
	}
	return daytonaObservationForSandbox(target, current), nil
}

func daytonaObservationsForMatches(
	targets []providers.RuntimeTarget,
	matches map[string][]sandbox,
) []providers.RuntimeObservation {
	resourceCounts := make(map[string]int, len(targets))
	machineCounts := make(map[storage.ID]int, len(targets))
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
		observations[index] = daytonaObservationForSandbox(target, matched[0])
	}
	return observations
}

func daytonaObservationForSandbox(
	target providers.RuntimeTarget,
	current sandbox,
) providers.RuntimeObservation {
	observation := target.UnknownObservation()
	expectedName, err := providers.MachineAllocationName(target.InstallationID, target.MachineID)
	if err != nil || current.ID != target.ProviderResourceID || !sandboxOwnedBy(current, expectedName) {
		return observation
	}
	observation.State = daytonaRuntimeState(current.State)
	return observation
}

func daytonaRuntimeState(value sandboxState) providers.RuntimeState {
	switch normalizeSandboxState(value) {
	case sandboxStateStarted:
		return providers.RuntimeStateRunning
	case sandboxStateStopped, sandboxStatePaused, sandboxStateArchived:
		return providers.RuntimeStateInactive
	case sandboxStateDestroyed:
		return providers.RuntimeStateTerminated
	case sandboxStateCreating, sandboxStateRestoring, sandboxStateDestroying,
		sandboxStateStarting, sandboxStateStopping, sandboxStatePendingBuild,
		sandboxStateBuildingSnapshot, sandboxStatePullingSnapshot, sandboxStateArchiving,
		sandboxStateResizing, sandboxStateSnapshotting, sandboxStateForking,
		sandboxStatePausing, sandboxStateResuming:
		return providers.RuntimeStateTransitional
	case sandboxStateError, sandboxStateBuildFailed, sandboxStateUnknown:
		return providers.RuntimeStateUnknown
	default:
		return providers.RuntimeStateUnknown
	}
}

func validRuntimeTarget(target providers.RuntimeTarget) bool {
	return target.InstallationID != storage.NilID && target.MachineID != storage.NilID &&
		target.ProviderResourceID != "" &&
		target.ProviderResourceID == strings.TrimSpace(target.ProviderResourceID)
}

var _ providers.RuntimeStateObserver = (*provider)(nil)
