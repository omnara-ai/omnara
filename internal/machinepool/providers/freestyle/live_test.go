package freestyle

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/testutil/providercontract"
)

func TestFreestyleProviderLiveSmoke(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("FREESTYLE_API_KEY"))
	if apiKey == "" {
		t.Skip("FREESTYLE_API_KEY is required")
	}
	snapshot := strings.TrimSpace(os.Getenv("OMNARA_FREESTYLE_TEST_SNAPSHOT"))
	if snapshot == "" {
		snapshot = "freestyle/ubuntu-sm"
	}
	omnaraAPIURL := strings.TrimSpace(os.Getenv("OMNARA_PUBLIC_API_URL"))
	if omnaraAPIURL == "" {
		omnaraAPIURL = "https://app.omnara.com/api/v1"
	}
	provisioning := testProvisioning(t)
	provisioning.ProviderOptions["snapshot"] = rawJSON(t, snapshot)
	provisioning.ProviderOptions["startup_script"] = rawJSON(t, "")
	machineProvider, err := (Definition{}).NewProvider(
		rawJSON(t, map[string]any{"allowed_snapshots": []string{snapshot}}),
		providers.RuntimeConfig{
			OmnaraAPIURL:      omnaraAPIURL,
			ProviderAuthToken: apiKey,
		},
	)
	if err != nil {
		t.Fatalf("new freestyle provider: %v", err)
	}
	concreteProvider, ok := machineProvider.(*provider)
	if !ok {
		t.Fatal("Freestyle provider has an unexpected implementation")
	}
	restAPI, ok := concreteProvider.api.(*restClient)
	if !ok {
		t.Fatal("Freestyle provider has an unexpected API client")
	}
	concreteProvider.api = liveTestAPI{apiClient: restAPI}
	machineID := uuid.New()
	cleanupResourceID, err := providers.MachineAllocationName(testInstallationID(), machineID)
	if err != nil {
		t.Fatalf("create cleanup allocation name: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := machineProvider.DeleteMachine(
			cleanupCtx,
			testInstallationID(),
			machineID,
			provisioning,
			cleanupResourceID,
		); err != nil {
			t.Errorf("delete live freestyle VM: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), provisioningTimeout)
	defer cancel()
	result, err := machineProvider.ProvisionMachine(
		ctx,
		testInstallationID(),
		machineID,
		provisioning,
		"live-smoke-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision live freestyle VM: %v", err)
	}
	if result.ProviderResourceID == "" {
		t.Fatal("expected provider resource id")
	}
	cleanupResourceID = result.ProviderResourceID
	current, found, err := restAPI.GetVM(ctx, cleanupResourceID)
	if err != nil || !found {
		t.Fatalf("get live freestyle VM = %+v, found %v, error %v", current, found, err)
	}
	if current.Metadata[providercontract.LiveResourceEnv] != providercontract.LiveResourceValue {
		t.Fatalf("live freestyle VM is missing the test resource marker")
	}
	observer, ok := machineProvider.(providers.RuntimeStateObserver)
	if !ok {
		t.Fatal("freestyle provider does not implement runtime observation")
	}
	target := providers.RuntimeTarget{
		InstallationID:      testInstallationID(),
		MachineID:           machineID,
		ProviderResourceID:  cleanupResourceID,
		MachineProvisioning: provisioning,
	}
	providercontract.WaitForPresentRuntimeObservation(t, ctx, target, func() (
		providers.RuntimeObservation,
		error,
	) {
		return observer.ObserveRuntimeState(ctx, target)
	})
}

type liveTestAPI struct {
	apiClient
}

func (a liveTestAPI) CreateVM(ctx context.Context, request createVMRequest) (vm, error) {
	request.Metadata[providercontract.LiveResourceEnv] = providercontract.LiveResourceValue
	return a.apiClient.CreateVM(ctx, request)
}
