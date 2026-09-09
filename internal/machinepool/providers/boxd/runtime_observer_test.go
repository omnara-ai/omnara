package boxd

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestBoxdObserveRuntimeStatesNormalizesMatches(t *testing.T) {
	running, runningVM := runtimeTargetAndVM(t, "running", vmStatusRunning)
	inactive, inactiveVM := runtimeTargetAndVM(t, "inactive", vmStatusHibernated)
	missing, _ := runtimeTargetAndVM(t, "missing", vmStatusRunning)
	duplicate, duplicateVM := runtimeTargetAndVM(t, "duplicate", vmStatusStopped)
	foreign, foreignVM := runtimeTargetAndVM(t, "foreign", vmStatusRunning)
	foreignVM.Name = "someone-else"
	transitional, transitionalVM := runtimeTargetAndVM(t, "transitional", vmStatusMigrating)
	unknown, unknownVM := runtimeTargetAndVM(t, "unknown", "future_state")

	api := newFakeAPI()
	api.listVMs = []vm{runningVM, duplicateVM, foreignVM, inactiveVM, duplicateVM, transitionalVM, unknownVM}
	targets := []providers.RuntimeTarget{running, inactive, missing, duplicate, foreign, transitional, unknown}
	observations, err := newTestProvider(api).ObserveRuntimeStates(context.Background(), targets)
	if err != nil {
		t.Fatalf("observe boxd runtimes: %v", err)
	}
	wantStates := []providers.RuntimeState{
		providers.RuntimeStateRunning,
		providers.RuntimeStateInactive,
		providers.RuntimeStateUnknown,
		providers.RuntimeStateUnknown,
		providers.RuntimeStateUnknown,
		providers.RuntimeStateTransitional,
		providers.RuntimeStateUnknown,
	}
	if len(observations) != len(targets) {
		t.Fatalf("observations = %d, want %d", len(observations), len(targets))
	}
	for index, observation := range observations {
		if observation.MachineID != targets[index].MachineID ||
			observation.ProviderResourceID != targets[index].ProviderResourceID ||
			observation.State != wantStates[index] {
			t.Fatalf("observation %d = %+v, want state %q for %+v", index, observation, wantStates[index], targets[index])
		}
	}
	if api.listCalls != 1 || api.getCalls != 0 {
		t.Fatalf("list calls = %d get calls = %d", api.listCalls, api.getCalls)
	}
}

func TestBoxdObserveRuntimeStatesDoesNotListWithoutValidTargets(t *testing.T) {
	api := newFakeAPI()
	observations, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{{}, {ProviderResourceID: " padded "}},
	)
	if err != nil {
		t.Fatalf("observe invalid boxd runtime targets: %v", err)
	}
	if len(observations) != 2 || observations[0].State != providers.RuntimeStateUnknown ||
		observations[1].State != providers.RuntimeStateUnknown {
		t.Fatalf("observations = %+v, want unknown observations", observations)
	}
	if api.listCalls != 0 {
		t.Fatalf("list calls = %d, want none", api.listCalls)
	}
	empty, err := newTestProvider(api).ObserveRuntimeStates(context.Background(), nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty observations = %+v, error %v", empty, err)
	}
}

func TestBoxdObserveRuntimeStatesFailsOpenOnProviderError(t *testing.T) {
	target, _ := runtimeTargetAndVM(t, "vm-1", vmStatusRunning)
	api := newFakeAPI()
	api.listErr = apiError{Code: codes.Unavailable}
	_, err := newTestProvider(api).ObserveRuntimeStates(context.Background(), []providers.RuntimeTarget{target})
	if err == nil || api.getCalls != 0 {
		t.Fatalf("observation error = %v, get calls = %d", err, api.getCalls)
	}
}

