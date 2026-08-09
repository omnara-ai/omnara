package daytona

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestDaytonaProviderLiveSmoke(t *testing.T) {
	if os.Getenv("OMNARA_DAYTONA_LIVE") != "1" {
		t.Skip("set OMNARA_DAYTONA_LIVE=1 to run live Daytona smoke test")
	}
	token := os.Getenv("DAYTONA_API_KEY")
	snapshotName := os.Getenv("OMNARA_DAYTONA_TEST_SNAPSHOT")
	target := os.Getenv("OMNARA_DAYTONA_TEST_TARGET")
	if token == "" || snapshotName == "" || target == "" {
		t.Skip("DAYTONA_API_KEY, OMNARA_DAYTONA_TEST_SNAPSHOT, and OMNARA_DAYTONA_TEST_TARGET are required")
	}
	config := mustRawJSON(t, map[string]any{
		"allowed_snapshots": []string{"*"},
		"allowed_targets":   []string{"*"},
	})
	provisional := executionstore.MachineProvisioningConfig{
		ProviderOptions: testOptions(t, snapshotName, target, ""),
	}
	omnaraPublicURL := os.Getenv("OMNARA_PUBLIC_URL")
	if omnaraPublicURL == "" {
		omnaraPublicURL = "https://app.omnara.com"
	}
	provider, err := (Definition{}).NewProvider(
		config,
		providers.RuntimeConfig{PublicURL: omnaraPublicURL, ProviderAuthToken: token},
	)
	if err != nil {
		t.Fatalf("new live daytona provider: %v", err)
	}
	facts, err := provider.PrepareProvisioning(context.Background(), provisional)
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
		if err := provider.DeleteMachine(
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
	resourceID, err := provider.ProvisionMachine(
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
	reprovisionCtx, reprovisionCancel := context.WithTimeout(context.Background(), provisioningTimeout)
	reprovisionedResourceID, err := provider.ProvisionMachine(
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
	_, found, err := provider.InspectMachine(
		inspectCtx,
		installationID,
		machineID,
		provisioning,
		resourceID.ProviderResourceID,
	)
	if err != nil || !found {
		t.Fatalf("inspect live daytona sandbox = found %v error %v", found, err)
	}
	observer, ok := provider.(providers.RuntimeStateObserver)
	if !ok {
		t.Fatal("daytona provider does not implement runtime observation")
	}
	runtimeTarget := providers.RuntimeTarget{
		InstallationID:      installationID,
		MachineID:           machineID,
		ProviderResourceID:  resourceID.ProviderResourceID,
		MachineProvisioning: provisioning,
	}
	observationCtx, observationCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer observationCancel()
	observation, err := observer.ObserveRuntimeState(observationCtx, runtimeTarget)
	if err != nil {
		t.Fatalf("observe live daytona sandbox runtime: %v", err)
	}
	assertLiveRuntimeObservation(t, runtimeTarget, observation)
	observations, err := observer.ObserveRuntimeStates(
		observationCtx,
		[]providers.RuntimeTarget{runtimeTarget},
	)
	if err != nil {
		t.Fatalf("bulk observe live daytona sandbox runtime: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("bulk live daytona runtime observations = %d, want 1", len(observations))
	}
	assertLiveRuntimeObservation(t, runtimeTarget, observations[0])
}

func assertLiveRuntimeObservation(
	t *testing.T,
	target providers.RuntimeTarget,
	observation providers.RuntimeObservation,
) {
	t.Helper()
	if observation.MachineID != target.MachineID ||
		observation.ProviderResourceID != target.ProviderResourceID ||
		!observation.State.Valid() || observation.State == providers.RuntimeStateUnknown {
		t.Fatalf("unexpected live runtime observation: %+v", observation)
	}
}
