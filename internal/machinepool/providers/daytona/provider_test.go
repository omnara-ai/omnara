package daytona

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestDaytonaProviderProvisionCreatesSandboxAndDaemonSession(t *testing.T) {
	api := newFakeAPI()
	provider := newTestProvider(api)
	installationID := uuid.New()
	machineID := uuid.New()
	provisioning := testMachineProvisioning(t, "team-snapshot", "us", "echo ready")
	resourceID, err := provider.ProvisionMachine(
		context.Background(),
		installationID,
		machineID,
		provisioning,
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision daytona sandbox: %v", err)
	}
	if resourceID.ProviderResourceID != api.sandbox.ID {
		t.Fatalf("resource id = %q, want %q", resourceID.ProviderResourceID, api.sandbox.ID)
	}
	if api.createRequest.Snapshot != "team-snapshot" || api.createRequest.Target != "us" {
		t.Fatalf("create request = %+v", api.createRequest)
	}
	if api.createRequest.AutoStopInterval != 0 || api.createRequest.AutoDeleteInterval != -1 {
		t.Fatalf("create persistence intervals = %+v", api.createRequest)
	}
	if api.createRequest.Env["OMNARA_MACHINE_TOKEN"] != "machine-token" ||
		api.createRequest.Env[testStartupScriptEnvVar] == "" {
		t.Fatalf("managed env = %#v", api.createRequest.Env)
	}
	if api.executedSession != daemonSessionName || !api.executeRequest.RunAsync ||
		!strings.Contains(api.executeRequest.Command, "/install/omnarad.sh") ||
		!strings.Contains(api.executeRequest.Command, "start --no-service") {
		t.Fatalf("daemon session request = session %q request %+v", api.executedSession, api.executeRequest)
	}
}

func TestDaytonaProviderProvisionIncludesMachineEnv(t *testing.T) {
	api := newFakeAPI()
	provider := newTestProvider(api)
	_, err := provider.ProvisionMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "team-snapshot", "us", ""),
		"machine-token",
		map[string]string{"APP_ENV": "production", "GITHUB_TOKEN": "resolved-secret"},
	)
	if err != nil {
		t.Fatalf("provision daytona sandbox: %v", err)
	}
	if api.createRequest.Env["APP_ENV"] != "production" ||
		api.createRequest.Env["GITHUB_TOKEN"] != "resolved-secret" {
		t.Fatalf("create env missing machine env: %#v", api.createRequest.Env)
	}
	if api.createRequest.Env["OMNARA_API_URL"] == "" ||
		api.createRequest.Env["OMNARA_MACHINE_TOKEN"] != "machine-token" {
		t.Fatalf("create env missing bootstrap env: %#v", api.createRequest.Env)
	}
}

func TestDaytonaProviderProvisionConvergesOnExistingSession(t *testing.T) {
	api := newFakeAPI()
	api.createErr = apiError{StatusCode: http.StatusConflict}
	api.sessionFound = true
	api.session = session{Commands: []command{{}}}
	provider := newTestProvider(api)
	resourceID, err := provider.ProvisionMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "team-snapshot", "us", ""),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision existing daytona sandbox: %v", err)
	}
	if resourceID.ProviderResourceID != api.sandbox.ID || api.getSandboxCalls != 1 || api.executeCalls != 0 ||
		api.createSessionCalls != 0 {
		t.Fatalf(
			"existing session result = resource %q get %d execute %d create %d",
			resourceID.ProviderResourceID,
			api.getSandboxCalls,
			api.executeCalls,
			api.createSessionCalls,
		)
	}
}

func TestDaytonaProviderProvisionRejectsUnexpectedOwnership(t *testing.T) {
	api := newFakeAPI()
	api.sandbox.Labels = map[string]string{"omnara-machine": "other"}
	_, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "team-snapshot", "us", ""),
		"machine-token",
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "expected ownership label") || api.createSessionCalls != 0 {
		t.Fatalf("ownership error = %v, create session calls = %d", err, api.createSessionCalls)
	}
}

func TestDaytonaProviderProvisionReplacesExitedSession(t *testing.T) {
	exitCode := 1
	api := newFakeAPI()
	api.sessionFound = true
	api.session = session{Commands: []command{{ExitCode: &exitCode}}}
	api.createSessionErr = apiError{StatusCode: http.StatusConflict}
	resourceID, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "team-snapshot", "us", ""),
		"machine-token",
		nil,
	)
	if err != nil || resourceID.ProviderResourceID != api.sandbox.ID || api.deleteSessionCalls != 1 ||
		api.createSessionCalls != 1 || api.executeCalls != 1 {
		t.Fatalf(
			"replace session = resource %q error %v deletes %d creates %d executes %d",
			resourceID.ProviderResourceID,
			err,
			api.deleteSessionCalls,
			api.createSessionCalls,
			api.executeCalls,
		)
	}
}

