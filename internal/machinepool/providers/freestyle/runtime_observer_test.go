package freestyle

import (
	"context"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestObserveRuntimeStatesNormalizesFreestyleLifecycle(t *testing.T) {
	states := map[string]string{
		"running":  vmStateRunning,
		"paused":   vmStatePaused,
		"starting": vmStateStarting,
		"stopped":  vmStateStopped,
		"unknown":  "future-state",
	}
	targets := make([]providers.RuntimeTarget, 0, len(states)+1)
	for resourceID := range states {
		targets = append(targets, providers.RuntimeTarget{
			InstallationID:     testInstallationID(),
			MachineID:          testMachineID(),
			ProviderResourceID: resourceID,
		})
	}
	targets = append(targets, providers.RuntimeTarget{
		InstallationID:     testInstallationID(),
		MachineID:          testMachineID(),
		ProviderResourceID: "missing",
	})

	api := &fakeAPI{getFunc: func(lookup string) (vm, bool, error) {
		state, found := states[lookup]
		if !found {
			return vm{}, false, nil
		}
		current := ownedVM(t, state, 2, 4096)
		current.ID = lookup
		return current, true, nil
	}}
	provider := newTestProvider(api)
	for _, target := range targets {
		observation, err := provider.ObserveRuntimeState(context.Background(), target)
		if err != nil {
			t.Fatalf("observe %q: %v", target.ProviderResourceID, err)
		}
		want := map[string]providers.RuntimeState{
			"running":  providers.RuntimeStateRunning,
			"paused":   providers.RuntimeStateInactive,
			"starting": providers.RuntimeStateTransitional,
			"stopped":  providers.RuntimeStateInactive,
			"unknown":  providers.RuntimeStateUnknown,
			"missing":  providers.RuntimeStateTerminated,
		}[target.ProviderResourceID]
		if observation.State != want {
			t.Fatalf("observation for %q = %q, want %q", target.ProviderResourceID, observation.State, want)
		}
	}
}

func TestObserveRuntimeStatesLeavesDuplicateTargetsUnknown(t *testing.T) {
	target := providers.RuntimeTarget{
		InstallationID:     testInstallationID(),
		MachineID:          testMachineID(),
		ProviderResourceID: "vm-123",
	}
	api := &fakeAPI{getFunc: func(string) (vm, bool, error) {
		return ownedVM(t, vmStateRunning, 2, 4096), true, nil
	}}
	observations, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{target, target},
	)
	if err != nil {
		t.Fatalf("observe duplicate targets: %v", err)
	}
	if len(api.getLookups) != 0 ||
		observations[0].State != providers.RuntimeStateUnknown ||
		observations[1].State != providers.RuntimeStateUnknown {
		t.Fatalf("duplicate observations = %+v, lookups = %#v", observations, api.getLookups)
	}
}

func TestObserveRuntimeStateRejectsForeignOwnership(t *testing.T) {
	foreign := ownedVM(t, vmStateRunning, 2, 4096)
	foreign.Metadata[machineMarkerKey] = "foreign"
	api := &fakeAPI{getFunc: func(string) (vm, bool, error) { return foreign, true, nil }}
	observation, err := newTestProvider(api).ObserveRuntimeState(
		context.Background(),
		providers.RuntimeTarget{
			InstallationID:     testInstallationID(),
			MachineID:          testMachineID(),
			ProviderResourceID: foreign.ID,
		},
	)
	if err != nil || observation.State != providers.RuntimeStateUnknown {
		t.Fatalf("foreign observation = %+v, error %v", observation, err)
	}
}
