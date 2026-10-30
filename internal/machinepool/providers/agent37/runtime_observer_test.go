package agent37

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/testutil/providercontract"
)

func testRuntimeTarget(resourceID string) providers.RuntimeTarget {
	return providers.RuntimeTarget{
		InstallationID:     uuid.New(),
		MachineID:          uuid.New(),
		ProviderResourceID: resourceID,
	}
}

func TestAgent37RuntimeStateMapsInstanceStatus(t *testing.T) {
	for status, want := range map[instanceStatus]providers.RuntimeState{
		instanceStatusRunning:      providers.RuntimeStateRunning,
		instanceStatusStopped:      providers.RuntimeStateInactive,
		instanceStatusSleeping:     providers.RuntimeStateInactive,
		instanceStatusProvisioning: providers.RuntimeStateTransitional,
		instanceStatusStarting:     providers.RuntimeStateTransitional,
		instanceStatusStopping:     providers.RuntimeStateTransitional,
		instanceStatusRestarting:   providers.RuntimeStateTransitional,
		instanceStatusUpdating:     providers.RuntimeStateTransitional,
		instanceStatusWaking:       providers.RuntimeStateTransitional,
		instanceStatusDeleting:     providers.RuntimeStateTransitional,
		instanceStatusFailed:       providers.RuntimeStateTerminated,
		instanceStatusDeleted:      providers.RuntimeStateTerminated,
		instanceStatus("other"):    providers.RuntimeStateUnknown,
		instanceStatus(""):         providers.RuntimeStateUnknown,
	} {
		if got := runtimeState(status); got != want {
			t.Errorf("runtime state for %q = %v, want %v", status, got, want)
		}
	}
}

func TestAgent37ObserveRuntimeStateReportsOwnedAndMissingInstances(t *testing.T) {
	api := newFakeAPI()
	target := testRuntimeTarget("a37inst001")
	api.created = ownedInstance(t, "a37inst001", instanceStatusRunning, target.InstallationID, target.MachineID)
	observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
	if err != nil {
		t.Fatalf("observe runtime state: %v", err)
	}
	providercontract.AssertRuntimeObservation(t, target, observation, providers.RuntimeStateRunning)

	api.getFound = false
	observation, err = newTestProvider(api).ObserveRuntimeState(context.Background(), target)
	if err != nil {
		t.Fatalf("observe missing runtime state: %v", err)
	}
	providercontract.AssertRuntimeObservation(t, target, observation, providers.RuntimeStateTerminated)
}

func TestAgent37ObserveRuntimeStateIgnoresForeignMismatchedAndInvalidTargets(t *testing.T) {
	target := testRuntimeTarget("a37inst001")
	for name, current := range map[string]instance{
		"foreign":  ownedInstance(t, "a37inst001", instanceStatusRunning, uuid.New(), uuid.New()),
		"other id": ownedInstance(t, "a37inst002", instanceStatusRunning, target.InstallationID, target.MachineID),
	} {
		api := newFakeAPI()
		api.created = current
		observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
		if err != nil {
			t.Fatalf("%s: observe runtime state: %v", name, err)
		}
		providercontract.AssertRuntimeObservation(t, target, observation, providers.RuntimeStateUnknown)
	}
	for name, invalid := range map[string]providers.RuntimeTarget{
		"no installation": {MachineID: target.MachineID, ProviderResourceID: "a37inst001"},
		"no machine":      {InstallationID: target.InstallationID, ProviderResourceID: "a37inst001"},
		"no resource id":  {InstallationID: target.InstallationID, MachineID: target.MachineID},
		"untrimmed id":    {InstallationID: target.InstallationID, MachineID: target.MachineID, ProviderResourceID: " a37inst001 "},
	} {
		api := newFakeAPI()
		observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), invalid)
		if err != nil || observation.State != providers.RuntimeStateUnknown || len(api.getLookups) != 0 {
			t.Errorf("%s: state = %v error = %v lookups = %v, want unknown without lookups",
				name, observation.State, err, api.getLookups)
		}
	}
}

func TestAgent37ObserveRuntimeStatesListsOnceAndSkipsDuplicates(t *testing.T) {
	api := newFakeAPI()
	if observations, err := newTestProvider(api).ObserveRuntimeStates(context.Background(), nil); err != nil ||
		len(observations) != 0 || api.listCalls != 0 {
		t.Fatalf("observe no targets = %+v error = %v list calls = %d", observations, err, api.listCalls)
	}

	listErr := errors.New("control plane is unavailable")
	api.listErr = listErr
	if _, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{testRuntimeTarget("a37inst001")},
	); !errors.Is(err, listErr) {
		t.Fatalf("observe runtime states error = %v, want the list failure", err)
	}

	api = newFakeAPI()
	sharedA := testRuntimeTarget("a37shared1")
	sharedB := testRuntimeTarget("a37shared1")
	running := testRuntimeTarget("a37first01")
	sleeping := testRuntimeTarget("a37second1")
	foreign := testRuntimeTarget("a37foreign")
	missing := testRuntimeTarget("a37missing")
	api.instances = []instance{
		ownedInstance(t, "a37shared1", instanceStatusRunning, sharedA.InstallationID, sharedA.MachineID),
		ownedInstance(t, "a37first01", instanceStatusRunning, running.InstallationID, running.MachineID),
		ownedInstance(t, "a37second1", instanceStatusSleeping, sleeping.InstallationID, sleeping.MachineID),
		ownedInstance(t, "a37foreign", instanceStatusRunning, uuid.New(), uuid.New()),
	}
	observations, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{sharedA, running, sharedB, sleeping, foreign, missing},
	)
	if err != nil {
		t.Fatalf("observe runtime states: %v", err)
	}
	want := []providers.RuntimeState{
		providers.RuntimeStateUnknown,
		providers.RuntimeStateRunning,
		providers.RuntimeStateUnknown,
		providers.RuntimeStateInactive,
		providers.RuntimeStateUnknown,
		providers.RuntimeStateTerminated,
	}
	if len(observations) != len(want) {
		t.Fatalf("observations = %+v", observations)
	}
	for index, state := range want {
		if observations[index].State != state {
			t.Fatalf("observation %d = %+v, want %v", index, observations[index], state)
		}
	}
	if api.listCalls != 1 || len(api.getLookups) != 0 {
		t.Fatalf("list calls = %d lookups = %v, want one list and no lookups", api.listCalls, api.getLookups)
	}
}
