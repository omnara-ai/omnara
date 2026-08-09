package unikraft

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestUnikraftProviderLiveSmoke(t *testing.T) {
	if os.Getenv("OMNARA_UNIKRAFT_LIVE") != "1" {
		t.Skip("set OMNARA_UNIKRAFT_LIVE=1 to run live Unikraft smoke test")
	}
	token := os.Getenv("OMNARA_UNIKRAFT_API_TOKEN")
	image := os.Getenv("OMNARA_UNIKRAFT_TEST_IMAGE")
	if token == "" || image == "" {
		t.Skip("OMNARA_UNIKRAFT_API_TOKEN and OMNARA_UNIKRAFT_TEST_IMAGE are required")
	}
	omnaraPublicURL := os.Getenv("OMNARA_PUBLIC_URL")
	if omnaraPublicURL == "" {
		omnaraPublicURL = "https://app.omnara.com"
	}
	machineProvisioning := testMachineProvisioning(
		t,
		map[string]any{"provider_options": map[string]any{"image": image}},
	)
	provider, err := (Definition{}).NewProvider(
		mustRawJSON(t, map[string]any{"allowed_images": []string{image}, "allowed_metros": []string{"*"}}),
		providers.RuntimeConfig{PublicURL: omnaraPublicURL, ProviderAuthToken: token},
	)
	if err != nil {
		t.Fatalf("new unikraft provider: %v", err)
	}
	machineID := uuid.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	providerResourceID, err := provider.ProvisionMachine(
		ctx,
		testInstallationID(),
		machineID,
		machineProvisioning,
		"live-smoke-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision live unikraft instance: %v", err)
	}
	if providerResourceID.ProviderResourceID == "" {
		t.Fatal("expected provider resource id")
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := provider.DeleteMachine(
			cleanupCtx,
			testInstallationID(),
			machineID,
			machineProvisioning,
			providerResourceID.ProviderResourceID,
		); err != nil {
			t.Fatalf("delete live unikraft instance: %v", err)
		}
	}()
	_, found, err := provider.InspectMachine(
		ctx,
		testInstallationID(),
		machineID,
		machineProvisioning,
		providerResourceID.ProviderResourceID,
	)
	if err != nil {
		t.Fatalf("inspect live unikraft instance: %v", err)
	}
	if !found {
		t.Fatal("expected live instance to be found")
	}
	observer, ok := provider.(providers.RuntimeStateObserver)
	if !ok {
		t.Fatal("unikraft provider does not implement runtime observation")
	}
	runtimeTarget := providers.RuntimeTarget{
		InstallationID:      testInstallationID(),
		MachineID:           machineID,
		ProviderResourceID:  providerResourceID.ProviderResourceID,
		MachineProvisioning: machineProvisioning,
	}
	observation, err := observer.ObserveRuntimeState(ctx, runtimeTarget)
	if err != nil {
		t.Fatalf("observe live unikraft instance runtime: %v", err)
	}
	assertLiveRuntimeObservation(t, runtimeTarget, observation)
	observations, err := observer.ObserveRuntimeStates(ctx, []providers.RuntimeTarget{runtimeTarget})
	if err != nil {
		t.Fatalf("bulk observe live unikraft instance runtime: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("bulk live unikraft runtime observations = %d, want 1", len(observations))
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
