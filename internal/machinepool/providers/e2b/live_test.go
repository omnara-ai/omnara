package e2b

import (
	"context"
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

func TestE2BProviderLiveSmoke(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("E2B_API_KEY"))
	if token == "" {
		t.Skip("E2B_API_KEY is required")
	}
	templateRef := strings.TrimSpace(os.Getenv("OMNARA_E2B_TEST_TEMPLATE"))
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
	liveAPI := liveTestAPI{apiClient: machineProvider.api}
	machineProvider.api = liveAPI
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		id := resourceID
		if id == "" {
			foundID, found, err := machineProvider.InspectMachine(cleanupCtx, installationID, machineID, provisioning, "")
			if err != nil {
				t.Errorf("find live e2b sandbox for cleanup: %v", err)
				return
			}
			if !found {
				return
			}
			id = foundID
		}
		if err := machineProvider.DeleteMachine(cleanupCtx, installationID, machineID, provisioning, id); err != nil {
			t.Errorf("delete live e2b sandbox: %v", err)
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

	require.Error(t, machineProvider.DeleteMachine(ctx, uuid.New(), machineID, provisioning, created))
	require.NoError(t, machineProvider.DeleteMachine(ctx, installationID, machineID, provisioning, created))
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
