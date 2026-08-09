package blaxel

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestBlaxelProviderLiveSmoke(t *testing.T) {
	if os.Getenv("OMNARA_BLAXEL_LIVE") != "1" {
		t.Skip("set OMNARA_BLAXEL_LIVE=1 to run live Blaxel smoke test")
	}
	token := strings.TrimSpace(os.Getenv("OMNARA_BLAXEL_API_TOKEN"))
	workspace := strings.TrimSpace(os.Getenv("OMNARA_BLAXEL_WORKSPACE"))
	image := strings.TrimSpace(os.Getenv("OMNARA_BLAXEL_TEST_IMAGE"))
	region := strings.TrimSpace(os.Getenv("OMNARA_BLAXEL_TEST_REGION"))
	if token == "" || workspace == "" || image == "" || region == "" {
		t.Skip(
			"OMNARA_BLAXEL_API_TOKEN, OMNARA_BLAXEL_WORKSPACE, " +
				"OMNARA_BLAXEL_TEST_IMAGE, and OMNARA_BLAXEL_TEST_REGION are required",
		)
	}
	omnaraPublicURL := strings.TrimSpace(os.Getenv("OMNARA_PUBLIC_URL"))
	if omnaraPublicURL == "" {
		omnaraPublicURL = "https://app.omnara.com"
	}
	machineProvisioning := testMachineProvisioning(t, map[string]any{
		"image": image, "region": region,
	})
	provider, err := (Definition{}).NewProvider(
		mustRawJSON(t, map[string]any{
			"workspace":       workspace,
			"allowed_images":  []string{image},
			"allowed_regions": []string{region},
		}),
		providers.RuntimeConfig{PublicURL: omnaraPublicURL, ProviderAuthToken: token},
	)
	if err != nil {
		t.Fatalf("new blaxel provider: %v", err)
	}
	machineID := uuid.New()
	providerResourceID, err := providers.MachineAllocationName(testInstallationID(), machineID)
	if err != nil {
		t.Fatalf("build live blaxel sandbox name: %v", err)
	}
	deleted := false
	t.Cleanup(func() {
		if deleted {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := provider.DeleteMachine(
			cleanupCtx,
			testInstallationID(),
			machineID,
			machineProvisioning,
			providerResourceID,
		); err != nil {
			t.Errorf("delete live blaxel sandbox: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	bootstrap := "live-smoke-token"
	firstResourceID, err := provider.ProvisionMachine(
		ctx,
		testInstallationID(),
		machineID,
		machineProvisioning,
		bootstrap,
		nil,
	)
	if err != nil {
		t.Fatalf("provision live blaxel sandbox: %v", err)
	}
	if firstResourceID.ProviderResourceID != providerResourceID {
		t.Fatalf("provider resource id = %q, want %q", firstResourceID.ProviderResourceID, providerResourceID)
	}
	secondResourceID, err := provider.ProvisionMachine(
		ctx,
		testInstallationID(),
		machineID,
		machineProvisioning,
		bootstrap,
		nil,
	)
	if err != nil {
		t.Fatalf("reprovision live blaxel sandbox: %v", err)
	}
	if secondResourceID.ProviderResourceID != providerResourceID {
		t.Fatalf(
			"reprovisioned resource id = %q, want %q",
			secondResourceID.ProviderResourceID,
			providerResourceID,
		)
	}
	inspectedResourceID, found, err := provider.InspectMachine(
		ctx,
		testInstallationID(),
		machineID,
		machineProvisioning,
		providerResourceID,
	)
	if err != nil {
		t.Fatalf("inspect live blaxel sandbox: %v", err)
	}
	if !found || inspectedResourceID != providerResourceID {
		t.Fatalf("inspect live blaxel sandbox = (%q, %t), want (%q, true)", inspectedResourceID, found, providerResourceID)
	}
	observer, ok := provider.(providers.RuntimeStateObserver)
	if !ok {
		t.Fatal("blaxel provider does not implement runtime observation")
	}
	runtimeTarget := providers.RuntimeTarget{
		InstallationID:      testInstallationID(),
		MachineID:           machineID,
		ProviderResourceID:  providerResourceID,
		MachineProvisioning: machineProvisioning,
	}
	observation, err := observer.ObserveRuntimeState(ctx, runtimeTarget)
	if err != nil {
		t.Fatalf("observe live blaxel sandbox runtime: %v", err)
	}
	assertLiveRuntimeObservation(t, runtimeTarget, observation)
	observations, err := observer.ObserveRuntimeStates(ctx, []providers.RuntimeTarget{runtimeTarget})
	if err != nil {
		t.Fatalf("bulk observe live blaxel sandbox runtime: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("bulk live blaxel runtime observations = %d, want 1", len(observations))
	}
	assertLiveRuntimeObservation(t, runtimeTarget, observations[0])
	if err := provider.DeleteMachine(
		ctx,
		testInstallationID(),
		machineID,
		machineProvisioning,
		providerResourceID,
	); err != nil {
		t.Fatalf("delete live blaxel sandbox: %v", err)
	}
	deleted = true
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
