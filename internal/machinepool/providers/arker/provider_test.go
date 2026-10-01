package arker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestArkerProviderProvisionForksByAllocationName(t *testing.T) {
	name := testAllocationName(t)
	fake := &fakeArker{}
	machineProvider := newTestProvider(fake.start(t, name).URL)

	result, err := machineProvider.ProvisionMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), "tok-1", nil, true,
	)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if result.ProviderResourceID != testVMID {
		t.Fatalf("resource id = %q, want %q", result.ProviderResourceID, testVMID)
	}
	if result.SandboxURL != "" {
		t.Fatalf("sandbox url = %q, want none", result.SandboxURL)
	}

	_, body, _, _ := fake.snapshot()
	if body["name"] != name {
		t.Fatalf("fork name = %v, want %q", body["name"], name)
	}
	if body["source_vm_name"] != "ubuntu-base" {
		t.Fatalf("fork source = %v, want ubuntu-base", body["source_vm_name"])
	}
	resources, ok := body["resources"].(map[string]any)
	if !ok {
		t.Fatalf("fork carried no resources: %v", body)
	}
	if resources["vcpu"] != float64(2) || resources["memory_mib"] != float64(4096) {
		t.Fatalf("fork resources = %v, want vcpu 2 and memory_mib 4096", resources)
	}
}

func TestArkerProviderProvisionKeyIsStableAcrossAttempts(t *testing.T) {
	name := testAllocationName(t)
	fake := &fakeArker{}
	machineProvider := newTestProvider(fake.start(t, name).URL)
	config := testProvisioning(testOptions())

	for _, token := range []string{"attempt-1", "attempt-2"} {
		if _, err := machineProvider.ProvisionMachine(
			context.Background(), testInstallationID, testMachineID, config, token, nil, true,
		); err != nil {
			t.Fatalf("provision under %s: %v", token, err)
		}
	}
	keys, _, _, _ := fake.snapshot()
	if len(keys) != 2 || keys[0] != name || keys[1] != name {
		t.Fatalf("attempts sent keys %v, want the allocation name %q", keys, name)
	}
}

func TestArkerProviderStartsTheDaemonInASessionWithTheMachineEnv(t *testing.T) {
	fake := &fakeArker{}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	if _, err := machineProvider.ProvisionMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), "tok-1", map[string]string{"MY_VAR": "value", "MY-VAR": "dropped"}, true,
	); err != nil {
		t.Fatalf("provision: %v", err)
	}
	_, _, env, command := fake.snapshot()
	var sessionID string
	fake.record(func() { sessionID = fake.runSessionID })
	if _, dropped := env["MY-VAR"]; dropped || env["OMNARA_MACHINE_TOKEN"] != "tok-1" || env["MY_VAR"] != "value" {
		t.Fatalf("session env = %v", env)
	}
	if env[providers.ManagedBootstrapScriptEnvVar] != providers.ManagedBootScriptPayload() {
		t.Fatal("session env is missing the bootstrap payload")
	}
	if command != daemonLauncherCommand || sessionID != "sess_1" {
		t.Fatalf("daemon run = %q in session %q", command, sessionID)
	}
}

func TestArkerProviderAdoptsOnlyALiveDaemon(t *testing.T) {
	for _, test := range []struct {
		name    string
		state   string
		command string
		adopted bool
	}{
		{name: "pending", state: "pending", adopted: true},
		{name: "running", state: "running", adopted: true},
		{name: "completed", state: "completed"},
		{name: "other command", state: "running", command: "sleep infinity"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeArker{existingRunState: test.state, existingRunCommand: test.command}
			machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

			if _, err := machineProvider.ProvisionMachine(
				context.Background(), testInstallationID, testMachineID,
				testProvisioning(testOptions()), "tok-2", nil, true,
			); err != nil {
				t.Fatalf("provision: %v", err)
			}
			want := int64(1)
			if test.adopted {
				want = 0
			}
			if fake.sessions.Load() != want || fake.runs.Load() != want {
				t.Fatalf("started %d sessions and %d runs, want %d of each",
					fake.sessions.Load(), fake.runs.Load(), want)
			}
		})
	}
}

func TestArkerProviderProvisionReportsAVMThatIsNotThisMachine(t *testing.T) {
	fake := &fakeArker{vmName: "omnara-mch-somebodyelse"}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	result, err := machineProvider.ProvisionMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), "tok-1", nil, true,
	)
	if !errors.Is(err, errNotThisMachine) {
		t.Fatalf("adopting a foreign vm must fail, got %v", err)
	}
	if result.ProviderResourceID != "" {
		t.Fatalf("resource id = %q, want a foreign vm left unrecorded", result.ProviderResourceID)
	}
	if fake.deletes.Load() != 0 {
		t.Fatalf("issued %d deletes during provisioning", fake.deletes.Load())
	}
}

func TestArkerProviderProvisionReportsTheResourceIDWhenTheDaemonFails(t *testing.T) {
	fake := &fakeArker{sessionError: true}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	result, err := machineProvider.ProvisionMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), "tok-1", nil, true,
	)
	if err == nil {
		t.Fatal("expected the daemon start to fail")
	}
	if result.ProviderResourceID != testVMID {
		t.Fatalf("failed provision dropped the resource id: %q", result.ProviderResourceID)
	}
}

