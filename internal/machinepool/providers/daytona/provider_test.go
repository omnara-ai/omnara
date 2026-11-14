package daytona

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
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
		true,
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
	name, err := providers.MachineAllocationName(installationID, machineID)
	require.NoError(t, err)
	if _, ok := api.createRequest.Env["OMNARA_MACHINE_TOKEN"]; ok ||
		api.createRequest.Env[testStartupScriptEnvVar] !=
			base64.StdEncoding.EncodeToString([]byte(providers.StartupScriptWithoutProxyEnv("echo ready"))) {
		t.Fatalf("managed env = %#v", api.createRequest.Env)
	}
	require.Equal(t, []createSecretRequest{{Name: name, Value: "machine-token", Hosts: []string{"api.omnara.test"}}},
		api.secretRequests)
	require.Equal(t, []map[string]string{{"OMNARA_MACHINE_TOKEN": name}}, api.createRequest.Secrets)
	require.Equal(t, []string{"create-secret", "create-sandbox"}, api.calls)
	if api.executedSession != daemonSessionName || !api.executeRequest.RunAsync ||
		!strings.Contains(api.executeRequest.Command, `"${OMNARA_INSTALLER_URL:?}"`) ||
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
		true,
	)
	if err != nil {
		t.Fatalf("provision daytona sandbox: %v", err)
	}
	if api.createRequest.Env["APP_ENV"] != "production" ||
		api.createRequest.Env["GITHUB_TOKEN"] != "resolved-secret" {
		t.Fatalf("create env missing machine env: %#v", api.createRequest.Env)
	}
	if api.createRequest.Env["OMNARA_API_URL"] == "" {
		t.Fatalf("create env missing bootstrap env: %#v", api.createRequest.Env)
	}
}

func TestDaytonaProviderProvisionFallsBackToThePlainTokenWithoutSecretAccess(t *testing.T) {
	for name, configure := range map[string]func(*fakeAPI, *provider){
		"key without manage:secrets": func(api *fakeAPI, _ *provider) {
			api.secretErr = apiError{StatusCode: http.StatusForbidden}
		},
		"no https API host": func(_ *fakeAPI, provider *provider) {
			provider.omnaraAPIURL = "http://localhost:8080"
		},
	} {
		t.Run(name, func(t *testing.T) {
			api := newFakeAPI()
			provider := newTestProvider(api)
			configure(api, provider)
			_, err := provider.ProvisionMachine(
				context.Background(), uuid.New(), uuid.New(),
				testMachineProvisioning(t, "team-snapshot", "us", "echo ready"), "machine-token", nil, true,
			)
			require.NoError(t, err)
			require.Equal(t, "machine-token", api.createRequest.Env["OMNARA_MACHINE_TOKEN"])
			require.Nil(t, api.createRequest.Secrets)
			require.Equal(t, base64.StdEncoding.EncodeToString([]byte("echo ready")),
				api.createRequest.Env[testStartupScriptEnvVar])
		})
	}
}

func TestDaytonaProviderProvisionHandlesMachineTokenSecretFailures(t *testing.T) {
	api := newFakeAPI()
	api.secretErr = apiError{StatusCode: http.StatusConflict}
	_, err := newTestProvider(api).ProvisionMachine(
		context.Background(), uuid.New(), uuid.New(),
		testMachineProvisioning(t, "team-snapshot", "us", ""), "machine-token", nil, true,
	)
	require.NoError(t, err)
	require.Len(t, api.createRequest.Secrets, 1)

	api = newFakeAPI()
	api.secretErr = apiError{StatusCode: http.StatusInternalServerError}
	_, err = newTestProvider(api).ProvisionMachine(
		context.Background(), uuid.New(), uuid.New(),
		testMachineProvisioning(t, "team-snapshot", "us", ""), "machine-token", nil, true,
	)
	require.Error(t, err)
	require.Equal(t, []string{"create-secret"}, api.calls)
}

