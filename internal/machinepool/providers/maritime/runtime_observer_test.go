package maritime

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestMaritimeRuntimeStateMapping(t *testing.T) {
	for state, want := range map[sandboxState]providers.RuntimeState{
		sandboxStateRunning:  providers.RuntimeStateRunning,
		sandboxStatePaused:   providers.RuntimeStateInactive,
		sandboxStateError:    providers.RuntimeStateInactive,
		sandboxStateStarting: providers.RuntimeStateTransitional,
		sandboxStatePausing:  providers.RuntimeStateTransitional,
		sandboxStateResuming: providers.RuntimeStateTransitional,
		sandboxStateEnded:    providers.RuntimeStateTerminated,
		"something-new":      providers.RuntimeStateUnknown,
	} {
		if got := runtimeState(state); got != want {
			t.Errorf("runtime state for %q = %q, want %q", state, got, want)
		}
	}
}

func TestMaritimeObserveRuntimeState(t *testing.T) {
	installationID, machineID := uuid.New(), uuid.New()
	target := providers.RuntimeTarget{
		InstallationID:     installationID,
		MachineID:          machineID,
		ProviderResourceID: "sbx-abc123",
	}

	api := newFakeAPI()
	api.created = ownedSandbox(t, "sbx-abc123", sandboxStatePaused, installationID, machineID)
	observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
	if err != nil || observation.State != providers.RuntimeStateInactive {
		t.Fatalf("observe paused = %+v, %v", observation, err)
	}

	api = newFakeAPI()
	api.getFound = false
	observation, err = newTestProvider(api).ObserveRuntimeState(context.Background(), target)
	if err != nil || observation.State != providers.RuntimeStateTerminated {
		t.Fatalf("observe missing = %+v, %v", observation, err)
	}

	api = newFakeAPI()
	api.created = ownedSandbox(t, "sbx-abc123", sandboxStateRunning, uuid.New(), machineID)
	observation, err = newTestProvider(api).ObserveRuntimeState(context.Background(), target)
	if err != nil || observation.State != providers.RuntimeStateUnknown {
		t.Fatalf("observe foreign = %+v, %v", observation, err)
	}

	api = newFakeAPI()
	api.getErrs = []error{errors.New("connection reset")}
	if _, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target); err == nil {
		t.Fatal("observe with a transport error succeeded")
	}

	invalid := target
	invalid.ProviderResourceID = " sbx-abc123"
	api = newFakeAPI()
	observation, err = newTestProvider(api).ObserveRuntimeState(context.Background(), invalid)
	if err != nil || observation.State != providers.RuntimeStateUnknown || len(api.getLookups) != 0 {
		t.Fatalf("observe invalid target = %+v, %v, lookups %d", observation, err, len(api.getLookups))
	}
}

func TestMaritimeObserveRuntimeStatesSkipsAmbiguousTargets(t *testing.T) {
	installationID, machineID := uuid.New(), uuid.New()
	api := newFakeAPI()
	api.created = ownedSandbox(t, "sbx-abc123", sandboxStateRunning, installationID, machineID)
	unique := providers.RuntimeTarget{
		InstallationID:     installationID,
		MachineID:          machineID,
		ProviderResourceID: "sbx-abc123",
	}
	duplicateA := providers.RuntimeTarget{
		InstallationID:     installationID,
		MachineID:          uuid.New(),
		ProviderResourceID: "sbx-dup",
	}
	duplicateB := duplicateA
	duplicateB.MachineID = uuid.New()
	observations, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(), []providers.RuntimeTarget{unique, duplicateA, duplicateB},
	)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if observations[0].State != providers.RuntimeStateRunning ||
		observations[1].State != providers.RuntimeStateUnknown ||
		observations[2].State != providers.RuntimeStateUnknown {
		t.Fatalf("observations = %+v", observations)
	}
	if len(api.getLookups) != 1 {
		t.Fatalf("lookups = %d, want 1", len(api.getLookups))
	}
}