func TestBoxdObserveRuntimeStateUsesFreshExactRead(t *testing.T) {
	t.Run("running and owned", func(t *testing.T) {
		target, current := runtimeTargetAndVM(t, "vm-1", vmStatusRunning)
		api := newFakeAPI()
		api.exists = true
		api.vm = current
		observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
		if err != nil || observation.State != providers.RuntimeStateRunning {
			t.Fatalf("observation = %+v, error %v", observation, err)
		}
		if len(api.getLookups) != 1 || api.getLookups[0] != target.ProviderResourceID {
			t.Fatalf("lookups = %#v", api.getLookups)
		}
	})
	t.Run("not found is terminated", func(t *testing.T) {
		target, _ := runtimeTargetAndVM(t, "missing", vmStatusRunning)
		api := newFakeAPI()
		observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
		if err != nil || observation.State != providers.RuntimeStateTerminated {
			t.Fatalf("observation = %+v, error %v", observation, err)
		}
	})
	t.Run("destroyed is terminated", func(t *testing.T) {
		target, current := runtimeTargetAndVM(t, "vm-1", vmStatusDestroyed)
		api := newFakeAPI()
		api.exists = true
		api.vm = current
		observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
		if err != nil || observation.State != providers.RuntimeStateTerminated {
			t.Fatalf("observation = %+v, error %v", observation, err)
		}
	})
	t.Run("ownership mismatch is unknown", func(t *testing.T) {
		target, current := runtimeTargetAndVM(t, "foreign", vmStatusRunning)
		current.Name = "someone-else"
		api := newFakeAPI()
		api.exists = true
		api.vm = current
		observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
		if err != nil || observation.State != providers.RuntimeStateUnknown {
			t.Fatalf("observation = %+v, error %v", observation, err)
		}
	})
	t.Run("resource id mismatch is unknown", func(t *testing.T) {
		target, current := runtimeTargetAndVM(t, "expected", vmStatusRunning)
		current.ID = "different"
		api := newFakeAPI()
		api.exists = true
		api.vm = current
		observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
		if err != nil || observation.State != providers.RuntimeStateUnknown {
			t.Fatalf("observation = %+v, error %v", observation, err)
		}
	})
	t.Run("invalid target is unknown without a read", func(t *testing.T) {
		api := newFakeAPI()
		observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), providers.RuntimeTarget{})
		if err != nil || observation.State != providers.RuntimeStateUnknown || api.getCalls != 0 {
			t.Fatalf("observation = %+v, error %v, gets %d", observation, err, api.getCalls)
		}
	})
	t.Run("provider error fails open", func(t *testing.T) {
		target, _ := runtimeTargetAndVM(t, "errored", vmStatusRunning)
		api := newFakeAPI()
		api.getErr = apiError{Code: codes.Unavailable}
		observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
		if err == nil || observation.State != providers.RuntimeStateUnknown {
			t.Fatalf("observation = %+v, error %v", observation, err)
		}
	})
}

func TestBoxdRuntimeStateAllowlist(t *testing.T) {
	tests := map[vmStatus]providers.RuntimeState{
		vmStatusRunning:     providers.RuntimeStateRunning,
		" RUNNING ":         providers.RuntimeStateRunning,
		vmStatusStopped:     providers.RuntimeStateInactive,
		vmStatusSuspended:   providers.RuntimeStateInactive,
		vmStatusStandby:     providers.RuntimeStateInactive,
		vmStatusHibernated:  providers.RuntimeStateInactive,
		vmStatusDestroyed:   providers.RuntimeStateTerminated,
		vmStatusPending:     providers.RuntimeStateTransitional,
		vmStatusStarting:    providers.RuntimeStateTransitional,
		vmStatusStopping:    providers.RuntimeStateTransitional,
		vmStatusHibernating: providers.RuntimeStateTransitional,
		vmStatusRebooting:   providers.RuntimeStateTransitional,
		vmStatusMigrating:   providers.RuntimeStateTransitional,
		vmStatusDestroying:  providers.RuntimeStateTransitional,
		vmStatusFailed:      providers.RuntimeStateUnknown,
		"future_state":      providers.RuntimeStateUnknown,
		"":                  providers.RuntimeStateUnknown,
	}
	for input, want := range tests {
		t.Run(string(input), func(t *testing.T) {
			if got := runtimeState(input); got != want {
				t.Fatalf("state %q = %q, want %q", input, got, want)
			}
		})
	}
}

func runtimeTargetAndVM(t *testing.T, resourceID string, status vmStatus) (providers.RuntimeTarget, vm) {
	t.Helper()
	target := providers.RuntimeTarget{
		InstallationID:     uuid.New(),
		MachineID:          uuid.New(),
		ProviderResourceID: resourceID,
	}
	expectedName, err := providers.MachineAllocationName(target.InstallationID, target.MachineID)
	require.NoError(t, err)
	return target, vm{ID: resourceID, Name: expectedName, Status: status, VCPU: 2, MemoryBytes: 8192 * mebibyte}
}
