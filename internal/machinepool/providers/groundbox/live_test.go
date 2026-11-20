package groundbox

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/testutil/providercontract"
	"github.com/stretchr/testify/require"
)

func TestGroundboxProviderLiveSmoke(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("GROUNDBOX_API_KEY"))
	if token == "" {
		t.Skip("GROUNDBOX_API_KEY is required")
	}
	omnaraPublicAPIURL := strings.TrimSpace(os.Getenv("OMNARA_PUBLIC_API_URL"))
	if omnaraPublicAPIURL == "" {
		omnaraPublicAPIURL = "https://api.omnara.com/v1"
	}
	machineProvider, err := newProvider(
		mustRawJSON(t, map[string]any{"allowed_templates": []string{"*"}}),
		providers.RuntimeConfig{OmnaraAPIURL: omnaraPublicAPIURL, ProviderAuthToken: token},
	)
	require.NoError(t, err)
	client, ok := machineProvider.api.(*restClient)
	require.True(t, ok)
	liveAPI := liveTestAPI{apiClient: client}
	machineProvider.api = liveAPI
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	options := map[string]any{"sleep_after_ms": 30000}
	if template := strings.TrimSpace(os.Getenv("OMNARA_GROUNDBOX_TEST_TEMPLATE")); template != "" {
		options["template"] = template
	}
	provisioning := testProvisioning(t, options)
	facts, err := machineProvider.PrepareProvisioning(ctx, provisioning)
	require.NoError(t, err)
	require.Equal(t, 2, *facts.CPU)
	require.Equal(t, 4096, *facts.MemoryMB)
	installationID, machineID := uuid.New(), uuid.New()
	var resourceID string
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		id := resourceID
		if id == "" {
			foundID, found, err := machineProvider.InspectMachine(cleanupCtx, installationID, machineID, provisioning, "")
			if err != nil {
				t.Errorf("find live groundbox sandbox for cleanup: %v", err)
				return
			}
			if !found {
				return
			}
			id = foundID
		}
		if err := machineProvider.DeleteMachine(cleanupCtx, installationID, machineID, provisioning, id); err != nil {
			t.Errorf("delete live groundbox sandbox: %v", err)
		}
	})
	provision := func(firstAttempt bool) providers.ProvisionMachineResult {
		provisionCtx, provisionCancel := context.WithTimeout(ctx, provisioningTimeout)
		defer provisionCancel()
		started := time.Now()
		result, err := machineProvider.ProvisionMachine(
			provisionCtx, installationID, machineID, provisioning, "live-smoke-token", nil, firstAttempt,
		)
		if result.ProviderResourceID != "" {
			resourceID = result.ProviderResourceID
		}
		require.NoError(t, err, "first attempt %t", firstAttempt)
		t.Logf("provision (first attempt %t) took %s", firstAttempt, time.Since(started))
		return result
	}
	created := provision(true)
	require.NotEmpty(t, created.ProviderResourceID)
	require.Equal(t, defaultAPIBaseURL+sandboxPath(created.ProviderResourceID), created.SandboxURL)
	require.Equal(t, created, provision(false))

	target, found, err := liveAPI.GetSandbox(ctx, created.ProviderResourceID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, providercontract.LiveResourceValue, target.Metadata[providercontract.LiveResourceLabel])
	require.Equal(t, sandboxStatusRunning, target.Status)
	require.Equal(t, 1, liveKeepers(ctx, t, liveAPI, created.ProviderResourceID, bootCommand))
	pid := liveReadFile(ctx, t, client, created.ProviderResourceID, daemonPIDPath)
	_, err = strconv.Atoi(strings.TrimSpace(pid))
	require.NoError(t, err, "daemon pid file = %q", pid)

	inspected, found, err := machineProvider.InspectMachine(ctx, installationID, machineID, provisioning, "")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, created.ProviderResourceID, inspected)

	runtimeTarget := providers.RuntimeTarget{
		InstallationID:      installationID,
		MachineID:           machineID,
		ProviderResourceID:  created.ProviderResourceID,
		MachineProvisioning: provisioning,
	}
	providercontract.WaitForPresentRuntimeObservation(t, ctx, runtimeTarget, func() (providers.RuntimeObservation, error) {
		return machineProvider.ObserveRuntimeState(ctx, runtimeTarget)
	})
	observations, err := machineProvider.ObserveRuntimeStates(ctx, []providers.RuntimeTarget{runtimeTarget})
	require.NoError(t, err)
	require.Len(t, observations, 1)
	providercontract.AssertRuntimeObservation(t, runtimeTarget, observations[0], providers.RuntimeStateRunning)

	livePause(ctx, t, client, created.ProviderResourceID)
	require.Eventually(t, func() bool {
		observation, err := machineProvider.ObserveRuntimeState(ctx, runtimeTarget)
		return err == nil && observation.State == providers.RuntimeStateInactive
	}, time.Minute, time.Second)
	wakeStarted := time.Now()
	require.NoError(t, machineProvider.WakeMachine(ctx, providers.WakeMachineInput(created)))
	t.Logf("wake from pause took %s", time.Since(wakeStarted))
	observation, err := machineProvider.ObserveRuntimeState(ctx, runtimeTarget)
	require.NoError(t, err)
	providercontract.AssertRuntimeObservation(t, runtimeTarget, observation, providers.RuntimeStateRunning)
	require.Equal(t, 1, liveKeepers(ctx, t, liveAPI, created.ProviderResourceID, wakeCommand))
	require.Equal(t, pid, liveReadFile(ctx, t, client, created.ProviderResourceID, daemonPIDPath))

	missingTarget := runtimeTarget
	missingTarget.MachineID = uuid.New()
	missingTarget.ProviderResourceID = "sbx_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	missing, err := machineProvider.ObserveRuntimeState(ctx, missingTarget)
	require.NoError(t, err)
	providercontract.AssertRuntimeObservation(t, missingTarget, missing, providers.RuntimeStateTerminated)

	require.Error(t, machineProvider.DeleteMachine(ctx, uuid.New(), machineID, provisioning, resourceID))
	require.NoError(t, machineProvider.DeleteMachine(ctx, installationID, machineID, provisioning, resourceID))
	require.Eventually(t, func() bool {
		observation, err := machineProvider.ObserveRuntimeState(ctx, runtimeTarget)
		return err == nil && observation.State == providers.RuntimeStateTerminated
	}, time.Minute, time.Second)
	require.NoError(t, machineProvider.DeleteMachine(ctx, installationID, machineID, provisioning, resourceID))
}

