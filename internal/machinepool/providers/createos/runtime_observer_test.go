package createos

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/testutil/providercontract"
)

func testRuntimeTarget(t *testing.T, resourceID string) (providers.RuntimeTarget, string) {
	t.Helper()
	installationID := uuid.New()
	machineID := uuid.New()
	return providers.RuntimeTarget{
		InstallationID:     installationID,
		MachineID:          machineID,
		ProviderResourceID: resourceID,
	}, mustAllocationName(t, installationID, machineID)
}

func TestCreateOSRuntimeStateMapsSandboxStatus(t *testing.T) {
	for _, test := range []struct {
		status sandboxStatus
		want   providers.RuntimeState
	}{
		{sandboxStatusRunning, providers.RuntimeStateRunning},
		{sandboxStatusPaused, providers.RuntimeStateInactive},
		{sandboxStatusCreating, providers.RuntimeStateTransitional},
		{sandboxStatusPausing, providers.RuntimeStateTransitional},
		{sandboxStatusResuming, providers.RuntimeStateTransitional},
		{sandboxStatusForking, providers.RuntimeStateTransitional},
		{sandboxStatusDestroying, providers.RuntimeStateTransitional},
		{sandboxStatusDestroyed, providers.RuntimeStateTerminated},
		{sandboxStatusFailed, providers.RuntimeStateTerminated},
		{sandboxStatusError, providers.RuntimeStateInactive},
		{sandboxStatus("something-new"), providers.RuntimeStateUnknown},
		{sandboxStatus(""), providers.RuntimeStateUnknown},
	} {
		if got := createOSRuntimeState(test.status); got != test.want {
			t.Errorf("runtime state for %q = %v, want %v", test.status, got, test.want)
		}
	}
}

func TestCreateOSObserveRuntimeStateReportsRunningSandbox(t *testing.T) {
	api := newFakeAPI()
	target, name := testRuntimeTarget(t, "sb-123")
	api.created = sandbox{ID: "sb-123", Name: name, Status: sandboxStatusRunning}
	observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
	if err != nil {
		t.Fatalf("observe runtime state: %v", err)
	}
	providercontract.AssertRuntimeObservation(t, target, observation, providers.RuntimeStateRunning)
}

func TestCreateOSObserveRuntimeStateReportsMissingSandboxAsTerminated(t *testing.T) {
	api := newFakeAPI()
	api.getFound = false
	target, _ := testRuntimeTarget(t, "sb-123")
	observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
	if err != nil {
		t.Fatalf("observe runtime state: %v", err)
	}
	providercontract.AssertRuntimeObservation(t, target, observation, providers.RuntimeStateTerminated)
}

func TestCreateOSObserveRuntimeStateIgnoresForeignSandbox(t *testing.T) {
	api := newFakeAPI()
	target, _ := testRuntimeTarget(t, "sb-123")
	api.created = sandbox{ID: "sb-123", Name: "omnara-someone-else", Status: sandboxStatusRunning}
	observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
	if err != nil {
		t.Fatalf("observe runtime state: %v", err)
	}
	providercontract.AssertRuntimeObservation(t, target, observation, providers.RuntimeStateUnknown)
}

func TestCreateOSObserveRuntimeStateSkipsInvalidTargets(t *testing.T) {
	valid, _ := testRuntimeTarget(t, "sb-123")
	for name, target := range map[string]providers.RuntimeTarget{
		"no installation": {MachineID: valid.MachineID, ProviderResourceID: "sb-123"},
		"no machine":      {InstallationID: valid.InstallationID, ProviderResourceID: "sb-123"},
		"no resource id":  {InstallationID: valid.InstallationID, MachineID: valid.MachineID},
		"untrimmed id": {
			InstallationID:     valid.InstallationID,
			MachineID:          valid.MachineID,
			ProviderResourceID: " sb-123 ",
		},
	} {
		api := newFakeAPI()
		observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
		if err != nil {
			t.Fatalf("%s: observe runtime state: %v", name, err)
		}
		if observation.State != providers.RuntimeStateUnknown {
			t.Errorf("%s: state = %v, want unknown", name, observation.State)
		}
		if len(api.getLookups) != 0 {
			t.Errorf("%s: sandbox lookups = %v, want none", name, api.getLookups)
		}
	}
}

func TestCreateOSObserveRuntimeStateWrapsLookupFailures(t *testing.T) {
	api := newFakeAPI()
	lookupErr := errors.New("control plane is unavailable")
	api.getErrs = []error{lookupErr}
	target, _ := testRuntimeTarget(t, "sb-123")
	if _, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target); !errors.Is(err, lookupErr) {
		t.Fatal("observe runtime state hid the lookup failure")
	}
}

func TestCreateOSObserveRuntimeStatesKeepsTargetOrder(t *testing.T) {
	api := newFakeAPI()
	first, firstName := testRuntimeTarget(t, "sb-first")
	second, secondName := testRuntimeTarget(t, "sb-second")
	api.getResults = []sandbox{
		{ID: "sb-first", Name: firstName, Status: sandboxStatusRunning},
		{ID: "sb-second", Name: secondName, Status: sandboxStatusPaused},
	}
	observations, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{first, second},
	)
	if err != nil {
		t.Fatalf("observe runtime states: %v", err)
	}
	if len(observations) != 2 {
		t.Fatalf("observations = %d, want 2", len(observations))
	}
	if observations[0].State != providers.RuntimeStateRunning {
		t.Errorf("first state = %v, want running", observations[0].State)
	}
	if observations[1].State != providers.RuntimeStateInactive {
		t.Errorf("second state = %v, want inactive", observations[1].State)
	}
}

func TestCreateOSObserveRuntimeStatesFailsWholeBatch(t *testing.T) {
	api := newFakeAPI()
	api.getErrs = []error{errors.New("control plane is unavailable")}
	target, _ := testRuntimeTarget(t, "sb-123")
	if _, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{target},
	); err == nil {
		t.Fatal("observe runtime states hid the lookup failure")
	}
}

func TestCreateOSObserveRuntimeStatesTreatsDuplicateTargetsAsUnknown(t *testing.T) {
	api := newFakeAPI()
	sharedResourceA, _ := testRuntimeTarget(t, "sb-shared")
	sharedResourceB, _ := testRuntimeTarget(t, "sb-shared")
	sharedMachineA, _ := testRuntimeTarget(t, "sb-machine-a")
	sharedMachineB := sharedMachineA
	sharedMachineB.ProviderResourceID = "sb-machine-b"
	unique, uniqueName := testRuntimeTarget(t, "sb-unique")
	api.getResults = []sandbox{{ID: "sb-unique", Name: uniqueName, Status: sandboxStatusRunning}}
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

func TestCreateOSObserveRuntimeStatesBoundsEachObservation(t *testing.T) {
	api := newFakeAPI()
	first, firstName := testRuntimeTarget(t, "sb-first")
	second, secondName := testRuntimeTarget(t, "sb-second")
	api.getResults = []sandbox{
		{ID: "sb-first", Name: firstName, Status: sandboxStatusRunning},
		{ID: "sb-second", Name: secondName, Status: sandboxStatusRunning},
	}
	if _, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{first, second},
	); err != nil {
		t.Fatalf("observe runtime states: %v", err)
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
