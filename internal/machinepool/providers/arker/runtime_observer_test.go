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

func TestArkerRuntimeObserverReportsALiveMachineAsRunning(t *testing.T) {
	fake := &fakeArker{}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	observation, err := machineProvider.ObserveRuntimeState(
		context.Background(),
		testRuntimeTarget(),
	)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if observation.State != providers.RuntimeStateRunning {
		t.Fatalf("state = %q, want running", observation.State)
	}
	if observation.ProviderResourceID != testVMID {
		t.Fatalf("observation resource id = %q, want %q", observation.ProviderResourceID, testVMID)
	}
}

func TestArkerRuntimeObserverReportsAnAbsentMachineAsTerminated(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/vms/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":"not_found","message":"gone"}}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	observation, err := newTestProvider(server.URL).ObserveRuntimeState(
		context.Background(),
		testRuntimeTarget(),
	)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if observation.State != providers.RuntimeStateTerminated {
		t.Fatalf("state = %q, want terminated", observation.State)
	}
}

func TestArkerRuntimeObserverReportsAFailedLookupAsUnknown(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/vms/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"code":"boom","message":"upstream"}}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	observation, err := newTestProvider(server.URL).ObserveRuntimeState(
		context.Background(),
		testRuntimeTarget(),
	)
	if err == nil {
		t.Fatal("a failed lookup must surface as an error")
	}
	if observation.State != providers.RuntimeStateUnknown {
		t.Fatalf("state = %q, want unknown so the machine is not retired", observation.State)
	}
}

func TestArkerRuntimeObserverReportsAForeignMachineAsUnknown(t *testing.T) {
	fake := &fakeArker{vmName: "omnara-mch-somebodyelse"}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	observation, err := machineProvider.ObserveRuntimeState(
		context.Background(),
		testRuntimeTarget(),
	)
	if err != nil {
		t.Fatalf("an ownership mismatch is one machine's problem, not a provider "+
			"outage, and reconciliation puts the whole scope on cooldown for an "+
			"error here: %v", err)
	}
	if observation.State != providers.RuntimeStateUnknown {
		t.Fatalf("state = %q, want unknown rather than a retirement", observation.State)
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
