package groundbox

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestGroundboxRuntimeStates(t *testing.T) {
	for status, want := range map[sandboxStatus]providers.RuntimeState{
		sandboxStatusRunning:      providers.RuntimeStateRunning,
		sandboxStatusPaused:       providers.RuntimeStateInactive,
		sandboxStatusShutDown:     providers.RuntimeStateInactive,
		sandboxStatusQueued:       providers.RuntimeStateTransitional,
		sandboxStatusStarting:     providers.RuntimeStateTransitional,
		sandboxStatusPausing:      providers.RuntimeStateTransitional,
		sandboxStatusResuming:     providers.RuntimeStateTransitional,
		sandboxStatusShuttingDown: providers.RuntimeStateTransitional,
		sandboxStatusStopping:     providers.RuntimeStateTransitional,
		sandboxStatusStopped:      providers.RuntimeStateTerminated,
		sandboxStatusFailed:       providers.RuntimeStateTerminated,
		"something_new":           providers.RuntimeStateUnknown,
	} {
		if got := runtimeState(status); got != want {
			t.Errorf("runtime state of %q = %q, want %q", status, got, want)
		}
	}
}

func TestGroundboxObserveRuntimeState(t *testing.T) {
	api := &fakeAPI{}
	ownedSandbox(t, api, "sbx_owned001", sandboxStatusPaused)
	api.add("sbx_foreign1", sandboxStatusRunning, 2, 4, map[string]string{machineLabel: "mch_other"})
	machineProvider := testProvider(api)
	target := providers.RuntimeTarget{
		InstallationID:     testInstallationID,
		MachineID:          testMachineID,
		ProviderResourceID: "sbx_owned001",
	}
	for name, test := range map[string]struct {
		resourceID string
		want       providers.RuntimeState
	}{
		"owned":   {resourceID: "sbx_owned001", want: providers.RuntimeStateInactive},
		"missing": {resourceID: "sbx_missing1", want: providers.RuntimeStateTerminated},
		"foreign": {resourceID: "sbx_foreign1", want: providers.RuntimeStateUnknown},
		"padded":  {resourceID: " sbx_owned001", want: providers.RuntimeStateUnknown},
	} {
		current := target
		current.ProviderResourceID = test.resourceID
		observation, err := machineProvider.ObserveRuntimeState(context.Background(), current)
		if err != nil || observation.State != test.want || observation.MachineID != testMachineID ||
			observation.ProviderResourceID != test.resourceID {
			t.Errorf("%s: observation = %+v, %v; want %q", name, observation, err, test.want)
		}
	}
	api.getErr = statusError(http.StatusBadGateway, "")
	if _, err := machineProvider.ObserveRuntimeState(context.Background(), target); err == nil {
		t.Fatal("observe ignored an API error")
	}
}

func TestGroundboxObserveRuntimeStatesSkipsAmbiguousTargets(t *testing.T) {
	api := &fakeAPI{}
	ownedSandbox(t, api, "sbx_owned001", sandboxStatusRunning)
	target := providers.RuntimeTarget{
		InstallationID:     testInstallationID,
		MachineID:          testMachineID,
		ProviderResourceID: "sbx_owned001",
	}
	duplicate := target
	duplicate.MachineID = uuid.New()
	observations, err := testProvider(api).ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{target, duplicate},
	)
	if err != nil || len(observations) != 2 {
		t.Fatalf("observations = %+v, %v", observations, err)
	}
	for _, observation := range observations {
		if observation.State != providers.RuntimeStateUnknown {
			t.Fatalf("observation = %+v, want unknown for a shared resource id", observation)
		}
	}
	if api.getCalls != 0 {
		t.Fatalf("get calls = %d, want none for ambiguous targets", api.getCalls)
	}
	observations, err = testProvider(api).ObserveRuntimeStates(context.Background(), []providers.RuntimeTarget{target})
	if err != nil || observations[0].State != providers.RuntimeStateRunning {
		t.Fatalf("observations = %+v, %v", observations, err)
	}
}
