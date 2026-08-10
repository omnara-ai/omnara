package unikraft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const runtimeObservationInitialBatchSize = 100

var _ providers.RuntimeStateObserver = (*provider)(nil)

type runtimeObservationGroup struct {
	metro   string
	indices []int
}

func (p *provider) ObserveRuntimeStates(
	ctx context.Context,
	targets []providers.RuntimeTarget,
) ([]providers.RuntimeObservation, error) {
	observations := make([]providers.RuntimeObservation, len(targets))
	resourceCounts := make(map[string]int, len(targets))
	machineCounts := make(map[storage.ID]int, len(targets))
	for _, target := range targets {
		resourceCounts[target.ProviderResourceID]++
		machineCounts[target.MachineID]++
	}
	groups := make([]runtimeObservationGroup, 0)
	groupByMetro := make(map[string]int)
	for index, target := range targets {
		observations[index] = target.UnknownObservation()
		if target.ProviderResourceID == "" ||
			target.ProviderResourceID != strings.TrimSpace(target.ProviderResourceID) ||
			resourceCounts[target.ProviderResourceID] != 1 ||
			machineCounts[target.MachineID] != 1 {
			continue
		}
		metro, valid := existingMachineMetro(target.MachineProvisioning)
		if !valid {
			continue
		}
		groupIndex, exists := groupByMetro[metro]
		if !exists {
			groupIndex = len(groups)
			groupByMetro[metro] = groupIndex
			groups = append(groups, runtimeObservationGroup{metro: metro})
		}
		groups[groupIndex].indices = append(groups[groupIndex].indices, index)
	}

	for _, group := range groups {
		api := p.apiForMetro(group.metro)
		for start := 0; start < len(group.indices); start += runtimeObservationInitialBatchSize {
			end := min(start+runtimeObservationInitialBatchSize, len(group.indices))
			if err := observeRuntimeStateBatch(
				ctx,
				api,
				observations,
				targets,
				group.indices[start:end],
			); err != nil {
				return nil, err
			}
		}
	}
	return observations, nil
}

func observeRuntimeStateBatch(
	ctx context.Context,
	api apiClient,
	observations []providers.RuntimeObservation,
	targets []providers.RuntimeTarget,
	indices []int,
) error {
	uuids := make([]string, len(indices))
	for index, targetIndex := range indices {
		uuids[index] = targets[targetIndex].ProviderResourceID
	}
	batch, err := api.GetInstancesByUUIDs(ctx, uuids)
	if errors.Is(err, providers.ErrResponseTooLarge) && len(indices) > 1 {
		middle := len(indices) / 2
		if err := observeRuntimeStateBatch(
			ctx,
			api,
			observations,
			targets,
			indices[:middle],
		); err != nil {
			return err
		}
		return observeRuntimeStateBatch(
			ctx,
			api,
			observations,
			targets,
			indices[middle:],
		)
	}
	if err != nil {
		return err
	}
	applyRuntimeObservationBatch(observations, targets, indices, batch)
	return nil
}

func (p *provider) ObserveRuntimeState(
	ctx context.Context,
	target providers.RuntimeTarget,
) (providers.RuntimeObservation, error) {
	observation := target.UnknownObservation()
	if target.ProviderResourceID == "" ||
		target.ProviderResourceID != strings.TrimSpace(target.ProviderResourceID) {
		return observation, nil
	}
	metro, valid := existingMachineMetro(target.MachineProvisioning)
	if !valid {
		return observation, nil
	}
	batch, err := p.apiForMetro(metro).GetInstancesByUUIDs(
		ctx,
		[]string{target.ProviderResourceID},
	)
	if err != nil {
		return observation, err
	}
	if len(batch.Instances) != 1 {
		return observation, fmt.Errorf(
			"unikraft exact runtime lookup returned %d instances, want exactly one",
			len(batch.Instances),
		)
	}
	result := batch.Instances[0]
	if result.UUID == target.ProviderResourceID && result.isNotFound() {
		if batch.notFoundItemsAuthoritative() {
			observation.State = providers.RuntimeStateTerminated
		}
		return observation, nil
	}
	if !batch.cleanEnvelope() {
		return observation, errors.New(
			"unikraft exact runtime lookup returned a non-authoritative response",
		)
	}
	if result.UUID != target.ProviderResourceID || !ownsRuntimeInstance(target, result) {
		return observation, nil
	}
	observation.State = normalizeRuntimeState(result)
	return observation, nil
}

func applyRuntimeObservationBatch(
	observations []providers.RuntimeObservation,
	targets []providers.RuntimeTarget,
	indices []int,
	batch instanceBatch,
) {
	indicesByUUID := make(map[string][]int, len(indices))
	for _, index := range indices {
		resourceID := targets[index].ProviderResourceID
		indicesByUUID[resourceID] = append(indicesByUUID[resourceID], index)
	}
	instanceByUUID := make(map[string]instance, len(batch.Instances))
	duplicateUUIDs := make(map[string]struct{})
	for _, result := range batch.Instances {
		if result.UUID == "" {
			continue
		}
		if _, requested := indicesByUUID[result.UUID]; !requested {
			continue
		}
		if _, duplicate := instanceByUUID[result.UUID]; duplicate {
			delete(instanceByUUID, result.UUID)
			duplicateUUIDs[result.UUID] = struct{}{}
			continue
		}
		if _, duplicate := duplicateUUIDs[result.UUID]; duplicate {
			continue
		}
		instanceByUUID[result.UUID] = result
	}
	for uuid, targetIndices := range indicesByUUID {
		result, exists := instanceByUUID[uuid]
		if !exists {
			continue
		}
		if result.isNotFound() {
			if !batch.notFoundItemsAuthoritative() {
				continue
			}
		} else if !batch.successfulItemsAuthoritative() {
			continue
		}
		state := normalizeRuntimeState(result)
		for _, index := range targetIndices {
			if !result.isNotFound() &&
				!ownsRuntimeInstance(targets[index], result) {
				continue
			}
			observations[index].State = state
		}
	}
}

func ownsRuntimeInstance(target providers.RuntimeTarget, result instance) bool {
	expectedName, err := providers.MachineAllocationName(target.InstallationID, target.MachineID)
	return err == nil && result.Name == expectedName
}

// Existing-resource operations need only the immutable control-plane location.
// They deliberately do not revalidate provisioning fields that may have become
// stricter after a machine was created.
func existingMachineMetro(
	machineProvisioning executionstore.MachineProvisioningConfig,
) (string, bool) {
	raw, exists := machineProvisioning.ProviderOptions["metro"]
	if !exists {
		return "", false
	}
	var metro string
	if err := json.Unmarshal(raw, &metro); err != nil || metro == "" ||
		metro != strings.TrimSpace(metro) || providers.ValidateDNSLabel(metro) != nil {
		return "", false
	}
	return metro, true
}

func normalizeRuntimeState(result instance) providers.RuntimeState {
	if result.isNotFound() {
		return providers.RuntimeStateTerminated
	}
	if result.Error != 0 || result.Status != responseStatusSuccess {
		return providers.RuntimeStateUnknown
	}
	switch result.State {
	case instanceStateRunning:
		return providers.RuntimeStateRunning
	case instanceStateStandby, instanceStateStopped:
		return providers.RuntimeStateInactive
	case instanceStateStarting, instanceStateDraining, instanceStateStopping:
		return providers.RuntimeStateTransitional
	case instanceStateDeleted:
		return providers.RuntimeStateTerminated
	case instanceStateTemplate:
		return providers.RuntimeStateUnknown
	default:
		return providers.RuntimeStateUnknown
	}
}
