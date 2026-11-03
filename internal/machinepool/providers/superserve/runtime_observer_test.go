package superserve

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

func TestSuperserveRuntimeStateMapsSandboxStatus(t *testing.T) {
	for status, want := range map[sandboxStatus]providers.RuntimeState{
		sandboxStatusActive:        providers.RuntimeStateRunning,
		sandboxStatusPaused:        providers.RuntimeStateInactive,
		sandboxStatusStarting:      providers.RuntimeStateTransitional,
		sandboxStatusPausing:       providers.RuntimeStateTransitional,
		sandboxStatusResuming:      providers.RuntimeStateTransitional,
		sandboxStatusMigrating:     providers.RuntimeStateTransitional,
		sandboxStatusFailed:        providers.RuntimeStateTerminated,
		sandboxStatusDeleted:       providers.RuntimeStateTerminated,
		sandboxStatus("something"): providers.RuntimeStateUnknown,
		sandboxStatus(""):          providers.RuntimeStateUnknown,
	} {
		if got := runtimeState(status); got != want {
			t.Errorf("runtime state for %q = %v, want %v", status, got, want)
		}
	}
}

func TestSuperserveObserveRuntimeStateReportsOwnedSandboxes(t *testing.T) {
	api := newFakeAPI()
	target := testRuntimeTarget("sb-123")
	api.created = ownedSandbox(t, "sb-123", sandboxStatusActive, target.InstallationID, target.MachineID)
	observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
	if err != nil {
		t.Fatalf("observe runtime state: %v", err)
	}
	providercontract.AssertRuntimeObservation(t, target, observation, providers.RuntimeStateRunning)

	api.created.Status = sandboxStatusPaused
	observation, err = newTestProvider(api).ObserveRuntimeState(context.Background(), target)
	if err != nil {
		t.Fatalf("observe paused runtime state: %v", err)
	}
	providercontract.AssertRuntimeObservation(t, target, observation, providers.RuntimeStateInactive)
}

func TestSuperserveObserveRuntimeStateReportsMissingSandboxAsTerminated(t *testing.T) {
	api := newFakeAPI()
	api.getFound = false
	target := testRuntimeTarget("sb-123")
	observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
	if err != nil {
		t.Fatalf("observe runtime state: %v", err)
	}
	providercontract.AssertRuntimeObservation(t, target, observation, providers.RuntimeStateTerminated)
}

func TestSuperserveObserveRuntimeStateIgnoresForeignAndMismatchedSandboxes(t *testing.T) {
	target := testRuntimeTarget("sb-123")
	for name, current := range map[string]sandbox{
		"foreign":  ownedSandbox(t, "sb-123", sandboxStatusActive, uuid.New(), uuid.New()),
		"other id": ownedSandbox(t, "sb-456", sandboxStatusActive, target.InstallationID, target.MachineID),
	} {
		api := newFakeAPI()
		api.created = current
		observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
		if err != nil {
			t.Fatalf("%s: observe runtime state: %v", name, err)
		}
		providercontract.AssertRuntimeObservation(t, target, observation, providers.RuntimeStateUnknown)
	}
}

func TestSuperserveObserveRuntimeStateSkipsInvalidTargets(t *testing.T) {
	valid := testRuntimeTarget("sb-123")
	for name, target := range map[string]providers.RuntimeTarget{
		"no installation": {MachineID: valid.MachineID, ProviderResourceID: "sb-123"},
		"no machine":      {InstallationID: valid.InstallationID, ProviderResourceID: "sb-123"},
		"no resource id":  {InstallationID: valid.InstallationID, MachineID: valid.MachineID},
		"untrimmed id":    {InstallationID: valid.InstallationID, MachineID: valid.MachineID, ProviderResourceID: " sb-123 "},
	} {
		api := newFakeAPI()
		observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
		if err != nil {
			t.Fatalf("%s: observe runtime state: %v", name, err)
		}
		if observation.State != providers.RuntimeStateUnknown || len(api.getLookups) != 0 {
			t.Errorf("%s: state = %v lookups = %v, want unknown without lookups", name, observation.State, api.getLookups)
		}
	}
}

func TestSuperserveObserveRuntimeStateWrapsLookupFailures(t *testing.T) {
	api := newFakeAPI()
	lookupErr := errors.New("control plane is unavailable")
	api.getErrs = []error{lookupErr}
	target := testRuntimeTarget("sb-123")
	if _, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target); !errors.Is(err, lookupErr) {
		t.Fatal("observe runtime state hid the lookup failure")
	}
	api.getErrs = []error{lookupErr}
	if _, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{target},
	); err == nil {
		t.Fatal("observe runtime states hid the lookup failure")
	}
}

func TestSuperserveObserveRuntimeStatesKeepsOrderAndBoundsEachLookup(t *testing.T) {
	api := newFakeAPI()
	first := testRuntimeTarget("sb-first")
	second := testRuntimeTarget("sb-second")
	api.getResults = []sandbox{
		ownedSandbox(t, "sb-first", sandboxStatusActive, first.InstallationID, first.MachineID),
		ownedSandbox(t, "sb-second", sandboxStatusPaused, second.InstallationID, second.MachineID),
	}
	observations, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{first, second},
	)
	if err != nil {
		t.Fatalf("observe runtime states: %v", err)
	}
	if len(observations) != 2 || observations[0].State != providers.RuntimeStateRunning ||
		observations[1].State != providers.RuntimeStateInactive {
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

func TestSuperserveObserveRuntimeStatesTreatsDuplicateTargetsAsUnknown(t *testing.T) {
	api := newFakeAPI()
	sharedResourceA := testRuntimeTarget("sb-shared")
	sharedResourceB := testRuntimeTarget("sb-shared")
	sharedMachineA := testRuntimeTarget("sb-machine-a")
	sharedMachineB := sharedMachineA
	sharedMachineB.ProviderResourceID = "sb-machine-b"
	unique := testRuntimeTarget("sb-unique")
	api.getResults = []sandbox{ownedSandbox(t, "sb-unique", sandboxStatusActive, unique.InstallationID, unique.MachineID)}
	observations, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{sharedResourceA, sharedResourceB, sharedMachineA, sharedMachineB, unique},
	)
	if err != nil {
		t.Fatalf("observe runtime states: %v", err)
	}
	for index, observation := range observations[:4] {
		if observation.State != providers.RuntimeStateUnknown {
			t.Fatalf("duplicate target %d state = %v, want unknown", index, observation.State)
		}
	}
	if observations[4].State != providers.RuntimeStateRunning || len(api.getLookups) != 1 {
		t.Fatalf("unique observation = %+v lookups = %v, want one exact read", observations[4], api.getLookups)
	}
}
