package createos

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func (p *provider) ObserveRuntimeStates(
	ctx context.Context,
	targets []providers.RuntimeTarget,
) ([]providers.RuntimeObservation, error) {
	observations := make([]providers.RuntimeObservation, len(targets))
	for index, target := range targets {
		observation, err := p.ObserveRuntimeState(ctx, target)
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
	if !validRuntimeTarget(target) {
		return observation, nil
	}
	targetSandbox, found, err := p.api.GetSandbox(ctx, target.ProviderResourceID)
	if err != nil {
		return observation, fmt.Errorf(
			"get createos sandbox %q for runtime observation: %w",
			target.ProviderResourceID,
			err,
		)
	}
	if !found {
		observation.State = providers.RuntimeStateTerminated
		return observation, nil
	}
	expectedName, err := allocationName(target.InstallationID, target.MachineID)
	if err != nil {
		return observation, fmt.Errorf("build createos allocation name: %w", err)
	}
	if targetSandbox.ID != target.ProviderResourceID || targetSandbox.Name != expectedName {
		return observation, nil
	}
	observation.State = createOSRuntimeState(targetSandbox.Status)
	return observation, nil
}

func createOSRuntimeState(status sandboxStatus) providers.RuntimeState {
	switch sandboxStatus(strings.ToLower(strings.TrimSpace(string(status)))) {
	case sandboxStatusRunning:
		return providers.RuntimeStateRunning
	case sandboxStatusPaused:
		return providers.RuntimeStateInactive
	case sandboxStatusCreating, sandboxStatusPausing, sandboxStatusResuming,
		sandboxStatusForking, sandboxStatusDestroying:
		return providers.RuntimeStateTransitional
	case sandboxStatusDestroyed, sandboxStatusFailed:
		return providers.RuntimeStateTerminated
	case sandboxStatusError:
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

var _ providers.RuntimeStateObserver = (*provider)(nil)
