package bead

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/providercontract"
	"github.com/stretchr/testify/require"
)

func TestBeadProviderLiveSmoke(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("BEAD_API_KEY"))
	if token == "" {
		t.Skip("BEAD_API_KEY is required")
	}
	templateRef := strings.TrimSpace(os.Getenv("OMNARA_BEAD_TEST_TEMPLATE"))
	if templateRef == "" {
		templateRef = "base"
	}
	omnaraPublicAPIURL := strings.TrimSpace(os.Getenv("OMNARA_PUBLIC_API_URL"))
	if omnaraPublicAPIURL == "" {
		omnaraPublicAPIURL = "https://api.omnara.com/v1"
	}
	machineProvider, err := newProvider(
		rawJSON(t, map[string]any{"allowed_templates": []string{"*"}}),
		providers.RuntimeConfig{OmnaraAPIURL: omnaraPublicAPIURL, ProviderAuthToken: token},
	)
	require.NoError(t, err)
	rest, ok := machineProvider.api.(*restClient)
	require.True(t, ok)
	for _, client := range []*http.Client{rest.httpClient, rest.lifecycleClient} {
		client.Transport = liveDiagnosticTransport{base: client.Transport, t: t}
	}
	liveAPI := liveTestAPI{apiClient: machineProvider.api}
	machineProvider.api = liveAPI
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	provisioning := executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, templateRef)}
	facts, err := machineProvider.PrepareProvisioning(ctx, provisioning)
	require.NoError(t, err)
	t.Logf("template %s resolved to cpu=%d memory_mb=%d", templateRef, *facts.CPU, *facts.MemoryMB)
	provisioning.CPU = facts.CPU
	provisioning.MemoryMB = facts.MemoryMB
	installationID, machineID := uuid.New(), uuid.New()
	var resourceID string
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		id := resourceID
		if id == "" {
			foundID, found, err := machineProvider.InspectMachine(cleanupCtx, installationID, machineID, provisioning, "")
			if err != nil {
				t.Errorf("find live bead sandbox for cleanup: %v", err)
				return
			}
			if !found {
				return
			}
			id = foundID
		}
		require.Eventually(t, func() bool {
			return machineProvider.DeleteMachine(cleanupCtx, installationID, machineID, provisioning, id) == nil
		}, 90*time.Second, time.Second, "delete live bead sandbox")
		remaining, err := liveAPI.FindSandboxes(cleanupCtx, nil)
		require.NoError(t, err)
		for _, current := range remaining {
			require.NotEqual(t, id, current.SandboxID, "live bead sandbox remains after cleanup")
		}

	})
	provision := func(firstAttempt bool) string {
		provisionCtx, provisionCancel := context.WithTimeout(ctx, provisioningTimeout)
		defer provisionCancel()
		result, err := machineProvider.ProvisionMachine(
			provisionCtx, installationID, machineID, provisioning, "live-smoke-token", nil, firstAttempt,
		)
		if result.ProviderResourceID != "" {
			resourceID = result.ProviderResourceID
		}
		require.NoError(t, err, "first attempt %t", firstAttempt)
		return result.ProviderResourceID
	}
	created := provision(true)
	require.NotEmpty(t, created)
	require.Equal(t, created, provision(false))

	target, found, err := liveAPI.GetSandbox(ctx, created)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "persistent", target.Kind)
	t.Logf("sandbox %s kind=%s", created, target.Kind)
	require.Equal(t, providercontract.LiveResourceValue, target.Metadata[providercontract.LiveResourceLabel])
	processes, err := liveAPI.ListProcesses(ctx, target)
	require.NoError(t, err)
	daemons := 0
	for _, current := range processes {
		if current.Tag == daemonProcessTag {
			daemons++
		}
	}
	require.Equal(t, 1, daemons, "processes = %+v", processes)
	inspected, found, err := machineProvider.InspectMachine(ctx, installationID, machineID, provisioning, "")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, created, inspected)

	runtimeTarget := providers.RuntimeTarget{
		InstallationID:      installationID,
		MachineID:           machineID,
		ProviderResourceID:  created,
		MachineProvisioning: provisioning,
	}
	providercontract.WaitForPresentRuntimeObservation(t, ctx, runtimeTarget, func() (providers.RuntimeObservation, error) {
		return machineProvider.ObserveRuntimeState(ctx, runtimeTarget)
	})
	observations, err := machineProvider.ObserveRuntimeStates(ctx, []providers.RuntimeTarget{runtimeTarget})
	require.NoError(t, err)
	require.Len(t, observations, 1)
	providercontract.AssertRuntimeObservation(t, runtimeTarget, observations[0], providers.RuntimeStateRunning)
	missingTarget := runtimeTarget
	missingTarget.MachineID = uuid.New()
	missingTarget.ProviderResourceID = "i" + strings.ReplaceAll(uuid.NewString(), "-", "")[:19]
	missing, err := machineProvider.ObserveRuntimeState(ctx, missingTarget)
	require.NoError(t, err)
	providercontract.AssertRuntimeObservation(t, missingTarget, missing, providers.RuntimeStateTerminated)

	// Pause the same sandbox to exercise a real resume without creating a second machine.
	require.NoError(t, rest.do(ctx, http.MethodPost, rest.apiURL("/sandboxes/"+created+"/pause"),
		rest.apiKey(), map[string]any{}, nil))
	require.Eventually(t, func() bool {
		observation, err := machineProvider.ObserveRuntimeState(ctx, runtimeTarget)
		return err == nil && observation.State == providers.RuntimeStateInactive
	}, time.Minute, time.Second)
	require.NoError(t, machineProvider.WakeMachine(ctx, providers.WakeMachineInput{ProviderResourceID: created}))
	require.NoError(t, machineProvider.WakeMachine(ctx, providers.WakeMachineInput{ProviderResourceID: created}))
	require.Eventually(t, func() bool {
		observation, err := machineProvider.ObserveRuntimeState(ctx, runtimeTarget)
		return err == nil && observation.State == providers.RuntimeStateRunning
	}, time.Minute, time.Second)
	require.Equal(t, created, provision(false))
	target, found, err = liveAPI.GetSandbox(ctx, created)
	require.NoError(t, err)
	require.True(t, found)
	processes, err = liveAPI.ListProcesses(ctx, target)
	require.NoError(t, err)
	daemons = 0
	for _, current := range processes {
		if current.Tag == daemonProcessTag {
			daemons++
		}
	}
	require.Equal(t, 1, daemons, "daemon must remain present after pause/resume")

	require.Error(t, machineProvider.DeleteMachine(ctx, uuid.New(), machineID, provisioning, created))
	require.Eventually(t, func() bool {
		return machineProvider.DeleteMachine(ctx, installationID, machineID, provisioning, created) == nil
	}, 90*time.Second, time.Second, "retry-safe delete after transient lifecycle errors")
	require.Eventually(t, func() bool {
		observation, err := machineProvider.ObserveRuntimeState(ctx, runtimeTarget)
		return err == nil && observation.State == providers.RuntimeStateTerminated
	}, time.Minute, time.Second)
	require.NoError(t, machineProvider.DeleteMachine(ctx, installationID, machineID, provisioning, created))
}