func TestDaytonaProviderProvisionDeletesTheSecretOnlyWhenCreateWasRejected(t *testing.T) {
	for status, wantCalls := range map[int][]string{
		http.StatusBadRequest:          {"create-secret", "create-sandbox", "find-secret", "delete-secret"},
		http.StatusInternalServerError: {"create-secret", "create-sandbox"},
		http.StatusTooManyRequests:     {"create-secret", "create-sandbox"},
	} {
		api := newFakeAPI()
		api.createErr = apiError{StatusCode: status}
		_, err := newTestProvider(api).ProvisionMachine(
			context.Background(), uuid.New(), uuid.New(),
			testMachineProvisioning(t, "team-snapshot", "us", ""), "machine-token", nil, true,
		)
		require.True(t, isStatus(err, status), "status %d error %v", status, err)
		require.Equal(t, wantCalls, api.calls, "status %d", status)
	}

	api := newFakeAPI()
	api.secretErr = apiError{StatusCode: http.StatusConflict}
	api.createErr = apiError{StatusCode: http.StatusBadRequest}
	_, err := newTestProvider(api).ProvisionMachine(
		context.Background(), uuid.New(), uuid.New(),
		testMachineProvisioning(t, "team-snapshot", "us", ""), "machine-token", nil, true,
	)
	require.True(t, isStatus(err, http.StatusBadRequest), "error %v", err)
	require.Equal(t, []string{"create-secret", "create-sandbox"}, api.calls,
		"a secret from an earlier attempt may back a running sandbox")
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
		true,
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
		true,
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
		true,
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
			true,
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
			true,
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
			true,
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
			true,
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
	require.NoError(t, err)
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

func TestDaytonaProviderDeleteMachineDeletesTheTokenSecretAfterTheSandbox(t *testing.T) {
	installationID := uuid.New()
	machineID := uuid.New()
	name, err := providers.MachineAllocationName(installationID, machineID)
	require.NoError(t, err)
	deleteMachine := func(api *fakeAPI) error {
		api.sandbox.Name = name
		api.sandbox.Labels = map[string]string{"omnara-machine": name}
		return newTestProvider(api).DeleteMachine(
			context.Background(), installationID, machineID,
			testMachineProvisioning(t, "team-snapshot", "us", ""), api.sandbox.ID,
		)
	}

	api := newFakeAPI()
	api.secretIDs = map[string]string{name: "secret-id"}
	require.NoError(t, deleteMachine(api))
	require.Equal(t, []string{"delete-sandbox", "find-secret", "delete-secret"}, api.calls)
	require.Equal(t, []string{"secret-id"}, api.deletedSecretIDs)

	api = newFakeAPI()
	api.findSecretErr = apiError{StatusCode: http.StatusInternalServerError}
	require.Error(t, deleteMachine(api))
	require.Equal(t, []string{"delete-sandbox", "find-secret"}, api.calls)

	api = newFakeAPI()
	api.findSecretErr = apiError{StatusCode: http.StatusForbidden}
	require.NoError(t, deleteMachine(api))
	require.Equal(t, []string{"delete-sandbox", "find-secret"}, api.calls)

	api = newFakeAPI()
	api.secretIDs = map[string]string{name: "secret-id"}
	api.missingSandboxIDs = map[string]bool{api.sandbox.ID: true, name: true}
	require.NoError(t, deleteMachine(api))
	require.Equal(t, []string{"find-secret", "delete-secret"}, api.calls)
}

func TestProviderCheckConnectionListsOneSandbox(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/sandbox" || r.URL.Query().Get("limit") != "1" {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	err := newTestProvider(newRESTClient(server.URL, "test-token", server.Client())).CheckConnection(
		context.Background(),
		executionstore.MachineProvisioningConfig{},
	)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("check connection error = %v, want HTTP 401", err)
	}
}
