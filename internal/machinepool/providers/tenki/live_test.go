package tenki

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/testutil/providercontract"
	"github.com/stretchr/testify/require"
)

func TestTenkiProviderLiveSmoke(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("TENKI_API_KEY"))
	if token == "" {
		t.Skip("TENKI_API_KEY is required")
	}
	omnaraPublicURL := strings.TrimSpace(os.Getenv("OMNARA_PUBLIC_URL"))
	if omnaraPublicURL == "" {
		omnaraPublicURL = "https://app.omnara.com"
	}
	omnaraPublicAPIURL := strings.TrimSpace(os.Getenv("OMNARA_PUBLIC_API_URL"))
	if omnaraPublicAPIURL == "" {
		omnaraPublicAPIURL = omnaraPublicURL + "/api/v1"
	}
	machineProvider, err := (Definition{}).NewProvider(nil, providers.RuntimeConfig{
		OmnaraAPIURL:      omnaraPublicAPIURL,
		ProviderAuthToken: token,
	})
	require.NoError(t, err)
	concreteProvider, ok := machineProvider.(*provider)
	require.True(t, ok)
	liveAPI := liveTestAPI{apiClient: concreteProvider.api}
	concreteProvider.api = liveAPI
	installationID, machineID := uuid.New(), uuid.New()
	provisioning := testProvisioning()
	var resourceID string
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		id := resourceID
		for attempt := 0; id == "" && attempt < 10; attempt++ {
			if attempt > 0 {
				select {
				case <-cleanupCtx.Done():
					return
				case <-time.After(2 * time.Second):
				}
			}
			foundID, found, err := machineProvider.InspectMachine(cleanupCtx, installationID, machineID, provisioning, "")
			if err != nil {
				t.Errorf("find live tenki session for cleanup: %v", err)
				return
			}
			if found {
				id = foundID
			}
		}
		if id == "" {
			return
		}
		if err := machineProvider.DeleteMachine(cleanupCtx, installationID, machineID, provisioning, id); err != nil {
			t.Errorf("delete live tenki session: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	result, err := machineProvider.ProvisionMachine(
		ctx, installationID, machineID, provisioning, "live-smoke-token", nil, true,
	)
	resourceID = result.ProviderResourceID
	require.NoError(t, err)
	require.NotEmpty(t, resourceID)
	require.Eventually(t, func() bool {
		existing, err := machineProvider.ProvisionMachine(
			ctx, installationID, machineID, provisioning, "live-smoke-token", nil, false,
		)
		return err == nil && existing.ProviderResourceID == resourceID
	}, time.Minute, time.Second)
	current, found, err := liveAPI.Get(ctx, resourceID)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, current.Sticky)
	require.Equal(t, providercontract.LiveResourceValue, current.Metadata[providercontract.LiveResourceLabel])
	inspected, found, err := machineProvider.InspectMachine(ctx, installationID, machineID, provisioning, "")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, resourceID, inspected)

	observer, ok := machineProvider.(providers.RuntimeStateObserver)
	require.True(t, ok)
	target := providers.RuntimeTarget{
		InstallationID:      installationID,
		MachineID:           machineID,
		ProviderResourceID:  resourceID,
		MachineProvisioning: provisioning,
	}
	providercontract.WaitForPresentRuntimeObservation(t, ctx, target, func() (providers.RuntimeObservation, error) {
		return observer.ObserveRuntimeState(ctx, target)
	})
	observations, err := observer.ObserveRuntimeStates(ctx, []providers.RuntimeTarget{target})
	require.NoError(t, err)
	require.Len(t, observations, 1)
	providercontract.AssertRuntimeObservation(
		t, target, observations[0], providers.RuntimeStateRunning, providers.RuntimeStateInactive,
	)
	missingTarget := target
	missingTarget.MachineID = uuid.New()
	missingTarget.ProviderResourceID = uuid.NewString()
	missing, err := observer.ObserveRuntimeState(ctx, missingTarget)
	require.NoError(t, err)
	providercontract.AssertRuntimeObservation(t, missingTarget, missing, providers.RuntimeStateTerminated)

	require.Error(t, machineProvider.DeleteMachine(ctx, uuid.New(), machineID, provisioning, resourceID))
	require.NoError(t, machineProvider.DeleteMachine(ctx, installationID, machineID, provisioning, resourceID))
	require.Eventually(t, func() bool {
		observation, err := observer.ObserveRuntimeState(ctx, target)
		return err == nil && observation.State == providers.RuntimeStateTerminated
	}, time.Minute, time.Second)
	require.NoError(t, machineProvider.DeleteMachine(ctx, installationID, machineID, provisioning, resourceID))
}

type liveTestAPI struct {
	apiClient
}

func (a liveTestAPI) Create(ctx context.Context, request createRequest) (session, error) {
	request.Metadata[providercontract.LiveResourceLabel] = providercontract.LiveResourceValue
	return a.apiClient.Create(ctx, request)
}
