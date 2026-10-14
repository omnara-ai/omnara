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
	omnaraPublicURL := strings.TrimSpace(os.Getenv("OMNARA_PUBLIC_URL"))
	if omnaraPublicURL == "" {
		omnaraPublicURL = "https://app.omnara.com"
	}
	omnaraPublicAPIURL := strings.TrimSpace(os.Getenv("OMNARA_PUBLIC_API_URL"))
	if omnaraPublicAPIURL == "" {
		omnaraPublicAPIURL = omnaraPublicURL + "/api/v1"
	}
	cpu := 4
	memoryMB := 8192
	provisioning := testProvisioning(t)
	provisioning.CPU = &cpu
	provisioning.MemoryMB = &memoryMB
	provisioning.ProviderOptions["snapshot"] = rawJSON(t, snapshot)
	provisioning.ProviderOptions["startup_script"] = rawJSON(t, "")
	machineProvider, err := (Definition{}).NewProvider(
		rawJSON(t, map[string]any{"allowed_snapshots": []string{snapshot}}),
		providers.RuntimeConfig{
			OmnaraAPIURL:      omnaraPublicAPIURL,
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
		true,
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
	if current.Metadata[providercontract.LiveResourceLabel] != providercontract.LiveResourceValue {
		t.Fatalf("live freestyle VM is missing the test resource marker")
	}
	if current.Resources.CPU != cpu || current.Resources.MemoryMB != memoryMB {
		t.Fatalf("live freestyle VM resources = %+v, want cpu=%d memory_mb=%d", current.Resources, cpu, memoryMB)
	}
	reprovisioned, err := machineProvider.ProvisionMachine(
		ctx,
		testInstallationID(),
		machineID,
		provisioning,
		"live-smoke-token",
		nil,
		true,
	)
	if err != nil || reprovisioned.ProviderResourceID != result.ProviderResourceID {
		t.Fatalf("reprovision live freestyle VM = %+v, error %v", reprovisioned, err)
	}
	inspectedResourceID, found, err := machineProvider.InspectMachine(
		ctx,
		testInstallationID(),
		machineID,
		provisioning,
		result.ProviderResourceID,
	)
	if err != nil || !found || inspectedResourceID != result.ProviderResourceID {
		t.Fatalf("inspect live freestyle VM = %q, found %v, error %v", inspectedResourceID, found, err)
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
	providercontract.WaitForPresentRuntimeObservation(t, ctx, target, func() (
		providers.RuntimeObservation,
		error,
	) {
		observations, err := observer.ObserveRuntimeStates(ctx, []providers.RuntimeTarget{target})
		if err != nil {
			return providers.RuntimeObservation{}, err
		}
		return observations[0], nil
	})
	missingTarget := target
	missingTarget.MachineID = uuid.New()
	missingTarget.ProviderResourceID = "vm-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	missingObservation, err := observer.ObserveRuntimeState(ctx, missingTarget)
	if err != nil {
		t.Fatalf("observe missing live freestyle VM: %v", err)
	}
	providercontract.AssertRuntimeObservation(
		t,
		missingTarget,
		missingObservation,
		providers.RuntimeStateTerminated,
	)
}

type liveTestAPI struct {
	apiClient
}

func (a liveTestAPI) CreateVM(ctx context.Context, request createVMRequest) (vm, error) {
	request.Metadata[providercontract.LiveResourceLabel] = providercontract.LiveResourceValue
	return a.apiClient.CreateVM(ctx, request)
}
