package daytona

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/providercontract"
)

func TestDaytonaProviderLiveSmoke(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("DAYTONA_API_KEY"))
	if token == "" {
		t.Skip("a Daytona API key is required")
	}
	snapshotName := strings.TrimSpace(os.Getenv("OMNARA_DAYTONA_TEST_SNAPSHOT"))
	if snapshotName == "" {
		snapshotName = "daytona-small"
	}
	target := strings.TrimSpace(os.Getenv("OMNARA_DAYTONA_TEST_TARGET"))
	if target == "" {
		target = "us"
	}
	config := mustRawJSON(t, map[string]any{
		"allowed_snapshots": []string{"*"},
		"allowed_targets":   []string{"*"},
	})
	provisional := executionstore.MachineProvisioningConfig{
		ProviderOptions: testOptions(t, snapshotName, target, ""),
	}
	omnaraPublicURL := strings.TrimSpace(os.Getenv("OMNARA_PUBLIC_URL"))
	if omnaraPublicURL == "" {
		omnaraPublicURL = "https://app.omnara.com"
	}
	machineProvider, err := (Definition{}).NewProvider(
		config,
		providers.RuntimeConfig{PublicURL: omnaraPublicURL, ProviderAuthToken: token},
	)
	if err != nil {
		t.Fatalf("new live daytona provider: %v", err)
	}
	concreteProvider, ok := machineProvider.(*provider)
	if !ok {
		t.Fatal("Daytona provider has an unexpected implementation")
	}
	concreteProvider.api = liveTestAPI{apiClient: concreteProvider.api}
	facts, err := machineProvider.PrepareProvisioning(context.Background(), provisional)
	if err != nil {
		t.Fatalf("prepare live daytona provisioning: %v", err)
	}
	provisioning := provisional
	provisioning.CPU = facts.CPU
	provisioning.MemoryMB = facts.MemoryMB
	installationID := uuid.New()
	machineID := uuid.New()
	cleanupResourceID, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("build live daytona sandbox name: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if err := machineProvider.DeleteMachine(
			cleanupCtx,
			installationID,
			machineID,
			provisioning,
			cleanupResourceID,
		); err != nil {
			t.Errorf("delete live daytona sandbox: %v", err)
		}
	})
	provisionCtx, provisionCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	resourceID, err := machineProvider.ProvisionMachine(
		provisionCtx,
		installationID,
		machineID,
		provisioning,
		"live-smoke-token",
		nil,
	)
	provisionCancel()
	if err != nil {
		t.Fatalf("provision live daytona sandbox: %v", err)
	}
	cleanupResourceID = resourceID.ProviderResourceID
	markerCtx, markerCancel := context.WithTimeout(context.Background(), 10*time.Second)
	created, found, err := concreteProvider.api.GetSandbox(markerCtx, cleanupResourceID)
	markerCancel()
	if err != nil || !found {
		t.Fatalf("get marked live Daytona sandbox = found %v error %v", found, err)
	}
	if created.Labels[providercontract.LiveResourceLabel] != providercontract.LiveResourceValue {
		t.Fatalf("live Daytona sandbox is missing its test marker: %+v", created.Labels)
	}
	reprovisionCtx, reprovisionCancel := context.WithTimeout(context.Background(), provisioningTimeout)
	reprovisionedResourceID, err := machineProvider.ProvisionMachine(
		reprovisionCtx,
		installationID,
		machineID,
		provisioning,
		"live-smoke-token",
		nil,
	)
	reprovisionCancel()
	if err != nil {
		t.Fatalf("reprovision live daytona sandbox: %v", err)
	}
	if reprovisionedResourceID.ProviderResourceID != resourceID.ProviderResourceID {
		t.Fatalf(
			"reprovisioned resource id = %q, want %q",
			reprovisionedResourceID.ProviderResourceID,
			resourceID.ProviderResourceID,
		)
	}
	inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer inspectCancel()
	_, found, err = machineProvider.InspectMachine(
		inspectCtx,
		installationID,
		machineID,
		provisioning,
		resourceID.ProviderResourceID,
	)
	if err != nil || !found {
		t.Fatalf("inspect live daytona sandbox = found %v error %v", found, err)
	}
	observer, ok := machineProvider.(providers.RuntimeStateObserver)
	if !ok {
		t.Fatal("daytona provider does not implement runtime observation")
	}
	runtimeTarget := providers.RuntimeTarget{
		InstallationID:      installationID,
		MachineID:           machineID,
		ProviderResourceID:  resourceID.ProviderResourceID,
		MachineProvisioning: provisioning,
	}
	observationCtx, observationCancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer observationCancel()
	providercontract.WaitForPresentRuntimeObservation(
		t,
		observationCtx,
		runtimeTarget,
		func() (providers.RuntimeObservation, error) {
			return observer.ObserveRuntimeState(observationCtx, runtimeTarget)
		},
	)
	providercontract.WaitForPresentRuntimeObservation(
		t,
		observationCtx,
		runtimeTarget,
		func() (providers.RuntimeObservation, error) {
			observations, err := observer.ObserveRuntimeStates(
				observationCtx,
				[]providers.RuntimeTarget{runtimeTarget},
			)
			if err != nil {
				return providers.RuntimeObservation{}, err
			}
			if len(observations) != 1 {
				return providers.RuntimeObservation{}, fmt.Errorf(
					"bulk live Daytona runtime observations = %d, want 1",
					len(observations),
				)
			}
			return observations[0], nil
		},
	)
}

type liveTestAPI struct {
	apiClient
}

func (a liveTestAPI) CreateSandbox(
	ctx context.Context,
	request createSandboxRequest,
) (sandbox, error) {
	request.Labels[providercontract.LiveResourceLabel] = providercontract.LiveResourceValue
	request.Env[providercontract.LiveResourceEnv] = providercontract.LiveResourceValue
	return a.apiClient.CreateSandbox(ctx, request)
}
