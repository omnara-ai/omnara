package revyl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func testProvider(api *fakeAPI) *provider {
	return &provider{api: api, omnaraAPIURL: "https://api.omnara.test/api/v1"}
}

func testProvisioning(t *testing.T) executionstore.MachineProvisioningConfig {
	t.Helper()
	return executionstore.MachineProvisioningConfig{
		ProviderOptions: map[string]json.RawMessage{
			"lifetime":       mustRawJSON(t, "1h"),
			"startup_script": mustRawJSON(t, "echo 'ready'"),
		},
	}
}

func provision(t *testing.T, api *fakeAPI) (providers.ProvisionMachineResult, error) {
	t.Helper()
	return testProvider(api).ProvisionMachine(
		context.Background(),
		testInstallationID(),
		testMachineID(),
		testProvisioning(t),
		"machine-token",
		map[string]string{"GREETING": "it's"},
		true,
	)
}

func TestProvisionMachineCreatesDeterministicComputerAndStartsDaemon(t *testing.T) {
	previous := computerPollInterval
	computerPollInterval = time.Millisecond
	t.Cleanup(func() { computerPollInterval = previous })
	id := testComputerID(t)
	api := &fakeAPI{
		createResult: computer{ID: id, OS: computerOSMacOS, State: computerStateStarting},
		getFound:     true,
		getResults: []computer{
			{ID: id, OS: computerOSMacOS, State: computerStateStarting},
			{ID: id, OS: computerOSMacOS, State: computerStateActive},
		},
	}

	result, err := provision(t, api)

	if err != nil {
		t.Fatalf("ProvisionMachine: %v", err)
	}
	if result.ProviderResourceID != id.String() {
		t.Fatalf("provider resource id = %q, want %q", result.ProviderResourceID, id)
	}
	want := createComputerRequest{ID: id, OS: computerOSMacOS, TimeoutSeconds: 3600}
	if len(api.createRequests) != 1 || api.createRequests[0] != want {
		t.Fatalf("create requests = %+v, want %+v", api.createRequests, want)
	}
	if len(api.writes) != 1 || len(api.execRequests) != 1 {
		t.Fatalf("writes = %d execs = %d, want 1 and 1", len(api.writes), len(api.execRequests))
	}
	argv := api.execRequests[0].Argv
	if len(argv) != 6 || argv[0] != "/bin/sh" || argv[2] != daemonInstallScript || argv[5] != "1" {
		t.Fatalf("install argv = %q", argv)
	}
	script, ok := api.writes[argv[4]+"-000"]
	if !ok {
		t.Fatalf("install script part %q was not uploaded: %v", argv[4]+"-000", api.writes)
	}
	for _, line := range []string{
		"export OMNARA_MACHINE_TOKEN='machine-token'\n",
		"export OMNARA_API_URL='https://api.omnara.test/api/v1'\n",
		"export GREETING='it'\"'\"'s'\n",
		"export " + providers.ManagedBootstrapScriptEnvVar + "='" + providers.ManagedBootScriptPayload() + "'\n",
	} {
		if !strings.Contains(string(script), line) {
			t.Errorf("start script is missing %q", line)
		}
	}
	if strings.Contains(strings.Join(argv, " "), "machine-token") {
		t.Fatal("install command argv contains the machine token")
	}
}

func TestProvisionMachineUploadsLargeStartScriptsInChunks(t *testing.T) {
	id := testComputerID(t)
	api := &fakeAPI{createResult: computer{ID: id, OS: computerOSMacOS, State: computerStateActive}}
	provisioning := executionstore.MachineProvisioningConfig{
		ProviderOptions: map[string]json.RawMessage{
			"startup_script": mustRawJSON(t, strings.Repeat("x", 64*1024)),
		},
	}

	_, err := testProvider(api).ProvisionMachine(
		context.Background(), testInstallationID(), testMachineID(), provisioning, "token", nil, true,
	)

	if err != nil {
		t.Fatalf("ProvisionMachine: %v", err)
	}
	argv := api.execRequests[0].Argv
	if argv[5] != "2" || len(api.writes) != 2 {
		t.Fatalf("parts = %s, writes = %d, want 2", argv[5], len(api.writes))
	}
	if len(api.writes[argv[4]+"-000"]) != maxFileWriteBytes {
		t.Fatalf("first part = %d bytes, want %d", len(api.writes[argv[4]+"-000"]), maxFileWriteBytes)
	}
}

func TestProvisionMachineFailsPermanentlyForEndedComputer(t *testing.T) {
	reason := "no_host"
	api := &fakeAPI{createResult: computer{
		ID: testComputerID(t), OS: computerOSMacOS, State: computerStateFailed, FailureReason: &reason,
	}}

	result, err := provision(t, api)

	if !errors.Is(err, providers.ErrPermanent) || !strings.Contains(err.Error(), "no_host") {
		t.Fatalf("ProvisionMachine error = %v, want permanent no_host error", err)
	}
	if result.ProviderResourceID != testComputerID(t).String() {
		t.Fatalf("provider resource id = %q, want the observed computer", result.ProviderResourceID)
	}
	if len(api.execRequests) != 0 {
		t.Fatal("daemon install ran on an ended computer")
	}
}