func TestArkerProviderProvisionFailsWhenTheDaemonRunEnds(t *testing.T) {
	for _, state := range []string{"completed", "failed"} {
		t.Run(state, func(t *testing.T) {
			fake := &fakeArker{runState: state}
			machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

			_, err := machineProvider.ProvisionMachine(
				context.Background(), testInstallationID, testMachineID,
				testProvisioning(testOptions()), "tok-1", nil, true,
			)
			if err == nil || !strings.Contains(err.Error(), "instead of running") {
				t.Fatalf("a %s daemon must fail the provision, got %v", state, err)
			}
		})
	}
}

func TestArkerProviderInspectFindsTheMachineByResourceID(t *testing.T) {
	fake := &fakeArker{}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	id, found, err := machineProvider.InspectMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), testVMID,
	)
	if err != nil || !found || id != testVMID {
		t.Fatalf("inspect = (%q, %v, %v), want the provisioned id", id, found, err)
	}
	var lookup string
	fake.record(func() { lookup = fake.lookup })
	if lookup != "/v1/vms/"+testVMID {
		t.Fatalf("looked up %q, want the recorded id", lookup)
	}
}

func TestArkerProviderInspectFindsAMachineWhoseIDWasNeverRecorded(t *testing.T) {
	fake := &fakeArker{}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	id, found, err := machineProvider.InspectMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), "",
	)
	if err != nil || !found || id != testVMID {
		t.Fatalf("inspect by name = (%q, %v, %v), want the vm id", id, found, err)
	}
	var lookup string
	fake.record(func() { lookup = fake.lookup })
	if lookup != "/v1/vms/"+testAllocationName(t) {
		t.Fatalf("looked up %q, want the allocation name", lookup)
	}
	if fake.forks.Load() != 0 {
		t.Fatal("inspect must not create anything")
	}
}

func TestArkerProviderInspectRejectsAVMBelongingToAnotherMachine(t *testing.T) {
	for _, resourceID := range []string{testVMID, ""} {
		fake := &fakeArker{vmName: "omnara-mch-somebodyelse"}
		machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

		_, found, err := machineProvider.InspectMachine(
			context.Background(), testInstallationID, testMachineID,
			testProvisioning(testOptions()), resourceID,
		)
		if found || !errors.Is(err, errNotThisMachine) {
			t.Fatalf("inspect %q of a foreign vm = (%v, %v), want an ownership error", resourceID, found, err)
		}
	}
}

func TestArkerProviderDeletesTheRecordedVMUsingOnlyItsRegion(t *testing.T) {
	fake := &fakeArker{}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	if err := machineProvider.DeleteMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(map[string]json.RawMessage{
			"region":         json.RawMessage(`"aws-us-west-2"`),
			"retired_option": json.RawMessage(`"value"`),
		}), testVMID,
	); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if fake.deletes.Load() != 1 {
		t.Fatalf("issued %d deletes, want one", fake.deletes.Load())
	}
}

func TestArkerProviderDeleteRefusesAVMBelongingToAnotherMachine(t *testing.T) {
	fake := &fakeArker{vmName: "omnara-mch-somebodyelse"}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	err := machineProvider.DeleteMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), testVMID,
	)
	if !errors.Is(err, errNotThisMachine) {
		t.Fatalf("deleting a foreign vm must fail, got %v", err)
	}
	if fake.deletes.Load() != 0 {
		t.Fatalf("issued %d deletes for a foreign vm", fake.deletes.Load())
	}
}

func TestArkerProviderDeleteFindsTheMachineByNameWhenTheRecordedIDIsGone(t *testing.T) {
	name := testAllocationName(t)
	deleted := make(chan string, 2)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/vms/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			deleted <- r.URL.Path
			fmt.Fprint(w, `{"deleted":true}`)
		case r.URL.Path == "/v1/vms/"+name:
			writeVM(w, name, "idle")
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":"not_found","message":"gone"}}`)
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	if err := newTestProvider(server.URL).DeleteMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), "vmh-stale",
	); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := <-deleted; got != "/v1/vms/"+testVMID {
		t.Fatalf("deleted %s, want the vm found by name", got)
	}
}

func TestArkerProviderDeleteIsRetrySafeWhenAlreadyAbsent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/vms/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":"not_found","message":"gone"}}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	if err := newTestProvider(server.URL).DeleteMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), testVMID,
	); err != nil {
		t.Fatalf("delete on an absent machine must succeed, got %v", err)
	}
}

func TestArkerProviderDeleteRequiresAResourceID(t *testing.T) {
	err := newTestProvider("https://example.invalid").DeleteMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), "",
	)
	if err == nil {
		t.Fatal("delete without a resource id must fail")
	}
}

func TestArkerProviderPrepareProvisioningEchoesTheRequestedSize(t *testing.T) {
	fake := &fakeArker{}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	facts, err := machineProvider.PrepareProvisioning(
		context.Background(),
		testProvisioning(testOptions()),
	)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if facts.CPU == nil || *facts.CPU != 2 || facts.MemoryMB == nil || *facts.MemoryMB != 4096 {
		t.Fatalf("facts = %+v, want the requested size", facts)
	}
	if fake.forks.Load() != 0 {
		t.Fatal("PrepareProvisioning must not touch external resources")
	}
}

func TestArkerProviderTargetsTheRegionEndpoint(t *testing.T) {
	if got := (&provider{}).regionBaseURL("aws-us-west-2"); got != "https://aws-us-west-2.arker.ai/api" {
		t.Fatalf("regional base url = %q", got)
	}
	overridden := &provider{apiBaseURL: "https://arker.example/api"}
	if got := overridden.regionBaseURL("aws-us-west-2"); got != "https://arker.example/api" {
		t.Fatalf("overridden base url = %q", got)
	}
}
