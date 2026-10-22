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

func TestAgent37ObserveRuntimeStatesBoundsLookupsAndSkipsDuplicates(t *testing.T) {
	api := newFakeAPI()
	lookupErr := errors.New("control plane is unavailable")
	api.getErrs = []error{lookupErr}
	if _, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{testRuntimeTarget("a37inst001")},
	); !errors.Is(err, lookupErr) {
		t.Fatalf("observe runtime states error = %v, want the lookup failure", err)
	}

	api = newFakeAPI()
	sharedA := testRuntimeTarget("a37shared1")
	sharedB := testRuntimeTarget("a37shared1")
	first := testRuntimeTarget("a37first01")
	second := testRuntimeTarget("a37second1")
	api.getResults = []instance{
		ownedInstance(t, "a37first01", instanceStatusRunning, first.InstallationID, first.MachineID),
		ownedInstance(t, "a37second1", instanceStatusSleeping, second.InstallationID, second.MachineID),
	}
	observations, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{sharedA, first, sharedB, second},
	)
	if err != nil {
		t.Fatalf("observe runtime states: %v", err)
	}
	if len(observations) != 4 || observations[0].State != providers.RuntimeStateUnknown ||
		observations[1].State != providers.RuntimeStateRunning || observations[2].State != providers.RuntimeStateUnknown ||
		observations[3].State != providers.RuntimeStateInactive {
		t.Fatalf("observations = %+v", observations)
	}
	if len(api.getDeadlines) != 2 {
		t.Fatalf("bounded lookups = %d, want 2", len(api.getDeadlines))
	}
	for _, remaining := range api.getDeadlines {
		if remaining <= 0 || remaining > runtimeObservationTimeout {
			t.Fatalf("lookup deadline = %v, want at most %v", remaining, runtimeObservationTimeout)
		}
	}
}