type liveTestAPI struct {
	apiClient
}

func (a liveTestAPI) CreateSandbox(ctx context.Context, key string, request createSandboxRequest) (sandbox, error) {
	request.Metadata[providercontract.LiveResourceLabel] = providercontract.LiveResourceValue
	return a.apiClient.CreateSandbox(ctx, key, request)
}

func liveKeepers(ctx context.Context, t *testing.T, api apiClient, id, cmd string) int {
	t.Helper()
	running, err := api.ListRunningCommands(ctx, id)
	require.NoError(t, err)
	return len(slices.DeleteFunc(running, func(current command) bool { return current.Cmd != cmd }))
}

func livePause(ctx context.Context, t *testing.T, client *restClient, id string) {
	t.Helper()
	var paused sandbox
	path := sandboxPath(id) + "/pause"
	require.NoError(t, client.do(ctx, client.lifecycleClient, http.MethodPost, path, nil, nil, &paused))
}

func liveReadFile(ctx context.Context, t *testing.T, client *restClient, id, path string) string {
	t.Helper()
	var contents string
	require.Eventually(t, func() bool {
		response, err := providers.DoHTTPResponse(
			ctx,
			client.httpClient,
			providers.Groundbox,
			http.MethodGet,
			client.baseURL+sandboxPath(id)+"/files?"+url.Values{"path": {path}}.Encode(),
			map[string]string{"Authorization": "Bearer " + client.apiKey},
			nil,
		)
		if err != nil || response.StatusCode != http.StatusOK || len(response.Body) == 0 {
			return false
		}
		contents = string(response.Body)
		return true
	}, 30*time.Second, time.Second)
	return contents
}