func TestProvisionMachineReportsDaemonInstallFailure(t *testing.T) {
	api := &fakeAPI{
		createResult: computer{ID: testComputerID(t), OS: computerOSMacOS, State: computerStateActive},
		execResult:   execResult{ExitCode: 1, Stderr: []byte("bootstrap failed\n")},
	}

	_, err := provision(t, api)

	if err == nil || !strings.Contains(err.Error(), "status 1: bootstrap failed") {
		t.Fatalf("ProvisionMachine error = %v, want install failure", err)
	}
}

func TestProvisionMachineRejectsReservedEnvBeforeCreating(t *testing.T) {
	api := &fakeAPI{}

	_, err := testProvider(api).ProvisionMachine(
		context.Background(), testInstallationID(), testMachineID(),
		executionstore.MachineProvisioningConfig{}, "token", map[string]string{"OMNARA_API_URL": "x"}, true,
	)

	if !errors.Is(err, providers.ErrPermanent) || len(api.createRequests) != 0 {
		t.Fatalf("error = %v creates = %d, want permanent error and no create", err, len(api.createRequests))
	}
}

func TestProvisionMachineRejectsForeignComputerButKeepsItsOwnID(t *testing.T) {
	api := &fakeAPI{createResult: computer{ID: uuid.New(), OS: computerOSMacOS, State: computerStateActive}}

	result, err := provision(t, api)

	if err == nil {
		t.Fatal("ProvisionMachine adopted a computer with another id")
	}
	if want := testComputerID(t).String(); result.ProviderResourceID != want {
		t.Fatalf("provider resource id = %q, want %q so the computer can still be deleted", result.ProviderResourceID, want)
	}
}

func TestClassifyCreateError(t *testing.T) {
	for _, test := range []struct {
		err       apiError
		permanent bool
	}{
		{apiError{StatusCode: http.StatusForbidden, Code: "computer_not_enabled"}, true},
		{apiError{StatusCode: http.StatusPaymentRequired}, true},
		{apiError{StatusCode: http.StatusConflict, Code: "run_conflict"}, true},
		{apiError{StatusCode: http.StatusConflict, Code: "request_conflict"}, false},
		{apiError{StatusCode: http.StatusTooManyRequests, Code: "computer_limit"}, false},
		{apiError{StatusCode: http.StatusServiceUnavailable}, false},
	} {
		if got := errors.Is(classifyCreateError(test.err), providers.ErrPermanent); got != test.permanent {
			t.Errorf("classifyCreateError(%v) permanent = %v, want %v", test.err, got, test.permanent)
		}
	}
}

func TestInspectMachine(t *testing.T) {
	id := testComputerID(t)
	for _, test := range []struct {
		name      string
		current   computer
		found     bool
		wantFound bool
	}{
		{
			name:      "active",
			current:   computer{ID: id, OS: computerOSMacOS, State: computerStateActive},
			found:     true,
			wantFound: true,
		},
		{
			name:      "closing",
			current:   computer{ID: id, OS: computerOSMacOS, State: computerStateClosing, Release: "pending"},
			found:     true,
			wantFound: true,
		},
		{
			name:    "released",
			current: computer{ID: id, OS: computerOSMacOS, State: computerStateStopped, Release: "confirmed"},
			found:   true,
		},
		{
			name:    "stopped before release is confirmed",
			current: computer{ID: id, OS: computerOSMacOS, State: computerStateStopped, Release: "pending"},
			found:   true,
		},
		{
			name:    "failed",
			current: computer{ID: id, OS: computerOSMacOS, State: computerStateFailed},
			found:   true,
		},
		{name: "missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := &fakeAPI{getFound: test.found, getResults: []computer{test.current}}

			resourceID, found, err := testProvider(api).InspectMachine(
				context.Background(), testInstallationID(), testMachineID(), executionstore.MachineProvisioningConfig{}, "",
			)

			if err != nil || found != test.wantFound {
				t.Fatalf("InspectMachine = %q, %v, %v; want found %v", resourceID, found, err, test.wantFound)
			}
			if found && resourceID != id.String() {
				t.Fatalf("resource id = %q, want %q", resourceID, id)
			}
		})
	}
}

func TestInspectMachineRejectsResourceOwnedByAnotherMachine(t *testing.T) {
	_, _, err := testProvider(&fakeAPI{}).InspectMachine(
		context.Background(), testInstallationID(), testMachineID(), executionstore.MachineProvisioningConfig{},
		uuid.NewString(),
	)
	if err == nil {
		t.Fatal("InspectMachine accepted another machine's resource id")
	}
}

func TestDeleteMachineStopsOwnedComputer(t *testing.T) {
	api := &fakeAPI{}
	id := testComputerID(t)

	err := testProvider(api).DeleteMachine(
		context.Background(), testInstallationID(), testMachineID(), executionstore.MachineProvisioningConfig{}, id.String(),
	)

	if err != nil || len(api.stopCalls) != 1 || api.stopCalls[0] != id {
		t.Fatalf("DeleteMachine = %v, stops %v; want one stop of %s", err, api.stopCalls, id)
	}
	if err := testProvider(api).DeleteMachine(
		context.Background(), testInstallationID(), testMachineID(), executionstore.MachineProvisioningConfig{}, "",
	); err == nil {
		t.Fatal("DeleteMachine accepted an empty resource id")
	}
}
