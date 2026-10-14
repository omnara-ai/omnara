package arker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func testRuntimeTarget() providers.RuntimeTarget {
	return providers.RuntimeTarget{
		InstallationID:      testInstallationID,
		MachineID:           testMachineID,
		ProviderResourceID:  testVMID,
		MachineProvisioning: testProvisioning(testOptions()),
	}
}

func statusServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, `{"error":{"code":"error","message":"error"}}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestArkerRuntimeObserverMapsTheVMState(t *testing.T) {
	for vmState, want := range map[string]providers.RuntimeState{
		"running": providers.RuntimeStateRunning,
		"idle":    providers.RuntimeStateInactive,
		"other":   providers.RuntimeStateUnknown,
	} {
		t.Run(vmState, func(t *testing.T) {
			fake := &fakeArker{vmState: vmState}
			machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

			observation, err := machineProvider.ObserveRuntimeState(context.Background(), testRuntimeTarget())
			if err != nil {
				t.Fatalf("observe: %v", err)
			}
			if observation.State != want || observation.ProviderResourceID != testVMID {
				t.Fatalf("observation = %+v, want %s %s", observation, want, testVMID)
			}
		})
	}
}

func TestArkerRuntimeObserverReportsAnAbsentMachineAsTerminated(t *testing.T) {
	observation, err := newTestProvider(statusServer(t, http.StatusNotFound).URL).
		ObserveRuntimeState(context.Background(), testRuntimeTarget())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if observation.State != providers.RuntimeStateTerminated {
		t.Fatalf("state = %q, want terminated", observation.State)
	}
}

func TestArkerRuntimeObserverReportsAFailedLookupAsAnError(t *testing.T) {
	machineProvider := newTestProvider(statusServer(t, http.StatusInternalServerError).URL)

	observation, err := machineProvider.ObserveRuntimeState(context.Background(), testRuntimeTarget())
	if err == nil || observation.State != providers.RuntimeStateUnknown {
		t.Fatalf("observation = %+v, %v, want unknown with an error", observation, err)
	}
	if _, err := machineProvider.ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{testRuntimeTarget()},
	); err == nil {
		t.Fatal("bulk observation must return the provider error")
	}
}

func TestArkerRuntimeObserverReportsAForeignMachineAsUnknown(t *testing.T) {
	fake := &fakeArker{vmName: "omnara-mch-somebodyelse"}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	observation, err := machineProvider.ObserveRuntimeState(context.Background(), testRuntimeTarget())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if observation.State != providers.RuntimeStateUnknown {
		t.Fatalf("state = %q, want unknown", observation.State)
	}
}

func TestArkerRuntimeObserverObservesEveryTarget(t *testing.T) {
	fake := &fakeArker{}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	targets := []providers.RuntimeTarget{testRuntimeTarget(), testRuntimeTarget()}
	observations, err := machineProvider.ObserveRuntimeStates(context.Background(), targets)
	if err != nil {
		t.Fatalf("observe states: %v", err)
	}
	if len(observations) != len(targets) {
		t.Fatalf("got %d observations for %d targets", len(observations), len(targets))
	}
	for i, observation := range observations {
		if observation.State != providers.RuntimeStateRunning {
			t.Fatalf("observation %d state = %q, want running", i, observation.State)
		}
	}
}

func TestArkerRuntimeObserverIgnoresATargetWithNoResourceID(t *testing.T) {
	fake := &fakeArker{}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	target := testRuntimeTarget()
	target.ProviderResourceID = ""
	observation, err := machineProvider.ObserveRuntimeState(context.Background(), target)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if observation.State != providers.RuntimeStateUnknown {
		t.Fatalf("state = %q, want unknown", observation.State)
	}
}