type liveTestAPI struct {
	apiClient
}

func (a liveTestAPI) CreateSandbox(ctx context.Context, request createSandboxRequest) (sandbox, error) {
	request.Metadata[providercontract.LiveResourceLabel] = providercontract.LiveResourceValue
	return a.apiClient.CreateSandbox(ctx, request)
}

// Only status, request identifiers and error codes are logged; API keys, guest
// access tokens and successful response bodies must never appear in live logs.
type liveDiagnosticTransport struct {
	base http.RoundTripper
	t    *testing.T
}

func (d liveDiagnosticTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	started := time.Now().UTC().Format(time.RFC3339Nano)
	requestID := uuid.NewString()
	request.Header.Set("X-Request-ID", requestID)
	base := d.base
	if base == nil {
		base = http.DefaultTransport
	}
	response, err := base.RoundTrip(request)
	if err != nil {
		d.t.Logf("%s %s %s transport_error=%T", started, request.Method, request.URL.Path, err)
		return nil, err
	}
	ids := map[string]string{"client_request_id": requestID}
	for _, key := range []string{"X-Request-ID", "Request-ID", "X-Bead-Request-ID", "CF-Ray"} {
		if value := response.Header.Get(key); value != "" {
			ids[key] = value
		}
	}
	codes := map[string]any{}
	if response.StatusCode >= 400 {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		_ = response.Body.Close()
		response.Body = io.NopCloser(bytes.NewReader(body))
		if readErr == nil {
			var payload map[string]any
			if json.Unmarshal(body, &payload) == nil {
				for _, key := range []string{"code", "error_code"} {
					if value, ok := payload[key]; ok {
						codes[key] = value
					}
				}
			} else if bytes.HasPrefix(bytes.TrimSpace(body), []byte("error code: ")) {
				codes["edge_error"] = string(bytes.TrimSpace(body))
			}
		}
	}
	d.t.Logf("%s %s %s status=%d request_ids=%v error_codes=%v", started,
		request.Method, request.URL.Path, response.StatusCode, ids, codes)
	return response, nil
}