func TestDaytonaProviderProvisionHandlesUnusableSandbox(t *testing.T) {
	t.Run("transitional", func(t *testing.T) {
		api := newFakeAPI()
		api.sandbox.State = "pulling_snapshot"
		api.sandboxStates = []sandboxState{sandboxStateStarting, sandboxStateStarted}
		provider := newTestProvider(api)
		resourceID, err := provider.ProvisionMachine(
			context.Background(),
			uuid.New(),
			uuid.New(),
			testMachineProvisioning(t, "team-snapshot", "us", ""),
			"token",
			nil,
		)
		if err != nil || resourceID.ProviderResourceID != api.sandbox.ID ||
			api.getSandboxCalls != 2 || api.deleteCalls != 0 {
			t.Fatalf(
				"transitional result = resource %q error %v gets %d deletes %d",
				resourceID.ProviderResourceID,
				err,
				api.getSandboxCalls,
				api.deleteCalls,
			)
		}
	})
	t.Run("transitional timeout", func(t *testing.T) {
		api := newFakeAPI()
		api.sandbox.State = "pulling_snapshot"
		provider := newTestProvider(api)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		result, err := provider.ProvisionMachine(
			ctx,
			uuid.New(),
			uuid.New(),
			testMachineProvisioning(t, "team-snapshot", "us", ""),
			"token",
			nil,
		)
		if !errors.Is(err, context.DeadlineExceeded) ||
			result.ProviderResourceID != api.sandbox.ID || api.deleteCalls != 0 {
			t.Fatalf(
				"transitional timeout = resource %q error %v deletes %d",
				result.ProviderResourceID,
				err,
				api.deleteCalls,
			)
		}
	})
	t.Run("terminal", func(t *testing.T) {
		api := newFakeAPI()
		api.sandbox.State = "error"
		provider := newTestProvider(api)
		_, err := provider.ProvisionMachine(
			context.Background(),
			uuid.New(),
			uuid.New(),
			testMachineProvisioning(t, "team-snapshot", "us", ""),
			"token",
			nil,
		)
		if !errors.Is(err, providers.ErrResourceReplaced) || api.deleteCalls != 1 {
			t.Fatalf("terminal result = error %v deletes %d", err, api.deleteCalls)
		}
	})
	t.Run("resource mismatch", func(t *testing.T) {
		api := newFakeAPI()
		api.sandbox.CPU = 4
		provider := newTestProvider(api)
		result, err := provider.ProvisionMachine(
			context.Background(),
			uuid.New(),
			uuid.New(),
			testMachineProvisioning(t, "team-snapshot", "us", ""),
			"token",
			nil,
		)
		if err == nil || !strings.Contains(
			err.Error(),
			"resources cpu=4 memory_mb=4096 do not match resolved machine resources cpu=2 memory_mb=4096",
		) {
			t.Fatalf(
				"resource mismatch = resource %q error %v",
				result.ProviderResourceID,
				err,
			)
		}
		if result.ProviderResourceID != api.sandbox.ID {
			t.Fatalf("resource mismatch id = %q, want %q", result.ProviderResourceID, api.sandbox.ID)
		}
	})
}

func TestDaytonaProviderInspectAndDelete(t *testing.T) {
	api := newFakeAPI()
	provider := newTestProvider(api)
	installationID := uuid.New()
	machineID := uuid.New()
	name, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		t.Fatal(err)
	}
	api.sandbox.Name = name
	api.sandbox.Labels = map[string]string{"omnara-machine": name}
	resourceID, found, err := provider.InspectMachine(
		context.Background(),
		installationID,
		machineID,
		testMachineProvisioning(t, "team-snapshot", "us", ""),
		"",
	)
	if err != nil || !found || resourceID != api.sandbox.ID {
		t.Fatalf("inspect = resource %q found %v error %v", resourceID, found, err)
	}
	if err := provider.DeleteMachine(
		context.Background(),
		installationID,
		machineID,
		testMachineProvisioning(t, "team-snapshot", "us", ""),
		resourceID,
	); err != nil {
		t.Fatalf("delete daytona sandbox: %v", err)
	}
	if api.deletedResourceID != resourceID {
		t.Fatalf("deleted resource id = %q, want %q", api.deletedResourceID, resourceID)
	}
	api.sandbox.Labels = map[string]string{"omnara-machine": "other"}
	if err := provider.DeleteMachine(
		context.Background(),
		installationID,
		machineID,
		testMachineProvisioning(t, "team-snapshot", "us", ""),
		resourceID,
	); err == nil || !strings.Contains(err.Error(), "ownership label") {
		t.Fatalf("foreign machine error = %v, want ownership error", err)
	}
	if api.deleteCalls != 1 {
		t.Fatalf("deleted foreign sandbox %d times", api.deleteCalls-1)
	}
	api.sandbox.Labels = map[string]string{"omnara-machine": name}
	api.missingSandboxIDs = map[string]bool{"stale": true}
	if err := provider.DeleteMachine(
		context.Background(),
		installationID,
		machineID,
		testMachineProvisioning(t, "team-snapshot", "us", ""),
		"stale",
	); err != nil {
		t.Fatalf("delete sandbox by allocation name: %v", err)
	}
	if api.deleteCalls != 2 || api.deletedResourceID != resourceID {
		t.Fatalf("delete calls = %d resource id = %q", api.deleteCalls, api.deletedResourceID)
	}
	api.missingSandboxIDs = map[string]bool{"already-absent": true, name: true}
	if err := provider.DeleteMachine(
		context.Background(),
		installationID,
		machineID,
		testMachineProvisioning(t, "team-snapshot", "us", ""),
		"already-absent",
	); err != nil {
		t.Fatalf("delete already absent machine: %v", err)
	}
	if api.deleteCalls != 2 {
		t.Fatalf("already absent machine caused %d delete calls", api.deleteCalls-2)
	}
}
