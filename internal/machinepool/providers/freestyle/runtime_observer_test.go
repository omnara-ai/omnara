package freestyle

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestObserveRuntimeStateNormalizesFreestyleLifecycle(t *testing.T) {
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
	if len(api.getLookups) != 0 || len(api.listFilters) != 0 ||
		observations[0].State != providers.RuntimeStateUnknown ||
		observations[1].State != providers.RuntimeStateUnknown {
		t.Fatalf("duplicate observations = %+v, lookups = %#v lists = %#v", observations, api.getLookups, api.listFilters)
	}
}

func TestObserveRuntimeStatesListsInstallationVMs(t *testing.T) {
	runningMachine := uuid.MustParse("30000000-0000-0000-0000-000000000003")
	pausedMachine := uuid.MustParse("40000000-0000-0000-0000-000000000004")
	missingMachine := uuid.MustParse("50000000-0000-0000-0000-000000000005")
	foreignMachine := uuid.MustParse("60000000-0000-0000-0000-000000000006")
	foreign := ownedVMFor(t, foreignMachine, "vm-foreign", vmStateRunning)
	foreign.Metadata[machineMarkerKey] = "mch_someoneelse"
	api := &fakeAPI{listPages: []vmList{
		{VMs: []vm{ownedVMFor(t, runningMachine, "vm-running", vmStateRunning), foreign}, TotalCount: 3},
		{VMs: []vm{ownedVMFor(t, pausedMachine, "vm-paused", vmStatePaused)}, TotalCount: 3},
	}}
	targets := []providers.RuntimeTarget{
		{InstallationID: testInstallationID(), MachineID: runningMachine, ProviderResourceID: "vm-running"},
		{InstallationID: testInstallationID(), MachineID: pausedMachine, ProviderResourceID: "vm-paused"},
		{InstallationID: testInstallationID(), MachineID: missingMachine, ProviderResourceID: "vm-missing"},
		{InstallationID: testInstallationID(), MachineID: foreignMachine, ProviderResourceID: "vm-foreign"},
	}

	observations, err := newTestProvider(api).ObserveRuntimeStates(context.Background(), targets)
	if err != nil {
		t.Fatalf("observe listed VMs: %v", err)
	}
	want := []providers.RuntimeState{
		providers.RuntimeStateRunning,
		providers.RuntimeStateInactive,
		providers.RuntimeStateUnknown,
		providers.RuntimeStateUnknown,
	}
	for index, observation := range observations {
		if observation.State != want[index] {
			t.Fatalf("observation[%d] = %q, want %q", index, observation.State, want[index])
		}
	}
	installationOwner, err := publicid.Encode(publicid.KindInstallation, testInstallationID())
	if err != nil {
		t.Fatalf("encode installation id: %v", err)
	}
	wantFilter := installationMarkerKey + ":" + installationOwner
	if len(api.getLookups) != 0 || !slices.Equal(api.listOffsets, []int{0, 2}) ||
		!slices.Equal(api.listFilters, []string{wantFilter, wantFilter}) {
		t.Fatalf("lookups = %#v offsets = %#v filters = %#v", api.getLookups, api.listOffsets, api.listFilters)
	}
}

func TestObserveRuntimeStatesReturnsListError(t *testing.T) {
	api := &fakeAPI{listErr: errors.New("rate limited")}
	_, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{{
			InstallationID:     testInstallationID(),
			MachineID:          testMachineID(),
			ProviderResourceID: "vm-123",
		}},
	)
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("list error = %v", err)
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
